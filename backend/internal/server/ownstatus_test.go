package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/prom"
)

// Own node status: never connected (unknown), disconnected after having connected (offline), and
// agent present but RPC unreachable (rpc_unreachable) must be distinct.
func TestOwnStatus(t *testing.T) {
	hostOnly := &collector.Snapshot{Hardware: &api.HardwareMetrics{}}
	full := &collector.Snapshot{Hardware: &api.HardwareMetrics{}, Ledger: &api.LedgerSummary{}}
	cases := []struct {
		name string
		res  collector.Result
		want api.NodeStatus
	}{
		{"還沒問過 / 從未拿到資料", collector.Result{}, api.NodeStatusUnknown},
		{"曾在線、現在拉不到", collector.Result{EverSeen: true}, api.NodeStatusOffline},
		{"agent 在、RPC 打不到", collector.Result{Snap: hostOnly, EverSeen: true}, api.NodeStatusRpcUnreachable},
		{"全通", collector.Result{Snap: full, EverSeen: true}, api.NodeStatusHealthy},
	}
	for _, c := range cases {
		if got := ownStatus(c.res); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

// Capability list: when the agent is offline RPC is "unknown" (null), not "down"; 401 must be
// flagged separately.
func TestCapabilities(t *testing.T) {
	s := &Server{} // Prometheus not configured: metrics are always null
	if c := s.capabilities("n", nil, nil); c.Agent || c.RpcReachable != nil || c.MetricsReachable != nil {
		t.Fatalf("agent 不在線:%+v", c)
	}
	c := s.capabilities("n", nil, &collector.Snapshot{Hardware: &api.HardwareMetrics{}, RpcUnauthorized: true})
	if !c.Agent || c.RpcReachable == nil || *c.RpcReachable || !c.RpcUnauthorized || len(c.MissingRpcApis) != 0 {
		t.Fatalf("401:%+v", c)
	}
	c = s.capabilities("n", nil, &collector.Snapshot{Ledger: &api.LedgerSummary{}, MissingAPIs: []string{"PERM"}})
	if !*c.RpcReachable || len(c.MissingRpcApis) != 1 {
		t.Fatalf("缺 PERM:%+v", c)
	}
	// The stored runtime is what a re-join defaults to, and re-joins happen while the agent is offline.
	docker := "docker"
	if c := s.capabilities("n", &docker, nil); c.AgentRuntime == nil || *c.AgentRuntime != api.AgentRuntimeDocker {
		t.Fatalf("offline agent should keep its stored runtime: %+v", c)
	}
	live := api.AgentRuntimeSystemd
	if c := s.capabilities("n", &docker, &collector.Snapshot{AgentRuntime: &live}); *c.AgentRuntime != api.AgentRuntimeSystemd {
		t.Fatalf("a live report should win over the stored one: %+v", c)
	}
}

// Metrics reachable: with Prometheus configured, the cached up is used only while the agent is
// online; offline is always null (what is left is the value from before the disconnect).
func TestCapabilitiesMetricsReachable(t *testing.T) {
	ready := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/-/ready" { // RunBesuUp asks Alertmanager only after writing the up cache: receiving it means the cache has a value
			select {
			case ready <- struct{}{}:
			default:
			}
			return
		}
		fmt.Fprint(w, `{"status":"success","data":{"result":[{"metric":{"node":"n"},"value":[0,"1"]}]}}`)
	}))
	defer srv.Close()
	pc := prom.New(srv.URL)
	pc.SetAlertmanager(srv.URL + "/-/reload")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pc.RunBesuUp(ctx)
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("背景刷新沒跑完")
	}
	s := &Server{prom: pc}
	if c := s.capabilities("n", nil, &collector.Snapshot{Ledger: &api.LedgerSummary{}}); c.MetricsReachable == nil || !*c.MetricsReachable {
		t.Fatalf("在線應帶 metrics 可達:%+v", c)
	}
	if c := s.capabilities("n", nil, nil); c.MetricsReachable != nil {
		t.Fatalf("不在線應為 null:%+v", c)
	}
}
