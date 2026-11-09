package server

import (
	"context"
	"math"
	"slices"
	"testing"
	"time"

	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/enode"
	"besu-manager/backend/internal/store"
)

// Test fixture: four validators v1..v4, vote target x.
const (
	tv1 = "0x0000000000000000000000000000000000000001"
	tv2 = "0x0000000000000000000000000000000000000002"
	tv3 = "0x0000000000000000000000000000000000000003"
	tv4 = "0x0000000000000000000000000000000000000004"
	tvx = "0x00000000000000000000000000000000000000aa"
)

var tset = []string{tv1, tv2, tv3, tv4}

func tAt(n int64) time.Time { return time.Unix(1_700_000_000+n*2, 0).UTC() }

func tVote(add bool, n int64) collector.SeenVote {
	return collector.SeenVote{Add: add, Block: n, Time: tAt(n)}
}

// tLookup finds every block; blocks before the validator-set change carry the before list, blocks
// after it carry the after list.
func tLookup(ch int64, before, after []string) blockSource {
	return func(n int64) (collector.BlockStamp, bool) {
		v := after
		if n < ch {
			v = before
		}
		return collector.BlockStamp{Number: n, Time: tAt(n), Validators: v}, true
	}
}

func byResult(recs []store.VoteRecord, result string) []store.VoteRecord {
	var out []store.VoteRecord
	for _, r := range recs {
		if r.Result == result {
			out = append(out, r)
		}
	}
	return out
}

// First round: two ballots on chain to add x -> open an in-progress row; threshold 3, 2 votes, four
// ballots.
func TestReconcileOpensLiveVote(t *testing.T) {
	view := collector.ChainView{EpochLength: 30000, Votes: map[collector.VoteRef]collector.SeenVote{
		{Target: tvx, Voter: tv1}: tVote(true, 10), {Target: tvx, Voter: tv2}: tVote(true, 12),
	}}
	got := reconcileVotes(nil, nil, tset, nil, view, tLookup(0, tset, tset))
	if len(got) != 1 || got[0].ID != 0 || got[0].Result != "open" || got[0].Votes != 2 || got[0].Needed != 3 || got[0].FirstBlock != 10 || len(got[0].Ballots) != 4 {
		t.Fatalf("%+v", got)
	}
}

// In progress to passed: the end block is the validator-set change block; ballots use the four from
// before the change, including the last ballot that made the majority (the recorder had not seen
// it); no duplicate backfill.
func TestReconcilePassed(t *testing.T) {
	b10, at10 := int64(10), tAt(10)
	open := []store.VoteRecord{{ID: 7, Address: tvx, Add: true, Result: "open", FirstBlock: 10, FirstAt: at10,
		Ballots: []store.Ballot{{Address: tv1, Block: &b10, At: &at10}, {Address: tv2}, {Address: tv3}, {Address: tv4}}}}
	after := append([]string{tvx}, tset...)
	view := collector.ChainView{EpochLength: 30000, Changed: map[string]int64{tvx: 21}, Votes: map[collector.VoteRef]collector.SeenVote{
		{Target: tvx, Voter: tv1}: tVote(true, 10), {Target: tvx, Voter: tv2}: tVote(true, 15), {Target: tvx, Voter: tv3}: tVote(true, 20),
	}}
	got := reconcileVotes(open, nil, after, nil, view, tLookup(21, tset, after))
	if len(got) != 1 {
		t.Fatalf("只該有結束那列(補記被同一輪擋掉):%+v", got)
	}
	r := got[0]
	if r.ID != 7 || r.Result != "passed" || *r.EndedBlock != 21 || !r.EndedAt.Equal(tAt(21)) || r.Votes != 3 || r.Needed != 3 || len(r.Ballots) != 4 || r.Ballots[2].Block == nil || *r.Ballots[2].Block != 20 {
		t.Fatalf("%+v", r)
	}
}

// Not passed by the epoch change: first ballot before the epoch start -> expired, ending at the
// epoch change block; an automatic re-vote in the same round after the epoch change -> a separate
// new row.
func TestReconcileExpiredAtEpochAndReopens(t *testing.T) {
	b10, at10 := int64(10), tAt(10)
	open := []store.VoteRecord{{ID: 3, Address: tvx, Add: true, Result: "open", FirstBlock: 10, FirstAt: at10, Votes: 1,
		Ballots: []store.Ballot{{Address: tv1, Block: &b10, At: &at10}}}}
	view := collector.ChainView{EpochLength: 30000, EpochStart: 30000, Votes: map[collector.VoteRef]collector.SeenVote{
		{Target: tvx, Voter: tv1}: tVote(true, 30001),
	}}
	got := reconcileVotes(open, nil, tset, nil, view, tLookup(0, tset, tset))
	ex, op := byResult(got, "expired"), byResult(got, "open")
	if len(ex) != 1 || ex[0].ID != 3 || *ex[0].EndedBlock != 30000 || ex[0].Votes != 1 {
		t.Fatalf("expired %+v", ex)
	}
	if len(op) != 1 || op[0].ID != 0 || op[0].FirstBlock != 30001 {
		t.Fatalf("新的一筆 %+v", op)
	}
}

