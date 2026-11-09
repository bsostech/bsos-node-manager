package rpcops

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"besu-manager/backend/internal/pki"
)

// Txpool snapshot: fetched only while the tab is open, never part of the collector;
// txpool_besuPendingTransactions carries raw and input for every tx, and a whole pool can reach MBs, so it is
// not fit to pull from every node every round.
const (
	// TxPoolMaxSenders: the most accounts to list (by tx count, most to least); each account costs one
	// eth_getTransactionCount.
	TxPoolMaxSenders = 200
	// txPoolMaxTxs: the most entries to fetch from besuPendingTransactions. Transactions beyond that still count in
	// the pool total; we just cannot get their nonce. Each entry carries raw and input: 500 entries at an average
	// calldata of ~4KB already hit the tunnel's 8MB response cap (plus the pool summary list on top).
	txPoolMaxTxs = 500
)

// ErrNoTxPool: the node has not enabled the TXPOOL namespace.
var ErrNoTxPool = errors.New("TXPOOL API not enabled on the node")

type TxPool struct {
	Syncing     bool // Besu disables the txpool while syncing and RPC returns empty; the other fields are zero values
	MaxSize     *int // entry cap of the sequenced txpool; layered returns -1 (capacity-based), recorded as nil
	LocalCount  int
	RemoteCount int
	Oldest      *time.Time // when the oldest entry entered the pool; nil when the pool is empty
	Truncated   bool
	Senders     []TxSender
}

type TxSender struct {
	Address   string // lowercase
	NextNonce uint64 // the next nonce due to go on chain
	Txs       []PoolTx
}

type PoolTx struct {
	Hash    string
	Nonce   uint64
	To      *string // nil for contract deployment
	AddedAt time.Time
	Local   bool
}

// TxPool: reads one node's txpool. When from is non-empty, queries only that address (node-side filter), not
// subject to the account cap. Two batches: the first gets the stats and both transaction lists, the second
// gets each account's on-chain nonce.
func (c *Client) TxPool(ctx context.Context, nodeID, from string) (TxPool, error) {
	pending := []any{txPoolMaxTxs}
	if from != "" {
		pending = append(pending, map[string]any{"from": map[string]string{"eq": strings.ToLower(from)}})
	}
	res, err := c.Batch(ctx, nodeID, []Call{
		{"eth_syncing", nil},
		{"txpool_besuStatistics", nil},
		{"txpool_besuTransactions", nil},
		{"txpool_besuPendingTransactions", pending},
	})
	if errors.Is(err, pki.ErrResponseTooLarge) && from == "" {
		// The pool is too large and the lists blow the tunnel's response cap: return stats only and mark the
		// list truncated. No fallback when querying a single address: the pool summary has no filter, so a
		// fallback would only yield an empty list, which would look like this address has no transactions
		return c.txPoolStatsOnly(ctx, nodeID)
	}
	if err != nil {
		return TxPool{}, err
	}
	stats, syncing, err := decodeHead(res)
	if err != nil || syncing {
		return TxPool{Syncing: syncing}, err
	}
	var metas []txMeta
	if err := res[2].Decode(&metas); err != nil {
		return TxPool{}, err
	}
	var txs []pendingTx
	if err := res[3].Decode(&txs); err != nil {
		return TxPool{}, err
	}

	p := stats.pool(false)
	p.Senders, p.Oldest, p.Truncated = groupPool(metas, txs, from != "", TxPoolMaxSenders)
	p.Truncated = p.Truncated || len(txs) >= txPoolMaxTxs
	if len(p.Senders) == 0 {
		return p, nil
	}

	calls := make([]Call, len(p.Senders))
	for i, s := range p.Senders {
		calls[i] = Call{"eth_getTransactionCount", []any{s.Address, "latest"}}
	}
	nonces, err := c.Batch(ctx, nodeID, calls)
	if err != nil {
		return TxPool{}, err
	}
	for i := range p.Senders {
		var hex string
		if err := nonces[i].Decode(&hex); err != nil {
			return TxPool{}, err
		}
		if p.Senders[i].NextNonce, err = parseHexUint(hex); err != nil {
			return TxPool{}, fmt.Errorf("eth_getTransactionCount: %w", err)
		}
	}
	return p, nil
}

// txPoolStatsOnly: fetches only sync status and stats (a few hundred bytes, never too large); Senders empty,
// Truncated true.
func (c *Client) txPoolStatsOnly(ctx context.Context, nodeID string) (TxPool, error) {
	res, err := c.Batch(ctx, nodeID, []Call{{"eth_syncing", nil}, {"txpool_besuStatistics", nil}})
	if err != nil {
		return TxPool{}, err
	}
	stats, syncing, err := decodeHead(res)
	if err != nil || syncing {
		return TxPool{Syncing: syncing}, err
	}
	return stats.pool(true), nil
}

