package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The routeMatch pass-through must only cover GET/HEAD requests with no matching route, which are guaranteed to fall
// through to the SPA fallback. It must not degrade into a crude "anything outside /api passes" rule, or a real route
// added later on a whim, such as /debug/pprof, would be mistaken for public. These tests pin that boundary.
func TestMiddlewareRouteMatchFallback(t *testing.T) {
	called := func() (http.Handler, *bool) {
		hit := false
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hit = true
			w.WriteHeader(http.StatusOK)
		}), &hit
	}

	// GET with no matching route -> passes (treated as SPA fallback).
	t.Run("unmatched GET passes through", func(t *testing.T) {
		s := &Service{routeMatch: func(method, path string) bool { return false }}
		next, hit := called()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/nodes/abc/settings", nil)
		s.Middleware(next).ServeHTTP(rec, req)
		if !*hit || rec.Code != http.StatusOK {
			t.Fatalf("want pass-through 200, got hit=%v code=%d", *hit, rec.Code)
		}
	})

	// A route that is actually registered (even outside /api) -> does not pass; without a session it gets 401.
	// This is the key case being guarded: if a route like /debug/pprof is added later, it must not be mistaken for public.
	t.Run("matched route still requires auth", func(t *testing.T) {
		s := &Service{routeMatch: func(method, path string) bool { return true }}
		next, hit := called()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/debug/pprof", nil)
		s.Middleware(next).ServeHTTP(rec, req)
		if *hit || rec.Code != http.StatusUnauthorized {
			t.Fatalf("want 401 blocked, got hit=%v code=%d", *hit, rec.Code)
		}
	})

	// routeMatch not wired (nil) -> conservative default, no pass-through (a safe default; it is not what does the blocking).
	t.Run("nil routeMatch defaults to blocked", func(t *testing.T) {
		s := &Service{}
		next, hit := called()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/nodes/abc/settings", nil)
		s.Middleware(next).ServeHTTP(rec, req)
		if *hit || rec.Code != http.StatusUnauthorized {
			t.Fatalf("want 401 when routeMatch unset, got hit=%v code=%d", *hit, rec.Code)
		}
	})

	// This pass-through only accepts GET/HEAD; a POST to an unrouted path must not pass (the SPA does not handle that method).
	t.Run("non-GET to unmatched path still requires auth", func(t *testing.T) {
		s := &Service{routeMatch: func(method, path string) bool { return false }}
		next, hit := called()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/nodes/abc/settings", nil)
		s.Middleware(next).ServeHTTP(rec, req)
		if *hit || rec.Code != http.StatusUnauthorized {
			t.Fatalf("want 401 for non-GET, got hit=%v code=%d", *hit, rec.Code)
		}
	})

	// /metrics is an actually registered route, and the test above shows the crude routeMatch rule does not let it through.
	// Prometheus (no session) must still reach it, which works because it is explicitly listed in isPublic and the handler
	// verifies the SD_TOKEN bearer itself (as /sd/targets does), not through the routeMatch fallback. This pins the isPublic entry itself.
	t.Run("/metrics is explicitly public (bearer-gated in handler, not session)", func(t *testing.T) {
		s := &Service{routeMatch: func(method, path string) bool { return true }}
		next, hit := called()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		s.Middleware(next).ServeHTTP(rec, req)
		if !*hit || rec.Code != http.StatusOK {
			t.Fatalf("want /metrics to pass through without session, got hit=%v code=%d", *hit, rec.Code)
		}
	})

	// /agent-proxy/{nodeID}/metrics carries a node id, so isPublic passes it by prefix (not listed one by one); the token is likewise verified in the handler,
	// and stripped before forwarding to the agent (see server/agentproxy.go).
	t.Run("/agent-proxy/... is explicitly public (bearer-gated in handler, not session)", func(t *testing.T) {
		s := &Service{routeMatch: func(method, path string) bool { return true }}
		next, hit := called()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/agent-proxy/node-a1/metrics", nil)
		s.Middleware(next).ServeHTTP(rec, req)
		if !*hit || rec.Code != http.StatusOK {
			t.Fatalf("want /agent-proxy to pass through without session, got hit=%v code=%d", *hit, rec.Code)
		}
	})

	// POST does not get the routeMatch fallback (it only passes GET/HEAD), so /alerts/webhook is reachable only because it is explicitly
	// listed in isPublic (Alertmanager has no session cookie). Again the handler verifies the bearer itself; the pass-through is not the guard.
	t.Run("/alerts/webhook is explicitly public (bearer-gated in handler, not session)", func(t *testing.T) {
		s := &Service{routeMatch: func(method, path string) bool { return true }}
		next, hit := called()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/alerts/webhook", nil)
		s.Middleware(next).ServeHTTP(rec, req)
		if !*hit || rec.Code != http.StatusOK {
			t.Fatalf("want /alerts/webhook to pass through without session, got hit=%v code=%d", *hit, rec.Code)
		}
	})
}

// Encoded slashes are rejected before any check: chi routes on RawPath, where "/nodes/a%2Fb" is a single {id} segment,
// but the decoded Path has two segments and matches no route, so it would be treated as nonexistent and skip permission checks (GET would even skip login).
func TestMiddlewareRejectsEncodedSlash(t *testing.T) {
	s := &Service{routeMatch: func(_, path string) bool { return path == "/api/v1/nodes/x" }}
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		for _, u := range []string{"/api/v1/nodes/a%2Fb", "/api/v1/nodes/a%2fb"} {
			hit := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hit = true })
			rec := httptest.NewRecorder()
			s.Middleware(next).ServeHTTP(rec, httptest.NewRequest(m, u, nil))
			if hit || rec.Code != http.StatusBadRequest {
				t.Fatalf("%s %s: want 400 before next, got hit=%v code=%d", m, u, hit, rec.Code)
			}
		}
	}
}
