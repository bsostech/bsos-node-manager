package agentstatus

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"besu-manager/agent/internal/api"
	"besu-manager/agent/internal/version"
)

func get(t *testing.T, query string) (int, api.AgentStatus) {
	return getWith(t, nil, query)
}

func getWith(t *testing.T, allow map[string]bool, query string) (int, api.AgentStatus) {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler(allow).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/agent-status"+query, nil))
	var st api.AgentStatus
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, st
}

// Listening = true; after closing = false; no port = null; invalid port = 400; version is always returned.
func TestHandler(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	if code, st := get(t, "?p2pPort="+port); code != 200 || st.P2pOpen == nil || !*st.P2pOpen || st.Version != version.Version {
		t.Fatalf("在聽: %d %+v", code, st)
	}
	if _, st := get(t, ""); st.Runtime == nil || !st.Runtime.Valid() {
		t.Fatalf("runtime must always be reported: %+v", st)
	}
	if _, st := getWith(t, map[string]bool{"8545": true}, "?p2pPort="+port); st.P2pOpen != nil {
		t.Fatalf("ALLOW_PORTS 沒列這個埠應不探: %+v", st)
	}
	if _, st := getWith(t, map[string]bool{port: true}, "?p2pPort="+port); st.P2pOpen == nil || !*st.P2pOpen {
		t.Fatalf("ALLOW_PORTS 有列: %+v", st)
	}
	ln.Close()
	if _, st := get(t, "?p2pPort="+port); st.P2pOpen == nil || *st.P2pOpen {
		t.Fatalf("關掉後: %+v", st)
	}
	if _, st := get(t, ""); st.P2pOpen != nil {
		t.Fatalf("沒帶埠: %+v", st)
	}
	// Boundary ports are valid; ALLOW_PORTS blocks the actual probe so only validation is checked
	for _, ok := range []string{"1", "65535"} {
		if code, st := getWith(t, map[string]bool{"8545": true}, "?p2pPort="+ok); code != 200 || st.P2pOpen != nil {
			t.Errorf("p2pPort=%s 得 %d %+v", ok, code, st)
		}
	}
	for _, bad := range []string{"0", "65536", "abc"} {
		if code, _ := get(t, "?p2pPort="+bad); code != http.StatusBadRequest {
			t.Errorf("p2pPort=%s 得 %d", bad, code)
		}
	}
}

// 127.0.0.1 unreachable, another address reachable -> true (Besu bound to only one interface).
func TestProbeAnyHost(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("沒有 IPv6 loopback")
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	if !probe(context.Background(), []string{"127.0.0.1", "::1"}, port) {
		t.Fatal("::1 在聽應為 true")
	}
}
