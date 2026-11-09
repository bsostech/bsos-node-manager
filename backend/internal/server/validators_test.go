package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/enode"
	"besu-manager/backend/internal/pki"
	"besu-manager/backend/internal/rpcops"
	"besu-manager/backend/internal/store"
)

func strp(s string) *string { return &s }

// Sent only to our own nodes whose validator address is in the current validator set;
// non-validators and addresses not in the set are skipped; address matching is case-insensitive.
func TestVoteTargets(t *testing.T) {
	set := []api.ValidatorSetEntry{{Address: "0xAAaa000000000000000000000000000000000001"}, {Address: "0xbb00000000000000000000000000000000000002"}}
	nodes := []store.Node{
		{ID: "node-a2", ValidatorAddress: strp("0xaaAA000000000000000000000000000000000001")},
		{ID: "node-a1", ValidatorAddress: strp("0xBB00000000000000000000000000000000000002")},
		{ID: "node-a3"}, // non-validator
		{ID: "node-a4", ValidatorAddress: strp("0xcc00000000000000000000000000000000000003")}, // has a key but not in the set
	}
	got := voteTargets(set, nodes)
	if len(got) != 2 || got[0] != "node-a1" || got[1] != "node-a2" {
		t.Fatalf("%v", got)
	}
	if got := voteTargets(set, nodes[2:]); len(got) != 0 {
		t.Fatalf("沒有驗證者節點 %v", got)
	}
}

