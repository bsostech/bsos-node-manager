package server

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"besu-manager/backend/internal/collector"
)

func TestResidualVotes(t *testing.T) {
	me := "0xa100000000000000000000000000000000000001"
	in, out := "0xbb00000000000000000000000000000000000002", "0xcc00000000000000000000000000000000000003"
	set := []string{me, strings.ToUpper(in[:2]) + strings.ToUpper(in[2:])}
	view := collector.ChainView{Votes: map[collector.VoteRef]collector.SeenVote{}, Changed: map[string]int64{}}

	// (add, remove) x (in set, not in set): only directions already satisfied count as leftovers
	pending := map[string]bool{in: true, out: false}
	if got := residualVotes(pending, set, me, view); !reflect.DeepEqual(got, []pendingVote{{in, true}, {out, false}}) {
		t.Fatalf("已滿足 %+v", got)
	}
	if got := residualVotes(map[string]bool{in: false, out: true}, set, me, view); len(got) != 0 {
		t.Fatalf("投票中的不撤 %+v", got)
	}

	// This node has its own remove vote for in on chain (after the change point): the node's "add
	// in" is a cancel vote, not withdrawn
	view.Votes[collector.VoteRef{Target: in, Voter: me}] = collector.SeenVote{Add: false, Block: 120}
	view.Changed[in] = 100
	if got := residualVotes(map[string]bool{in: true}, set, me, view); len(got) != 0 {
		t.Fatalf("取消票 %+v", got)
	}
	// That remove vote is before the change point (already void): add in is a leftover
	view.Changed[in] = 130
	if got := residualVotes(map[string]bool{in: true}, set, me, view); len(got) != 1 {
		t.Fatalf("作廢的鏈上票不擋 %+v", got)
	}
	// A same-direction vote on chain: leftover
	view.Votes[collector.VoteRef{Target: out, Voter: me}] = collector.SeenVote{Add: false, Block: 140}
	if got := residualVotes(map[string]bool{out: false}, set, me, view); len(got) != 1 {
		t.Fatalf("同方向 %+v", got)
	}
	// Others' opposite votes do not count: only this node's own
	view.Votes[collector.VoteRef{Target: out, Voter: "0xe000000000000000000000000000000000000009"}] = collector.SeenVote{Add: true, Block: 150}
	if got := residualVotes(map[string]bool{out: false}, set, me, view); len(got) != 1 {
		t.Fatalf("別人的票 %+v", got)
	}
}

// pendingOps is a fake NodeOps implementing only PendingVotes (no other method is called).
type pendingOps struct {
	NodeOps
	pending map[string]map[string]bool
}

func (o pendingOps) PendingVotes(_ context.Context, nodeID, _ string) (map[string]bool, error) {
	p, ok := o.pending[nodeID]
	if !ok {
		return nil, errors.New("讀不到")
	}
	return p, nil
}

func TestFreshResiduals(t *testing.T) {
	x, y := "0xbb00000000000000000000000000000000000002", "0xcc00000000000000000000000000000000000003"
	view := collector.ChainView{Votes: map[collector.VoteRef]collector.SeenVote{}, Changed: map[string]int64{}}
	set := []string{x}
	cands := []residualCand{
		{nodeID: "n1", voter: "0xa1", validators: set}, // the snapshot has a leftover "add x"
		{nodeID: "n2", voter: "0xa2", validators: set}, // read fails
		{nodeID: "n3", voter: "0xa3", validators: set},
	}
	ops := pendingOps{pending: map[string]map[string]bool{
		"n1": {x: true},
		"n3": {x: false, y: false}, // on re-read the direction has become remove x (being voted); y is not in the set, so remove is satisfied
	}}
	now := time.Now()
	got := freshResiduals(context.Background(), ops, cands, view, map[string]time.Time{}, now)
	if len(got) != 2 || got[0].nodeID != "n1" || !reflect.DeepEqual(got[0].votes, []pendingVote{{x, true}}) ||
		got[1].nodeID != "n3" || !reflect.DeepEqual(got[1].votes, []pendingVote{{y, false}}) {
		t.Fatalf("%+v", got)
	}
	// The platform just voted on x: the snapshot may not have caught up, so x is not withdrawn this
	// round
	got = freshResiduals(context.Background(), ops, cands, view, map[string]time.Time{x: now.Add(-time.Minute)}, now)
	if len(got) != 1 || got[0].nodeID != "n3" {
		t.Fatalf("剛動過的地址 %+v", got)
	}
	// Past voteQuiet it is withdrawn as usual
	if got = freshResiduals(context.Background(), ops, cands, view, map[string]time.Time{x: now.Add(-voteQuiet)}, now); len(got) != 2 {
		t.Fatalf("過了 voteQuiet %+v", got)
	}
}
