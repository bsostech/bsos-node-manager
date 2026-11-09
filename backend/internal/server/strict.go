package server

import (
	"context"
	"errors"
	"log"
	"net/http"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/store"
)

// Error handling for strict handlers is replaced with this set: returns Error JSON (with reason),
// shared by the Core and Enterprise contracts.

// StrictRequestError: request decode failure (bad JSON) -> 400 JSON. WithOptions does not fill
// defaults like NewStrictHandler does; leaving it nil nil-panics.
func StrictRequestError(w http.ResponseWriter, _ *http.Request, err error) {
	writeError(w, apierr.New(api.ErrorCodeInvalid, api.ErrorReasonBodyDecodeFailed, map[string]string{"detail": err.Error()}))
}

// StrictResponseError: error returned by a handler -> 503 if the audit cannot be written or the
// database cannot be reached at all, otherwise 500. Only a fixed reason is returned and the raw text goes
// to the log: raw text often carries internal hosts, DNs and database addresses (e.g. LDAP unreachable,
// database down), and the login API would return these before authenticating.
func StrictResponseError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrAudit) { // do not return raw database errors
		apierr.Write(w, http.StatusServiceUnavailable, apierr.New(api.ErrorCodeInternal, api.ErrorReasonAuditUnavailable, nil))
		return
	}
	log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
	if store.Unavailable(err) {
		apierr.Write(w, http.StatusServiceUnavailable, apierr.New(api.ErrorCodeInternal, api.ErrorReasonDbUnavailable, nil))
		return
	}
	apierr.Write(w, http.StatusInternalServerError, apierr.New(api.ErrorCodeInternal, api.ErrorReasonInternalError, nil))
}

// Scopes: operation permissions of the Core contract (generated code puts the security scope into
// ctx), read by auth.RequireScopes.
func Scopes(ctx context.Context) ([]string, bool) {
	sc, ok := ctx.Value(api.BearerAuthScopes).([]string)
	return sc, ok
}

// writeError: 400 + Error JSON (for places outside the strict handler that write their own response).
func writeError(w http.ResponseWriter, e api.Error) { apierr.Write(w, http.StatusBadRequest, e) }

// ParamError: generated code failed to parse path / query parameters (e.g. from=abc) -> 400 JSON,
// same as ValidateRequests. Parameter parsing runs before all middleware, so this is 400 even when
// not logged in (existing order).
func ParamError(w http.ResponseWriter, _ *http.Request, err error) {
	writeError(w, apierr.New(api.ErrorCodeInvalid, api.ErrorReasonParamFormat, map[string]string{"detail": err.Error()}))
}
