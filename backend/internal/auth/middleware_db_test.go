package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"besu-manager/backend/internal/store"
)

// When the database is unreachable, a request carrying a session cookie is not "not logged in": APIs that need login return 503 (so the frontend does not kick the user back to the login page),
// public paths and frontend pages pass as usual; requests without a cookie still get 401.
func TestMiddlewareDBDownIsNotUnauthorized(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/x?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s := New(store.New(pool), "org-test", false)
	s.SetRouteMatch(func(_, path string) bool { return path == "/api/v1/nodes" })
	h := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	for _, tc := range []struct {
		path   string
		cookie bool
		want   int
	}{
		{"/api/v1/nodes", true, http.StatusServiceUnavailable},
		{"/api/v1/nodes", false, http.StatusUnauthorized},
		{"/install.sh", true, http.StatusOK},
		{"/healthz", true, http.StatusOK},
		{"/nodes/node-a1", true, http.StatusOK}, // SPA path
	} {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		if tc.cookie {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: "some-session", HttpOnly: true, Secure: true})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("GET %s cookie=%v = %d, want %d", tc.path, tc.cookie, w.Code, tc.want)
		}
		// the error carries a reason, which the UI translates
		if want := map[int]string{http.StatusUnauthorized: "login_required", http.StatusServiceUnavailable: "db_unavailable"}[tc.want]; want != "" {
			var e struct{ Reason string }
			if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || e.Reason != want {
				t.Errorf("GET %s reason %q, want %q: %s", tc.path, e.Reason, want, w.Body)
			}
		}
	}
}

// Database unreachable during login: return the original error; it must not become ErrBadCredentials (that would be treated as wrong credentials and log a failed login).
func TestAuthenticateDBDownIsNotBadCredentials(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/x?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s := New(store.New(pool), "org-test", false)
	_, err = s.Authenticate(context.Background(), "admin", "whatever-password")
	if err == nil || errors.Is(err, ErrBadCredentials) {
		t.Fatalf("資料庫錯誤被當成帳密錯: %v", err)
	}
}
