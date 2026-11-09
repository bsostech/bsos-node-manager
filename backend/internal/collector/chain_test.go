package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/rpcops"
)

func healthy(h int64) Result {
	return Result{Snap: &Snapshot{Ledger: &api.LedgerSummary{ChainHeight: &h}}}
}

// Source = the highest healthy node; on equal height the smaller id; syncing and RPC-unreachable nodes do not count.
func TestChainSource(t *testing.T) {
	behind := 3
	syncing := Result{Snap: &Snapshot{Ledger: &api.LedgerSummary{ChainHeight: new(int64), BlocksBehind: &behind}}}
	*syncing.Snap.Ledger.ChainHeight = 999
	cache := map[string]Result{"b": healthy(100), "a": healthy(100), "c": healthy(90), "s": syncing, "x": {Snap: &Snapshot{}}}
	// tie-breaking on equal height must not rely on map order: it has to be a across many runs
	for range 50 {
		if id, h := chainSource(cache); id != "a" || h != 100 {
			t.Fatalf("got %s %d", id, h)
		}
	}
	if id, _ := chainSource(map[string]Result{"x": {}}); id != "" {
		t.Fatalf("沒有健康節點應為空:%s", id)
	}
}

func TestMissingBlocks(t *testing.T) {
	// cold start: fetch [height-36, height]; on a very short chain block 0 is not fetched
	if _, f := missingBlocks(nil, 100, BlockWindow); len(f) != BlockWindow+1 || f[0] != 64 || f[len(f)-1] != 100 {
		t.Fatalf("冷啟動:%v", f)
	}
	if _, f := missingBlocks(nil, 5, BlockWindow); len(f) != 5 || f[0] != 1 {
		t.Fatalf("短鏈:%v", f)
	}
	// incremental: 64..100 known, height reaches 103 -> drop 64..66, fetch only 101..103
	var have []BlockStamp
	for n := int64(64); n <= 100; n++ {
		have = append(have, BlockStamp{Number: n})
	}
	keep, f := missingBlocks(have, 103, BlockWindow)
	if len(f) != 3 || f[0] != 101 || len(keep) != 34 || keep[0].Number != 67 {
		t.Fatalf("增量:keep=%d first=%d fetch=%v", len(keep), keep[0].Number, f)
	}
	// source switches to a node one block behind: drop only the extra block, no refetch
	if keep, f := missingBlocks(have, 99, BlockWindow); len(f) != 1 || f[0] != 63 || keep[len(keep)-1].Number != 99 {
		t.Fatalf("落後一塊:keep last=%d fetch=%v", keep[len(keep)-1].Number, f)
	}
	// latest known block exactly one window above height: not yet a chain replacement, blocks in the window stay
	if keep, f := missingBlocks(have, 64, BlockWindow); len(keep) != 1 || keep[0].Number != 64 || len(f) != 36 || f[0] != 28 {
		t.Fatalf("剛好一個視窗:keep=%v fetch=%v", keep, f)
	}
	// height drops back by more than a window (chain replaced): refetch everything
	if keep, f := missingBlocks(have, 50, BlockWindow); len(keep) != 0 || len(f) != 37 {
		t.Fatalf("倒退:keep=%d fetch=%d", len(keep), len(f))
	}
}

// updateChain: fills blocks from the source node, timestamps ordered by block number.
func TestUpdateChain(t *testing.T) {
	rpc := &fakeRPC{answers: map[string]any{"eth_getBlockByNumber": map[string]string{"timestamp": "0x10"}}}
	c := New(nil, nil, rpc)
	c.cache["a"] = healthy(3)
	c.updateChain(context.Background())
	v := c.Chain()
	if v.SourceNodeID != "a" || len(v.Blocks) != 3 || v.Blocks[0].Number != 1 || !v.Blocks[2].Time.Equal(time.Unix(16, 0)) {
		t.Fatalf("got %+v", v)
	}
	// fetch fails: the source still switches to this round's node, only known blocks are kept
	c.cache["b"] = healthy(5)
	rpc.err = errors.New("down")
	c.updateChain(context.Background())
	if v := c.Chain(); v.SourceNodeID != "b" || len(v.Blocks) != 3 {
		t.Fatalf("抓失敗:%+v", v)
	}
	c.cache["a"], c.cache["b"] = Result{}, Result{}
	c.updateChain(context.Background())
	if v := c.Chain(); v.SourceNodeID != "" || len(v.Blocks) != 3 {
		t.Fatalf("沒有健康節點時清來源、保留區塊:%+v", v)
	}
}

func TestTallyBlocks(t *testing.T) {
	vs := []string{"0xa", "0xb", "0xc"}
	b := func(n int64, voter, target string, add bool, validators []string) BlockStamp {
		s := BlockStamp{Number: n, Proposer: voter, BFT: true, Validators: validators}
		if target != "" {
			s.Vote = &BlockVote{Target: target, Add: add}
		}
		return s
	}
	votes, changed := tallyBlocks([]BlockStamp{
		b(10, "0xa", "0xt", true, vs),
		b(11, "0xb", "0xt", true, vs),
		b(12, "0xa", "0xt", false, vs),                                            // 0xa switches to a remove vote: overrides the add at 10
		b(13, "0xc", "0xt", true, append(vs, "0xt")),                              // list gains 0xt: change point 13
		b(15, "0xc", "", false, vs),                                               // not contiguous with 13: no change detection
		{Number: 16, Proposer: "0xb", Vote: &BlockVote{Target: "0xu", Add: true}}, // undecodable block: ignored
	})
	if len(votes) != 3 || votes[VoteRef{"0xt", "0xa"}] != (SeenVote{Add: false, Block: 12}) || votes[VoteRef{"0xt", "0xb"}].Block != 11 || votes[VoteRef{"0xt", "0xc"}].Block != 13 {
		t.Fatalf("votes %v", votes)
	}
	if len(changed) != 1 || !slices.Equal(changed["0xt"], []int64{13}) {
		t.Fatalf("changed %v", changed)
	}
}

