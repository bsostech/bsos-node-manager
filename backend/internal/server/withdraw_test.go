package server

import (
	"context"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/store"
)

// Fixture for withdrawal tests: four validators, a1 and a2 belong to our organization, and the vote
// target is x (not in the set).
const (
	wa1 = "0xa100000000000000000000000000000000000001"
	wa2 = "0xa200000000000000000000000000000000000002"
	wb1 = "0xb100000000000000000000000000000000000003"
	wb2 = "0xb200000000000000000000000000000000000004"
	wx  = "0xcc00000000000000000000000000000000000005"
)

func wSet() []api.ValidatorSetEntry {
	return []api.ValidatorSetEntry{{Address: wa1}, {Address: wa2}, {Address: wb1}, {Address: wb2}}
}

func wView(votes map[collector.VoteRef]collector.SeenVote) collector.ChainView {
	return collector.ChainView{EpochLength: 30000, Votes: votes, Changed: map[string]int64{},
		Blocks: []collector.BlockStamp{{Number: 200, BFT: true, Validators: []string{wa1, wa2, wb1, wb2}}}}
}

func wv(add bool, n int64) collector.SeenVote {
	return collector.SeenVote{Add: add, Block: n, Time: time.Unix(1_700_000_000+n, 0).UTC()}
}

// Withdrawal routing: ballots already counted on chain are offset with an opposite-direction vote;
// ones only in the pending list (or already withdrawn) just have the pending vote removed;
// non-validators are not sent anything.
func TestWithdrawPlan(t *testing.T) {
	a1, a2, a3 := wa1, wa2, "0xa300000000000000000000000000000000000006"
	nodes := []store.Node{{ID: "node-a1", ValidatorAddress: &a1}, {ID: "node-a2", ValidatorAddress: &a2}, {ID: "node-a3", ValidatorAddress: &a3}, {ID: "node-x"}}
	view := wView(map[collector.VoteRef]collector.SeenVote{
		{Target: wx, Voter: wa1}: wv(true, 100), // counted: offset it
		{Target: wx, Voter: "0xa300000000000000000000000000000000000006"}: wv(false, 120), // already a counter-vote: do not offset again
	})
	got := withdrawPlan(nodes, view, "0xCC00000000000000000000000000000000000005")
	if len(got) != 1 || got["node-a1"] != true {
		t.Fatalf("%+v", got)
	}
}

// The in-progress vote row: withdrawing (counter-vote pending, chain still has the original
// direction) still counts and carries withdrawing; withdrawn (latest on chain is the counter-vote)
// is listed with countedAt null and withdrawnAt set; a vote with only withdrawn ballots left is not
// listed. Voting the original direction again while withdrawing -> a normal counted ballot.
func TestGroupVotesWithdrawal(t *testing.T) {
	voters := map[string]string{"node-a1": wa1, "node-a2": wa2}
	view := wView(map[collector.VoteRef]collector.SeenVote{
		{Target: wx, Voter: wa1}: wv(true, 100),  // a1 counted, withdrawing
		{Target: wx, Voter: wa2}: wv(false, 130), // a2 withdrawn (counter-vote written at 130)
		{Target: wx, Voter: wb1}: wv(true, 110),  // another organization
	})
	pending := map[string]map[string]bool{"node-a1": {wx: false}, "node-a2": {}}
	got := groupVotes(wSet(), pending, voters, view)
	if len(got) != 1 || len(got[0].Voters) != 3 {
		t.Fatalf("%+v", got)
	}
	by := map[string]api.ValidatorVoter{}
	for _, v := range got[0].Voters {
		by[v.Address] = v
	}
	if v := by[wa1]; v.Withdrawing == nil || !*v.Withdrawing || v.CountedAt == nil || *v.Pending {
		t.Fatalf("a1 撤回中:%+v", v)
	}
	if v := by[wa2]; v.WithdrawnAt == nil || v.CountedAt != nil || *v.Pending || v.Withdrawing != nil {
		t.Fatalf("a2 已撤回:%+v", v)
	}
	if v := by[wb1]; v.Withdrawing != nil || v.WithdrawnAt != nil {
		t.Fatalf("別單位:%+v", v)
	}
	// Voting the original direction again while withdrawing: the pending list has the original
	// direction, so it is a normal counted ballot
	got = groupVotes(wSet(), map[string]map[string]bool{"node-a1": {wx: true}, "node-a2": {}}, voters, view)
	for _, v := range got[0].Voters {
		if v.Address == wa1 && (v.Withdrawing != nil || !*v.Pending) {
			t.Fatalf("投回原方向:%+v", v)
		}
	}
	// The other organization withdrew too: only our withdrawn ballots remain, so the vote is not
	// listed
	view.Votes[collector.VoteRef{Target: wx, Voter: wb1}] = wv(false, 140)
	view.Votes[collector.VoteRef{Target: wx, Voter: wa1}] = wv(false, 135)
	if got := groupVotes(wSet(), map[string]map[string]bool{"node-a1": {}, "node-a2": {}}, voters, view); len(got) != 0 {
		t.Fatalf("票數歸零不列:%+v", got)
	}
}

