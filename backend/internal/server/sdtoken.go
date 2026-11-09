package server

import (
	"crypto/subtle"
	"net/http"
)

// sdBearerOK is the bearer gate shared by the four internal endpoints (/sd/targets, /metrics,
// /alerts/webhook, /agent-proxy/...). They all sit on the same router and are also exposed on
// 80/443, so they protect themselves with this rather than relying on port isolation.
//
// An empty token = the endpoint is disabled entirely (404, not 401: something not configured should
// not look like "wrong password"). Returns false when the response has already been written; the
// caller just returns.
func sdBearerOK(w http.ResponseWriter, r *http.Request, token string) bool {
	if token == "" {
		http.NotFound(w, r)
		return false
	}
	// Constant-time comparison: a byte-by-byte comparison would leak through response time how many
	// characters of the token were guessed right
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}