// One block swaps several addresses at once: change points are recorded only on addresses that actually left or joined; unchanged addresses are unaffected
func TestTallyBlocksSeveralChanges(t *testing.T) {
	before := []string{"0xa", "0xb", "0xc", "0xd"}
	after := []string{"0xa", "0xb", "0xx", "0xy"} // 0xc and 0xd leave, 0xx and 0xy join
	_, changed := tallyBlocks([]BlockStamp{
		{Number: 20, BFT: true, Proposer: "0xa", Validators: before},
		{Number: 21, BFT: true, Proposer: "0xb", Validators: after},
	})
	if len(changed) != 4 || !slices.Equal(changed["0xc"], []int64{21}) || !slices.Equal(changed["0xd"], []int64{21}) || !slices.Equal(changed["0xx"], []int64{21}) || !slices.Equal(changed["0xy"], []int64{21}) {
		t.Fatalf("changed %v", changed)
	}
}

// Add takes effect, then removed: the change point moves to the block where the removal took effect; a validator who voted only to add, not to remove, keeps the old vote, but it lies before the change point
func TestTallyBlocksAddThenRemove(t *testing.T) {
	base := []string{"0xa", "0xb", "0xc"}
	with := []string{"0xa", "0xb", "0xc", "0xx"}
	var blocks []BlockStamp
	for n := int64(10); n <= 30; n++ {
		b := BlockStamp{Number: n, BFT: true, Proposer: "0xa", Validators: base}
		switch {
		case n >= 13 && n < 25:
			b.Validators = with
		}
		switch n {
		case 10, 11, 12:
			b.Proposer, b.Vote = []string{"0xa", "0xb", "0xc"}[n-10], &BlockVote{Target: "0xx", Add: true}
		case 20, 21:
			b.Proposer, b.Vote = []string{"0xa", "0xb"}[n-20], &BlockVote{Target: "0xx", Add: false}
		}
		blocks = append(blocks, b)
	}
	votes, changed := tallyBlocks(blocks)
	if !slices.Equal(changed["0xx"], []int64{13, 25}) { // both the add and the remove are recorded
		t.Fatalf("change point %v", changed)
	}
	if c := votes[VoteRef{"0xx", "0xc"}]; !c.Add || c.Block != 12 || c.Block >= 25 {
		t.Fatalf("0xc 的舊加入票應在變動點之前:%+v", c)
	}
	if a := votes[VoteRef{"0xx", "0xa"}]; a.Add || a.Block != 20 {
		t.Fatalf("0xa 後一票蓋前一票:%+v", a)
	}
}