// In progress = (address, direction) not yet reflected in the validator set; on-chain ballots are
// counted by VoteTally rules, and our own pendingVotes fill in those not yet written.
func TestGroupVotes(t *testing.T) {
	a1, a2, ext := "0xa100000000000000000000000000000000000001", "0xa200000000000000000000000000000000000002", "0xe000000000000000000000000000000000000003"
	gone := "0xd000000000000000000000000000000000000004" // validator already removed from the set
	x, y := "0xbb00000000000000000000000000000000000005", "0xcc00000000000000000000000000000000000006"
	set := []api.ValidatorSetEntry{{Address: a1}, {Address: strings.ToUpper(a2[:2]) + a2[2:]}, {Address: ext}, {Address: y}}
	voters := map[string]string{"node-a1": a1, "node-a2": a2}
	view := collector.ChainView{
		Votes: map[collector.VoteRef]collector.SeenVote{
			{Target: x, Voter: a1}:   {Add: true, Block: 120, Time: time.Unix(1_700_000_120, 0)}, // ours, counted
			{Target: x, Voter: ext}:  {Add: true, Block: 118},                                    // other organization
			{Target: x, Voter: gone}: {Add: true, Block: 119},                                    // voter no longer in the set: voided
			{Target: y, Voter: ext}:  {Add: true, Block: 90},                                     // y already in the set: the add is reflected, not listed
			{Target: y, Voter: a1}:   {Add: false, Block: 95},                                    // y's add took effect at 100: this ballot is before the change point, voided
			{Target: y, Voter: a2}:   {Add: false, Block: 130},                                   // removal ballot after the change point: counts
		},
		Changed: map[string]int64{y: 100},
	}
	pending := map[string]map[string]bool{
		"node-a1": {strings.ToUpper(x): true}, // already counted on chain: not duplicated
		"node-a2": {x: true, y: false},        // x not yet written; y's removal already counted at 130
	}
	got := groupVotes(set, pending, voters, view)
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	addX, dropY := got[0], got[1]
	if addX.Address != x || !addX.Add || len(addX.Voters) != 3 {
		t.Fatalf("加入 x: %+v", addX)
	}
	// Sorted by address: a1 (ours, 120), a2 (ours, not yet written), ext (other organization, 118)
	v1, v2, v3 := addX.Voters[0], addX.Voters[1], addX.Voters[2]
	if v1.Address != a1 || v1.NodeId == nil || *v1.NodeId != "node-a1" || v1.CountedAt == nil || *v1.CountedAt != 120 || v1.CountedAtTime == nil || !v1.CountedAtTime.Equal(time.Unix(1_700_000_120, 0)) {
		t.Fatalf("a1 %+v", v1)
	}
	if v2.Address != a2 || v2.NodeId == nil || v2.CountedAt != nil || v2.CountedAtTime != nil {
		t.Fatalf("a2 %+v", v2)
	}
	if v3.Address != ext || v3.NodeId != nil || v3.CountedAt == nil || *v3.CountedAt != 118 {
		t.Fatalf("ext %+v", v3)
	}
	if dropY.Address != y || dropY.Add || len(dropY.Voters) != 1 || dropY.Voters[0].Address != a2 || *dropY.Voters[0].CountedAt != 130 {
		t.Fatalf("移除 y: %+v", dropY)
	}
	// pending: true if the ballot is still on the node (our counted ballots are marked too), false
	// for other organizations
	is := func(p *bool, want bool) bool { return p != nil && *p == want }
	if !is(v1.Pending, true) || !is(v2.Pending, true) || !is(v3.Pending, false) || !is(dropY.Voters[0].Pending, true) {
		t.Fatalf("pending %+v %+v %+v %+v", v1, v2, v3, dropY.Voters[0])
	}
	// After withdrawal (the node reported, the ballot is not in the list): counted ballots are
	// still listed, pending is false
	if got := groupVotes(set, map[string]map[string]bool{"node-a1": {}}, voters, view); len(got) != 2 || !is(got[0].Voters[0].Pending, false) || got[0].Voters[0].CountedAt == nil {
		t.Fatalf("撤回後 %+v", got)
	}
	// No snapshot for the node (pending list not reported): unknown, pending omitted; other
	// organizations are still false
	if got := groupVotes(set, nil, voters, view); len(got) != 2 || got[0].Voters[0].Pending != nil || !is(got[0].Voters[1].Pending, false) {
		t.Fatalf("沒有快照 %+v", got)
	}
	// x was added and later removed (change point 140): an old ballot that only voted to add does
	// not revive "add x"
	view.Changed[x] = 140
	if got := groupVotes(set, nil, voters, view); len(got) != 1 || got[0].Address != y {
		t.Fatalf("移除生效後 %+v", got)
	}
	delete(view.Changed, x)
	// a1 already voted on chain to add x, and the node switched to removing x: x is not in the set,
	// so removal is a no-op (Besu will not cast it, and the on-chain add ballot still counts); no
	// extra row
	if got := groupVotes(set, map[string]map[string]bool{"node-a1": {x: false}}, voters, view); len(got) != 2 || !got[0].Add || got[0].Voters[0].CountedAt == nil {
		t.Fatalf("反方向待投 %+v", got)
	}
	// After it takes effect (x joined the set): the "add x" row disappears, even if the node still
	// holds the ballot
	if got := groupVotes(append(set, api.ValidatorSetEntry{Address: x}), pending, voters, view); len(got) != 1 || got[0].Address != y {
		t.Fatalf("生效後 %+v", got)
	}
}

// One node failing does not affect the others; failure reasons are reported per node.
func TestSendEach(t *testing.T) {
	got := sendEach([]string{"node-a1", "node-a2", "node-a3"}, func(id string) error {
		if id == "node-a2" {
			return errors.New("節點離線")
		}
		return nil
	})
	if len(got) != 3 || !got[0].Ok || got[1].Ok || got[1].Params == nil || (*got[1].Params)["detail"] != "節點離線" || !got[2].Ok {
		t.Fatalf("%+v", got)
	}
}

// The two cases where voting is impossible return a 409 message: consensus type unknown, and our
// organization has no validator node.
func TestVoteBlocked(t *testing.T) {
	if voteBlocked("", []string{"node-a1"}) != api.ErrorReasonVoteNoConsensus || voteBlocked("ibft2", nil) != api.ErrorReasonNoValidatorNodes || voteBlocked("qbft", []string{"node-a1"}) != "" {
		t.Fatal("voteBlocked")
	}
}

// Ballots in the same direction as the current state are rejected (different case counts as the
// same address); no rejection when the validator set is unknown.
func TestNoopVote(t *testing.T) {
	set := []api.ValidatorSetEntry{{Address: "0xab00000000000000000000000000000000000001"}}
	in, out := "0xAB00000000000000000000000000000000000001", "0xab00000000000000000000000000000000000002"
	if noopVote(set, in, true) != api.ErrorReasonAlreadyValidator || noopVote(set, out, false) != api.ErrorReasonNotValidator {
		t.Fatal("noop direction not blocked")
	}
	if noopVote(set, in, false) != "" || noopVote(set, out, true) != "" || noopVote(nil, in, true) != "" {
		t.Fatal("real vote blocked")
	}
}

