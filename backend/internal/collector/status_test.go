package collector

import (
	"testing"

	"besu-manager/backend/internal/api"
)

func ptr[T any](v T) *T { return &v }

// Snapshot.Status: derives node status from live values. No ledger layer -> rpc_unreachable; any out-of-sync signal -> syncing, otherwise healthy.
func TestSnapshotStatus(t *testing.T) {
	cases := []struct {
		name string
		snap Snapshot
		want api.NodeStatus
	}{
		{"沒有帳本層 → rpc_unreachable", Snapshot{Hardware: &api.HardwareMetrics{}}, api.NodeStatusRpcUnreachable},
		{"InSync=false → syncing", Snapshot{P2p: &api.P2PStatus{InSync: ptr(false)}, Ledger: &api.LedgerSummary{}}, api.NodeStatusSyncing},
		{"InSync=true 且不落後 → healthy", Snapshot{
			P2p:    &api.P2PStatus{InSync: ptr(true)},
			Ledger: &api.LedgerSummary{BlocksBehind: ptr(0)},
		}, api.NodeStatusHealthy},
		{"BlocksBehind>0 → syncing", Snapshot{Ledger: &api.LedgerSummary{BlocksBehind: ptr(152)}}, api.NodeStatusSyncing},
		{"欄位皆 nil(無訊號)→ healthy", Snapshot{
			P2p:    &api.P2PStatus{InSync: nil},
			Ledger: &api.LedgerSummary{BlocksBehind: nil},
		}, api.NodeStatusHealthy},
	}
	for _, c := range cases {
		if got := c.snap.Status(); got != c.want {
			t.Errorf("%s: Status()=%v, want %v", c.name, got, c.want)
		}
	}
}

// Platform-computed lag: 0 = caught up with the chain head, still healthy; only above 0 does it become syncing.
func TestSnapshotStatusHeadBehind(t *testing.T) {
	s := Snapshot{Ledger: &api.LedgerSummary{BlocksBehind: ptr(0)}, HeadBehind: ptr(0)}
	if got := s.Status(); got != api.NodeStatusHealthy {
		t.Fatalf("HeadBehind=0:%v", got)
	}
	s.HeadBehind = ptr(1)
	if got := s.Status(); got != api.NodeStatusSyncing {
		t.Fatalf("HeadBehind=1:%v", got)
	}
}