// Leftover votes: once this node's same-direction vote is already in a block (the withdrawal
// counter-vote), remove it without waiting for voteQuiet; ones not yet in a block still wait as
// before.
func TestFreshResidualsLandedSkipsQuiet(t *testing.T) {
	view := wView(map[collector.VoteRef]collector.SeenVote{{Target: wx, Voter: wa1}: wv(false, 130)})
	cands := []residualCand{{nodeID: "n1", voter: wa1, validators: []string{wa1, wa2, wb1, wb2}}, {nodeID: "n2", voter: wa2, validators: []string{wa1, wa2, wb1, wb2}}}
	ops := pendingOps{pending: map[string]map[string]bool{"n1": {wx: false}, "n2": {wx: false}}}
	now := time.Now()
	got := freshResiduals(context.Background(), ops, cands, view, map[string]time.Time{wx: now}, now)
	if len(got) != 1 || got[0].nodeID != "n1" {
		t.Fatalf("%+v", got)
	}
}

// Vote history: our withdrawn ballots keep the original ballot with withdrawnAt and do not count;
// other organizations switching direction are still removed. Withdrawals are also marked when the
// vote ends (count dropped to zero).
func TestReconcileOwnWithdrawal(t *testing.T) {
	set := []string{wa1, wa2, wb1, wb2}
	own := map[string]bool{wa1: true, wa2: true}
	b100, at100, b110, at110 := int64(100), time.Unix(1_700_000_100, 0).UTC(), int64(110), time.Unix(1_700_000_110, 0).UTC()
	open := []store.VoteRecord{{ID: 3, Address: wx, Add: true, Result: store.VoteOpen, FirstBlock: 100, FirstAt: at100,
		Ballots: []store.Ballot{{Address: wa1, Block: &b100, At: &at100}, {Address: wb1, Block: &b110, At: &at110}}}}
	view := wView(map[collector.VoteRef]collector.SeenVote{
		{Target: wx, Voter: wa1}: wv(false, 130), // a1 withdraws
		{Target: wx, Voter: wb1}: wv(true, 110),
	})
	lookup := func(n int64) (collector.BlockStamp, bool) {
		return collector.BlockStamp{Number: n, Time: time.Unix(1_700_000_000+n, 0)}, true
	}
	got := reconcileVotes(open, nil, set, own, view, lookup)
	if len(got) != 1 || got[0].Votes != 1 {
		t.Fatalf("%+v", got)
	}
	var a1 store.Ballot
	for _, b := range got[0].Ballots {
		if b.Address == wa1 {
			a1 = b
		}
	}
	if a1.Block == nil || *a1.Block != 100 || a1.WithdrawnAt == nil || countVoted(got[0].Ballots) != 1 {
		t.Fatalf("a1 撤回 %+v", got[0].Ballots)
	}
	// b1 also switches to the opposite direction (another organization): removed; only the
	// withdrawn one is left -> count drops to zero, ending at the latest opposite ballot, and a1
	// still has withdrawnAt
	view.Votes[collector.VoteRef{Target: wx, Voter: wb1}] = wv(false, 150)
	got = reconcileVotes([]store.VoteRecord{{ID: 3, Address: wx, Add: true, Result: store.VoteOpen, FirstBlock: 100, FirstAt: at100, Ballots: got[0].Ballots}}, nil, set, own, view, lookup)
	if len(got) != 1 || got[0].Result != store.VoteExpired || *got[0].EndedBlock != 150 || got[0].Votes != 0 {
		t.Fatalf("歸零 %+v", got)
	}
	for _, b := range got[0].Ballots {
		if b.Address == wa1 && b.WithdrawnAt == nil || b.Address == wb1 && b.Block != nil {
			t.Fatalf("a1 要帶 withdrawnAt、b1(別單位改投)要變成沒投 %+v", got[0].Ballots)
		}
	}
	// Response: a withdrawn ballot keeps its withdrawnAt
	end, at := int64(150), time.Unix(1_700_000_150, 0)
	rec := got[0]
	rec.EndedBlock, rec.EndedAt = &end, &at
	if h := voteHistory([]store.VoteRecord{rec}, map[string]bool{"0xa100000000000000000000000000000000000001": true}, nil); h[0].Ballots[0].WithdrawnAt == nil {
		t.Fatalf("%+v", h)
	}
}