func TestValidAddress(t *testing.T) {
	if !validAddress("0xAB00000000000000000000000000000000000001") || validAddress("0xab") || validAddress("ab00000000000000000000000000000000000000001") || validAddress("0xzz00000000000000000000000000000000000001") {
		t.Fatal("validAddress")
	}
}

// Round-robin proposing: sorted by the addresses of that block's validator set, a<b<c<d. round=0
// has only the proposer's slot; round=2 means the two ahead of it had their turn but did not
// produce.
func TestValidatorTurns(t *testing.T) {
	abcd := []string{"0xD", "0xa", "0xC", "0xb"}
	at := time.Unix(1_700_000_000, 0)
	blocks := []collector.BlockStamp{
		{Number: 1, Proposer: "0xa", BFT: true, Validators: abcd, Time: at, TxCount: 3, GasUsed: 21000, GasLimit: 30_000_000},
		{Number: 2, Proposer: "0xb", BFT: true, Validators: abcd},
		{Number: 3, Proposer: "0xd", BFT: true, Round: 1, Validators: abcd},                   // c should have produced but did not, so d did
		{Number: 4, Proposer: "0xb", BFT: true, Round: 2, Validators: abcd},                   // b produced only after two round changes: d and a, ahead of b, had their turn and missed
		{Number: 5, Proposer: "0xb", BFT: true, Round: 9, Validators: abcd},                   // round >= validator count: each of the other three gets one miss
		{Number: 6, Proposer: "0xzz", BFT: true, Validators: abcd},                            // not in that block's list: not marked
		{Number: 7, Proposer: "0xc"},                                                          // extraData cannot be decoded: not marked (cannot be treated as c having produced)
		{Number: 8, Proposer: "0xe", BFT: true, Round: 1, Validators: []string{"0xa", "0xe"}}, // list changed to a, e: a is ahead of e
	}
	got := validatorTurns(blocks, nil)
	want := map[string]string{"a": "1:+ 4:- 5:- 8:-", "b": "2:+ 4:+ 5:+", "c": "3:- 5:-", "d": "3:+ 4:- 5:-", "e": "8:+"}
	for a, w := range want {
		s := ""
		for i, x := range got[a] {
			if i > 0 {
				s += " "
			}
			mark := "-"
			if x.Proposed {
				mark = "+"
			}
			s += fmt.Sprintf("%d:%s", x.Block, mark)
		}
		if s != w {
			t.Errorf("%s: got %q want %q", a, s, w)
		}
	}
	// The block it produced carries time, tx count and gas; the missed turn is all null
	if x := got["a"][0]; x.At == nil || !x.At.Equal(at) || *x.TxCount != 3 || *x.GasUsed != 21000 || *x.GasLimit != 30_000_000 {
		t.Fatalf("出的那次: %+v", x)
	}
	if x := got["a"][1]; x.At != nil || x.TxCount != nil || x.GasUsed != nil || x.GasLimit != nil {
		t.Fatalf("漏的那次應全 null: %+v", x)
	}
	// extraData carries no list (validator set lives in a contract): sort by the current list
	contract := validatorTurns([]collector.BlockStamp{{Number: 9, Proposer: "0xb", BFT: true, Round: 1}}, abcd)
	if len(contract["a"]) != 1 || contract["a"][0].Proposed || len(contract["b"]) != 1 || !contract["b"][0].Proposed {
		t.Fatalf("沒帶名單: %+v", contract)
	}
	// No truncation: every turn in the cache is present
	many := make([]collector.BlockStamp, 0, 1000)
	for i := 0; i < 1000; i++ {
		many = append(many, collector.BlockStamp{Number: int64(i), BFT: true, Validators: abcd, Proposer: []string{"0xa", "0xb", "0xc", "0xd"}[i%4]})
	}
	if n := len(validatorTurns(many, nil)["a"]); n != 250 {
		t.Fatalf("want 250 turns, got %d", n)
	}
}

