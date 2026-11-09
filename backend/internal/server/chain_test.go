package server

import (
	"strings"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/enode"
)

// Block quorum ceil(2N/3): when N is a multiple of 3 it must not add 1 (the old floor(2N/3)+1 was 1
// too many for N=3, 6, 12).
func TestBlockQuorum(t *testing.T) {
	for n, want := range map[int]int{1: 1, 3: 2, 4: 3, 6: 4, 7: 5, 12: 8} {
		if got := blockQuorum(n); got != want {
			t.Errorf("N=%d: got %d want %d", n, got, want)
		}
	}
}

func TestChainInfo(t *testing.T) {
	now := time.Unix(10_000, 0)
	if got := chainInfo(collector.ChainView{}, nil, collector.BlockStamp{}, false, now); got.SourceNodeId != nil || len(got.BlockIntervals) != 0 || got.Stability24h == nil {
		t.Fatalf("沒有來源:%+v", got)
	}
	h, id := int64(10), int64(1337)
	alg := api.Qbft
	vals := []string{"0x1", "0x2", "0x3", "0x4", "0x5", "0x6"}
	src := &collector.Snapshot{AsOf: now.Add(-7 * time.Second), ChainID: &id, Ledger: &api.LedgerSummary{ChainHeight: &h}, Consensus: &api.ConsensusStatus{Algorithm: &alg, Validators: &vals}}
	view := collector.ChainView{SourceNodeID: "node-a1", Blocks: []collector.BlockStamp{
		{Number: 8, Time: now.Add(-6 * time.Second)}, {Number: 9, Time: now.Add(-4 * time.Second)}, {Number: 10, Time: now}}}
	got := chainInfo(view, src, view.Blocks[2], true, now)
	if *got.SourceNodeId != "node-a1" || *got.ChainId != 1337 || *got.Consensus != api.Qbft || *got.Height != 10 {
		t.Fatalf("基本欄位:%+v", got)
	}
	if got.DataAgeSec == nil || *got.DataAgeSec != 7 {
		t.Fatalf("資料年齡:%v", got.DataAgeSec)
	}
	if *got.ValidatorCount != 6 || *got.BlockQuorum != 4 {
		t.Fatalf("門檻:%d/%d", *got.ValidatorCount, *got.BlockQuorum)
	}
	if len(got.BlockIntervals) != 2 || got.BlockIntervals[0] != 2 || got.BlockIntervals[1] != 4 || !got.LatestBlockAt.Equal(now) {
		t.Fatalf("間隔:%v %v", got.BlockIntervals, got.LatestBlockAt)
	}
	if got.EpochLength != nil || got.EvmVersion != nil || got.BlockPeriodSeconds != nil || got.GenesisAt != nil {
		t.Fatalf("不知道時為 null:%+v", got)
	}
	view.EpochLength, view.EpochStart, view.VotesFrom = 30000, 0, 7
	if got := chainInfo(view, src, view.Blocks[2], true, now); *got.EpochLength != 30000 || *got.EpochStart != 0 || *got.VotesFrom != 7 {
		t.Fatalf("epoch:%+v", got)
	}
	// The 2-second lightweight update got a block newer than the cache: height and latest block
	// time come from that block; the three block-time numbers, EVM version, genesis time
	gen := now.Add(-time.Hour)
	pending := 3
	src.Ledger.TxpoolPending = &pending
	view.BlockPeriod, view.EmptyBlockPeriod, view.GenesisAt = 2, 60, &gen
	view.Forks = []collector.Fork{{Name: "Shanghai", At: 0, AtTime: true}}
	head := collector.BlockStamp{Number: 11, Time: now.Add(2 * time.Second), TxCount: 1}
	got = chainInfo(view, src, head, true, now)
	if *got.Height != 11 || !got.LatestBlockAt.Equal(head.Time) || *got.EvmVersion != "Shanghai" || !got.GenesisAt.Equal(gen) {
		t.Fatalf("鏈頭:%+v", got)
	}
	if *got.BlockPeriodSeconds != 2 || *got.EmptyBlockPeriodSeconds != 60 || *got.ExpectedBlockPeriodSeconds != 2 {
		t.Fatalf("出塊時間:%v %v %v", *got.BlockPeriodSeconds, *got.EmptyBlockPeriodSeconds, *got.ExpectedBlockPeriodSeconds)
	}
	pending = 0
	if got := chainInfo(view, src, head, true, now); *got.ExpectedBlockPeriodSeconds != 60 {
		t.Fatalf("池空時應為空塊間隔:%v", *got.ExpectedBlockPeriodSeconds)
	}
}