// Voting the original direction again after withdrawing (counts again this round): no longer marked
// withdrawn, and it counts.
func TestReconcileRevoteAfterWithdrawal(t *testing.T) {
	set := []string{wa1, wa2, wb1, wb2}
	own := map[string]bool{wa1: true}
	b100, at100, at130 := int64(100), time.Unix(1_700_000_100, 0).UTC(), time.Unix(1_700_000_130, 0).UTC()
	open := []store.VoteRecord{{ID: 3, Address: wx, Add: true, Result: store.VoteOpen, FirstBlock: 100, FirstAt: at100,
		Ballots: []store.Ballot{{Address: wa1, Block: &b100, At: &at100, WithdrawnAt: &at130}}}}
	view := wView(map[collector.VoteRef]collector.SeenVote{{Target: wx, Voter: wa1}: wv(true, 150)}) // 150 votes add again
	got := reconcileVotes(open, nil, set, own, view, func(n int64) (collector.BlockStamp, bool) { return collector.BlockStamp{Number: n}, true })
	if len(got) != 1 || got[0].Votes != 1 {
		t.Fatalf("%+v", got)
	}
	for _, b := range got[0].Ballots {
		if b.Address == wa1 && b.WithdrawnAt != nil {
			t.Fatalf("重投後不該還標撤回 %+v", b)
		}
	}
}

// An opposite-direction ballot cast by our organization after the pass (a different vote: proposing
// to remove a node just added) is not a withdrawal of this one; the same goes for one cast in the
// block of the validator-set change (150).
func TestReconcilePassedIgnoresLaterOppositeVote(t *testing.T) {
	own := map[string]bool{wa1: true}
	b100, at100 := int64(100), time.Unix(1_700_000_100, 0).UTC()
	after := []string{wx, wa1, wa2, wb1, wb2}
	lookup := func(n int64) (collector.BlockStamp, bool) {
		v := after
		if n < 150 {
			v = []string{wa1, wa2, wb1, wb2}
		}
		return collector.BlockStamp{Number: n, Time: time.Unix(1_700_000_000+n, 0), Validators: v}, true
	}
	for _, at := range []int64{150, 160} {
		open := []store.VoteRecord{{ID: 3, Address: wx, Add: true, Result: store.VoteOpen, FirstBlock: 100, FirstAt: at100,
			Ballots: []store.Ballot{{Address: wa1, Block: &b100, At: &at100}}}}
		view := wView(map[collector.VoteRef]collector.SeenVote{
			{Target: wx, Voter: wa1}: wv(false, at), // after the add passed at 150, a1 proposes removing x
			{Target: wx, Voter: wa2}: wv(true, 120), {Target: wx, Voter: wb1}: wv(true, 149),
		})
		view.Changed = map[string]int64{wx: 150}
		got := reconcileVotes(open, nil, after, own, view, lookup)
		var passed store.VoteRecord
		for _, r := range got {
			if r.Result == store.VotePassed {
				passed = r
			}
		}
		if passed.ID != 3 || passed.Votes != 3 {
			t.Fatalf("移除票在 %d:通過要 3 票(a1 的移除是另一場) %+v", at, got)
		}
		for _, b := range passed.Ballots {
			if b.WithdrawnAt != nil {
				t.Fatalf("移除票在 %d:不該標撤回 %+v", at, passed.Ballots)
			}
		}
	}
}

