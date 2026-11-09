package server

import (
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/collector"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// Tx pool metrics: usage is emitted only when there is a limit; oldest is 0 for an empty pool and
// the seconds waited when there are transactions; nothing is emitted when RPC is unreachable or
// TXPOOL is not enabled.
func TestTxpoolMetrics(t *testing.T) {
	now := time.Date(2026, 10, 29, 5, 1, 0, 0, time.UTC)
	at := now.Add(-75 * time.Second)
	pending := func(n int) *api.LedgerSummary { return &api.LedgerSummary{TxpoolPending: &n} }
	limit := 400
	collect := func(s *collector.Snapshot) map[string]float64 {
		ch := make(chan prometheus.Metric, 4)
		txpoolMetrics(ch, "node-a", s, now)
		close(ch)
		out := map[string]float64{}
		for m := range ch {
			var d dto.Metric
			if err := m.Write(&d); err != nil || d.Label[0].GetValue() != "node-a" {
				t.Fatalf("%v %v", err, d.Label)
			}
			out[m.Desc().String()] = d.GetGauge().GetValue()
		}
		return out
	}
	got := collect(&collector.Snapshot{Ledger: pending(100), TxpoolMax: &limit, TxpoolListed: true, TxpoolOldest: &at})
	if len(got) != 2 || got[txpoolUsageDesc.String()] != 25 || got[txpoolOldestDesc.String()] != 75 {
		t.Fatalf("sequenced %v", got)
	}
	got = collect(&collector.Snapshot{Ledger: pending(0), TxpoolListed: true})
	if v, ok := got[txpoolOldestDesc.String()]; len(got) != 1 || !ok || v != 0 {
		t.Fatalf("layered 空池 %v", got)
	}
	for _, s := range []*collector.Snapshot{nil, {}, {Ledger: &api.LedgerSummary{}, TxpoolListed: true}} {
		if got := collect(s); len(got) != 0 {
			t.Fatalf("不該出 %v", got)
		}
	}
}
