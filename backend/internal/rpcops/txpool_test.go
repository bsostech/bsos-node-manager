package rpcops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"besu-manager/backend/internal/pki"
	"besu-manager/backend/internal/store"
)

// rpcFunc: a fake node answering by method and params; when returning an error object, result holds *RPCError.
type rpcFunc func(method string, params []json.RawMessage) any

func (f rpcFunc) GetNode(_ context.Context, id string) (*store.Node, error) {
	return &store.Node{ID: id, RpcPort: 8545}, nil
}

func (f rpcFunc) PostLoopback(_ context.Context, _ string, _ int, body []byte, _ http.Header) (int, []byte, error) {
	var reqs []struct {
		ID     int               `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &reqs); err != nil {
		return 400, nil, nil
	}
	out := make([]map[string]any, len(reqs))
	for i, r := range reqs {
		item := map[string]any{"jsonrpc": "2.0", "id": r.ID}
		if e, ok := f(r.Method, r.Params).(*RPCError); ok {
			item["error"] = e
		} else {
			item["result"] = f(r.Method, r.Params)
		}
		out[i] = item
	}
	b, _ := json.Marshal(out)
	return 200, b, nil
}

var t0 = time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)

func meta(hash string, local bool, ago time.Duration) txMeta {
	return txMeta{Hash: hash, Local: local, AddedAt: t0.Add(-ago).Format(time.RFC3339Nano)}
}

func ptx(hash, from string, nonce string) pendingTx {
	return pendingTx{Hash: hash, From: from, Nonce: nonce}
}

func TestGroupPool(t *testing.T) {
	metas := []txMeta{
		meta("0xa1", true, time.Minute), meta("0xa2", true, 2*time.Minute), meta("0xb1", false, 10*time.Minute),
		meta("0xc1", false, time.Hour),        // only in besuTransactions (the part cut from the pending list), still counts as oldest
		{Hash: "0xd1", AddedAt: "not-a-time"}, // unparsable format: zero value, does not affect the oldest time
	}
	txs := []pendingTx{
		ptx("0xA2", "0xAAAA", "0x2a"), ptx("0xa1", "0xaaaa", "0x29"), ptx("0xb1", "0xbbbb", "0x5"),
		ptx("0xzz", "0xbbbb", "0x6"), // only in the pending list: skipped
	}
	ss, oldest, trunc := groupPool(metas, txs, false, 200)
	if len(ss) != 2 || ss[0].Address != "0xaaaa" || len(ss[0].Txs) != 2 || ss[1].Address != "0xbbbb" || len(ss[1].Txs) != 1 {
		t.Fatalf("senders = %+v", ss)
	}
	if ss[0].Txs[0].Nonce != 41 || ss[0].Txs[1].Nonce != 42 || !ss[0].Txs[0].Local {
		t.Fatalf("txs not sorted or merged: %+v", ss[0].Txs)
	}
	if trunc || oldest == nil || !oldest.Equal(t0.Add(-time.Hour)) {
		t.Fatalf("oldest = %v trunc = %v, want whole-pool oldest", oldest, trunc)
	}

	// With from: the oldest time only covers this address
	_, oldest, _ = groupPool(metas, txs[:2], true, 200)
	if oldest == nil || !oldest.Equal(t0.Add(-2*time.Minute)) {
		t.Fatalf("filtered oldest = %v", oldest)
	}

	// Account count exactly at the cap: list all, not truncated
	if ss, _, trunc = groupPool(metas, txs, false, 2); trunc || len(ss) != 2 {
		t.Fatalf("max == 帳戶數: %d 個 trunc=%v", len(ss), trunc)
	}

	// Truncated: accounts with more transactions first
	ss, _, trunc = groupPool(metas, txs, false, 1)
	if !trunc || len(ss) != 1 || ss[0].Address != "0xaaaa" {
		t.Fatalf("truncate = %+v %v", ss, trunc)
	}
}

func TestTxPoolTotals(t *testing.T) {
	tx := func(n uint64) PoolTx { return PoolTx{Nonce: n} }
	p := TxPool{Senders: []TxSender{
		{NextNonce: 42, Txs: []PoolTx{tx(43), tx(44)}},         // 42 missing: two queued
		{NextNonce: 7, Txs: []PoolTx{tx(7), tx(8), tx(10)}},    // 7, 8 pending; 9 missing: 10 queued
		{NextNonce: 15, Txs: []PoolTx{tx(14), tx(15), tx(16)}}, // 14 just mined, not counted; 15, 16 pending
		{NextNonce: 3, Txs: []PoolTx{tx(3)}},                   // pending
		{NextNonce: 20, Txs: []PoolTx{tx(20), tx(20), tx(21)}}, // two entries with the same nonce (replacement transition): not a gap
	}}
	pending, queued := p.Totals()
	if pending != 7 || queued != 3 {
		t.Fatalf("totals = %d/%d, want 7/3", pending, queued)
	}
}

func TestTxPool(t *testing.T) {
	var gotFilter string
	node := rpcFunc(func(m string, params []json.RawMessage) any {
		switch m {
		case "eth_syncing":
			return false
		case "txpool_besuStatistics":
			return map[string]int{"maxSize": -1, "localCount": 2, "remoteCount": 1}
		case "txpool_besuTransactions":
			return []map[string]any{
				{"hash": "0xa1", "isReceivedFromLocalSource": true, "addedToPoolAt": "2026-10-14T00:59:00.123456Z"},
				{"hash": "0xa3", "isReceivedFromLocalSource": true, "addedToPoolAt": "2026-10-14T00:58:00Z"},
				{"hash": "0xb1", "isReceivedFromLocalSource": false, "addedToPoolAt": "2026-10-14T00:50:00Z"},
				// Not in the pending list (the cut-off part): still counts as oldest when unfiltered
				{"hash": "0xc1", "isReceivedFromLocalSource": false, "addedToPoolAt": "2026-10-14T00:40:00Z"},
			}
		case "txpool_besuPendingTransactions":
			if len(params) > 1 {
				gotFilter = string(params[1])
			}
			return []map[string]any{
				{"hash": "0xa1", "from": "0xAAAA", "to": "0xcc", "nonce": "0x29"},
				{"hash": "0xa3", "from": "0xaaaa", "to": nil, "nonce": "0x2b"},
				{"hash": "0xb1", "from": "0xbbbb", "to": "0xcc", "nonce": "0x5"},
			}
		case "eth_getTransactionCount":
			var addr string
			_ = json.Unmarshal(params[0], &addr)
			return map[string]string{"0xaaaa": "0x29", "0xbbbb": "0x5"}[addr]
		}
		return &RPCError{Code: -32601, Message: "Method not found"}
	})
	c := New(node, nil, node)

	p, err := c.TxPool(context.Background(), "n1", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Syncing || p.MaxSize != nil || p.LocalCount != 2 || p.RemoteCount != 1 || len(p.Senders) != 2 {
		t.Fatalf("pool = %+v", p)
	}
	if p.Truncated || p.Senders[0].NextNonce != 41 || p.Senders[0].Txs[1].To != nil || p.Oldest == nil || !p.Oldest.Equal(time.Date(2026, 10, 14, 0, 40, 0, 0, time.UTC)) {
		t.Fatalf("sender = %+v oldest = %v", p.Senders[0], p.Oldest)
	}
	if pending, queued := p.Totals(); pending != 2 || queued != 1 {
		t.Fatalf("totals = %d/%d", pending, queued) // 0xaaaa: 41 pending, 42 missing, 43 queued; 0xbbbb: 5 pending
	}

	if _, err := c.TxPool(context.Background(), "n1", "0xAAAA"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotFilter, `"eq":"0xaaaa"`) {
		t.Fatalf("filter = %s", gotFilter)
	}
}

// maxSize 0 means unknown; truncated only when pending returns a full txPoolMaxTxs entries.
func TestTxPoolMaxSizeAndTruncated(t *testing.T) {
	for _, tc := range []struct {
		n     int
		trunc bool
	}{{txPoolMaxTxs - 1, false}, {txPoolMaxTxs, true}} {
		node := rpcFunc(func(m string, _ []json.RawMessage) any {
			switch m {
			case "eth_syncing":
				return false
			case "txpool_besuStatistics":
				return map[string]int{"maxSize": 0}
			case "txpool_besuTransactions":
				return []any{}
			case "txpool_besuPendingTransactions":
				return make([]map[string]any, tc.n)
			}
			return &RPCError{Code: -32601, Message: "Method not found"}
		})
		p, err := New(node, nil, node).TxPool(context.Background(), "n1", "")
		if err != nil {
			t.Fatal(err)
		}
		if p.MaxSize != nil || p.Truncated != tc.trunc {
			t.Errorf("%d 筆: MaxSize=%v Truncated=%v,want nil/%v", tc.n, p.MaxSize, p.Truncated, tc.trunc)
		}
	}
}

func TestTxPoolSyncingAndDisabled(t *testing.T) {
	syncing := rpcFunc(func(m string, _ []json.RawMessage) any {
		if m == "eth_syncing" {
			return map[string]string{"startingBlock": "0x0", "currentBlock": "0x10", "highestBlock": "0x99"}
		}
		return []any{}
	})
	p, err := New(syncing, nil, syncing).TxPool(context.Background(), "n1", "")
	if err != nil || !p.Syncing {
		t.Fatalf("syncing pool = %+v, %v", p, err)
	}

	noTxpool := rpcFunc(func(m string, _ []json.RawMessage) any {
		if m == "eth_syncing" {
			return false
		}
		return &RPCError{Code: -32604, Message: "Method not enabled"}
	})
	if _, err := New(noTxpool, nil, noTxpool).TxPool(context.Background(), "n1", ""); !errors.Is(err, ErrNoTxPool) {
		t.Fatalf("err = %v, want ErrNoTxPool", err)
	}
}

func TestTxPoolPermitted(t *testing.T) {
	for _, m := range []string{"eth_syncing", "txpool_besuStatistics", "txpool_besuTransactions", "txpool_besuPendingTransactions", "eth_getTransactionCount"} {
		if !Permits(m) {
			t.Errorf("platform token does not permit %s", m)
		}
	}
}

// tooLargeNode: the batch asking for the full pool lists returns "exceeds tunnel response cap"; others answer
// via rpcFunc. Records how many calls each batch asked for.
type tooLargeNode struct {
	rpcFunc
	sizes []int
}

func (f *tooLargeNode) PostLoopback(ctx context.Context, id string, port int, body []byte, h http.Header) (int, []byte, error) {
	var reqs []json.RawMessage
	_ = json.Unmarshal(body, &reqs)
	f.sizes = append(f.sizes, len(reqs))
	if strings.Contains(string(body), "txpool_besuTransactions") {
		return 0, nil, fmt.Errorf("agent %s: %w", id, pki.ErrResponseTooLarge)
	}
	return f.rpcFunc.PostLoopback(ctx, id, port, body, h)
}

// Pool too large and lists exceed the tunnel cap: fall back to stats only; the page still has counts, and the
// list is marked truncated and empty.
func TestTxPoolTooLargeFallsBackToStats(t *testing.T) {
	node := &tooLargeNode{rpcFunc: func(m string, _ []json.RawMessage) any {
		switch m {
		case "eth_syncing":
			return false
		case "txpool_besuStatistics":
			return map[string]int{"maxSize": -1, "localCount": 30000, "remoteCount": 70000} // layered: capacity-based, no entry cap
		}
		return nil
	}}
	p, err := New(node, nil, node).TxPool(context.Background(), "n1", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.LocalCount != 30000 || p.RemoteCount != 70000 || p.MaxSize != nil ||
		!p.Truncated || len(p.Senders) != 0 || p.Oldest != nil {
		t.Fatalf("%+v", p)
	}
	if len(node.sizes) != 2 || node.sizes[0] != 4 || node.sizes[1] != 2 {
		t.Fatalf("先送整包、退回時只送兩筆:%v", node.sizes)
	}
}

// Single-address query exceeds the cap: return an error instead of falling back to stats only (the empty
// fallback list would be read as this address having no transactions).
func TestTxPoolTooLargeLookupErrors(t *testing.T) {
	node := &tooLargeNode{rpcFunc: func(string, []json.RawMessage) any { return nil }}
	_, err := New(node, nil, node).TxPool(context.Background(), "n1", "0x"+strings.Repeat("ab", 20))
	if !errors.Is(err, pki.ErrResponseTooLarge) || len(node.sizes) != 1 {
		t.Fatalf("err=%v sizes=%v", err, node.sizes)
	}
}

// The stats-only fallback batch: a syncing node still returns Syncing, and no TXPOOL still returns ErrNoTxPool.
func TestTxPoolStatsOnlyHead(t *testing.T) {
	syncing := &tooLargeNode{rpcFunc: func(m string, _ []json.RawMessage) any {
		if m == "eth_syncing" {
			return map[string]string{"currentBlock": "0x1", "highestBlock": "0x9"}
		}
		return nil
	}}
	if p, err := New(syncing, nil, syncing).TxPool(context.Background(), "n1", ""); err != nil || !p.Syncing {
		t.Fatalf("同步中:%+v %v", p, err)
	}
	noPool := &tooLargeNode{rpcFunc: func(m string, _ []json.RawMessage) any {
		if m == "eth_syncing" {
			return false
		}
		return &RPCError{Code: -32604, Message: "Method not enabled"}
	}}
	if _, err := New(noPool, nil, noPool).TxPool(context.Background(), "n1", ""); !errors.Is(err, ErrNoTxPool) {
		t.Fatalf("沒開 TXPOOL:%v", err)
	}
}
