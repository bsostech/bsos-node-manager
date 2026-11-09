package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/prom"
	"besu-manager/backend/internal/rpcops"
)

// snapRPC answers the collector's batch for every node not in down: height 16, the IBFT validator
// set, and the node's admin_peers (enodes); down nodes give no response at all.
type snapRPC struct {
	down       map[string]bool
	validators []string
	peers      map[string][]string
}

func (f snapRPC) Batch(_ context.Context, nodeID string, calls []rpcops.Call) ([]rpcops.Result, error) {
	if f.down[nodeID] {
		return nil, rpcops.ErrNoResponse
	}
	out := make([]rpcops.Result, len(calls))
	for i, c := range calls {
		var v any
		switch c.Method {
		case "eth_blockNumber":
			v = "0x10"
		case "ibft_getValidatorsByBlockNumber":
			v = f.validators
		case "admin_peers":
			list := []map[string]string{}
			for _, en := range f.peers[nodeID] {
				list = append(list, map[string]string{"enode": en})
			}
			v = list
		default:
			out[i] = rpcops.Result{Err: &rpcops.RPCError{Code: -32601, Message: "Method not found"}}
			continue
		}
		raw, _ := json.Marshal(v)
		out[i] = rpcops.Result{Raw: raw}
	}
	return out, nil
}

func (snapRPC) ProbeAuth(context.Context, string) string { return "disabled" }

// pollCollector runs one collector round over rpc and waits until every listed node has a result.
func pollCollector(t *testing.T, s *Server, rpc collector.RPC, ids ...string) {
	t.Helper()
	s.col = collector.New(s.store, downRequester{}, rpc)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.col.Run(ctx, time.Hour); close(done) }()
	defer func() { cancel(); <-done }()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		all := true
		for _, id := range ids {
			if _, ok := s.col.Get(id); !ok {
				all = false
			}
		}
		if all {
			return
		}
	}
	t.Fatalf("collector 沒在時限內問完 %v", ids)
}

// heapProm is a Prometheus that reports 42% JVM heap for every listed node; it returns once the
// background refresh has filled the cache.
func heapProm(t *testing.T, nodes ...string) *prom.Client {
	t.Helper()
	ready := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/-/ready" { // asked only after the up / heap cache is written
			select {
			case ready <- struct{}{}:
			default:
			}
			return
		}
		res := []map[string]any{}
		for _, n := range nodes {
			res = append(res, map[string]any{"metric": map[string]string{"node": n}, "value": []any{0, "42"}})
		}
		raw, _ := json.Marshal(res)
		fmt.Fprintf(w, `{"status":"success","data":{"result":%s}}`, raw)
	}))
	t.Cleanup(srv.Close)
	pc := prom.New(srv.URL)
	pc.SetAlertmanager(srv.URL + "/-/reload")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go pc.RunBesuUp(ctx)
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("背景刷新沒跑完")
	}
	return pc
}

// Node list: lastSeen is the same collection time GET /nodes/{id}/cert reports, and missing for a
// node with no snapshot this round; such a node is still tagged validator from the validator set
// reported by another node; JVM heap is shown only for nodes that answered this round.
func TestListNodesLastSeenValidatorHeap(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	for _, id := range []string{"node-a1", "node-a2", "node-a3"} {
		if err := s.store.UpsertOwnNode(ctx, id, "org-test", 8545, 9545, nil); err != nil {
			t.Fatal(err)
		}
	}
	enA2, addrA2 := mkPeer(t, "a2", "10.0.0.2:30303")
	if err := s.store.SetNodeIdentity(ctx, "node-a2", addrA2, enA2); err != nil {
		t.Fatal(err)
	}
	s.prom = heapProm(t, "node-a1", "node-a2")
	pollCollector(t, s, snapRPC{down: map[string]bool{"node-a2": true}, validators: []string{addrA2}}, "node-a1", "node-a2", "node-a3")

	r, err := s.ListNodes(ctx, api.ListNodesRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]api.NodeSummary{}
	for _, n := range r.(api.ListNodes200JSONResponse).Nodes {
		got[n.Id] = n
	}
	a1, a2 := got["node-a1"], got["node-a2"]
	cr, err := s.GetNodeCert(ctx, api.GetNodeCertRequestObject{Id: "node-a1"})
	if err != nil {
		t.Fatal(err)
	}
	if cert := cr.(api.GetNodeCert200JSONResponse); a1.LastSeen == nil || cert.LastSeen == nil || !a1.LastSeen.Equal(*cert.LastSeen) {
		t.Errorf("a1 lastSeen 應與憑證頁相同:%v %v", a1.LastSeen, cert.LastSeen)
	}
	if a2.LastSeen != nil {
		t.Errorf("a2 這輪沒快照不該有 lastSeen:%v", a2.LastSeen)
	}
	if a2.IsValidator == nil || !*a2.IsValidator {
		t.Errorf("a2 沒快照也應由驗證者名單標為驗證者:%v", a2.IsValidator)
	}
	if a1.JvmHeapPercent == nil || *a1.JvmHeapPercent != 42 || a2.JvmHeapPercent != nil {
		t.Errorf("heap 只給這輪有回的節點:a1=%v a2=%v", a1.JvmHeapPercent, a2.JvmHeapPercent)
	}
}