// When counter-votes reach a majority (list unchanged) the whole vote is voided: not listed, b2 who
// did not withdraw does not count, and withdrawal routing no longer casts an opposite vote for b2.
func TestWithdrawalReachesMajority(t *testing.T) {
	voters := map[string]string{"node-a1": wa1, "node-a2": wa2}
	view := wView(map[collector.VoteRef]collector.SeenVote{
		{Target: wx, Voter: wa1}: wv(false, 52), {Target: wx, Voter: wa2}: wv(false, 54),
		{Target: wx, Voter: wb1}: wv(false, 56), {Target: wx, Voter: wb2}: wv(true, 35),
	})
	if got := groupVotes(wSet(), map[string]map[string]bool{"node-a1": {}, "node-a2": {}}, voters, view); len(got) != 0 {
		t.Fatalf("整場作廢不列:%+v", got)
	}
	b2 := wb2
	if plan := withdrawPlan([]store.Node{{ID: "node-b2", ValidatorAddress: &b2}}, view, wx); len(plan) != 0 {
		t.Fatalf("作廢的票不用抵銷:%+v", plan)
	}
}

// A new round after the void: our voters who withdrew in the previous round are not marked
// withdrawn.
func TestGroupVotesNewRoundAfterCutoff(t *testing.T) {
	voters := map[string]string{"node-a1": wa1, "node-a2": wa2}
	view := wView(map[collector.VoteRef]collector.SeenVote{
		{Target: wx, Voter: wa1}: wv(false, 45), {Target: wx, Voter: wb1}: wv(false, 48), {Target: wx, Voter: wb2}: wv(false, 50), // majority at 50 voids it
		{Target: wx, Voter: wa2}: wv(true, 60), // new round
	})
	got := groupVotes(wSet(), map[string]map[string]bool{"node-a1": {}, "node-a2": {}}, voters, view)
	if len(got) != 1 || len(got[0].Voters) != 1 || got[0].Voters[0].Address != wa2 {
		t.Fatalf("新一輪只有 a2:%+v", got)
	}
}

// A voter removed and re-added after voting: Besu has already dropped that ballot, so the vote page
// does not list it and it is not a withdrawal.
func TestGroupVotesReaddedVoter(t *testing.T) {
	voters := map[string]string{"node-a1": wa1, "node-a2": wa2}
	view := wView(map[collector.VoteRef]collector.SeenVote{
		{Target: wx, Voter: wa1}: wv(true, 40), // a1 removed at 50, re-added at 120
		{Target: wx, Voter: wa2}: wv(true, 60),
		{Target: wx, Voter: wb1}: wv(false, 30), // b1's opposite-direction ballot from before it was removed and re-added
	})
	view.Changed[wa1], view.Changed[wb1] = 120, 100
	view.Changes = map[string][]int64{wa1: {50, 120}, wb1: {100}}
	got := groupVotes(wSet(), map[string]map[string]bool{"node-a1": {}, "node-a2": {}}, voters, view)
	if len(got) != 1 || len(got[0].Voters) != 1 || got[0].Voters[0].Address != wa2 {
		t.Fatalf("只剩 a2:%+v", got)
	}
}
