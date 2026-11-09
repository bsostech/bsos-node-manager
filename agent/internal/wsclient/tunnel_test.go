package wsclient

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hashicorp/yamux"
)

// End to end over the path the platform actually uses: the platform opens a stream on the yamux session and
// sends HTTP with X-Target -> the agent's allowlist -> forwarded to the node locally. Besu metrics deliberately
// listen on a random port (not 9545) to verify the agent code hard-codes no Besu port.
// The platform-side transport mirrors agentTransport in backend/internal/pki/mux.go (it cannot be imported
// across modules).
func TestTunnelEndToEnd(t *testing.T) {
	besu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = io.WriteString(w, "ethereum_blockchain_height 42\n")
	}))
	defer besu.Close()
	u, _ := url.Parse(besu.URL)
	_, besuPort, _ := net.SplitHostPort(u.Host)

	host := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = io.WriteString(w, "bsos_node_cpu_percent 1\n")
	})

	// one yamux session: the agent side runs the allowlist, the platform side uses it as a transport.
	run := func(t *testing.T, allowPorts map[string]bool) *http.Client {
		a, b := net.Pipe()
		agentSess, err := yamux.Server(a, muxConfig())
		if err != nil {
			t.Fatal(err)
		}
		platformSess, err := yamux.Client(b, muxConfig())
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: allowlist(host, allowPorts)}
		go srv.Serve(agentSess)
		t.Cleanup(func() { srv.Close(); agentSess.Close(); platformSess.Close() })
		return &http.Client{Transport: &http.Transport{
			DialContext: func(context.Context, string, string) (net.Conn, error) { return platformSess.Open() },
		}}
	}
	get := func(c *http.Client, path, target string) (int, string, string) {
		req, _ := http.NewRequest(http.MethodGet, "http://agent"+path, nil)
		if target != "" {
			req.Header.Set(targetHeader, target)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(b)
	}

	c := run(t, nil)
	if code, ct, body := get(c, "/metrics", "127.0.0.1:"+besuPort); code != 200 || ct != "text/plain; version=0.0.4" || !strings.Contains(body, "ethereum_blockchain_height 42") {
		t.Fatalf("Besu metrics via tunnel: %d %q %q", code, ct, body)
	}
	if code, _, body := get(c, "/host-metrics", ""); code != 200 || !strings.Contains(body, "bsos_node_cpu_percent") {
		t.Fatalf("host metrics: %d %q", code, body)
	}
	if code, _, _ := get(c, "/metrics", "10.0.0.1:"+besuPort); code != http.StatusForbidden {
		t.Fatalf("non-loopback target: %d", code)
	}
	if code, _, _ := get(c, "/api/v1/snapshot", ""); code != http.StatusNotFound {
		t.Fatalf("old snapshot path: %d", code)
	}
	// nothing listening on the target port (e.g. the port the platform recorded differs from Besu's actual
	// one): the reverse proxy returns 502, which Prometheus records as up == 0.
	// Port 1 is used: a privileged port nobody listens on in tests; more reliable than "open then close a
	// random port", which another process could grab.
	deadPort := "1"
	if code, _, _ := get(c, "/metrics", "127.0.0.1:"+deadPort); code != http.StatusBadGateway {
		t.Fatalf("nothing listening: %d", code)
	}

	// ALLOW_PORTS restricts: unlisted ports get 403, listed ones are forwarded.
	c = run(t, map[string]bool{besuPort: true})
	if code, _, _ := get(c, "/metrics", "127.0.0.1:"+besuPort); code != 200 {
		t.Fatalf("allowed port: %d", code)
	}
	if code, _, _ := get(c, "/metrics", "127.0.0.1:"+deadPort); code != http.StatusForbidden {
		t.Fatalf("port outside ALLOW_PORTS: %d", code)
	}
}