// The latest batch fails partway: the complete-tally start reaches only above the block that was not fetched
// First-round fetch fails: VotesFrom does not exceed height and no backfill starts (the backfill start cannot be an unfetched block); once the next round fetches everything, the backfill starts and completes.
func TestUpdateChainPartialFetch(t *testing.T) {
	backfillPause = time.Millisecond
	rpc := &failingRPC{chainRPC: chainRPC{voteAt: 50}, failFrom: KeepBlocks + 1900}
	c := New(nil, nil, rpc)
	h, n, epoch := int64(KeepBlocks+2000), 4, int64(30000) // higher than the cache, so there are earlier blocks to backfill
	c.cache["a"] = Result{Snap: &Snapshot{Ledger: &api.LedgerSummary{ChainHeight: &h}, Consensus: &api.ConsensusStatus{ValidatorCount: &n}, EpochLength: &epoch}}
	c.updateChain(context.Background())
	c.mu.RLock()
	v, busy, noOlder := c.chain, c.backfilling, c.noOlder
	c.mu.RUnlock()
	if v.VotesFrom != h || v.tracking || busy || noOlder != -1 {
		t.Fatalf("partial fetch: votesFrom %d tracking %v backfilling %v noOlder %d", v.VotesFrom, v.tracking, busy, noOlder)
	}
	rpc.failFrom = 1 << 62
	c.updateChain(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.RLock()
		busy := c.backfilling
		c.mu.RUnlock()
		if !busy || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if v := c.Chain(); v.VotesFrom != 0 || v.Votes[VoteRef{"0x1111111111111111111111111111111111111111", "0x10fd2c060eed3590a5f7d538435ccf607b51fb9c"}].Block != 50 {
		t.Fatalf("after retry: votesFrom %d votes %v", v.VotesFrom, v.Votes)
	}
}

// failingRPC: the whole batch fails if it includes any block at or above failFrom.
type failingRPC struct {
	chainRPC
	failFrom int64
}

func (f *failingRPC) Batch(ctx context.Context, id string, calls []rpcops.Call) ([]rpcops.Result, error) {
	for _, c := range calls {
		var n int64
		fmt.Sscanf(c.Params[0].(string), "0x%x", &n)
		if n >= f.failFrom {
			return nil, errors.New("down")
		}
	}
	return f.chainRPC.Batch(ctx, id, calls)
}

func TestMergeTallyAcrossEpochs(t *testing.T) {
	var v ChainView
	startEpoch(&v, 30000, 30020)
	if v.EpochStart != 30000 || v.VotesFrom != 30000 {
		t.Fatalf("start %+v", v)
	}
	// backfill goes newest to oldest: older votes do not override newer ones; votes from the previous epoch are not accepted
	mergeTally(&v, map[VoteRef]SeenVote{{"0xt", "0xa"}: {Add: false, Block: 30010}}, map[string][]int64{"0xt": {30008}})
	mergeTally(&v, map[VoteRef]SeenVote{{"0xt", "0xa"}: {Add: true, Block: 30002}, {"0xt", "0xb"}: {Add: true, Block: 29990}}, map[string][]int64{"0xt": {29995, 30001, 30008}})
	if len(v.Votes) != 1 || v.Votes[VoteRef{"0xt", "0xa"}].Block != 30010 || v.Changed["0xt"] != 30008 || !slices.Equal(v.Changes["0xt"], []int64{30001, 30008}) {
		t.Fatalf("merge %+v", v)
	}
	// epoch change: cleared, VotesFrom moves to the new start
	startEpoch(&v, 30000, 60001)
	if len(v.Votes) != 0 || len(v.Changed) != 0 || len(v.Changes) != 0 || v.EpochStart != 60000 || v.VotesFrom != 60000 || !votesComplete(v) {
		t.Fatalf("rollover %+v", v)
	}
	// epoch unknown: start 0, nothing cleared
	var u ChainView
	mergeTally(&u, map[VoteRef]SeenVote{{"0xt", "0xa"}: {Add: true, Block: 5}}, nil)
	startEpoch(&u, 0, 90000)
	if len(u.Votes) != 1 {
		t.Fatalf("unknown epoch dropped %+v", u)
	}
}

// chainRPC: returns block headers by number; below oldest returns null (a checkpoint-synced node lacks them), and the voteAt block carries extraAddVote.
type chainRPC struct {
	oldest, voteAt int64
	hole           int64 // this block returns null (0 = none)
	mu             sync.Mutex
	asked          []int64
}

func (f *chainRPC) Batch(_ context.Context, _ string, calls []rpcops.Call) ([]rpcops.Result, error) {
	out := make([]rpcops.Result, len(calls))
	for i, c := range calls {
		var n int64
		fmt.Sscanf(c.Params[0].(string), "0x%x", &n)
		f.mu.Lock()
		f.asked = append(f.asked, n)
		f.mu.Unlock()
		if n < f.oldest || n == f.hole {
			out[i] = rpcops.Result{Raw: []byte("null")}
			continue
		}
		extra, miner := extraNoVote, "0x10fd2c060eed3590a5f7d538435ccf607b51fb9c"
		if n == f.voteAt {
			extra = extraAddVote
		}
		b, _ := json.Marshal(map[string]any{"timestamp": fmt.Sprintf("0x%x", n), "miner": miner, "extraData": extra,
			"gasUsed": fmt.Sprintf("0x%x", n*10), "gasLimit": "0x1c9c380", "transactions": []string{"0x01", "0x02"}})
		out[i] = rpcops.Result{Raw: b}
	}
	return out, nil
}

func (f *chainRPC) ProbeAuth(context.Context, string) string { return "enabled" }

func TestBackfillVotes(t *testing.T) {
	backfillPause = 0
	target := VoteRef{"0x1111111111111111111111111111111111111111", "0x10fd2c060eed3590a5f7d538435ccf607b51fb9c"}

	// backfill from 1200 down to the epoch start, picking up the vote at block 10; newest to oldest, each batch overlapping the previous by one block
	rpc := &chainRPC{voteAt: 10}
	c := New(nil, nil, rpc)
	c.chain = ChainView{EpochLength: 30000, EpochStart: 0, VotesFrom: 1200}
	c.backfilling = true
	c.backfillVotes(context.Background(), "a", 0, 0, 1200)
	if v := c.Chain(); v.VotesFrom != 0 || v.Votes[target].Block != 10 || c.backfilling {
		t.Fatalf("backfill %+v", v)
	}
	// ascending within each batch: [701,1200], [202,701], [1,202]
	if a := rpc.asked; len(a) != 1202 || a[0] != 701 || a[499] != 1200 || a[500] != 202 || a[999] != 701 || a[1000] != 1 || a[1201] != 202 {
		t.Fatalf("batches not newest-first with overlap: %d %v", len(a), []int64{a[0], a[499], a[500], a[999], a[1000], a[len(a)-1]})
	}

	// the chain is replaced during the backfill (old and new both in epoch 0): the old chain's votes are not merged in
	c = New(nil, nil, &chainRPC{voteAt: 10})
	c.chain = ChainView{EpochLength: 30000, VotesFrom: 1200, gen: 1}
	c.backfillVotes(context.Background(), "a", 0, 0, 1200)
	if v := c.Chain(); len(v.Votes) != 0 || v.VotesFrom != 1200 {
		t.Fatalf("chain switched %+v", v)
	}

	// source has no headers before 300: stop at 300, this epoch is not retried
	c = New(nil, nil, &chainRPC{oldest: 300, voteAt: 10})
	c.chain = ChainView{EpochLength: 30000, VotesFrom: 1200}
	c.backfillVotes(context.Background(), "a", 0, 0, 1200)
	if v := c.Chain(); v.VotesFrom != 300 || len(v.Votes) != 0 || c.noOlder != 0 {
		t.Fatalf("no older %+v noOlder=%d", v, c.noOlder)
	}

	// one block missing in the middle ([hi ... 1001] present, 1000 null, older ones present too): only the contiguous run down from hi is accepted, votes before 1000 do not count
	c = New(nil, nil, &chainRPC{hole: 1000, voteAt: 900})
	c.chain = ChainView{EpochLength: 30000, VotesFrom: 1200}
	c.backfillVotes(context.Background(), "a", 0, 0, 1200)
	if v := c.Chain(); v.VotesFrom != 1001 || len(v.Votes) != 0 {
		t.Fatalf("hole %+v", v)
	}

	// epoch changes during the backfill: old-epoch votes are not accepted
	c = New(nil, nil, &chainRPC{voteAt: 10})
	c.chain = ChainView{EpochLength: 30000, EpochStart: 30000, VotesFrom: 30000}
	c.backfillVotes(context.Background(), "a", 0, 0, 1200)
	if v := c.Chain(); len(v.Votes) != 0 || v.VotesFrom != 30000 {
		t.Fatalf("rolled over %+v", v)
	}

	// the first batch has no headers at all (empty response): stop, VotesFrom unchanged
	c = New(nil, nil, &chainRPC{oldest: 5000})
	c.chain = ChainView{EpochLength: 30000, VotesFrom: 1200}
	c.backfillVotes(context.Background(), "a", 0, 0, 1200)
	if v := c.Chain(); v.VotesFrom != 1200 || len(v.Votes) != 0 {
		t.Fatalf("empty %+v", v)
	}
}

// updateChain starts the backfill; votes are not lost when the backfill runs concurrently with the next updateChain (run with -race).
func TestUpdateChainBackfillsConcurrently(t *testing.T) {
	backfillPause = time.Millisecond
	rpc := &chainRPC{voteAt: 50}
	c := New(nil, nil, rpc)
	h, n, epoch := int64(KeepBlocks+2000), 4, int64(30000) // higher than the cache, so there are earlier blocks to backfill
	c.cache["a"] = Result{Snap: &Snapshot{Ledger: &api.LedgerSummary{ChainHeight: &h}, Consensus: &api.ConsensusStatus{ValidatorCount: &n}, EpochLength: &epoch}}
	c.updateChain(context.Background()) // backfill outside the cached range in the background
	for i := 0; i < 20; i++ {
		h++
		c.updateChain(context.Background())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.RLock()
		busy := c.backfilling
		c.mu.RUnlock()
		if !busy || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	v := c.Chain()
	if v.VotesFrom != 0 || v.Votes[VoteRef{"0x1111111111111111111111111111111111111111", "0x10fd2c060eed3590a5f7d538435ccf607b51fb9c"}].Block != 50 {
		t.Fatalf("after backfill %+v", v.VotesFrom)
	}
	// the round after the backfill completes: with the epoch starting at 0, VotesFrom 0 = complete and must not be reset as if not started
	h++
	c.updateChain(context.Background())
	if v := c.Chain(); v.VotesFrom != 0 {
		t.Fatalf("complete votesFrom reset to %d", v.VotesFrom)
	}
}

// Blocks with undecodable extraData (non-BFT or corrupt): timestamp and proposer are kept, but marked BFT=false and excluded from the production grid.
func TestFetchBlocksMarksUndecodableExtra(t *testing.T) {
	rpc := &fakeRPC{answers: map[string]any{"eth_getBlockByNumber": map[string]string{"timestamp": "0x10", "miner": "0xAA", "extraData": "0x1234"}}}
	c := New(nil, nil, rpc)
	got, err := c.fetchBlocks(context.Background(), "a", []int64{7})
	if err != nil || len(got) != 1 || got[0].BFT || got[0].Proposer != "0xaa" || got[0].Vote != nil {
		t.Fatalf("got %+v err %v", got, err)
	}
	rpc.answers["eth_getBlockByNumber"] = map[string]any{"timestamp": "0x10", "miner": "0xAA", "extraData": extraNoVote,
		"gasUsed": "0x50", "gasLimit": "0x1c9c380", "transactions": []string{"0x01", "0x02"}}
	if got, _ := c.fetchBlocks(context.Background(), "a", []int64{8}); len(got) != 1 || !got[0].BFT {
		t.Fatalf("decodable block should be BFT: %+v", got)
	} else if got[0].TxCount != 2 || got[0].GasUsed != 80 || got[0].GasLimit != 30_000_000 {
		t.Fatalf("交易數與 gas: %+v", got[0])
	}
}

func TestVotesComplete(t *testing.T) {
	if (ChainView{}).VotesComplete() {
		t.Fatal("還不知道 epoch")
	}
	v := ChainView{EpochLength: 30000, EpochStart: 30000, VotesFrom: 30500, tracking: true}
	if v.VotesComplete() {
		t.Fatal("補抓中")
	}
	v.VotesFrom = 30000
	if !v.VotesComplete() {
		t.Fatal("已完整")
	}
}

// Epoch length is chain-wide: when the source node lacks ADMIN, use what other nodes in our org report; none = 0 (unknown), no fallback to the default.
func TestChainEpoch(t *testing.T) {
	e := int64(500)
	cache := map[string]Result{"a": {Snap: &Snapshot{}}, "c": {Snap: &Snapshot{EpochLength: &e}}, "b": {}}
	if got := chainEpoch(cache, "a"); got != 500 {
		t.Fatalf("應取其他節點回報的 epoch:%d", got)
	}
	// if the source node reports it, use the source's value regardless of id order
	f := int64(700)
	cache["d"] = Result{Snap: &Snapshot{EpochLength: &f}}
	if got := chainEpoch(cache, "d"); got != 700 {
		t.Fatalf("應優先用來源節點:%d", got)
	}
	// source node unreachable (no snapshot): fall back to other nodes, must not panic
	if got := chainEpoch(cache, "b"); got != 500 {
		t.Fatalf("來源沒有 snapshot 應取其他節點:%d", got)
	}
	delete(cache, "c")
	delete(cache, "d")
	if got := chainEpoch(cache, "a"); got != 0 {
		t.Fatalf("沒有節點回報應為 0:%d", got)
	}
}

// Block time: separate medians for blocks with transactions and empty blocks; a configured value wins; a few slow blocks do not matter; without empty blocks the empty period equals the busy one; with neither, use the setting; non-contiguous blocks do not count.
func TestBlockPeriods(t *testing.T) {
	// stamps: each number is the interval from the previous block; negative = empty block (0 transactions)
	stamps := func(gaps ...float64) []BlockStamp {
		at := time.Unix(1_700_000_000, 0)
		out := []BlockStamp{{Number: 1, Time: at}}
		for i, g := range gaps {
			tx := 1
			if g < 0 {
				g, tx = -g, 0
			}
			at = at.Add(time.Duration(g * float64(time.Second)))
			out = append(out, BlockStamp{Number: int64(i + 2), Time: at, TxCount: tx})
		}
		return out
	}
	for _, tc := range []struct {
		name        string
		configured  float64
		blocks      []BlockStamp
		busy, empty float64
	}{
		{"沒設空塊間隔", 2, stamps(-2, -2, -2), 2, 2},
		{"少一位驗證者", 2, stamps(-2, -2, -14, -2, -2, -2, -14, -2), 2, 2},
		{"設了空塊間隔", 2, stamps(2, 2, -60, -60, -60, 2), 2, 60},
		{"一直有交易", 2, stamps(2, 2, 2), 2, 2},
		{"讀不到設定", 0, stamps(5, 5, 4, -30, -30), 5, 30},
		{"讀不到設定、全是空塊", 0, stamps(-3, -3), 3, 3},
		{"沒有區塊", 2, nil, 2, 2},
		{"不連續", 0, []BlockStamp{{Number: 1}, {Number: 5, Time: time.Unix(100, 0)}}, 0, 0},
	} {
		if busy, empty := blockPeriods(tc.configured, tc.blocks); busy != tc.busy || empty != tc.empty {
			t.Errorf("%s: want %g/%g got %g/%g", tc.name, tc.busy, tc.empty, busy, empty)
		}
	}
}

// Expected seconds per block: use the busy period only when transactions are waiting in the pool and the latest block has transactions; an empty pool, an empty latest block (just going from idle to busy) or no TXPOOL all use the empty-block period.
func TestExpectedBlockPeriod(t *testing.T) {
	v := ChainView{BlockPeriod: 2, EmptyBlockPeriod: 60}
	one, zero := 1, 0
	for _, tc := range []struct {
		name    string
		tx      int
		pending *int
		want    float64
	}{
		{"忙碌", 3, &one, 2}, {"池空", 3, &zero, 60}, {"剛轉忙", 0, &one, 60}, {"沒開 TXPOOL", 3, nil, 60},
	} {
		if got := v.ExpectedBlockPeriod(BlockStamp{TxCount: tc.tx}, tc.pending); got != tc.want {
			t.Errorf("%s: want %g got %g", tc.name, tc.want, got)
		}
	}
}

// EVM version: the latest one active at the chain head; block numbers compare against height, times against timestamp; no forks = Frontier; forks not yet reached do not count.
func TestEvmVersion(t *testing.T) {
	at := time.Unix(1000, 0)
	for _, tc := range []struct {
		name  string
		forks []Fork
		want  string
	}{
		{"沒有分叉", []Fork{}, "Frontier"},
		{"只有區塊號", []Fork{{Name: "Berlin", At: 0}, {Name: "London", At: 50}}, "London"},
		{"區塊號還沒到", []Fork{{Name: "Berlin", At: 0}, {Name: "London", At: 500}}, "Berlin"},
		{"只有時間", []Fork{{Name: "Shanghai", At: 0, AtTime: true}}, "Shanghai"},
		{"時間還沒到", []Fork{{Name: "London", At: 0}, {Name: "Shanghai", At: 0, AtTime: true}, {Name: "Cancun", At: 2000, AtTime: true}}, "Shanghai"},
		{"剛好到分叉高度", []Fork{{Name: "Berlin", At: 0}, {Name: "London", At: 100}}, "London"},
		{"剛好到分叉時間", []Fork{{Name: "London", At: 0}, {Name: "Shanghai", At: 1000, AtTime: true}}, "Shanghai"},
	} {
		if got := EvmVersion(tc.forks, 100, at); got != tc.want {
			t.Errorf("%s: want %s got %s", tc.name, tc.want, got)
		}
	}
}

// Hard fork settings: only keys that change the EVM, in activation order; the difficulty bomb does not count; admin_nodeInfo unreadable = nil.
func TestForkSchedule(t *testing.T) {
	b, _ := json.Marshal(map[string]any{"protocols": map[string]any{"eth": map[string]any{"config": map[string]any{
		"chainId": 1337, "cancunTime": 10, "londonBlock": 5, "arrowGlacierBlock": 6, "shanghaiTime": 0, "ibft2": map[string]any{"epochLength": 30000},
	}}}})
	got := forkSchedule(rpcops.Result{Raw: b})
	want := []Fork{{Name: "London", At: 5}, {Name: "Shanghai", At: 0, AtTime: true}, {Name: "Cancun", At: 10, AtTime: true}}
	if got == nil || fmt.Sprint(*got) != fmt.Sprint(want) {
		t.Fatalf("got %v", got)
	}
	if forkSchedule(rpcops.Result{Err: &rpcops.RPCError{Code: -32604, Message: "Method not enabled"}}) != nil {
		t.Fatal("ADMIN 沒開應為 nil")
	}
}

// The validator turn record looks only at the last SignerWindow blocks.
func TestTurnBlocks(t *testing.T) {
	v := ChainView{Blocks: make([]BlockStamp, KeepBlocks)}
	if n := len(v.TurnBlocks()); n != SignerWindow {
		t.Fatalf("want %d got %d", SignerWindow, n)
	}
	if n := len((ChainView{Blocks: make([]BlockStamp, 10)}).TurnBlocks()); n != 10 {
		t.Fatalf("不到 SignerWindow 塊應全取:%d", n)
	}
}

// Latest block's extraData undecodable: fall back to the most recent block with a list.
func TestLatestValidators(t *testing.T) {
	v := ChainView{Blocks: []BlockStamp{{BFT: true, Validators: []string{"0xa"}}, {BFT: false}}}
	if got := v.LatestValidators(); len(got) != 1 || got[0] != "0xa" {
		t.Fatalf("got %v", got)
	}
}

// Epoch unknown: keep only votes in the current window (proposals are still listed), no backfill, tally not complete, gen incremented so an in-flight backfill stops;
// once known, the tally reaccumulates from the window start at that time instead of reusing the old VotesFrom.
func TestUpdateChainUnknownEpoch(t *testing.T) {
	backfillPause = time.Millisecond
	c := New(nil, nil, &chainRPC{voteAt: 1990})
	h, n := int64(2000), 4
	c.cache["a"] = Result{Snap: &Snapshot{Ledger: &api.LedgerSummary{ChainHeight: &h}, Consensus: &api.ConsensusStatus{ValidatorCount: &n}}}
	ref := VoteRef{"0x1111111111111111111111111111111111111111", "0x10fd2c060eed3590a5f7d538435ccf607b51fb9c"}
	c.chain.gen = 7
	c.updateChain(context.Background())
	c.mu.RLock()
	busy, gen := c.backfilling, c.chain.gen
	c.mu.RUnlock()
	v := c.Chain()
	if v.EpochLength != 0 || busy || v.VotesComplete() || gen != 8 {
		t.Fatalf("unknown epoch: epoch %d backfilling %v complete %v gen %d", v.EpochLength, busy, v.VotesComplete(), gen)
	}
	if v.Votes[ref].Block != 1990 || !v.Votes[ref].Time.Equal(time.Unix(1990, 0)) {
		t.Fatalf("視窗內的票應留著(提議要列得出來):%v", v.Votes)
	}
	// once the window slides past that vote it is no longer kept (only the current window counts)
	h = 1990 + KeepBlocks + 10
	c.updateChain(context.Background())
	if v := c.Chain(); len(v.Votes) != 0 {
		t.Fatalf("滑出視窗的票不該留:%v", v.Votes)
	}
	h += 100
	epoch := int64(30000)
	c.cache["b"] = Result{Snap: &Snapshot{EpochLength: &epoch}}
	c.updateChain(context.Background())
	v = c.Chain()
	if v.EpochLength != 30000 || v.VotesFrom != h-KeepBlocks {
		t.Fatalf("known again: epoch %d votesFrom %d", v.EpochLength, v.VotesFrom)
	}
}

// Latest block: use the lightweight update if newer than the cache; fall back to the cache's last block when the source changed or it was cleared; also use the cache's when the update is older.
func TestHead(t *testing.T) {
	c := New(nil, nil, nil)
	if _, ok := c.Head(); ok {
		t.Fatal("沒有區塊應為 false")
	}
	c.chain = ChainView{SourceNodeID: "a", Blocks: []BlockStamp{{Number: 10}}}
	c.head, c.headSource = &BlockStamp{Number: 12, TxCount: 1}, "a"
	if h, _ := c.Head(); h.Number != 12 || h.TxCount != 1 {
		t.Fatalf("輕量更新較新:%+v", h)
	}
	c.headSource = "b"
	if h, _ := c.Head(); h.Number != 10 {
		t.Fatalf("來源換了:%+v", h)
	}
	c.head, c.headSource = &BlockStamp{Number: 9}, "a"
	if h, _ := c.Head(); h.Number != 10 {
		t.Fatalf("比快取舊:%+v", h)
	}
	c.head = nil
	if h, _ := c.Head(); h.Number != 10 {
		t.Fatalf("清掉之後:%+v", h)
	}
}

// Vote cutoff: when counter-votes reach a majority, Besu discards all votes for the address (list unchanged), including others' earlier unretracted votes. As observed on a local four-node network:
// a1 and a2 vote to add x then retract (remove votes at 52, 54), b1 and b2 vote to add (34, 35), b1 retracts (56) -> 3 remove votes reach a majority, cutoff at 57.
func TestVoteCutoffs(t *testing.T) {
	x := "0x00000000000000000000000000000000000000dd"
	a1, a2, b1, b2 := "0x01", "0x02", "0x03", "0x04"
	v := ChainView{Blocks: []BlockStamp{{Number: 60, BFT: true, Validators: []string{a1, a2, b1, b2}}}, Changed: map[string]int64{},
		Votes: map[VoteRef]SeenVote{
			{Target: x, Voter: a1}: {Add: false, Block: 52}, {Target: x, Voter: a2}: {Add: false, Block: 54},
			{Target: x, Voter: b1}: {Add: false, Block: 56}, {Target: x, Voter: b2}: {Add: true, Block: 35},
		}}
	if c := v.VoteCutoffs()[x]; c != 57 {
		t.Fatalf("作廢點 %d", c)
	}
	if _, ok := v.LatestVote(x, b2); ok {
		t.Fatal("b2 的加入票已作廢")
	}
	// no majority yet: nothing discarded, the list change point still applies
	delete(v.Votes, VoteRef{Target: x, Voter: b1})
	v.Changed[x] = 10
	if c := v.VoteCutoffs()[x]; c != 10 {
		t.Fatalf("沒過半 %d", c)
	}
	// no list: the threshold cannot be determined, only the list change point is used
	if c := (ChainView{Changed: map[string]int64{x: 10}, Votes: v.Votes}).VoteCutoffs()[x]; c != 10 {
		t.Fatalf("沒有名單 %d", c)
	}
}

// New votes after the cutoff still count; votes of a validator removed from the list are not replayed (Besu dropped them when removing it).
func TestVoteCutoffsAfterAndStripped(t *testing.T) {
	x := "0x00000000000000000000000000000000000000dd"
	a, b, c, r := "0x01", "0x02", "0x03", "0x09"
	v := ChainView{Blocks: []BlockStamp{{Number: 80, BFT: true, Validators: []string{a, b, c}}}, Changed: map[string]int64{r: 50},
		Votes: map[VoteRef]SeenVote{
			{Target: x, Voter: r}: {Add: false, Block: 40}, // r removed at 50: this vote does not count
			{Target: x, Voter: a}: {Add: false, Block: 42},
		}}
	if c := v.VoteCutoffs()[x]; c != 0 {
		t.Fatalf("r 的票不該湊成過半:%d", c)
	}
	if _, ok := v.LatestVote(x, a); !ok {
		t.Fatal("a 的票還算數")
	}
	// a new round after the cutoff: that vote counts
	v.Votes = map[VoteRef]SeenVote{
		{Target: x, Voter: a}: {Add: false, Block: 60}, {Target: x, Voter: b}: {Add: false, Block: 61}, // majority at 61, votes discarded
		{Target: x, Voter: c}: {Add: true, Block: 70},
	}
	if cut := v.VoteCutoffs()[x]; cut != 62 {
		t.Fatalf("作廢點 %d", cut)
	}
	if _, ok := v.LatestVote(x, c); !ok {
		t.Fatal("作廢點之後的票照算")
	}
}

// A voter removed and re-added after voting (Changed[voter] after this vote): Besu dropped the vote when removing it, so after re-adding it does not count and does not help reach a majority.
func TestVoteReaddedVoter(t *testing.T) {
	x := "0x00000000000000000000000000000000000000dd"
	a, b, c := "0x01", "0x02", "0x03"
	v := ChainView{Blocks: []BlockStamp{{Number: 80, BFT: true, Validators: []string{a, b, c}}}, Changed: map[string]int64{b: 50}, Changes: map[string][]int64{b: {50}},
		Votes: map[VoteRef]SeenVote{
			{Target: x, Voter: a}: {Add: false, Block: 60},
			{Target: x, Voter: b}: {Add: false, Block: 40}, // b removed at 50 and re-added
		}}
	if _, ok := v.LatestVote(x, b); ok {
		t.Fatal("b 加回前的票不該算")
	}
	if _, ok := v.LatestVote(x, a); !ok {
		t.Fatal("a 的票還算數")
	}
	if c := v.VoteCutoffs()[x]; c != 0 {
		t.Fatalf("b 的票不該湊成過半:%d", c)
	}
	v.Votes[VoteRef{Target: x, Voter: b}] = SeenVote{Add: true, Block: 55} // re-votes after being re-added (opposite direction, does not make a majority)
	if _, ok := v.LatestVote(x, b); !ok {
		t.Fatal("加回之後的票照算")
	}
}

// The Changes returned by Chain() is a copy: callers modifying it do not touch the cache.
func TestChainCopiesChanges(t *testing.T) {
	c := &Collector{}
	c.chain.Changes = map[string][]int64{"0xa": {10, 20}}
	v := c.Chain()
	v.Changes["0xa"][0] = 99
	if c.chain.Changes["0xa"][0] != 10 {
		t.Fatal("Changes 共用了底層陣列")
	}
}

func TestHeadBehind(t *testing.T) {
	cache := map[string]Result{"a": healthy(100), "b": healthy(99), "d": healthy(98), "c": healthy(25), "x": {Snap: &Snapshot{}}}
	// block period unknown: tolerance 2 blocks; 1 behind counts as 0, exactly 2 behind counts
	got := headBehind(cache, 0)
	if got["a"] != 0 || got["b"] != 0 || got["d"] != 2 || got["c"] != 75 {
		t.Fatalf("容忍 2:%v", got)
	}
	if _, ok := got["x"]; ok {
		t.Fatal("RPC 打不到的不該算")
	}
	// 0.5 seconds per block: tolerance ceil(5/0.5)=10, 9 behind still counts as 0
	cache["b"] = healthy(91)
	if got := headBehind(cache, 0.5); got["b"] != 0 || got["c"] != 75 {
		t.Fatalf("容忍 10:%v", got)
	}
	// a node reporting itself syncing is not the chain head even if it is higher
	behind := 1
	tall := healthy(500)
	tall.Snap.Ledger.BlocksBehind = &behind
	cache["t"] = tall
	if got := headBehind(cache, 0); got["a"] != 0 || got["c"] != 75 {
		t.Fatalf("同步中的節點不該當鏈頭:%v", got)
	}
	if headBehind(map[string]Result{"x": {}}, 2) != nil {
		t.Fatal("沒有鏈頭不該算")
	}

	// the chain head looks only at self-reported status: a node the platform judged behind does not make chainSource switch away, and the larger of self-reported lag still applies
	c := healthy(25).Snap
	c.HeadBehind = new(int)
	*c.HeadBehind = 75
	if c.Status() != api.NodeStatusSyncing || *c.BlocksBehind() != 75 {
		t.Fatalf("平台算的落後要讓狀態變 syncing:%v %v", c.Status(), *c.BlocksBehind())
	}
	a := healthy(100)
	a.Snap.HeadBehind = new(int)
	*a.Snap.HeadBehind = 3 // even if last round computed it as behind, the chain-head source still picks it
	if id, _ := chainSource(map[string]Result{"a": a, "c": {Snap: c}}); id != "a" {
		t.Fatalf("鏈頭來源被 HeadBehind 影響:%s", id)
	}
	self := 9
	c.Ledger.BlocksBehind = &self
	*c.HeadBehind = 0
	if *c.BlocksBehind() != 9 {
		t.Fatalf("自己回報的較大時用自己回報的:%d", *c.BlocksBehind())
	}
	*c.HeadBehind = 20
	if *c.BlocksBehind() != 20 {
		t.Fatalf("平台算的較大時用平台算的:%d", *c.BlocksBehind())
	}
	c.HeadBehind = nil
	if *c.BlocksBehind() != 9 {
		t.Fatalf("平台沒算時用自己回報的:%d", *c.BlocksBehind())
	}
}

// Cutoff and removal boundaries: a vote exactly at the cutoff block counts; a removal in the same block as the vote, or exactly at to, does not count as removed after voting.
func TestVoteBoundaries(t *testing.T) {
	x, a := "0x00000000000000000000000000000000000000dd", "0x01"
	v := ChainView{Changes: map[string][]int64{a: {50}}}
	if v.VoteDiscarded(a, 50, math.MaxInt64) || v.VoteDiscarded(a, 49, 50) || !v.VoteDiscarded(a, 49, 51) {
		t.Fatal("VoteDiscarded 邊界")
	}
	cut := map[string]int64{x: 60}
	if !v.Counts(VoteRef{Target: x, Voter: a}, SeenVote{Block: 60}, cut) || v.Counts(VoteRef{Target: x, Voter: a}, SeenVote{Block: 59}, cut) {
		t.Fatal("Counts 邊界")
	}
}

// Votes are discarded exactly when the majority threshold is reached; votes in the list-change block are replayed too.
func TestVoteCutoffsBoundaries(t *testing.T) {
	x := "0x00000000000000000000000000000000000000dd"
	a, b, c, d := "0x01", "0x02", "0x03", "0x04"
	// 4 validators, threshold 3: exactly 3 add votes
	v := ChainView{Blocks: []BlockStamp{{Number: 80, BFT: true, Validators: []string{a, b, c, d}}},
		Votes: map[VoteRef]SeenVote{{Target: x, Voter: a}: {Add: true, Block: 60}, {Target: x, Voter: b}: {Add: true, Block: 61}, {Target: x, Voter: c}: {Add: true, Block: 62}}}
	if cut := v.VoteCutoffs()[x]; cut != 63 {
		t.Fatalf("剛好過半:%d", cut)
	}
	// 3 validators, threshold 2: one vote at the change-point block, one in the next block
	v = ChainView{Blocks: []BlockStamp{{Number: 80, BFT: true, Validators: []string{a, b, c}}}, Changed: map[string]int64{x: 50},
		Votes: map[VoteRef]SeenVote{{Target: x, Voter: a}: {Add: true, Block: 50}, {Target: x, Voter: b}: {Add: true, Block: 51}}}
	if cut := v.VoteCutoffs()[x]; cut != 52 {
		t.Fatalf("變動點那一塊的票:%d", cut)
	}
}

// The epoch start block belongs to this epoch: accepted on merge, kept on epoch change (votes, Changed and Changes alike).
func TestEpochStartBlockKept(t *testing.T) {
	ref := VoteRef{"0xt", "0xa"}
	var v ChainView
	startEpoch(&v, 100, 250)
	mergeTally(&v, map[VoteRef]SeenVote{ref: {Add: true, Block: 200}}, map[string][]int64{"0xt": {199, 200}})
	if v.Votes[ref].Block != 200 || v.Changed["0xt"] != 200 || !slices.Equal(v.Changes["0xt"], []int64{200}) {
		t.Fatalf("合併:%+v", v)
	}
	v.Votes[ref] = SeenVote{Add: true, Block: 300}
	v.Changed["0xt"], v.Changes["0xt"] = 300, []int64{250, 300}
	startEpoch(&v, 100, 350)
	if v.EpochStart != 300 || v.Votes[ref].Block != 300 || v.Changed["0xt"] != 300 || !slices.Equal(v.Changes["0xt"], []int64{300}) {
		t.Fatalf("換季:%+v", v)
	}
}

// The genesis time is fetched in the first round; never again afterwards.
func TestUpdateChainGenesisAt(t *testing.T) {
	rpc := &fakeRPC{answers: map[string]any{"eth_getBlockByNumber": map[string]string{"timestamp": "0x10"}}}
	c := New(nil, nil, rpc)
	c.cache["a"] = healthy(3)
	c.updateChain(context.Background())
	if g := c.Chain().GenesisAt; g == nil || !g.Equal(time.Unix(16, 0)) {
		t.Fatalf("GenesisAt %v", g)
	}
}

// Latest known block exactly one window above height: not yet a chain replacement, votes and gen stay (same as missingBlocks).
func TestUpdateChainWindowEdgeNotNewChain(t *testing.T) {
	c := New(nil, nil, &chainRPC{})
	h, epoch := int64(100), int64(30000)
	c.cache["a"] = Result{Snap: &Snapshot{Ledger: &api.LedgerSummary{ChainHeight: &h}, EpochLength: &epoch}}
	ref := VoteRef{"0xt", "0xa"}
	c.chain = ChainView{Blocks: []BlockStamp{{Number: h + KeepBlocks}}, Votes: map[VoteRef]SeenVote{ref: {Add: true, Block: 50}}, gen: 5}
	c.updateChain(context.Background())
	c.mu.RLock()
	gen := c.chain.gen
	c.mu.RUnlock()
	if v := c.Chain(); gen != 5 || v.Votes[ref].Block != 50 {
		t.Fatalf("gen %d votes %v", gen, v.Votes)
	}
}

// Epoch unknown: no backfill starts even if there are unread blocks before the window (otherwise it would scan the whole chain).
func TestUpdateChainUnknownEpochNoBackfill(t *testing.T) {
	h := int64(KeepBlocks + 2000)
	// a backfill would read blocks before the window (block 0 is read for the genesis time and does not count): such a read blocks until the test ends, so if one starts by mistake, backfilling is guaranteed to still be true
	rpc := &gatedRPC{below: h - KeepBlocks, gate: make(chan struct{})}
	t.Cleanup(func() { close(rpc.gate) })
	c := New(nil, nil, rpc)
	c.cache["a"] = healthy(h)
	c.updateChain(context.Background())
	c.mu.RLock()
	busy, from := c.backfilling, c.chain.VotesFrom
	c.mu.RUnlock()
	if busy || from != h-KeepBlocks {
		t.Fatalf("backfilling %v votesFrom %d", busy, from)
	}
}

// gatedRPC: answers like chainRPC; requests for blocks after block 0 and below below wait until gate is closed.
type gatedRPC struct {
	chainRPC
	below int64
	gate  chan struct{}
}

func (f *gatedRPC) Batch(ctx context.Context, node string, calls []rpcops.Call) ([]rpcops.Result, error) {
	for _, c := range calls {
		var n int64
		fmt.Sscanf(c.Params[0].(string), "0x%x", &n)
		if n > 0 && n < f.below {
			select { // bounded: if reading blocks before the window ever becomes synchronous, the test fails within seconds instead of hanging until the go test timeout
			case <-f.gate:
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
			break
		}
	}
	return f.chainRPC.Batch(ctx, node, calls)
}