// Count dropped to zero: voters switched to the opposite direction -> expired, ending at the latest
// opposite ballot; voter removed from the validator set -> ends at the block where it was removed.
func TestReconcileZeroVotes(t *testing.T) {
	b10, at10, b11, at11 := int64(10), tAt(10), int64(11), tAt(11)
	mk := func() []store.VoteRecord {
		return []store.VoteRecord{{ID: 5, Address: tvx, Add: true, Result: "open", FirstBlock: 10, FirstAt: at10, Votes: 2,
			Ballots: []store.Ballot{{Address: tv1, Block: &b10, At: &at10}, {Address: tv2, Block: &b11, At: &at11}}}}
	}
	view := collector.ChainView{EpochLength: 30000, Votes: map[collector.VoteRef]collector.SeenVote{
		{Target: tvx, Voter: tv1}: tVote(false, 40), {Target: tvx, Voter: tv2}: tVote(false, 44),
	}}
	got := reconcileVotes(mk(), nil, tset, nil, view, tLookup(0, tset, tset))
	if ex := byResult(got, "expired"); len(ex) != 1 || *ex[0].EndedBlock != 44 || ex[0].Votes != 0 {
		t.Fatalf("反方向 %+v", got)
	}
	// v1 and v2 removed (blocks 50 and 52), their ballots voided
	view = collector.ChainView{EpochLength: 30000, Changed: map[string]int64{tv1: 50, tv2: 52}, Votes: map[collector.VoteRef]collector.SeenVote{
		{Target: tvx, Voter: tv1}: tVote(true, 10), {Target: tvx, Voter: tv2}: tVote(true, 11),
	}}
	got = reconcileVotes(mk(), nil, []string{tv3, tv4}, nil, view, tLookup(0, tset, tset))
	if ex := byResult(got, "expired"); len(ex) != 1 || *ex[0].EndedBlock != 52 {
		t.Fatalf("投票者被移出 %+v", got)
	}
}

// Still being voted on: update the count, do not end.
func TestReconcileUpdatesOpen(t *testing.T) {
	b10, at10 := int64(10), tAt(10)
	open := []store.VoteRecord{{ID: 9, Address: tvx, Add: true, Result: "open", FirstBlock: 10, FirstAt: at10, Votes: 1,
		Ballots: []store.Ballot{{Address: tv1, Block: &b10, At: &at10}}}}
	view := collector.ChainView{EpochLength: 30000, Votes: map[collector.VoteRef]collector.SeenVote{
		{Target: tvx, Voter: tv1}: tVote(true, 10), {Target: tvx, Voter: tv2}: tVote(true, 15),
	}}
	got := reconcileVotes(open, nil, tset, nil, view, tLookup(0, tset, tset))
	if len(got) != 1 || got[0].ID != 9 || got[0].Result != "open" || got[0].Votes != 2 || got[0].FirstBlock != 10 {
		t.Fatalf("%+v", got)
	}
}

// The platform never saw it in progress (freshly installed, or passed while down): backfill a
// passed row from this epoch's validator-set change, with the direction inferred from the current
// list and the pre-change list taken from block ch-1; changes without ballots are not backfilled.
func TestReconcileBackfillsPassed(t *testing.T) {
	after := []string{tv1, tv2, tv3} // v4 removed
	view := collector.ChainView{EpochLength: 30000, Changed: map[string]int64{tv4: 31, tvx: 5}, Votes: map[collector.VoteRef]collector.SeenVote{
		{Target: tv4, Voter: tv1}: tVote(false, 25), {Target: tv4, Voter: tv2}: tVote(false, 28), {Target: tv4, Voter: tv3}: tVote(false, 30),
	}}
	got := reconcileVotes(nil, nil, after, nil, view, tLookup(31, tset, after))
	if len(got) != 1 {
		t.Fatalf("只補 v4(x 那次變更沒有票):%+v", got)
	}
	r := got[0]
	if r.Address != tv4 || r.Add || r.Result != "passed" || *r.EndedBlock != 31 || r.FirstBlock != 25 || r.Votes != 3 || r.Needed != 3 || len(r.Ballots) != 4 {
		t.Fatalf("%+v", r)
	}
}

