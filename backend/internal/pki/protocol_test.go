package pki

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// Protocol version missing or too old: 426 with a reason, and the rejection time is recorded; new enough is
// let through (the rejection record is cleared by handleWS once connected).
func TestAgentProtocolOK(t *testing.T) {
	s := &Service{}
	check := func(proto string) (*httptest.ResponseRecorder, bool) {
		r := httptest.NewRequest(http.MethodGet, "/pki/v1/ws", nil)
		if proto != "" {
			r.Header.Set(agentProtocolHeader, proto)
		}
		w := httptest.NewRecorder()
		return w, s.agentProtocolOK(w, r, "node-a1")
	}
	for _, p := range []string{"", "0", "abc", strconv.Itoa(MinAgentProtocol - 1)} {
		w, ok := check(p)
		if ok || w.Code != http.StatusUpgradeRequired || !strings.Contains(w.Body.String(), "upgrade-agent.sh") {
			t.Fatalf("協定 %q 應被拒: ok=%v code=%d body=%q", p, ok, w.Code, w.Body.String())
		}
	}
	if s.AgentOutdatedAt("node-a1") == nil || s.AgentOutdatedAt("node-a2") != nil {
		t.Fatal("被拒的那台要記下時間,其他台不受影響")
	}
	for _, p := range []string{strconv.Itoa(MinAgentProtocol), strconv.Itoa(MinAgentProtocol + 1)} {
		if w, ok := check(p); !ok || w.Code != http.StatusOK {
			t.Fatalf("協定 %s 應放行: ok=%v code=%d", p, ok, w.Code)
		}
	}
}
