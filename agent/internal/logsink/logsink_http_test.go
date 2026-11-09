package logsink

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"besu-manager/agent/internal/api"
)

// GET /logs: after must be a non-negative integer (400 otherwise); after and limit page through the buffer.
func TestServeHTTP(t *testing.T) {
	s := New()
	for _, l := range []string{"one", "two", "three"} {
		s.add(l)
	}
	get := func(query string) (int, api.NodeLogs) {
		t.Helper()
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/logs"+query, nil))
		var out api.NodeLogs
		if rec.Code == http.StatusOK {
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("Content-Type = %q", ct)
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, out
	}
	for _, q := range []string{"?after=-1", "?after=abc", "?after=1.5"} {
		if code, _ := get(q); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", q, code)
		}
	}
	if code, out := get(""); code != http.StatusOK || len(out.Lines) != 3 || out.Lines[0].Text != "one" || out.Next != 3 || out.Epoch != s.epoch {
		t.Fatalf("全部: %d %+v", code, out)
	}
	if _, out := get("?after=1&limit=1"); len(out.Lines) != 1 || out.Lines[0].Seq != 2 || out.Lines[0].Text != "two" || out.Next != 2 {
		t.Fatalf("after=1&limit=1: %+v", out)
	}
	if _, out := get("?limit=junk"); len(out.Lines) != 3 {
		t.Fatalf("limit 壞值應走預設: %+v", out)
	}
}

// ValidateAddr on this host's real interfaces: a non-loopback interface IP is refused unless that interface
// is an internal bridge (sysfs is faked so the result does not depend on the machine).
func TestValidateAddrRealInterfaces(t *testing.T) {
	var name, ip string
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
				name, ip = ifc.Name, n.IP.String()
			}
		}
	}
	if name == "" {
		t.Skip("no non-loopback IPv4 interface")
	}
	root := t.TempDir()
	route := filepath.Join(root, "route")
	if err := os.WriteFile(route, []byte("Iface\tDestination\tGateway\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldNet, oldRoute := sysNet, procRoute
	sysNet, procRoute = root, route
	defer func() { sysNet, procRoute = oldNet, oldRoute }()
	addr := net.JoinHostPort(ip, "5170")

	if err := ValidateAddr("127.0.0.1:5170"); err != nil {
		t.Fatalf("loopback 應放行: %v", err)
	}
	// The unspecified address is refused on its own, even if an interface list were to report it.
	for _, a := range []string{"0.0.0.0:5170", "[::]:5170"} {
		if err := validateAddr(a, func() (map[string]bool, error) { return map[string]bool{"0.0.0.0": true, "::": true}, nil }); err == nil {
			t.Fatalf("%s 應被拒", a)
		}
	}
	if err := ValidateAddr(addr); err == nil {
		t.Fatalf("%s(%s)不是 bridge,應被拒", addr, name)
	}
	if err := os.MkdirAll(filepath.Join(root, name, "bridge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAddr(addr); err != nil {
		t.Fatalf("%s 是內部 bridge,應放行: %v", name, err)
	}
	// the same bridge carrying the default route is external
	if err := os.WriteFile(route, []byte("Iface\tDestination\tGateway\n"+name+"\t00000000\t0100A8C0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAddr(addr); err == nil {
		t.Fatalf("承載預設路由的 %s 應被拒", name)
	}
}