// End block time not found: leave it this round and do not open a new row either.
func TestReconcileWaitsForBlock(t *testing.T) {
	b10, at10 := int64(10), tAt(10)
	open := []store.VoteRecord{{ID: 1, Address: tvx, Add: true, Result: "open", FirstBlock: 10, FirstAt: at10, Ballots: []store.Ballot{{Address: tv1, Block: &b10, At: &at10}}}}
	view := collector.ChainView{EpochLength: 30000, EpochStart: 30000, Votes: map[collector.VoteRef]collector.SeenVote{{Target: tvx, Voter: tv1}: tVote(true, 30001)}}
	none := func(int64) (collector.BlockStamp, bool) { return collector.BlockStamp{}, false }
	if got := reconcileVotes(open, nil, tset, nil, view, none); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// Response: only ended votes are returned; our own ballots are marked own; ballot names are overlaid.
func TestVoteHistory(t *testing.T) {
	end, at, b := int64(21), tAt(21), int64(10)
	recs := []store.VoteRecord{
		{Address: tvx, Add: true, Result: "passed", FirstBlock: 10, FirstAt: tAt(10), EndedBlock: &end, EndedAt: &at, Votes: 1, Needed: 3,
			Ballots: []store.Ballot{{Address: tv1, Block: &b, At: &at}, {Address: tv2}}},
		{Address: tvx, Add: false, Result: "open"},
	}
	got := voteHistory(recs, map[string]bool{"0x0000000000000000000000000000000000000001": true}, map[string]string{"0000000000000000000000000000000000000001": "本單位驗證者"})
	if len(got) != 1 || got[0].Ballots[0].Name == nil || *got[0].Ballots[0].Name != "本單位驗證者" || got[0].Ballots[0].Own == nil || got[0].Ballots[1].Own != nil {
		t.Fatalf("%+v", got)
	}
}

// Should end but the end block's time is not found: backfill must not write a row for it either
// (otherwise ending it next round collides with the unique key and the whole round fails to write).
// lookup fails the first time and succeeds the second, simulating a transient network failure.
func TestReconcileWaitingBlocksBackfill(t *testing.T) {
	b10, at10 := int64(10), tAt(10)
	open := []store.VoteRecord{{ID: 7, Address: tvx, Add: true, Result: "open", FirstBlock: 10, FirstAt: at10, Ballots: []store.Ballot{{Address: tv1, Block: &b10, At: &at10}}}}
	after := append([]string{tvx}, tset...)
	view := collector.ChainView{EpochLength: 30000, Changed: map[string]int64{tvx: 21}, Votes: map[collector.VoteRef]collector.SeenVote{
		{Target: tvx, Voter: tv1}: tVote(true, 10), {Target: tvx, Voter: tv2}: tVote(true, 15), {Target: tvx, Voter: tv3}: tVote(true, 20),
	}}
	calls := 0
	flaky := func(n int64) (collector.BlockStamp, bool) {
		calls++
		if calls == 1 {
			return collector.BlockStamp{}, false
		}
		return tLookup(21, tset, after)(n)
	}
	if got := reconcileVotes(open, nil, after, nil, view, flaky); len(got) != 0 {
		t.Fatalf("等下一輪,這輪不該寫:%+v", got)
	}
}

// An ended vote already in the DB (the row backfilled while down): not backfilled again, and no
// block lookup needed.
func TestReconcileSkipsRecorded(t *testing.T) {
	after := []string{tv1, tv2, tv3}
	view := collector.ChainView{EpochLength: 30000, Changed: map[string]int64{tv4: 31}, Votes: map[collector.VoteRef]collector.SeenVote{
		{Target: tv4, Voter: tv1}: tVote(false, 25), {Target: tv4, Voter: tv2}: tVote(false, 28), {Target: tv4, Voter: tv3}: tVote(false, 30),
	}}
	none := func(int64) (collector.BlockStamp, bool) {
		t.Fatal("不該查區塊")
		return collector.BlockStamp{}, false
	}
	recorded := map[endedKey]bool{{voteKey{tv4, false}, 31}: true}
	if got := reconcileVotes(nil, recorded, after, nil, view, none); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// Ballots gone but no reason found (validator set changed, cache has not read that block yet): do
// not close, check next round.
func TestReconcileZeroWithoutCauseWaits(t *testing.T) {
	b10, at10 := int64(10), tAt(10)
	open := []store.VoteRecord{{ID: 2, Address: tvx, Add: true, Result: "open", FirstBlock: 10, FirstAt: at10, Ballots: []store.Ballot{{Address: tv1, Block: &b10, At: &at10}}}}
	view := collector.ChainView{EpochLength: 30000, Votes: map[collector.VoteRef]collector.SeenVote{{Target: tvx, Voter: tv1}: tVote(true, 10)}}
	if got := reconcileVotes(open, nil, append([]string{tvx}, tset...), nil, view, tLookup(0, tset, tset)); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// Someone switched to the opposite direction: the in-progress row's ballots no longer count them as
// voted and the count drops; the first ballot does not move later.
func TestReconcileDropsReversedVoter(t *testing.T) {
	b10, at10, b12, at12 := int64(10), tAt(10), int64(12), tAt(12)
	open := []store.VoteRecord{{ID: 4, Address: tvx, Add: true, Result: "open", FirstBlock: 10, FirstAt: at10, Votes: 2,
		Ballots: []store.Ballot{{Address: tv1, Block: &b10, At: &at10}, {Address: tv2, Block: &b12, At: &at12}}}}
	view := collector.ChainView{EpochLength: 30000, Votes: map[collector.VoteRef]collector.SeenVote{
		{Target: tvx, Voter: tv1}: tVote(false, 30), {Target: tvx, Voter: tv2}: tVote(true, 12),
	}}
	got := reconcileVotes(open, nil, tset, nil, view, tLookup(0, tset, tset))
	if len(got) != 1 || got[0].Votes != 1 || got[0].FirstBlock != 10 || got[0].Ballots[0].Block != nil || got[0].Ballots[1].Block == nil {
		t.Fatalf("%+v", got)
	}
}

// Incomplete tally (backfill in progress, epoch unknown): the whole round does not touch the DB
// (the Server has no store and panics if it does).
func TestRecordVoteHistorySkipsIncompleteTally(t *testing.T) {
	if err := (&Server{}).recordVoteHistory(context.Background(), collector.ChainView{}); err != nil {
		t.Fatal(err)
	}
}

// A voter removed and re-added after voting: in progress only the others count; if all are like
// that -> count drops to zero, ending at the latest re-add block; not counted in ballots on pass.
func TestReconcileReaddedVoter(t *testing.T) {
	b10, at10, b11, at11 := int64(10), tAt(10), int64(11), tAt(11)
	mk := func() []store.VoteRecord {
		return []store.VoteRecord{{ID: 5, Address: tvx, Add: true, Result: "open", FirstBlock: 10, FirstAt: at10, Votes: 2,
			Ballots: []store.Ballot{{Address: tv1, Block: &b10, At: &at10}, {Address: tv2, Block: &b11, At: &at11}}}}
	}
	votes := map[collector.VoteRef]collector.SeenVote{{Target: tvx, Voter: tv1}: tVote(true, 10), {Target: tvx, Voter: tv2}: tVote(true, 11)}
	view := collector.ChainView{EpochLength: 30000, Changed: map[string]int64{tv2: 50}, Changes: map[string][]int64{tv2: {50}}, Votes: votes}
	got := reconcileVotes(mk(), nil, tset, nil, view, tLookup(0, tset, tset))
	if len(got) != 1 || got[0].Result != "open" || got[0].Votes != 1 || countVoted(got[0].Ballots) != 1 {
		t.Fatalf("只剩 v1 %+v", got)
	}
	view.Changed[tv1], view.Changes[tv1] = 52, []int64{52}
	got = reconcileVotes(mk(), nil, tset, nil, view, tLookup(0, tset, tset))
	if ex := byResult(got, "expired"); len(ex) != 1 || *ex[0].EndedBlock != 52 || ex[0].Votes != 0 {
		t.Fatalf("歸零 %+v", got)
	}
	// Passed (x added at 60): v2's ballot was voided at 50 and does not count; three ballots from
	// v1, v3, v4
	after := append([]string{tvx}, tset...)
	view = collector.ChainView{EpochLength: 30000, Changed: map[string]int64{tv2: 50, tvx: 60}, Changes: map[string][]int64{tv2: {50}, tvx: {60}}, Votes: map[collector.VoteRef]collector.SeenVote{
		{Target: tvx, Voter: tv1}: tVote(true, 10), {Target: tvx, Voter: tv2}: tVote(true, 11),
		{Target: tvx, Voter: tv3}: tVote(true, 55), {Target: tvx, Voter: tv4}: tVote(true, 58),
	}}
	got = reconcileVotes(nil, nil, after, nil, view, tLookup(60, tset, after))
	if p := byResult(got, "passed"); len(p) != 1 || p[0].Votes != 3 {
		t.Fatalf("通過 %+v", got)
	}
	// The DB already recorded v2's ballot for the same vote (passed after removal before the
	// recorder ran): it does not count either
	got = reconcileVotes(mk(), nil, after, nil, view, tLookup(60, tset, after))
	if p := byResult(got, "passed"); len(p) != 1 || p[0].Votes != 3 || p[0].Ballots[1].Block != nil {
		t.Fatalf("DB 的舊票 %+v", got)
	}
	// Re-voting after being re-added: the in-progress ballot is the new block, not the voided one
	// in the DB
	view = collector.ChainView{EpochLength: 30000, Changed: map[string]int64{tv2: 50}, Changes: map[string][]int64{tv2: {50}}, Votes: map[collector.VoteRef]collector.SeenVote{
		{Target: tvx, Voter: tv1}: tVote(true, 10), {Target: tvx, Voter: tv2}: tVote(true, 55),
	}}
	got = reconcileVotes(mk(), nil, tset, nil, view, tLookup(0, tset, tset))
	if len(got) != 1 || got[0].Votes != 2 || got[0].Ballots[1].Block == nil || *got[0].Ballots[1].Block != 55 {
		t.Fatalf("重投 %+v", got)
	}
}

// A voter removed before the pass and re-added after it (Changed only has the re-add block):
// Changes still remembers the removal block, so that ballot does not count.
func TestReconcilePassedVoterRemovedThenReaddedAfter(t *testing.T) {
	after := append([]string{tvx}, tset...)
	view := collector.ChainView{EpochLength: 30000, Changed: map[string]int64{tv2: 70, tvx: 60}, Changes: map[string][]int64{tv2: {50, 70}, tvx: {60}},
		Votes: map[collector.VoteRef]collector.SeenVote{
			{Target: tvx, Voter: tv1}: tVote(true, 10), {Target: tvx, Voter: tv2}: tVote(true, 11),
			{Target: tvx, Voter: tv3}: tVote(true, 55), {Target: tvx, Voter: tv4}: tVote(true, 58),
		}}
	got := reconcileVotes(nil, nil, after, nil, view, tLookup(60, tset, after))
	if p := byResult(got, "passed"); len(p) != 1 || p[0].Votes != 3 {
		t.Fatalf("v2 在 50 移出:不算 %+v", got)
	}
}

// Removal of v4 passes at 21 and v4 is voted back in at 30, both before the next recorder round:
// the open removal row ends as passed at 21, not as not-passed at the re-add.
func TestReconcileRemovalPassedThenReadded(t *testing.T) {
	b10, at10 := int64(10), tAt(10)
	open := []store.VoteRecord{{ID: 7, Address: tv4, Add: false, Result: "open", FirstBlock: 10, FirstAt: at10,
		Ballots: []store.Ballot{{Address: tv1, Block: &b10, At: &at10}, {Address: tv2}, {Address: tv3}, {Address: tv4}}}}
	view := collector.ChainView{EpochLength: 30000, Changed: map[string]int64{tv4: 30}, Changes: map[string][]int64{tv4: {21, 30}},
		Votes: map[collector.VoteRef]collector.SeenVote{
			{Target: tv4, Voter: tv1}: tVote(true, 25), {Target: tv4, Voter: tv2}: tVote(true, 28),
		}}
	without := []string{tv1, tv2, tv3}
	lookup := func(n int64) (collector.BlockStamp, bool) {
		v := tset
		if n >= 21 && n < 30 {
			v = without
		}
		return collector.BlockStamp{Number: n, Time: tAt(n), Validators: v}, true
	}
	got := reconcileVotes(open, nil, tset, nil, view, lookup)
	i := slices.IndexFunc(got, func(r store.VoteRecord) bool { return r.ID == 7 })
	if i < 0 || got[i].Result != "passed" || *got[i].EndedBlock != 21 {
		t.Fatalf("移除已在 21 通過:%+v", got)
	}
}

// tChain builds blocks from..to (BFT, validator list tset); votes[n] is the ballot carried by block
// n (proposer -> direction for target x).
func tChain(from, to int64, votes map[int64]collector.BlockVote, proposers map[int64]string) []collector.BlockStamp {
	var out []collector.BlockStamp
	for n := from; n <= to; n++ {
		b := collector.BlockStamp{Number: n, Time: tAt(n), BFT: true, Validators: tset, Proposer: proposers[n]}
		if v, ok := votes[n]; ok {
			b.Vote = &v
		}
		out = append(out, b)
	}
	return out
}

// Our v1 votes to add x and withdraws between two recorder rounds (opposite counter-vote): backfill
// an expired row with 0 votes, v1 marked withdrawn, ending at the counter-vote block.
func TestBackfillZeroedWithdrawn(t *testing.T) {
	view := collector.ChainView{EpochLength: 30000, Blocks: tChain(1, 20,
		map[int64]collector.BlockVote{10: {Target: tvx, Add: true}, 14: {Target: tvx, Add: false}},
		map[int64]string{10: tv1, 14: tv1})}
	got := backfillZeroed(view, tset, map[string]bool{tv1: true}, nil)
	if len(got) != 1 {
		t.Fatalf("應補一列:%+v", got)
	}
	r := got[0]
	if r.Address != tvx || !r.Add || r.Result != store.VoteExpired || r.Votes != 0 || r.Needed != 3 || r.FirstBlock != 10 || *r.EndedBlock != 14 {
		t.Fatalf("%+v", r)
	}
	for _, b := range r.Ballots {
		if b.Address == tv1 && (b.WithdrawnAt == nil || !b.WithdrawnAt.Equal(tAt(14)) || *b.Block != 10) {
			t.Fatalf("v1 應標撤回、留原本那張:%+v", b)
		}
	}
}

// The same vote already in the DB (open or ended), or just closed by the normal flow this round:
// not backfilled. The normal flow's end block matches the replay's.
func TestBackfillZeroedSkipsKnown(t *testing.T) {
	view := collector.ChainView{EpochLength: 30000,
		Votes:  map[collector.VoteRef]collector.SeenVote{{Target: tvx, Voter: tv1}: tVote(false, 14)},
		Blocks: tChain(1, 20, map[int64]collector.BlockVote{10: {Target: tvx, Add: true}, 14: {Target: tvx, Add: false}}, map[int64]string{10: tv1, 14: tv1})}
	b10, at10 := int64(10), tAt(10)
	open := []store.VoteRecord{{ID: 5, Address: tvx, Add: true, Result: "open", FirstBlock: 10, FirstAt: at10, Votes: 1,
		Ballots: []store.Ballot{{Address: tv1, Block: &b10, At: &at10}}}}
	if got := backfillZeroed(view, tset, map[string]bool{tv1: true}, open); len(got) != 0 {
		t.Fatalf("進行中的列擋住:%+v", got)
	}
	recs := reconcileVotes(open, nil, tset, map[string]bool{tv1: true}, view, tLookup(0, tset, tset))
	ex := byResult(recs, "expired")
	if len(ex) != 1 || *ex[0].EndedBlock != 14 {
		t.Fatalf("一般流程應在 14 結掉:%+v", recs)
	}
	if got := backfillZeroed(view, tset, map[string]bool{tv1: true}, recs); len(got) != 0 {
		t.Fatalf("這輪剛結的不重複補:%+v", got)
	}
}

// Passed by majority: the list changed, left to backfillPassed, not recorded here.
func TestBackfillZeroedSkipsPassed(t *testing.T) {
	view := collector.ChainView{EpochLength: 30000, Blocks: tChain(1, 20,
		map[int64]collector.BlockVote{10: {Target: tvx, Add: true}, 11: {Target: tvx, Add: true}, 12: {Target: tvx, Add: true}},
		map[int64]string{10: tv1, 11: tv2, 12: tv3})}
	if got := backfillZeroed(view, tset, nil, nil); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// Another organization's v2 voted and then switched direction: backfill a row with v2's ballot
// reset to not voted (cannot tell a withdrawal from a change of mind, so it is not marked
// withdrawn).
func TestBackfillZeroedOtherOrgSwitch(t *testing.T) {
	view := collector.ChainView{EpochLength: 30000, Blocks: tChain(1, 20,
		map[int64]collector.BlockVote{10: {Target: tvx, Add: true}, 13: {Target: tvx, Add: false}},
		map[int64]string{10: tv2, 13: tv2})}
	got := backfillZeroed(view, tset, map[string]bool{tv1: true}, nil)
	if len(got) != 1 || *got[0].EndedBlock != 13 {
		t.Fatalf("%+v", got)
	}
	for _, b := range got[0].Ballots {
		if b.Block != nil || b.WithdrawnAt != nil {
			t.Fatalf("別單位的票應改成沒投:%+v", b)
		}
	}
}

// Ballots cast before the cache range that still count form the starting tally: someone else voting
// and withdrawing inside the range does not bring the count to zero, so nothing is recorded.
func TestBackfillZeroedSeedsFromVotesBeforeCache(t *testing.T) {
	view := collector.ChainView{EpochLength: 30000,
		Votes:  map[collector.VoteRef]collector.SeenVote{{Target: tvx, Voter: tv1}: tVote(true, 5), {Target: tvx, Voter: tv2}: tVote(false, 12)},
		Blocks: tChain(8, 20, map[int64]collector.BlockVote{10: {Target: tvx, Add: true}, 12: {Target: tvx, Add: false}}, map[int64]string{10: tv2, 12: tv2})}
	if got := backfillZeroed(view, tset, map[string]bool{tv2: true}, nil); len(got) != 0 {
		t.Fatalf("v1 那票還在,不是歸零:%+v", got)
	}
}

// Opposite-direction ballots (direction already the current state) reach a majority: Besu voids the
// whole vote and the list does not change. Only one vote is recorded, ending at the void block.
func TestBackfillZeroedVoidedByOppositeQuorum(t *testing.T) {
	view := collector.ChainView{EpochLength: 30000, Blocks: tChain(1, 20,
		map[int64]collector.BlockVote{10: {Target: tvx, Add: true}, 11: {Target: tvx, Add: false}, 12: {Target: tvx, Add: false}, 13: {Target: tvx, Add: false}},
		map[int64]string{10: tv1, 11: tv2, 12: tv3, 13: tv4})}
	got := backfillZeroed(view, tset, nil, nil)
	if len(got) != 1 || *got[0].EndedBlock != 13 || got[0].FirstBlock != 10 {
		t.Fatalf("%+v", got)
	}
}

// Cache with a gap: only the contiguous run after the gap is replayed.
func TestBackfillZeroedOnlyContiguousTail(t *testing.T) {
	blocks := append(tChain(1, 9, map[int64]collector.BlockVote{5: {Target: tvx, Add: true}}, map[int64]string{5: tv1}),
		tChain(12, 20, map[int64]collector.BlockVote{13: {Target: tvx, Add: true}, 15: {Target: tvx, Add: false}}, map[int64]string{13: tv2, 15: tv2})...)
	view := collector.ChainView{EpochLength: 30000, Blocks: blocks}
	got := backfillZeroed(view, tset, nil, nil)
	if len(got) != 1 || got[0].FirstBlock != 13 || *got[0].EndedBlock != 15 {
		t.Fatalf("%+v", got)
	}
}

// The in-progress row just closed this round (block 10), and the same address and direction started
// and dropped to zero again between rounds (12-14): backfilled anyway, not blocked by the old copy
// (end treated as infinity).
func TestBackfillZeroedAfterOpenClosedThisPass(t *testing.T) {
	view := collector.ChainView{EpochLength: 30000,
		Votes: map[collector.VoteRef]collector.SeenVote{{Target: tvx, Voter: tv2}: tVote(false, 10), {Target: tvx, Voter: tv1}: tVote(false, 14)},
		Blocks: tChain(1, 20, map[int64]collector.BlockVote{5: {Target: tvx, Add: true}, 10: {Target: tvx, Add: false}, 12: {Target: tvx, Add: true}, 14: {Target: tvx, Add: false}},
			map[int64]string{5: tv2, 10: tv2, 12: tv1, 14: tv1})}
	b5, at5 := int64(5), tAt(5)
	open := []store.VoteRecord{{ID: 7, Address: tvx, Add: true, Result: "open", FirstBlock: 5, FirstAt: at5, Votes: 1,
		Ballots: []store.Ballot{{Address: tv2, Block: &b5, At: &at5}}}}
	own := map[string]bool{tv1: true}
	recs := reconcileVotes(open, nil, tset, own, view, tLookup(0, tset, tset))
	if ex := byResult(recs, "expired"); len(ex) != 1 || *ex[0].EndedBlock != 10 {
		t.Fatalf("一般流程應在 10 結掉:%+v", recs)
	}
	got := backfillZeroed(view, tset, own, knownVotes(open, nil, recs))
	if len(got) != 1 || got[0].FirstBlock != 12 || *got[0].EndedBlock != 14 {
		t.Fatalf("12–14 那場應補:%+v", got)
	}
}

// Ended rows in the DB: the same vote (same end block or one apart) is not backfilled; a vote
// closed by the normal flow has the same end block as the replay computes.
func TestBackfillZeroedSkipsEndedRow(t *testing.T) {
	view := collector.ChainView{EpochLength: 30000,
		Votes:  map[collector.VoteRef]collector.SeenVote{{Target: tvx, Voter: tv1}: tVote(false, 14)},
		Blocks: tChain(1, 20, map[int64]collector.BlockVote{10: {Target: tvx, Add: true}, 14: {Target: tvx, Add: false}}, map[int64]string{10: tv1, 14: tv1})}
	own := map[string]bool{tv1: true}
	replayed := backfillZeroed(view, tset, own, nil)
	b10, at10 := int64(10), tAt(10)
	open := []store.VoteRecord{{ID: 5, Address: tvx, Add: true, Result: "open", FirstBlock: 10, FirstAt: at10, Votes: 1,
		Ballots: []store.Ballot{{Address: tv1, Block: &b10, At: &at10}}}}
	regular := byResult(reconcileVotes(open, nil, tset, own, view, tLookup(0, tset, tset)), "expired")
	if len(replayed) != 1 || len(regular) != 1 || *replayed[0].EndedBlock != *regular[0].EndedBlock || replayed[0].FirstBlock != regular[0].FirstBlock {
		t.Fatalf("兩條路算的應一樣:重演 %+v,一般 %+v", replayed, regular)
	}
	for _, end := range []int64{14, 13} {
		e := end
		ended := []store.VoteRecord{{ID: 5, Address: tvx, Add: true, Result: store.VoteExpired, FirstBlock: 10, FirstAt: at10, EndedBlock: &e}}
		if got := backfillZeroed(view, tset, own, knownVotes(nil, ended, nil)); len(got) != 0 {
			t.Fatalf("已結束在 %d 的列擋住:%+v", end, got)
		}
	}
}

// withdrawals: our opposite-direction ballot cast in the validator-set change block counts as a
// withdrawal in this round; ones before the change do not.
func TestWithdrawalsFromChangeBlock(t *testing.T) {
	view := collector.ChainView{Changed: map[string]int64{tvx: 15}, Votes: map[collector.VoteRef]collector.SeenVote{
		{Target: tvx, Voter: tv1}: tVote(false, 15), {Target: tvx, Voter: tv2}: tVote(false, 14)}}
	got := withdrawals(view, voteKey{tvx, true}, map[string]bool{tv1: true, tv2: true})
	if len(got) != 1 || got[tv1].Block != 15 {
		t.Fatalf("%+v", got)
	}
}

// markWithdrawn: with no withdrawal data this round (cached ballots cleared after the epoch change)
// the stored withdrawal mark is kept; voters never stored are not marked.
func TestMarkWithdrawnKeepsStored(t *testing.T) {
	b10, at10, at14 := int64(10), tAt(10), tAt(14)
	stored := []store.Ballot{{Address: tv1, Block: &b10, At: &at10, WithdrawnAt: &at14}, {Address: tv2, Block: &b10, At: &at10}}
	ballots := []store.Ballot{{Address: tv1, Block: &b10, At: &at10}, {Address: tv2, Block: &b10, At: &at10}}
	got := markWithdrawn(ballots, stored, nil, nil)
	if got[0].WithdrawnAt == nil || !got[0].WithdrawnAt.Equal(at14) || got[1].WithdrawnAt != nil {
		t.Fatalf("%+v", got)
	}
}

// votesFor: interval [from, to): a ballot in the from block is included, one in the to block
// (validator-set change) is not; other directions are excluded.
func TestVotesForRange(t *testing.T) {
	view := collector.ChainView{Votes: map[collector.VoteRef]collector.SeenVote{
		{Target: tvx, Voter: tv1}: tVote(true, 10), {Target: tvx, Voter: tv2}: tVote(true, 15),
		{Target: tvx, Voter: tv3}: tVote(true, 12), {Target: tvx, Voter: tv4}: tVote(false, 11)}}
	got := votesFor(view, tvx, true, 10, 15)
	if len(got) != 2 || got[tv1].Block != 10 || got[tv3].Block != 12 {
		t.Fatalf("%+v", got)
	}
}

// mergeBallots: when the same voter is in both the DB and the cache, the ballot in the earlier
// block wins regardless of which side it is on.
func TestMergeBallotsKeepsEarlier(t *testing.T) {
	b10, at10, b9, at9 := int64(10), tAt(10), int64(9), tAt(9)
	stored := []store.Ballot{{Address: tv1, Block: &b10, At: &at10}, {Address: tv3, Block: &b9, At: &at9}}
	got := mergeBallots(stored, map[string]collector.SeenVote{tv1: tVote(true, 12), tv2: tVote(true, 11), tv3: tVote(true, 8)}, collector.ChainView{}, math.MaxInt64)
	if len(got) != 3 || got[tv1].Block != 10 || got[tv2].Block != 11 || got[tv3].Block != 8 {
		t.Fatalf("%+v", got)
	}
}

// validatorsBefore: if the previous block carries a list, use it (in original order); only without
// one (validator set in a contract) is it derived backwards from the current list.
func TestValidatorsBeforeBlockList(t *testing.T) {
	inSet := map[string]bool{}
	for _, a := range append([]string{tvx}, tset...) {
		inSet[enode.NormalizeAddress(a)] = true
	}
	withList := func(int64) (collector.BlockStamp, bool) {
		return collector.BlockStamp{Validators: []string{tv4, tv1}}, true
	}
	if got := validatorsBefore(20, tvx, true, inSet, withList); !slices.Equal(got, []string{tv4, tv1}) {
		t.Fatalf("有名單:%v", got)
	}
	noList := func(int64) (collector.BlockStamp, bool) { return collector.BlockStamp{}, true }
	if got := validatorsBefore(20, tvx, true, inSet, noList); !slices.Equal(got, tset) {
		t.Fatalf("沒名單應反推成加入前的四位:%v", got)
	}
}

// inBlock: matches addresses regardless of case; returns false for addresses not in the list.
func TestInBlock(t *testing.T) {
	b := collector.BlockStamp{Validators: []string{tv1, "0x00000000000000000000000000000000000000AA"}}
	if !inBlock(b, tvx) || inBlock(b, tv2) {
		t.Fatal("inBlock 判斷錯")
	}
}

// overlapsKnown: intervals whose endpoints touch (one side's end block equals the other's first
// ballot) overlap; different directions do not.
func TestOverlapsKnownTouching(t *testing.T) {
	e14 := int64(14)
	known := []store.VoteRecord{{Address: tvx, Add: true, FirstBlock: 10, EndedBlock: &e14}}
	at := func(first, end int64, add bool) store.VoteRecord {
		return store.VoteRecord{Address: tvx, Add: add, FirstBlock: first, EndedBlock: &end}
	}
	for _, c := range []struct {
		r    store.VoteRecord
		want bool
	}{
		{at(5, 10, true), true}, {at(14, 20, true), true}, {at(5, 9, true), false}, {at(15, 20, true), false}, {at(10, 14, false), false},
	} {
		if got := overlapsKnown(known, c.r); got != c.want {
			t.Errorf("[%d, %d] add=%v:got %v want %v", c.r.FirstBlock, *c.r.EndedBlock, c.r.Add, got, c.want)
		}
	}
}

// Backfill uses the list carried by the cached block itself: with six validators now and four in
// the block, the threshold and ballots follow the block's four.
func TestBackfillZeroedUsesBlockMembers(t *testing.T) {
	set := append(slices.Clone(tset), "0x0000000000000000000000000000000000000005", "0x0000000000000000000000000000000000000006")
	view := collector.ChainView{EpochLength: 30000, Blocks: tChain(1, 20,
		map[int64]collector.BlockVote{10: {Target: tvx, Add: true}, 14: {Target: tvx, Add: false}},
		map[int64]string{10: tv1, 14: tv1})}
	got := backfillZeroed(view, set, map[string]bool{tv1: true}, nil)
	if len(got) != 1 || got[0].Needed != 3 || len(got[0].Ballots) != 4 {
		t.Fatalf("%+v", got)
	}
}

// The starting tally only opens a vote with ballots opposite to the current state: a single
// opposite ballot before the cache (removing x, which is not in the set) is not a vote; voting and
// withdrawing inside the cache is.
func TestBackfillZeroedSeedIgnoresOppositeDirection(t *testing.T) {
	view := collector.ChainView{EpochLength: 30000,
		Votes:  map[collector.VoteRef]collector.SeenVote{{Target: tvx, Voter: tv2}: tVote(false, 6), {Target: tvx, Voter: tv1}: tVote(false, 12)},
		Blocks: tChain(8, 20, map[int64]collector.BlockVote{10: {Target: tvx, Add: true}, 12: {Target: tvx, Add: false}}, map[int64]string{10: tv1, 12: tv1})}
	got := backfillZeroed(view, tset, map[string]bool{tv1: true}, nil)
	if len(got) != 1 || got[0].FirstBlock != 10 || *got[0].EndedBlock != 12 {
		t.Fatalf("%+v", got)
	}
}

// Starting tally with several same-direction ballots: the first ballot is the earliest. In the
// cache v1 and v2 are no longer in the block list (Besu drops their ballots when removing them), so
// this vote ends at the first cached block. The fixture is deliberately inconsistent: Changes does
// not record this removal. With a consistent fixture the void or removal would exclude the old
// ballots first and the "take the earliest" line would never be reached.
func TestBackfillZeroedSeedFirstIsEarliest(t *testing.T) {
	blocks := tChain(8, 20, nil, nil)
	for i := range blocks {
		blocks[i].Validators = []string{tv3, tv4}
	}
	view := collector.ChainView{EpochLength: 30000, Blocks: blocks,
		Votes: map[collector.VoteRef]collector.SeenVote{{Target: tvx, Voter: tv1}: tVote(true, 3), {Target: tvx, Voter: tv2}: tVote(true, 5)}}
	got := backfillZeroed(view, tset, nil, nil)
	if len(got) != 1 || got[0].FirstBlock != 3 || !got[0].FirstAt.Equal(tAt(3)) || *got[0].EndedBlock != 8 {
		t.Fatalf("%+v", got)
	}
}

// Same-direction ballots exactly at the threshold mean passed: later blocks carry x in the list,
// and no extra row is backfilled.
func TestBackfillZeroedPassedAtExactQuorum(t *testing.T) {
	blocks := tChain(1, 20,
		map[int64]collector.BlockVote{10: {Target: tvx, Add: true}, 11: {Target: tvx, Add: true}, 12: {Target: tvx, Add: true}},
		map[int64]string{10: tv1, 11: tv2, 12: tv3})
	for i := range blocks {
		if blocks[i].Number > 12 { // block 12 reaches 3 votes; from the next block x is in the list
			blocks[i].Validators = append(slices.Clone(tset), tvx)
		}
	}
	view := collector.ChainView{EpochLength: 30000, Changed: map[string]int64{tvx: 13}, Blocks: blocks}
	if got := backfillZeroed(view, append(slices.Clone(tset), tvx), nil, nil); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// x was added at block 21 and v1 proposed removing it in that same block: the removal is a new
// vote that started at the change, not one the change ended, so it stays open.
func TestReconcileVoteStartingAtChangeBlockStaysOpen(t *testing.T) {
	b21, at21 := int64(21), tAt(21)
	after := append([]string{tvx}, tset...)
	open := []store.VoteRecord{{ID: 4, Address: tvx, Add: false, Result: "open", FirstBlock: 21, FirstAt: at21, Votes: 1,
		Ballots: []store.Ballot{{Address: tv1, Block: &b21, At: &at21}}}}
	view := collector.ChainView{EpochLength: 30000, Changed: map[string]int64{tvx: 21}, Votes: map[collector.VoteRef]collector.SeenVote{
		{Target: tvx, Voter: tv1}: tVote(false, 21),
	}}
	got := reconcileVotes(open, nil, after, nil, view, tLookup(21, tset, after))
	if len(got) != 1 || got[0].ID != 4 || got[0].Result != store.VoteOpen || got[0].EndedBlock != nil {
		t.Fatalf("%+v", got)
	}
}

// A vote whose first ballot is in the epoch start block belongs to the new epoch: the epoch change
// does not end it.
func TestReconcileVoteAtEpochStartStaysOpen(t *testing.T) {
	b, at := int64(30000), tAt(30000)
	open := []store.VoteRecord{{ID: 6, Address: tvx, Add: true, Result: "open", FirstBlock: 30000, FirstAt: at, Votes: 1,
		Ballots: []store.Ballot{{Address: tv1, Block: &b, At: &at}}}}
	view := collector.ChainView{EpochLength: 30000, EpochStart: 30000, Votes: map[collector.VoteRef]collector.SeenVote{
		{Target: tvx, Voter: tv1}: tVote(true, 30000),
	}}
	got := reconcileVotes(open, nil, tset, nil, view, tLookup(0, tset, tset))
	if len(got) != 1 || got[0].ID != 6 || got[0].Result != store.VoteOpen || got[0].EndedBlock != nil {
		t.Fatalf("%+v", got)
	}
}

// A ballot cast in the epoch start block, before the cache range, is part of the starting tally: v2
// voting and withdrawing inside the range leaves v1's ballot, so the count never reaches zero.
func TestBackfillZeroedSeedsFromEpochStartBlock(t *testing.T) {
	view := collector.ChainView{EpochLength: 30000, EpochStart: 30000,
		Votes: map[collector.VoteRef]collector.SeenVote{{Target: tvx, Voter: tv1}: tVote(true, 30000), {Target: tvx, Voter: tv2}: tVote(false, 30007)},
		Blocks: tChain(30005, 30010, map[int64]collector.BlockVote{30006: {Target: tvx, Add: true}, 30007: {Target: tvx, Add: false}},
			map[int64]string{30006: tv2, 30007: tv2})}
	if got := backfillZeroed(view, tset, map[string]bool{tv2: true}, nil); len(got) != 0 {
		t.Fatalf("v1's ballot from the epoch start still counts: %+v", got)
	}
}