// One of our nodes is definitely not producing: the agent is up but RPC is unreachable, or the
// agent disconnected while the chain is halted; an agent disconnect while the chain is still
// producing does not count (left to the turn history), nor do 401 (Besu is still signing blocks)
// and never connected (just restarted).
func TestOwnNodeNotProducing(t *testing.T) {
	h := int64(1)
	for name, tc := range map[string]struct {
		res    collector.Result
		halted bool
		want   bool
	}{
		"從未連上":          {collector.Result{}, true, false},
		"agent 斷線、鏈在出塊": {collector.Result{EverSeen: true}, false, false},
		"agent 斷線、停鏈":   {collector.Result{EverSeen: true}, true, true},
		"RPC 打不到":       {collector.Result{Snap: &collector.Snapshot{}, EverSeen: true}, false, true},
		"RPC 401":       {collector.Result{Snap: &collector.Snapshot{RpcUnauthorized: true}, EverSeen: true}, true, false},
		"健康":            {collector.Result{Snap: &collector.Snapshot{Ledger: &api.LedgerSummary{ChainHeight: &h}}, EverSeen: true}, true, false},
	} {
		if got := ownNodeNotProducing(tc.res, tc.halted); got != tc.want {
			t.Errorf("%s: got %v want %v", name, got, tc.want)
		}
	}
}

// Round timeouts: how many times each scheduled proposer was skipped, most to least; our own carry
// org / nodeId and named ones carry the name; validators never skipped are not listed.
func TestRoundChangesOf(t *testing.T) {
	a, b, c := "0x00000000000000000000000000000000000000aa", "0x00000000000000000000000000000000000000bb", "0x00000000000000000000000000000000000000cc"
	miss, hit := api.ValidatorTurn{Proposed: false}, api.ValidatorTurn{Proposed: true}
	turns := map[string][]api.ValidatorTurn{
		enode.NormalizeAddress(a): {miss, hit}, enode.NormalizeAddress(b): {miss, miss, miss}, enode.NormalizeAddress(c): {hit},
	}
	org, node := "org-A", "node-a1"
	set := []api.ValidatorSetEntry{{Address: a, OrgId: &org, NodeId: &node}, {Address: b}, {Address: c}}
	got := roundChangesOf(turns, set, map[string]string{enode.NormalizeAddress(b): "北區"})
	if got.Total != 4 || len(got.ByProposer) != 2 {
		t.Fatalf("%+v", got)
	}
	if p := got.ByProposer[0]; p.Address != b || p.Count != 3 || p.Name == nil || *p.Name != "北區" || p.OrgId != nil {
		t.Fatalf("第一位:%+v", p)
	}
	if p := got.ByProposer[1]; p.Address != a || p.Count != 1 || *p.OrgId != "org-A" || *p.NodeId != "node-a1" {
		t.Fatalf("第二位:%+v", p)
	}
	if got := roundChangesOf(nil, set, nil); got.Total != 0 || got.ByProposer == nil {
		t.Fatalf("沒有逾時:%+v", got)
	}
}

// Last hour: counted back from now, so after a long halt no blocks remain.
func TestLastHour(t *testing.T) {
	now := time.Unix(100_000, 0)
	blocks := []collector.BlockStamp{{Number: 1, Time: now.Add(-2 * time.Hour)}, {Number: 2, Time: now.Add(-time.Hour)}, {Number: 3, Time: now.Add(-time.Minute)}}
	if got := lastHour(blocks, now); len(got) != 2 || got[0].Number != 2 {
		t.Fatalf("%+v", got)
	}
	if got := lastHour(blocks, now.Add(3*time.Hour)); len(got) != 0 {
		t.Fatalf("停鏈很久:%+v", got)
	}
}