// Production verdict: 2 misses in a row = false; occasional misses are still true; after a long run
// of misses during downtime, one produced block on recovery is true; no turn history = null.
func TestProducing(t *testing.T) {
	turns := func(marks string) []api.ValidatorTurn {
		out := make([]api.ValidatorTurn, len(marks))
		for i, m := range marks {
			out[i] = api.ValidatorTurn{Block: int64(i), Proposed: m == '+'}
		}
		return out
	}
	for _, c := range []struct {
		name  string
		marks string
		want  *bool
	}{
		{"沒輪到過", "", nil},
		{"連漏 2 次", "++++--", new(false)},
		{"只輪到一次就漏", "-", new(true)},
		{"偶爾漏", "+-++-+++-+", new(true)},
		{"漏很多次沒連漏", strings.Repeat("-+", 20), new(true)},
		{"停機漏 16 次後恢復", strings.Repeat("-", 16) + "+", new(true)},
	} {
		got := producing(turns(c.marks))
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// Addresses reported on chain carry 0x with mixed case and still match our own nodes (the lookup
// table is keyed without 0x, so the chain address has to be normalized the same way).
func TestValidatorEntries(t *testing.T) {
	enA1, addrA1 := mkPeer(t, "a1", "10.0.0.1:30303")
	enA2, addrA2 := mkPeer(t, "a2", "10.0.0.2:30303")
	_, addrB1 := mkPeer(t, "b1", "172.16.0.1:30303")
	nodes := []store.Node{{ID: "node-a1", Org: "org-a", ReportedEnode: &enA1}, {ID: "node-a2", Org: "org-a", ReportedEnode: &enA2}}
	got := validatorEntries([]string{"0x" + strings.ToUpper(enode.NormalizeAddress(addrA1)), addrA2, addrB1}, nodes)
	if len(got) != 3 {
		t.Fatalf("got %d entries", len(got))
	}
	if got[0].NodeId == nil || *got[0].NodeId != "node-a1" || got[0].OrgId == nil || *got[0].OrgId != "org-a" {
		t.Errorf("大寫、帶 0x 的本單位地址沒對上:%+v", got[0])
	}
	if got[1].NodeId == nil || *got[1].NodeId != "node-a2" {
		t.Errorf("用 reported enode 推出的本單位地址沒對上:%+v", got[1])
	}
	if got[2].NodeId != nil || got[2].OrgId != nil {
		t.Errorf("外單位不該有 nodeId/orgId:%+v", got[2])
	}
}

// Chain-level numbers: not producing counts only producing=false (null does not count); fault
// tolerance f = N - ceil(2N/3) (Besu's production quorum; with 3 validators, 1 can stop and blocks
// are still produced).
func TestNewChainStats(t *testing.T) {
	set := []api.ValidatorSetEntry{{Producing: new(true)}, {Producing: new(false)}, {Producing: nil}, {Producing: new(false)}}
	if got := newChainStats(set, 2, true); got != (chainStats{blockPeriod: 2, validators: 4, notProducing: 2, tolerance: 1, halted: true}) {
		t.Fatalf("4 位: %+v", got)
	}
	for n, f := range map[int]int{1: 0, 2: 0, 3: 1, 4: 1, 6: 2, 7: 2, 10: 3} {
		if got := newChainStats(make([]api.ValidatorSetEntry, n), 0, false); got.tolerance != f {
			t.Errorf("%d 位: f want %d got %d", n, f, got.tolerance)
		}
	}
}

func TestKnownValidator(t *testing.T) {
	addr := "0x10FD2C060EED3590A5F7D538435CCF607B51FB9C"
	set := []string{"0x120cee331c15b3f6e129a80c1540167bd2f9a733", "0x10fd2c060eed3590a5f7d538435ccf607b51fb9c"}
	if v := knownValidator(&addr, set); v == nil || !*v {
		t.Fatalf("在驗證者集裡(大小寫不同)應為 true:%v", v)
	}
	other := "0x2222222222222222222222222222222222222222"
	if v := knownValidator(&other, set); v == nil || *v {
		t.Fatalf("不在驗證者集裡應為 false:%v", v)
	}
	if v := knownValidator(nil, set); v != nil {
		t.Fatalf("不知道節點地址應為 nil:%v", *v)
	}
	if v := knownValidator(&addr, nil); v != nil {
		t.Fatalf("不知道驗證者集應為 nil:%v", *v)
	}
}

func TestIsHalted(t *testing.T) {
	now := time.Unix(10_000, 0)
	if !isHalted(now.Add(-40*time.Second), now, 2, 20) {
		t.Fatal("2 秒一塊、40 秒沒出塊(20 倍)應算停鏈")
	}
	if isHalted(now.Add(-39*time.Second), now, 2, 20) {
		t.Fatal("還沒到 20 倍不算停鏈")
	}
	if isHalted(time.Time{}, now, 2, 20) || isHalted(now.Add(-time.Hour), now, 0, 20) {
		t.Fatal("不知道鏈頭或出塊秒數就當沒停")
	}
}

func TestDisconnectedFromAll(t *testing.T) {
	pubOf := func(c string) string { return strings.Repeat(c, 128) }
	pa2, pb1 := pubOf("2"), pubOf("3")
	a2, b1 := pubAddress(pa2), "0x"+strings.ToUpper(strings.TrimPrefix(pubAddress(pb1), "0x"))
	perNode := map[string][]collector.ObservedPeer{
		"node-b1": {{Pubkey: pb1}}, // another node of ours
		"node-b2": {{Pubkey: pb1}},
	}
	if !disconnectedFromAll(perNode, a2) {
		t.Fatal("本單位沒有任何節點連著 a2,應算斷線")
	}
	if disconnectedFromAll(perNode, b1) {
		t.Fatal("有節點連著的(地址大小寫不同)不算斷線")
	}
	if disconnectedFromAll(map[string][]collector.ObservedPeer{"node-b1": {}, "node-b2": {}}, a2) {
		t.Fatal("本單位節點的 peer 清單全空(多半是自己斷網)不能算對方斷線")
	}
	if disconnectedFromAll(nil, a2) {
		t.Fatal("一台都讀不到 peer 清單 = 不知道")
	}
}

// Per-node failure reasons: if the underlying error carries a reason use the root one, otherwise
// operation_failed + raw text; the successful node has none.
func TestSendEachReasons(t *testing.T) {
	offline := apierr.Wrap(api.ErrorReasonNodeUnreachable, map[string]string{"nodeId": "b"}, fmt.Errorf("agent b: %w", pki.ErrAgentOffline))
	noResp := apierr.Wrap(api.ErrorReasonNodeRpcNoResponse, map[string]string{"detail": offline.Error()}, fmt.Errorf("%w: %w", rpcops.ErrNoResponse, offline))
	besu := apierr.Wrap(api.ErrorReasonNodeRpcError, map[string]string{"method": "qbft_proposeValidatorVote", "code": "-32604", "message": "Method not enabled"},
		fmt.Errorf("qbft_proposeValidatorVote: %w", &rpcops.RPCError{Code: -32604, Message: "Method not enabled"}))
	dial := apierr.Wrap(api.ErrorReasonNodeRpcNoResponse, map[string]string{"detail": "connection refused"}, fmt.Errorf("%w: connection refused", rpcops.ErrNoResponse))
	errs := map[string]error{"b": noResp, "c": besu, "d": errors.New("boom"), "e": dial}
	out := sendEach([]string{"a", "b", "c", "d", "e"}, func(id string) error { return errs[id] })
	want := map[string]struct {
		reason api.ErrorReason
		k, v   string
	}{
		"b": {api.ErrorReasonNodeUnreachable, "nodeId", "b"},
		"c": {api.ErrorReasonNodeRpcError, "message", "Method not enabled"},
		"d": {api.ErrorReasonOperationFailed, "detail", "boom"},
		"e": {api.ErrorReasonNodeRpcNoResponse, "detail", "connection refused"},
	}
	for _, r := range out {
		w, failed := want[r.NodeId]
		if !failed {
			if !r.Ok || r.Reason != nil || r.Error != nil {
				t.Errorf("%s 成功不該帶原因: %+v", r.NodeId, r)
			}
			continue
		}
		if r.Ok || r.Reason == nil || *r.Reason != w.reason || r.Params == nil || (*r.Params)[w.k] != w.v || r.Error == nil || *r.Error != apierr.Message(w.reason, *r.Params) {
			t.Errorf("%s: %+v", r.NodeId, r)
		}
	}
}

// The vote list is sorted by address, not adds first: the smallest and largest addresses are
// removals, the middle one is an add.
func TestGroupVotesOrderByAddress(t *testing.T) {
	a1 := "0xa100000000000000000000000000000000000001"
	lo, mid, hi := "0x1100000000000000000000000000000000000011", "0x5500000000000000000000000000000000000055", "0x9900000000000000000000000000000000000099"
	set := []api.ValidatorSetEntry{{Address: a1}, {Address: lo}, {Address: hi}}
	view := collector.ChainView{Votes: map[collector.VoteRef]collector.SeenVote{
		{Target: lo, Voter: a1}: {Add: false, Block: 10}, {Target: mid, Voter: a1}: {Add: true, Block: 11}, {Target: hi, Voter: a1}: {Add: false, Block: 12},
	}}
	got := groupVotes(set, nil, map[string]string{"node-a1": a1}, view)
	if len(got) != 3 || got[0].Address != lo || got[0].Add || got[1].Address != mid || !got[1].Add || got[2].Address != hi || got[2].Add {
		t.Fatalf("%+v", got)
	}
}

// validatorRPC: a healthy IBFT 2.0 node at height 16 reporting the given validator set; block
// headers cannot be fetched, so the chain cache holds no blocks and no validator list of its own.
type validatorRPC struct{ set string }

func (f validatorRPC) Batch(_ context.Context, _ string, calls []rpcops.Call) ([]rpcops.Result, error) {
	out := make([]rpcops.Result, len(calls))
	for i, c := range calls {
		switch c.Method {
		case "eth_blockNumber":
			out[i] = rpcops.Result{Raw: []byte(`"0x10"`)}
		case "eth_syncing":
			out[i] = rpcops.Result{Raw: []byte(`false`)}
		case "ibft_getValidatorsByBlockNumber":
			out[i] = rpcops.Result{Raw: []byte(f.set)}
		default:
			out[i] = rpcops.Result{Err: &rpcops.RPCError{Code: -32601, Message: "Method not found"}}
		}
	}
	return out, nil
}

func (validatorRPC) ProbeAuth(context.Context, string) string { return "disabled" }

// With nothing in the chain cache, the validator set and consensus type come from what a node
// reports: the validators page lists them, the node tags fall back to them, and the chain-level
// gauges are produced because there is a source node and a known set.
func TestValidatorSetFromNodeConsensus(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	if err := s.store.UpsertOwnNode(ctx, "node-a1", "org-test", 8545, 9545, nil); err != nil {
		t.Fatal(err)
	}
	v1, v2 := "0x"+strings.Repeat("1", 40), "0x"+strings.Repeat("2", 40)
	s.col = collector.New(s.store, downRequester{}, validatorRPC{set: `["` + v1 + `","` + v2 + `"]`})
	pollCtx, cancel := context.WithCancel(context.Background())
	go s.col.Run(pollCtx, time.Hour)
	for i := 0; i < 100 && s.col.Chain().SourceNodeID == ""; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if s.col.Chain().SourceNodeID != "node-a1" || s.col.Chain().LatestValidators() != nil {
		t.Fatalf("fixture: want source node-a1 and no cached validator list, got %+v", s.col.Chain())
	}

	set, consensus, err := s.currentValidatorSet(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(set) != 2 || set[0].Address != v1 || set[1].Address != v2 || consensus != string(api.Ibft2) {
		t.Fatalf("validator set = %+v, consensus = %q", set, consensus)
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.validatorSet(nodes); len(got) != 2 || got[0] != v1 {
		t.Fatalf("validatorSet should fall back to the node's report: %v", got)
	}
	if cs, ok := s.currentChainStats(ctx); !ok || cs.validators != 2 {
		t.Fatalf("chain stats = %+v, ok = %v", cs, ok)
	}
}
