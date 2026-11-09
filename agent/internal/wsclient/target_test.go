package wsclient

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// X-Target forwarding: loopback is forwarded as-is, non-loopback and ports outside ALLOW_PORTS are always
// 403, and the forwarded request does not carry X-Target.
func TestAllowlistTarget(t *testing.T) {
	besu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(targetHeader) != "" {
			t.Error("X-Target 不該被轉到節點")
		}
		b, _ := io.ReadAll(r.Body)
		w.Write(append([]byte("echo:"), b...))
	}))
	defer besu.Close()
	u, _ := url.Parse(besu.URL) // 127.0.0.1:<port>
	_, port, _ := net.SplitHostPort(u.Host)

	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("帶 X-Target 不該打到 router") })
	cases := []struct {
		name, target string
		ports        map[string]bool
		want         int
	}{
		{"loopback", u.Host, nil, 200},
		{"loopback allowed port", u.Host, map[string]bool{port: true}, 200},
		{"port not allowed", u.Host, map[string]bool{"1": true}, 403},
		{"not loopback", "10.0.0.1:" + port, nil, 403},
		{"hostname", "localhost:" + port, nil, 403},
		{"no port", "127.0.0.1", nil, 403},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "http://agent/", strings.NewReader("ping"))
		req.Header.Set(targetHeader, c.target)
		allowlist(h, c.ports).ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: status=%d want %d", c.name, rec.Code, c.want)
		}
		if c.want == 200 && rec.Body.String() != "echo:ping" {
			t.Errorf("%s: body=%q", c.name, rec.Body.String())
		}
	}
}

// Without X-Target only the agent's own paths pass (host metrics, log buffer, log blocks); the rest are 404.
func TestAllowlistOwnPaths(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(r.URL.Path)) })
	for path, want := range map[string]int{"/host-metrics": 200, "/logs": 200, "/logblocks": 200, "/": 404, "/debug/pprof": 404} {
		rec := httptest.NewRecorder()
		allowlist(h, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://agent"+path, nil))
		if rec.Code != want {
			t.Errorf("%s: status=%d want %d", path, rec.Code, want)
		}
	}
}