// decodeHead: the first two entries shared by both read modes (eth_syncing, txpool_besuStatistics). While
// syncing Besu disables the txpool, and the other fields are ignored.
func decodeHead(res []Result) (txPoolStats, bool, error) {
	var syncing json.RawMessage
	if err := res[0].Decode(&syncing); err != nil {
		return txPoolStats{}, false, err
	}
	if string(syncing) != "false" {
		return txPoolStats{}, true, nil
	}
	var stats txPoolStats
	if err := res[1].Decode(&stats); err != nil {
		if MethodUnavailable(err) {
			return txPoolStats{}, false, ErrNoTxPool
		}
		return txPoolStats{}, false, err
	}
	return stats, false, nil
}

// Response of txpool_besuStatistics.
type txPoolStats struct {
	MaxSize     int `json:"maxSize"`
	LocalCount  int `json:"localCount"`
	RemoteCount int `json:"remoteCount"`
}

func (s txPoolStats) pool(truncated bool) TxPool {
	p := TxPool{LocalCount: s.LocalCount, RemoteCount: s.RemoteCount, Truncated: truncated}
	if s.MaxSize > 0 {
		p.MaxSize = &s.MaxSize
	}
	return p
}

// One entry of txpool_besuTransactions: only hash, time added, and whether it was submitted locally.
type txMeta struct {
	Hash    string `json:"hash"`
	Local   bool   `json:"isReceivedFromLocalSource"`
	AddedAt string `json:"addedToPoolAt"`
}

// at: time added to the pool; an unparsable format becomes the zero value (so one oddly formatted entry does
// not turn the whole snapshot into a 502).
func (m txMeta) at() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, m.AddedAt)
	return t
}

// oldestOf: the earliest time added; nil if none (or none parse).
func oldestOf(metas []txMeta) *time.Time {
	var oldest *time.Time
	for _, m := range metas {
		if t := m.at(); !t.IsZero() && (oldest == nil || t.Before(*oldest)) {
			oldest = &t
		}
	}
	return oldest
}

// PoolOldest: txpool_besuTransactions response -> time the oldest entry entered the pool (nil = pool is
// empty). ok=false = no response or unparsable.
func PoolOldest(r Result) (oldest *time.Time, ok bool) {
	var metas []txMeta
	if r.Decode(&metas) != nil {
		return nil, false
	}
	return oldestOf(metas), true
}

// One entry of txpool_besuPendingTransactions: the full transaction; only the fields we use (input, raw,
// value are not read).
type pendingTx struct {
	Hash  string  `json:"hash"`
	From  string  `json:"from"`
	To    *string `json:"to"`
	Nonce string  `json:"nonce"`
}

// groupPool: merges the two lists by hash and groups by from; takes the top max accounts by tx count, with
// nonces ascending within each account. Oldest time: without from it covers the whole pool (metas is the
// whole pool); with from only this address's transactions. Transactions present in only one of the lists
// (entered or left the pool between the two reads) are skipped. Also marked truncated when pendingTx hits the
// entry cap: the cut-off transactions have no nonce, so their accounts would look like they have nonce gaps.
func groupPool(metas []txMeta, txs []pendingTx, filtered bool, max int) (senders []TxSender, oldest *time.Time, truncated bool) {
	byHash := make(map[string]txMeta, len(metas))
	for _, m := range metas {
		byHash[strings.ToLower(m.Hash)] = m
	}
	if !filtered {
		oldest = oldestOf(metas)
	}
	idx := map[string]int{}
	for _, t := range txs {
		m, ok := byHash[strings.ToLower(t.Hash)]
		nonce, err := parseHexUint(t.Nonce)
		if !ok || err != nil {
			continue
		}
		added := m.at()
		if filtered && !added.IsZero() && (oldest == nil || added.Before(*oldest)) {
			oldest = &added
		}
		from := strings.ToLower(t.From)
		i, seen := idx[from]
		if !seen {
			i = len(senders)
			idx[from] = i
			senders = append(senders, TxSender{Address: from})
		}
		senders[i].Txs = append(senders[i].Txs, PoolTx{Hash: t.Hash, Nonce: nonce, To: t.To, AddedAt: added, Local: m.Local})
	}
	for i := range senders {
		slices.SortFunc(senders[i].Txs, func(a, b PoolTx) int { return cmp.Compare(a.Nonce, b.Nonce) })
	}
	slices.SortFunc(senders, func(a, b TxSender) int {
		return cmp.Or(cmp.Compare(len(b.Txs), len(a.Txs)), cmp.Compare(a.Address, b.Address))
	})
	if len(senders) > max {
		senders, truncated = senders[:max], true
	}
	return senders, oldest, truncated
}

// Totals: pending = consecutive transactions starting from NextNonce; queued = transactions after a gap.
// Nonces below NextNonce (just mined, not yet cleared from the pool) are not counted.
func (p TxPool) Totals() (pending, queued int) {
	for _, s := range p.Senders {
		next, gap := s.NextNonce, false
		for _, t := range s.Txs {
			switch {
			case t.Nonce < next: // mined but not cleared, or a second entry with the same nonce (replacement transition)
			case !gap && t.Nonce == next:
				pending++
				next++
			default:
				gap = true
				queued++
			}
		}
	}
	return pending, queued
}

func parseHexUint(s string) (uint64, error) {
	return strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
}
