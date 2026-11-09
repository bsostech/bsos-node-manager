package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"besu-manager/backend/internal/api"
)

// Validate requests against the contract: mounted on the real generated routes (handlers are
// Unimplemented, so passing validation yields 501). A fake middleware stands in RequireScopes'
// position to check the order: unauthenticated gets 401 first and insufficient permission 403
// first, never 400 for bad content first.
func TestValidateRequests(t *testing.T) {
	spec, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	login := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Header.Get("X-Test-Login") {
			case "":
				w.WriteHeader(http.StatusUnauthorized)
				return
			case "no-perm":
				w.WriteHeader(http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	r := chi.NewRouter()
	h := api.HandlerWithOptions(api.Unimplemented{}, api.ChiServerOptions{
		BaseURL: "/api/v1", BaseRouter: r, ErrorHandlerFunc: ParamError,
		Middlewares: []api.MiddlewareFunc{ValidateRequests(spec, "/api/v1"), login},
	})

	allow := `"accountsAllowlist":["0x` + strings.Repeat("a", 40) + `"]`
	for _, c := range []struct {
		name, method, path, body, ctype string
		login                           string // "" = logged in; "none" = not logged in; "no-perm" = insufficient permission
		want                            int
		reason                          string // the reason expected with 400; field-level ones are written as reason:field
	}{
		{"白名單沒帶 accountsAllowlist", "PUT", "/api/v1/permissioning", `{"revision":1}`, "application/json", "", 400, "field_required:accountsAllowlist"},
		{"白名單沒帶 revision", "PUT", "/api/v1/permissioning", `{` + allow + `}`, "application/json", "", 400, "field_required:revision"},
		{"白名單合法", "PUT", "/api/v1/permissioning", `{"revision":1,` + allow + `}`, "application/json", "", 501, ""},
		{"多帶欄位放行", "PUT", "/api/v1/permissioning", `{"revision":1,"foo":1,` + allow + `}`, "application/json", "", 501, ""},
		{"唯讀欄位放行", "PUT", "/api/v1/permissioning", `{"revision":1,"nodes":[],"custom":[],` + allow + `}`, "application/json", "", 501, ""},
		{"唯讀欄位內容缺欄位或型別不對也放行", "PUT", "/api/v1/permissioning", `{"revision":1,"nodes":[{}],"custom":[{"nodeId":"node-a1","revision":"x"}],` + allow + `}`, "application/json", "", 501, ""},
		{"剝掉唯讀欄位後照驗其他欄位", "PUT", "/api/v1/permissioning", `{"revision":"x","custom":[{}],` + allow + `}`, "application/json", "", 400, "field_type:revision"},
		{"charset 放行", "PUT", "/api/v1/permissioning", `{"revision":1,` + allow + `}`, "application/json; charset=utf-8", "", 501, ""},
		{"沒帶 Content-Type", "PUT", "/api/v1/permissioning", `{"revision":1,` + allow + `}`, "", "", 400, "content_type_json"},
		{"型別不對", "PUT", "/api/v1/permissioning", `{"revision":"x",` + allow + `}`, "application/json", "", 400, "field_type:revision"},
		{"規則沒帶 name", "PUT", "/api/v1/alert-rules/r1", `{"forDuration":"5m","severity":"warning"}`, "application/json", "", 400, "field_required:name"},
		{"通知語言列舉外", "PUT", "/api/v1/alert-settings/locale", `{"language":"fr","timezone":"UTC"}`, "application/json", "", 400, "field_not_allowed:language"},
		{"通知時區沒帶", "PUT", "/api/v1/alert-settings/locale", `{"language":"en"}`, "application/json", "", 400, "field_required:timezone"},
		{"通知時區空字串 = 跟主機", "PUT", "/api/v1/alert-settings/locale", `{"language":"en","timezone":""}`, "application/json", "", 501, ""},
		{"通知語言時區合法", "PUT", "/api/v1/alert-settings/locale", `{"language":"zh","timezone":"Asia/Taipei"}`, "application/json", "", 501, ""},
		{"列舉外的指標", "GET", "/api/v1/nodes/node-a1/series?metric=bogus", "", "", "", 400, "field_not_allowed:metric"},
		{"range 格式不對", "GET", "/api/v1/nodes/node-a1/series?metric=cpu&range=1y", "", "", "", 400, "field_format:range"},
		{"時序合法", "GET", "/api/v1/nodes/node-a1/series?metric=cpu&range=6h", "", "", "", 501, ""},
		{"參數綁定失敗是 JSON", "GET", "/api/v1/nodes/node-a1/series?metric=cpu&from=abc", "", "", "", 400, "param_format"},
		// A required query parameter of a struct type (time.Time) that is missing fails in the generated binder,
		// which runs before the middlewares: param_format, even for a caller that is not signed in.
		{"必填時間參數沒帶", "GET", "/api/v1/auth/login-events", "", "", "", 400, "param_format"},
		{"必填時間參數沒帶也先於登入", "GET", "/api/v1/auth/login-events", "", "", "none", 400, "param_format"},
		{"必填時間參數合法", "GET", "/api/v1/auth/login-events?since=2026-11-01T00:00:00Z", "", "", "", 501, ""},
		{"JSON 解不開", "PUT", "/api/v1/permissioning", `{"revision":`, "application/json", "", 400, "body_not_json"},
		{"整個 body 沒帶", "PUT", "/api/v1/permissioning", "", "application/json", "", 400, "body_required"},
		{"陣列少於 minItems", "POST", "/api/v1/rpc-users", `{"username":"a","permissions":[]}`, "application/json", "", 400, "field_schema_mismatch:permissions"},
		{"沒登入先 401", "PUT", "/api/v1/permissioning", `{"revision":1}`, "application/json", "none", 401, ""},
		{"權限不足先 403", "PUT", "/api/v1/permissioning", `{"revision":1}`, "application/json", "no-perm", 403, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
			if c.ctype != "" {
				req.Header.Set("Content-Type", c.ctype)
			}
			switch c.login {
			case "":
				req.Header.Set("X-Test-Login", "ok")
			case "no-perm":
				req.Header.Set("X-Test-Login", "no-perm")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != c.want {
				t.Fatalf("status %d, want %d: %s", w.Code, c.want, w.Body)
			}
			if c.want != 400 {
				return
			}
			var e api.Error
			if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || e.Code != api.ErrorCodeInvalid {
				t.Fatalf("400 要是 {code:invalid}: %s", w.Body)
			}
			if c.reason != "" {
				want, field, _ := strings.Cut(c.reason, ":")
				if e.Reason == nil || string(*e.Reason) != want {
					t.Errorf("reason %v, want %s: %s", e.Reason, want, w.Body)
				} else if field != "" && (e.Params == nil || (*e.Params)["field"] != field) {
					t.Errorf("params %v, want field=%s", e.Params, field)
				}
			}
			if strings.Contains(e.Message, "Schema:") || len(e.Message) > 300 {
				t.Errorf("訊息不該帶 schema 原文: %q", e.Message)
			}
		})
	}
}

// The contract itself must pass OpenAPI validation. When a single-line form (`{ type: ...,
// description: ... }`) has a description containing an ASCII comma without quotes, YAML splits it
// at the comma and the second half becomes an extra key; codegen does not catch it, but this
// reports extra sibling fields.
func TestSpecValid(t *testing.T) {
	spec, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// readOnly fields are dropped before the handler as well, so a malformed one cannot fail the
// strict handler's decode; the rest of the body keeps its numbers exactly as sent.
func TestValidateRequestsDropsReadOnlyForHandler(t *testing.T) {
	spec, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	var got string
	capture := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got = string(b)
			r.Body = io.NopCloser(strings.NewReader(got))
			next.ServeHTTP(w, r)
		})
	}
	r := chi.NewRouter()
	h := api.HandlerWithOptions(api.Unimplemented{}, api.ChiServerOptions{
		BaseURL: "/api/v1", BaseRouter: r, ErrorHandlerFunc: ParamError,
		Middlewares: []api.MiddlewareFunc{capture, ValidateRequests(spec, "/api/v1")},
	})
	addr := `"0x` + strings.Repeat("a", 40) + `"`
	for _, c := range []struct{ name, body, want string }{
		{"剝掉唯讀欄位", `{"revision":9007199254740993,"nodes":[{}],"custom":[{"revision":"x"}],"accountsAllowlist":[` + addr + `]}`,
			`{"accountsAllowlist":[` + addr + `],"revision":9007199254740993}`},
		{"沒有唯讀欄位原樣送", `{"revision": 1, "accountsAllowlist":[` + addr + `]}`, `{"revision": 1, "accountsAllowlist":[` + addr + `]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("PUT", "/api/v1/permissioning", strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusNotImplemented || got != c.want {
				t.Fatalf("status %d, handler body %s, want %s", w.Code, got, c.want)
			}
		})
	}
}
