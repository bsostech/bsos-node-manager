package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"besu-manager/backend/internal/store"
)

func TestBuildSDTargets(t *testing.T) {
	nodes := []store.Node{
		{ID: "node-a1", Org: "org-A"},
		{ID: "node-a2", Org: "org-A"}, // not joined (no issuance record) -> skipped
		{ID: "node-a3", Org: "org-A"},
	}
	enrolled := map[string]bool{"node-a1": true, "node-a3": true}
	out := buildSDTargets(nodes, enrolled, "platform:8080", "http")
	if len(out) != 4 {
		t.Fatalf("目標數 = %d, 期望 4(兩台 × host/besu,跳過未 join 者)", len(out))
	}
	// The target is the platform itself; which node is scraped is decided by __metrics_path__
	// (nodes have no externally listening port)
	if out[0].Targets[0] != "platform:8080" {
		t.Errorf("target 應是平台本身: %q", out[0].Targets[0])
	}
	if out[0].Labels["__metrics_path__"] != "/agent-proxy/node-a1/metrics" {
		t.Errorf("metrics 路徑不對: %q", out[0].Labels["__metrics_path__"])
	}
	if out[0].Labels["__scheme__"] != "http" {
		t.Errorf("scheme 不對: %q", out[0].Labels["__scheme__"])
	}
	if out[0].Labels["node"] != "node-a1" || out[0].Labels["org"] != "org-A" || out[0].Labels["source"] != "host" {
		t.Errorf("label 不對: %+v", out[0].Labels)
	}
	if out[1].Labels["__metrics_path__"] != "/agent-proxy/node-a1/besu-metrics" || out[1].Labels["source"] != "besu" {
		t.Errorf("Besu 原生 metrics 目標不對: %+v", out[1].Labels)
	}
}

// Isolated nodes (certificate rejected, not a network problem) must not be fed to Prometheus.
func TestBuildSDTargetsSkipsQuarantined(t *testing.T) {
	now := time.Now().UTC()
	nodes := []store.Node{
		{ID: "node-a1", Org: "org-A"},
		{ID: "node-a2", Org: "org-A", QuarantinedAt: &now}, // quarantined -> skipped
	}
	enrolled := map[string]bool{"node-a1": true, "node-a2": true}
	out := buildSDTargets(nodes, enrolled, "platform:8080", "http")
	if len(out) != 2 {
		t.Fatalf("目標數 = %d, 期望 2(node-a1 的 host/besu,跳過已隔離者)", len(out))
	}
	if out[0].Labels["node"] != "node-a1" {
		t.Errorf("不該包含被隔離的 node-a2: %+v", out)
	}
}

func TestSDTargetsDisabledWhenNoToken(t *testing.T) {
	h := SDTargetsHandler(nil, "") // empty token = disabled
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/sd/targets", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("未設 token 應 404, 得 %d", rec.Code)
	}
}

func TestSDTargetsRejectsBadToken(t *testing.T) {
	h := SDTargetsHandler(nil, "secret")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sd/targets", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	h(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("錯 token 應 401, 得 %d", rec.Code)
	}
}
