package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"besu-manager/backend/internal/auth/authtest"
)

// Every operation in the contract declares exactly one known scope; those with security: [] (no login required) must match isPublic.
// A new operation that forgets its scope is caught here first; if it slipped through, RequireScopes would reject it at runtime anyway.
func TestCoreOperationsDeclarePermission(t *testing.T) {
	known := map[Permission]bool{PermMonitorView: true, PermValidatorsView: true, PermNodesOnboard: true, PermMembersManage: true, PermLoggedIn: true}
	for _, o := range authtest.Load(t, coreSpec) {
		switch {
		case o.Public:
			if !isPublic(o.Method, o.Example()) {
				t.Errorf("%s(%s %s)是 security: [],但 isPublic 沒列,登入那關會擋", o.ID, o.Method, o.Path)
			}
		case len(o.Scopes) != 1 || !known[Permission(o.Scopes[0])]:
			t.Errorf("%s(%s %s)要宣告恰好一個已知權限,現在是 %q", o.ID, o.Method, o.Path, o.Scopes)
		case isPublic(o.Method, o.Example()):
			t.Errorf("%s(%s %s)在 isPublic 裡,合約卻要求權限", o.ID, o.Method, o.Path)
		}
	}
}

// RequireScopes enforces the scope declared in the contract; no scope, multiple scopes, or security: [] without an isPublic entry are all rejected.
func TestRequireScopes(t *testing.T) {
	s := &Service{}
	viewer := &Principal{Role: "viewer"}
	for _, c := range []struct {
		name     string
		scopes   []string
		declared bool
		p        *Principal
		path     string
		want     int
	}{
		{"declared, role has it", []string{"monitor:view"}, true, viewer, "/api/v1/nodes", 200},
		{"declared, role lacks it", []string{"nodes:onboard"}, true, viewer, "/api/v1/nodes", 403},
		{"logged-in", []string{"logged-in"}, true, &Principal{Role: "admin"}, "/api/v1/me", 200},
		{"undeclared (inherits bearerAuth: [])", []string{}, true, viewer, "/api/v1/nodes", 403},
		{"undeclared denies every role", []string{}, true, &Principal{Role: "operator"}, "/api/v1/nodes", 403},
		{"two scopes", []string{"monitor:view", "nodes:onboard"}, true, viewer, "/api/v1/nodes", 403},
		{"security: [] and public", nil, false, nil, "/api/v1/auth/login", 200},
		{"security: [] but not public", nil, false, viewer, "/api/v1/nodes", 403},
		{"no principal", []string{"monitor:view"}, true, nil, "/api/v1/nodes", 401},
	} {
		h := s.RequireScopes(func(context.Context) ([]string, bool) { return c.scopes, c.declared })(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
		method := http.MethodGet
		if c.path == "/api/v1/auth/login" {
			method = http.MethodPost
		}
		req := httptest.NewRequest(method, c.path, nil)
		if c.p != nil {
			req = req.WithContext(WithPrincipal(req.Context(), c.p))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, rec.Code, c.want)
		}
	}
}
