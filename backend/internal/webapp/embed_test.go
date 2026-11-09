package webapp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
)

// When index.html (a real build output, or the hard-coded placeholder page when never built) exists, the root
// path should return 200.
func TestHandlerServesIndex(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// When dist/ has no real index.html (this repo commits only .gitkeep; see ../.gitignore), return the
// hard-coded placeholder page rather than a bare 404. This test assumes the test environment has not run npm
// run build (true for CI and ordinary go test).
func TestHandlerServesNotBuiltMessageWhenNoIndexHTML(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "npm run build") {
		t.Fatalf("want 提示 npm run build 的寫死訊息,got: %s", rec.Body.String())
	}
}

// SPA fallback: a non-existent path (a frontend client-side route) should return the index.html content, not 404.
func TestHandlerFallsBackToIndexForUnknownPath(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/some/client-side/route", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200(fallback to index.html)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("body 應為 index.html 內容,got: %s", rec.Body.String())
	}
}

// index.html (including the copy served by SPA fallback) is always no-cache; embed.FS has no ModTime, so
// conditional GETs do not work, and this header is the only way to at least make the browser check every time
// instead of sticking with an old index.html and never refreshing.
func TestHandlerIndexNoCache(t *testing.T) {
	for _, path := range []string{"/", "/some/client-side/route"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		Handler().ServeHTTP(rec, req)
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q, want no-cache", path, got)
		}
	}
}

// An existing static file returns the file itself, not the SPA fallback. Handler reads the embedded dist
// directly with no seam for injecting an FS, so we borrow dist/.gitkeep (an empty file always present in the
// repo) as that static file: empty body, not index.html or the placeholder page.
func TestHandlerServesExistingStaticFile(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/.gitkeep", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("status = %d, body = %q,want 200 與空 body", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache(不在 assets/ 底下)", got)
	}
}

// Non-GET/HEAD must be rejected, so requests like POST /api/... are not accidentally swallowed as static files.
func TestHandlerRejectsNonGet(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// Unregistered /api/ paths (a feature this version lacks, or a typo) return 404 JSON; returning index.html +
// 200 would make callers think it succeeded.
func TestHandlerUnknownAPIIs404JSON(t *testing.T) {
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/nope"},
		{http.MethodGet, "/api"},
		{http.MethodHead, "/api/v1/external-orgs"},
		{http.MethodPost, "/api/v1/nope"},
		{http.MethodDelete, "/api/v1/nodes/x/nope"},
	} {
		rec := httptest.NewRecorder()
		Handler().ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s status = %d, want 404", c.method, c.path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s %s Content-Type = %q, want application/json", c.method, c.path, ct)
		}
		if c.method == http.MethodHead {
			continue // HEAD has no body
		}
		// Same shape as other API errors: code, English message, reason
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s %s body 不是 JSON: %v(%s)", c.method, c.path, err, rec.Body.String())
			continue
		}
		if len(body) != 3 || body["code"] != "not_found" || body["reason"] != "route_not_found" || body["message"] != apierr.Message(api.ErrorReasonRouteNotFound, nil) {
			t.Errorf("%s %s body = %v,want {code: not_found, reason: route_not_found, message: 英文句型}", c.method, c.path, body)
		}
	}
	// Only the /api/ level counts; frontend routes that happen to start with api are unaffected
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/apiary", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /apiary status = %d, want 200(SPA fallback)", rec.Code)
	}
}

// Path exists, method does not: 405 JSON, with Allow listing only the methods this path has.
func TestMethodNotAllowedJSON(t *testing.T) {
	h := MethodNotAllowed(func(method, path string) bool { return path == "/api/v1/alert-rules" && method == http.MethodGet })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/alert-rules", nil))
	var body struct{ Code, Reason string }
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Values("Allow")[0] != "GET" || len(rec.Header().Values("Allow")) != 1 {
		t.Fatalf("status = %d, Allow = %v", rec.Code, rec.Header().Values("Allow"))
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != "invalid" || body.Reason != "method_not_allowed" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}
