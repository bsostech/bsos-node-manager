package collector

import (
	"context"
	"net/http"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
)

// The port is taken from the enode and passed to the agent; the version and probe result go into the snapshot. Before the enode is read, no port is sent and p2pOpen is not accepted.
func TestFetchAgentStatus(t *testing.T) {
	req := &fakeRequester{status: http.StatusOK, body: `{}`, agent: `{"version":"2.0.0","p2pOpen":false}`}
	c := New(nil, req, qbftNode())
	c.known = map[string]string{"node-a": "enode://" + pub64 + "@10.0.0.5:30304?discport=30305"}
	snap := c.fetch(context.Background(), "node-a", Result{}, true)
	if req.agentPath != "/agent-status?p2pPort=30304" || *snap.AgentVersion != "2.0.0" || snap.P2PPort != 30304 || snap.P2POpen == nil || *snap.P2POpen {
		t.Fatalf("path=%s snap=%+v", req.agentPath, snap)
	}
	c.known = map[string]string{}
	snap = c.fetch(context.Background(), "node-a", Result{}, true)
	if req.agentPath != "/agent-status" || snap.P2PPort != 0 || snap.P2POpen != nil {
		t.Fatalf("沒 enode: path=%s snap=%+v", req.agentPath, snap)
	}
}

// The five cases, plus: after a port change the old "was connected" no longer counts, and the memory survives rounds where the agent is offline.
func TestBesuRunning(t *testing.T) {
	yes, no := true, false
	h := int64(1)
	rpcOK := &api.LedgerSummary{ChainHeight: &h}
	now := time.Now()
	cases := []struct {
		name string
		r    Result
		want *bool
	}{
		{"agent 不在線", Result{P2PSeenPort: 30303}, nil},
		{"RPC 打得到", Result{Snap: &Snapshot{Ledger: rpcOK, P2PPort: 30303, P2POpen: &no}}, &yes},
		{"P2P 連上", Result{Snap: &Snapshot{P2PPort: 30303, P2POpen: &yes}}, &yes},
		{"P2P 不通、這埠曾連上", Result{Snap: &Snapshot{P2PPort: 30303, P2POpen: &no}, P2PSeenPort: 30303}, &no},
		{"P2P 不通、從沒連上", Result{Snap: &Snapshot{P2PPort: 30303, P2POpen: &no}}, nil},
		{"不知道埠", Result{Snap: &Snapshot{}}, nil},
		{"換了埠", Result{Snap: &Snapshot{P2PPort: 30304, P2POpen: &no}, P2PSeenPort: 30303}, nil},
	}
	for _, c := range cases {
		got := c.r.BesuRunning()
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	r := nextResult(Result{}, &Snapshot{P2PPort: 30303, P2POpen: &yes}, now)
	r = nextResult(r, nil, now)
	r = nextResult(r, &Snapshot{P2PPort: 30303, P2POpen: &no}, now)
	if r.P2PSeenPort != 30303 || r.BesuRunning() == nil || *r.BesuRunning() {
		t.Fatalf("斷線後回來、Besu 停了應為 false: %+v", r)
	}
}