// With 3600 cached blocks, turn history only look at the last 1000: earlier missed rounds do not
// count toward turn history (the validators page's missed-round count covers only this range too).
func TestTurnsUseLastWindowOnly(t *testing.T) {
	a, b := "0x00000000000000000000000000000000000000aa", "0x00000000000000000000000000000000000000bb"
	var blocks []collector.BlockStamp
	for n := int64(1); n <= collector.KeepBlocks; n++ {
		round := 0
		if n <= 100 { // long ago a missed 50 times (round 1, proposer b = a's turn but it did not produce)
			round = int(n % 2)
		}
		proposer := a
		if round == 1 || n%2 == 0 {
			proposer = b
		}
		blocks = append(blocks, collector.BlockStamp{Number: n, BFT: true, Round: round, Proposer: proposer, Validators: []string{a, b}})
	}
	view := collector.ChainView{Blocks: blocks}
	turns := validatorTurns(view.TurnBlocks(), []string{a, b})
	for _, tr := range turns[enode.NormalizeAddress(a)] {
		if !tr.Proposed {
			t.Fatalf("最後 1000 塊之前的漏回合不該算進來:%+v", tr)
		}
	}
	if all := validatorTurns(view.Blocks, []string{a, b}); missedTurns(all[enode.NormalizeAddress(a)]) == 0 {
		t.Fatal("整個快取的話 a 會有漏回合(這個測試的前提)")
	}
}

// With more cached blocks than BlockWindow, block intervals are exactly BlockWindow entries, taken
// from the last stretch (block i is i seconds after the previous one).
func TestChainInfoIntervalsLastWindow(t *testing.T) {
	now := time.Unix(100_000, 0)
	n := collector.BlockWindow + 5
	var blocks []collector.BlockStamp
	for i := 0; i < n; i++ {
		blocks = append(blocks, collector.BlockStamp{Number: int64(i), Time: now.Add(time.Duration(i*(i+1)/2) * time.Second)})
	}
	src := &collector.Snapshot{AsOf: now, Ledger: &api.LedgerSummary{}}
	got := chainInfo(collector.ChainView{SourceNodeID: "node-a1", Blocks: blocks}, src, collector.BlockStamp{}, false, now)
	if len(got.BlockIntervals) != collector.BlockWindow || got.BlockIntervals[0] != float32(n-collector.BlockWindow) || got.BlockIntervals[len(got.BlockIntervals)-1] != float32(n-1) {
		t.Fatalf("間隔 %d 筆:%v", len(got.BlockIntervals), got.BlockIntervals)
	}
}

// Round timeout ordering: count most to least, ties by address. turns is a map, so run several
// times to get different iteration orders.
func TestRoundChangesOfOrder(t *testing.T) {
	addr := func(c string) string { return "0x" + strings.Repeat("0", 38) + c }
	missN := func(n int) []api.ValidatorTurn { return make([]api.ValidatorTurn, n) } // Proposed defaults to false = did not produce
	// Lower addresses have fewer misses, and only 13/14 tie: sorting by count is the reverse of
	// sorting by address
	counts := map[string]int{"11": 1, "13": 2, "14": 2, "15": 3, "16": 4, "17": 5}
	turns := map[string][]api.ValidatorTurn{}
	for c, n := range counts {
		turns[enode.NormalizeAddress(addr(c))] = missN(n)
	}
	want := []string{addr("17"), addr("16"), addr("15"), addr("13"), addr("14"), addr("11")}
	for range 10 {
		got := roundChangesOf(turns, nil, nil)
		var order []string
		for _, p := range got.ByProposer {
			order = append(order, p.Address)
		}
		if strings.Join(order, ",") != strings.Join(want, ",") {
			t.Fatalf("排序:\n got %v\nwant %v", order, want)
		}
	}
}
