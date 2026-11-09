package server

import (
	"testing"
	"time"

	"besu-manager/backend/internal/rpcops"
)

func TestTxPoolResponse(t *testing.T) {
	now := time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)
	oldest := now.Add(-25 * time.Minute)
	to := "0xcc"
	p := rpcops.TxPool{
		LocalCount: 3, RemoteCount: 1, Oldest: &oldest,
		Senders: []rpcops.TxSender{
			{Address: "0xaaaa", NextNonce: 42, Txs: []rpcops.PoolTx{{Hash: "0x1", Nonce: 43, To: &to, AddedAt: oldest, Local: true}, {Hash: "0x2", Nonce: 44}}},
			{Address: "0xbbbb", NextNonce: 7, Txs: []rpcops.PoolTx{{Hash: "0x3", Nonce: 7}, {Hash: "0x4", Nonce: 8}}},
		},
	}
	out := txPoolResponse(p, now)
	if out.Totals.Pending != 2 || out.Totals.Queued != 2 || !out.Totals.OldestAddedAt.Equal(oldest) {
		t.Fatalf("totals = %+v", out.Totals)
	}
	if out.MaxSize != nil || out.Rejected != nil || out.Syncing || out.LocalCount != 3 {
		t.Fatalf("pool = %+v", out)
	}
	if s := out.Senders[0]; s.NextNonce != 42 || len(s.Txs) != 2 || s.Txs[0].To == nil || *s.Txs[0].To != "0xcc" || !s.Txs[0].Local || s.Txs[1].To != nil {
		t.Fatalf("sender = %+v", s)
	}

	// syncing: everything else empty, senders is an empty array, not null
	out = txPoolResponse(rpcops.TxPool{Syncing: true}, now)
	if !out.Syncing || out.Senders == nil || len(out.Senders) != 0 || out.Totals.OldestAddedAt != nil {
		t.Fatalf("syncing = %+v", out)
	}
}
