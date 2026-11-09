package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/store"
)

// Metric fields in the node list: included when available, null when they cannot be fetched (never
// 0). Host metrics do not depend on RPC, so CPU and disk are present even when RPC is unreachable.
func TestSummarizeMetrics(t *testing.T) {
	now := time.Now()
	cpu, disk := float32(42.5), float32(61)
	pending, behind, h := 3, 7, int64(100)
	ver := "besu/v25.4.1"
	used, total := int64(1), int64(4)
	hw := &api.HardwareMetrics{CpuPercent: &cpu, DiskUsedPercent: &disk, MemUsedBytes: &used, MemTotalBytes: &total}

	full := summarize(store.Node{ID: "n"}, collector.Result{EverSeen: true, Snap: &collector.Snapshot{
		Hardware: hw, ClientVersion: &ver,
		Ledger: &api.LedgerSummary{ChainHeight: &h, TxpoolPending: &pending, BlocksBehind: &behind},
	}}, now)
	if full.CpuPercent == nil || full.DiskUsedPercent == nil || full.TxpoolPending == nil || full.LastSeen == nil || full.ClientVersion == nil {
		t.Fatalf("全通時欄位不該是 null:%+v", full)
	}
	if full.BlocksBehind == nil || *full.BlocksBehind != 7 {
		t.Fatalf("blocksBehind:%v", full.BlocksBehind)
	}
	if *full.CpuPercent != cpu || *full.DiskUsedPercent != disk || *full.TxpoolPending != 3 || *full.ClientVersion != ver {
		t.Fatalf("全通:%+v", full)
	}

	hostOnly := summarize(store.Node{ID: "n"}, collector.Result{EverSeen: true, Snap: &collector.Snapshot{Hardware: hw}}, now)
	if hostOnly.CpuPercent == nil || hostOnly.DiskUsedPercent == nil || hostOnly.MemPercent == nil || *hostOnly.MemPercent != 25 {
		t.Fatalf("RPC 打不到也該有主機指標:%+v", hostOnly)
	}
	if hostOnly.BlocksBehind != nil || hostOnly.TxpoolPending != nil || hostOnly.ClientVersion != nil || hostOnly.ChainHeight != nil {
		t.Fatalf("RPC 打不到時 Besu 欄位要是 null:%+v", hostOnly)
	}

	offline := summarize(store.Node{ID: "n"}, collector.Result{EverSeen: true}, now)
	if offline.CpuPercent != nil || offline.DiskUsedPercent != nil || offline.MemPercent != nil || offline.TxpoolPending != nil || offline.ClientVersion != nil || offline.LastSeen != nil {
		t.Fatalf("離線全部 null:%+v", offline)
	}
}

// When total memory is 0 (agent cannot read it), do not divide: memory percentage is null.
func TestSummarizeMemPercentZeroTotal(t *testing.T) {
	used, zero := int64(1), int64(0)
	got := summarize(store.Node{ID: "n"}, collector.Result{EverSeen: true, Snap: &collector.Snapshot{Hardware: &api.HardwareMetrics{MemUsedBytes: &used, MemTotalBytes: &zero}}}, time.Now())
	if got.MemPercent != nil {
		t.Fatalf("總量 0 應為 null: %v", *got.MemPercent)
	}
}

// No Prometheus configured (dev environment) and no agent reply yet: all 24 buckets of the last 24
// hours are unknown, Besu memory and host memory are null.
func TestListNodesWithoutPrometheus(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	if err := s.store.UpsertOwnNode(ctx, "node-a1", "org-test", 8545, 9545, nil); err != nil {
		t.Fatal(err)
	}
	s.col = collector.New(s.store, downRequester{}, allowRPC{})
	r, err := s.ListNodes(ctx, api.ListNodesRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	n := r.(api.ListNodes200JSONResponse).Nodes[0]
	if n.Availability24h == nil || len(*n.Availability24h) != 24 || (*n.Availability24h)[0] != api.HourUnknown || (*n.Availability24h)[23] != api.HourUnknown || n.JvmHeapPercent != nil || n.MemPercent != nil {
		t.Fatalf("沒設 Prometheus: %+v", n)
	}
}

// No healthy source node (collector has not fetched from any node yet): /metrics emits no
// bsos_chain_*, rather than passing off 0 as data.
func TestChainGaugesWithoutSource(t *testing.T) {
	s, _ := accessTestServer(t)
	s.col = collector.New(s.store, downRequester{}, allowRPC{})
	if cs, ok := s.currentChainStats(context.Background()); ok {
		t.Fatalf("沒有來源節點不該有鏈層級數字:%+v", cs)
	}
	ch := make(chan prometheus.Metric, 16)
	(&platformCollector{srv: s}).Collect(ch)
	close(ch)
	for m := range ch {
		if strings.HasPrefix(m.Desc().String(), `Desc{fqName: "bsos_chain_`) {
			t.Fatalf("不該吐 %s", m.Desc())
		}
	}
}
