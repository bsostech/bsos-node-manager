package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/go-chi/chi/v5"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
)

// ValidateRequests validates requests against the contract (path / query parameters, and required
// fields, types, enums and formats of the request body); failures get a 400 {code:"invalid"} and
// the handler is not called. The strict server only decodes JSON into Go structs, so a missing
// required field is just the zero value and out-of-enum values reach the handler.
//
// It is mounted in the generated code's ChiServerOptions.Middlewares before RequireScopes: the
// generated code wraps from last to first, so earlier ones are inner, which means unauthenticated
// and insufficient-permission requests still get 401 / 403 first. By then chi has matched the
// route, and its route pattern is used to find the operation in the contract; kin-openapi's router
// is not used (creating it requires running Validate over the whole contract). Routes outside the
// contract are not validated. readOnly fields present in requests are ignored rather than rejected
// (the contract says "ignored on PUT"): they are dropped from the body before validation, since
// ExcludeReadOnlyValidations only skips the field's own required / must-not-appear checks and
// still validates what is inside it, and the handler gets the same body so a malformed readOnly
// field cannot fail its decode either. Extra fields not in the contract are not rejected
// (OpenAPI default).
func ValidateRequests(doc *openapi3.T, baseURL string) func(http.Handler) http.Handler {
	opts := &openapi3filter.Options{
		AuthenticationFunc:         openapi3filter.NoopAuthenticationFunc, // permissions are handled by auth.RequireScopes
		ExcludeReadOnlyValidations: true,
		ExcludeResponseBody:        true,
		SkipSettingDefaults:        true, // validate only, never modify: do not fill contract defaults into the request; handlers keep handling missing fields themselves
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rctx := chi.RouteContext(r.Context())
			path := strings.TrimPrefix(rctx.RoutePattern(), baseURL)
			item := doc.Paths.Find(path)
			var op *openapi3.Operation
			if item != nil {
				op = item.GetOperation(r.Method)
			}
			if op == nil {
				next.ServeHTTP(w, r)
				return
			}
			params := make(map[string]string, len(rctx.URLParams.Keys))
			for i, k := range rctx.URLParams.Keys {
				params[k] = rctx.URLParams.Values[i]
			}
			if body := withoutReadOnly(r, op); body != nil {
				r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
			}
			in := &openapi3filter.RequestValidationInput{
				Request:    r,
				PathParams: params,
				Route:      &routers.Route{Spec: doc, Path: path, PathItem: item, Method: r.Method, Operation: op},
				Options:    opts,
			}
			if err := openapi3filter.ValidateRequest(r.Context(), in); err != nil {
				writeError(w, requestError(err))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// withoutReadOnly reads the JSON body and, when it holds readOnly fields, returns it re-encoded
// without them (UseNumber keeps numbers as written); nil otherwise, with r.Body left readable from
// the start. Invalid JSON comes back nil so the validator reports it as before.
func withoutReadOnly(r *http.Request, op *openapi3.Operation) []byte {
	if op.RequestBody == nil || op.RequestBody.Value == nil || r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	mt := op.RequestBody.Value.Content.Get("application/json")
	if mt == nil || mt.Schema == nil || mt.Schema.Value == nil {
		return nil
	}
	raw, err := io.ReadAll(r.Body)
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if d.Decode(&v) != nil || !dropReadOnly(mt.Schema.Value, v) {
		return nil
	}
	body, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return body
}

// dropReadOnly deletes the properties s marks readOnly from v, descending into objects, allOf
// members and array items; it reports whether anything was deleted.
func dropReadOnly(s *openapi3.Schema, v any) bool {
	dropped := false
	for _, a := range s.AllOf {
		if a.Value != nil && dropReadOnly(a.Value, v) {
			dropped = true
		}
	}
	switch v := v.(type) {
	case map[string]any:
		for k, p := range s.Properties {
			if _, ok := v[k]; !ok || p.Value == nil {
				continue
			}
			if p.Value.ReadOnly {
				delete(v, k)
				dropped = true
			} else if dropReadOnly(p.Value, v[k]) {
				dropped = true
			}
		}
	case []any:
		if s.Items != nil && s.Items.Value != nil {
			for _, e := range v {
				if dropReadOnly(s.Items.Value, e) {
					dropped = true
				}
			}
		}
	}
	return dropped
}

// requestError converts a kin-openapi error -> an error with a reason; params.field is the
// offending field (JSON path for the body, e.g. accountsAllowlist.3). Schema rules that do not map
// to a known kind (minItems, maxLength, etc.) give field_schema_mismatch / body_schema_mismatch,
// with detail holding kin-openapi's raw reason. SchemaError.Error() is not used: it appends the
// whole schema.
func requestError(err error) api.Error {
	invalid := func(msg string) api.Error { return api.Error{Code: api.ErrorCodeInvalid, Message: msg} }
	field := func(r api.ErrorReason, f string) api.Error {
		return apierr.New(api.ErrorCodeInvalid, r, map[string]string{"field": f})
	}
	var re *openapi3filter.RequestError
	if !errors.As(err, &re) {
		return invalid("request does not match the contract") // the validator only returns RequestError; unreachable
	}
	var pe *openapi3filter.ParseError
	if re.RequestBody != nil && errors.As(re.Err, &pe) {
		return apierr.New(api.ErrorCodeInvalid, api.ErrorReasonBodyNotJson, nil)
	}
	name := ""
	if re.Parameter != nil {
		name = re.Parameter.Name
	}
	var se *openapi3.SchemaError
	if errors.As(re.Err, &se) {
		if p := se.JSONPointer(); len(p) > 0 {
			if name != "" {
				name += "."
			}
			name += strings.Join(p, ".")
		}
		if name != "" {
			switch se.SchemaField {
			case "required":
				return field(api.ErrorReasonFieldRequired, name)
			case "enum":
				return field(api.ErrorReasonFieldNotAllowed, name)
			case "pattern", "format":
				return field(api.ErrorReasonFieldFormat, name)
			case "type":
				return field(api.ErrorReasonFieldType, name)
			}
			return apierr.New(api.ErrorCodeInvalid, api.ErrorReasonFieldSchemaMismatch, map[string]string{"field": name, "detail": se.Reason})
		}
		return apierr.New(api.ErrorCodeInvalid, api.ErrorReasonBodySchemaMismatch, map[string]string{"detail": se.Reason})
	}
	if errors.Is(re.Err, openapi3filter.ErrInvalidRequired) {
		if name == "" {
			return apierr.New(api.ErrorCodeInvalid, api.ErrorReasonBodyRequired, nil)
		}
		return field(api.ErrorReasonFieldRequired, name)
	}
	if name != "" {
		return field(api.ErrorReasonFieldFormat, name)
	}
	if strings.Contains(re.Reason, "Content-Type") {
		return apierr.New(api.ErrorCodeInvalid, api.ErrorReasonContentTypeJson, nil)
	}
	return apierr.New(api.ErrorCodeInvalid, api.ErrorReasonBodySchemaMismatch, map[string]string{"detail": re.Reason})
}
