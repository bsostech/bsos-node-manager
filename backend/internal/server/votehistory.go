package server

import (
	"context"
	"maps"
	"math"
	"slices"
	"sort"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/enode"
	"besu-manager/backend/internal/store"
)

// Vote history: a background job that records the outcome of every validator vote from the votes on
// chain. There are only two outcomes: passed (majority reached, the validator set changed) and
// expired (not passed by the epoch change, or the vote count dropped to zero). Only votes written
// into blocks count, not the nodes' pending-vote lists.

// voteKey identifies one vote: (address, direction); the address is 0x + lowercase.
type voteKey struct {
	addr string
	add  bool
}

// liveVote is one vote in progress on chain: the latest ballot of each voter (still in the
// validator set).
type liveVote map[string]collector.SeenVote

// liveVotes is a pure function. It returns the (address, direction) pairs being voted on chain, by
// the same rules as groupVotes: directions already reflected in the validator set do not count,
// ballots before Changed do not count, and ballots whose voter is not in the validator set, or was
// removed and re-added after voting, do not count. inSet is keyed by NormalizeAddress'd addresses.
func liveVotes(inSet map[string]bool, view collector.ChainView) map[voteKey]liveVote {
	out := map[voteKey]liveVote{}
	cutoff := view.VoteCutoffs()
	for ref, sv := range view.Votes {
		target := enode.NormalizeAddress(ref.Target)
		if !inSet[enode.NormalizeAddress(ref.Voter)] || !view.Counts(ref, sv, cutoff) || inSet[target] == sv.Add {
			continue
		}
		k := voteKey{"0x" + target, sv.Add}
		if out[k] == nil {
			out[k] = liveVote{}
		}
		out[k][ref.Voter] = sv
	}
	return out
}

// blockSource looks up a block header by number (cache first, then the node); ok=false means not
// found.
type blockSource func(n int64) (collector.BlockStamp, bool)

// reconcileVotes is a pure function computing one round of changes: it compares the open rows in
// the DB with the chain (view and the current validator set `set`), rewrites the outcome of votes
// that ended, updates the ballots of those still open and opens a row for new ones; then it
// backfills passes from this epoch's validator-set changes that the platform never saw. Votes whose
// end time cannot be found stay open and are retried next round. recorded holds the (address,
// direction, end block) of ended votes already in the DB; backfill skips them so blocks are not
// looked up every round. The caller guarantees view.VotesComplete().
func reconcileVotes(open []store.VoteRecord, recorded map[endedKey]bool, set []string, own map[string]bool, view collector.ChainView, lookup blockSource) []store.VoteRecord {
	inSet := map[string]bool{}
	for _, a := range set {
		inSet[enode.NormalizeAddress(a)] = true
	}
	live := liveVotes(inSet, view)
	withdrawn := func(k voteKey) map[string]collector.SeenVote { return withdrawals(view, k, own) }
	var out []store.VoteRecord
	stillOpen := map[voteKey]bool{}
	closedAt := map[voteKey]int64{} // ended as passed this round (skipped by backfill)
	for _, r := range open {
		k := voteKey{r.Address, r.Add}
		ended, ok := endVote(r, live[k], inSet, withdrawn(k), view, lookup)
		switch {
		case ok:
			out = append(out, ended)
			if ended.Result == store.VotePassed {
				closedAt[k] = *ended.EndedBlock
			}
		case ended.ID != 0: // should end but the end block cannot be found: leave it this round
			stillOpen[k] = true
		default: // still being voted on
			stillOpen[k] = true
			out = append(out, liveRecord(r, live[k], set, withdrawn(k), view))
		}
	}
	for k, votes := range live {
		if !stillOpen[k] {
			out = append(out, liveRecord(store.VoteRecord{Address: k.addr, Add: k.add, Result: store.VoteOpen}, votes, set, withdrawn(k), view))
		}
	}
	return append(out, backfillPassed(set, inSet, view, lookup, func(k voteKey, ch int64) bool {
		// Do not backfill votes that ended this round, ones still open (including those that should
		// end but whose time is unknown and wait for the next round), or ones the DB already has:
		// they would collide with that row's unique key.
		return closedAt[k] == ch || stillOpen[k] || recorded[endedKey{k, ch}]
	})...)
}

// firstChangeAfter returns the first block after `after` where addr changed the validator set, or
// the latest change when none is later (callers then see ch <= after).
func firstChangeAfter(view collector.ChainView, addr string, after int64) int64 {
	for _, n := range view.Changes[addr] {
		if n > after {
			return n
		}
	}
	return view.Changed[addr]
}

// endedKey identifies an ended vote: (address, direction, end block), same as the DB unique key.
type endedKey struct {
	voteKey
	block int64
}

// endVote decides whether an open row has ended. ok means ended and returns the row with its
// outcome filled in; not ended returns the zero value; should have ended but the end block's time
// cannot be found returns ok=false with a non-zero ID (the caller leaves it this round).
func endVote(r store.VoteRecord, now liveVote, inSet map[string]bool, wd map[string]collector.SeenVote, view collector.ChainView, lookup blockSource) (store.VoteRecord, bool) {
	waiting := store.VoteRecord{ID: r.ID}
	// Passed: after the first ballot this address changed the validator set. It counts as passed
	// only in the right direction; a change in the opposite direction (caused by another vote)
	// voids this vote's ballots and counts as not passed. Judge by the first change after the first
	// ballot, not the latest: a removal that passed and was voted back in before this round would
	// otherwise be judged by the re-add and recorded as not passed.
	if ch := firstChangeAfter(view, r.Address, r.FirstBlock); ch > r.FirstBlock {
		b, ok := lookup(ch)
		if !ok {
			return waiting, false
		}
		before := validatorsBefore(ch, r.Address, r.Add, inSet, lookup)
		e := r
		// ponytail: counts from this vote's first ballot; if the same vote is voided first
		// (counter-votes reach a majority) and then passes right after, with no recorder round in
		// between, ballots from before the void are counted too
		counted := votesFor(view, r.Address, r.Add, r.FirstBlock, ch)
		// An opposite-direction ballot cast after the validator-set change is a different vote
		// (e.g. proposing to remove a node that was just added), not a withdrawal of this one
		maps.DeleteFunc(wd, func(_ string, sv collector.SeenVote) bool { return sv.Block >= ch })
		e.Ballots = markWithdrawn(ballotsFor(before, mergeBallots(r.Ballots, counted, view, ch)), r.Ballots, wd, counted)
		e.Votes, e.Needed = countVoted(e.Ballots), voteNeeded(len(before))
		e.Result = store.VoteExpired
		if inBlock(b, r.Address) == r.Add {
			e.Result = store.VotePassed
		}
		e.EndedBlock, e.EndedAt = &ch, &b.Time
		return e, true
	}
	// Epoch change: the first ballot is before the start of this epoch, so the on-chain tally has
	// been cleared
	if r.FirstBlock < view.EpochStart {
		b, ok := lookup(view.EpochStart)
		if !ok {
			return waiting, false
		}
		e := r
		e.Result, e.EndedBlock, e.EndedAt = store.VoteExpired, &view.EpochStart, &b.Time
		return e, true
	}
	if len(now) > 0 {
		return store.VoteRecord{}, false
	}
	// Count dropped to zero: voters switched to the opposite direction (take the latest such
	// ballot), or voters were removed from the validator set (including removed and re-added) and
	// their ballots voided (take the block of their last change)
	var end int64
	for _, bl := range r.Ballots {
		if bl.Block == nil {
			continue
		}
		if sv, ok := view.Votes[collector.VoteRef{Target: r.Address, Voter: bl.Address}]; ok && sv.Add != r.Add {
			end = max(end, sv.Block)
		}
		if !inSet[enode.NormalizeAddress(bl.Address)] || view.VoteDiscarded(bl.Address, *bl.Block, math.MaxInt64) {
			end = max(end, view.Changed[bl.Address])
		}
	}
	if end == 0 { // unknown why it disappeared (e.g. the set changed but the cache has not read that block yet): do not close it, check next round
		return waiting, false
	}
	b, ok := lookup(end)
	if !ok {
		return waiting, false
	}
	e := r
	e.Ballots = clearUncounted(markWithdrawn(slices.Clone(r.Ballots), r.Ballots, wd, nil))
	e.Result, e.Votes, e.EndedBlock, e.EndedAt = store.VoteExpired, 0, &end, &b.Time
	return e, true
}

// backfillPassed (ponytail: only this epoch, only the last change per address, ballots only from
// the cache; anything more complete needs scanning back through blocks): for validator-set changes
// in this epoch that the platform never recorded as open (platform freshly installed, or the pass
// happened while it was down), it backfills a passed row from the cached ballots. The direction is
// inferred from whether the address is in the validator set now; Changed keeps only the last change
// per address, so an add followed by a remove in the same epoch only recovers the last one. Changes
// with no ballots are not backfilled. Rows already recorded are rejected by the DB unique key
// (address, add, ended_block).
func backfillPassed(set []string, inSet map[string]bool, view collector.ChainView, lookup blockSource, skip func(voteKey, int64) bool) []store.VoteRecord {
	var out []store.VoteRecord
	for addr, ch := range view.Changed {
		add := inSet[enode.NormalizeAddress(addr)]
		k := voteKey{addr, add}
		if ch < max(view.EpochStart, 1) || skip(k, ch) {
			continue
		}
		votes := votesFor(view, addr, add, view.EpochStart, ch)
		if len(votes) == 0 {
			continue
		}
		b, ok := lookup(ch)
		if !ok {
			continue
		}
		before := validatorsBefore(ch, addr, add, inSet, lookup)
		r := store.VoteRecord{Address: addr, Add: add, Result: store.VotePassed, EndedBlock: &ch, EndedAt: &b.Time}
		r.Ballots = ballotsFor(before, votes)
		r.FirstBlock, r.FirstAt = firstVote(votes)
		r.Votes, r.Needed = countVoted(r.Ballots), voteNeeded(len(before))
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}

// liveRecord updates an open row from the current ballots on chain. ballots keeps only voters that
// still count (those who switched to the opposite direction do not); for the same voter the earlier
// ballot already in the DB wins (the cache keeps only its latest one); the first ballot is when
// this vote started and can only move earlier, never later.
func liveRecord(r store.VoteRecord, votes liveVote, set []string, wd map[string]collector.SeenVote, view collector.ChainView) store.VoteRecord {
	merged := mergeBallots(r.Ballots, votes, view, math.MaxInt64)
	maps.DeleteFunc(merged, func(voter string, _ collector.SeenVote) bool {
		_, counted := votes[voter]
		_, withdrawn := wd[voter] // our own withdrawn voters keep their original ballot, marked withdrawn
		return !counted && !withdrawn
	})
	first, firstAt := firstVote(merged)
	if r.FirstBlock == 0 || first < r.FirstBlock {
		r.FirstBlock, r.FirstAt = first, firstAt
	}
	r.Ballots = markWithdrawn(ballotsFor(set, merged), r.Ballots, wd, votes)
	r.Votes, r.Needed = len(votes), voteNeeded(len(set))
	return r
}

// withdrawals is a pure function: voters of our own organization (own, 0x + lowercase) that
// withdrew from this vote -> when the counter-vote was written into a block: their latest ballot on
// chain for this address (after the validator-set change) is in the opposite direction. It uses the
// validator-set change points, not the void points (VoteCutoffs): when counter-votes reach a
// majority, the counter-vote that gets voided sits before the void point itself, and it is still a
// withdrawal. Other organizations switching to the opposite direction do not count: the platform
// cannot tell a withdrawal from a change of mind.
func withdrawals(view collector.ChainView, k voteKey, own map[string]bool) map[string]collector.SeenVote {
	out := map[string]collector.SeenVote{}
	for ref, sv := range view.Votes {
		if ref.Target == k.addr && own[ref.Voter] && sv.Add != k.add && sv.Block >= view.Changed[ref.Target] && !view.VoteDiscarded(ref.Voter, sv.Block, math.MaxInt64) {
			out[ref.Voter] = sv
		}
	}
	return out
}

// clearUncounted: when a vote closes because its count dropped to zero, everyone's ballot except
// our own withdrawn ones (already marked WithdrawnAt, keeping the original ballot) is reset to not
// voted, so it matches the zero count (this covers other organizations switching direction and
// votes Besu voided entirely because counter-votes reached a majority).
func clearUncounted(ballots []store.Ballot) []store.Ballot {
	for i, b := range ballots {
		if b.WithdrawnAt == nil {
			ballots[i].Block, ballots[i].At = nil, nil
		}
	}
	return ballots
}

// markWithdrawn sets WithdrawnAt on withdrawn voters. This round's wd takes priority, otherwise the
// previously stored value is kept (after an epoch change the cached ballots are cleared but the
// stored ones remain). Voters in counted (counting again this round: re-voted after withdrawing)
// are not marked, and any previously stored value is cleared.
func markWithdrawn(ballots, stored []store.Ballot, wd, counted map[string]collector.SeenVote) []store.Ballot {
	prev := map[string]*time.Time{}
	for _, b := range stored {
		if b.WithdrawnAt != nil {
			prev[b.Address] = b.WithdrawnAt
		}
	}
	for i, b := range ballots {
		if _, again := counted[b.Address]; again || b.Block == nil {
			ballots[i].WithdrawnAt = nil
			continue
		}
		if sv, ok := wd[b.Address]; ok {
			at := sv.Time
			ballots[i].WithdrawnAt = &at
		} else if p := prev[b.Address]; p != nil {
			ballots[i].WithdrawnAt = p
		}
	}
	return ballots
}

// votesFor returns the cached ballots for addr in direction add whose block is in [from, to) (voter
// -> that ballot).
func votesFor(view collector.ChainView, addr string, add bool, from, to int64) map[string]collector.SeenVote {
	out := map[string]collector.SeenVote{}
	for ref, sv := range view.Votes {
		// A voter removed after voting but before the pass had its ballot voided before the pass;
		// removal after the pass does not matter
		if ref.Target == addr && sv.Add == add && sv.Block >= from && sv.Block < to && !view.VoteDiscarded(ref.Voter, sv.Block, to) {
			out[ref.Voter] = sv
		}
	}
	return out
}

// mergeBallots merges the ballots stored in the DB with the cached ones; for the same voter the one
// in the earlier block wins (the first ballot it cast in this vote). Stored ballots voided before
// `to` (the block where this vote ended; math.MaxInt64 while still open) because the voter was
// removed do not count (same as votesFor).
func mergeBallots(stored []store.Ballot, votes map[string]collector.SeenVote, view collector.ChainView, to int64) map[string]collector.SeenVote {
	out := map[string]collector.SeenVote{}
	for _, b := range stored {
		if b.Block != nil && b.At != nil && !view.VoteDiscarded(b.Address, *b.Block, to) {
			out[b.Address] = collector.SeenVote{Block: *b.Block, Time: *b.At}
		}
	}
	for voter, sv := range votes {
		if old, ok := out[voter]; !ok || sv.Block < old.Block {
			out[voter] = sv
		}
	}
	return out
}

// ballotsFor returns the ballot of each validator eligible to vote (voters, sorted by address);
// Block / At are nil for those who did not vote.
func ballotsFor(voters []string, votes map[string]collector.SeenVote) []store.Ballot {
	out := make([]store.Ballot, 0, len(voters))
	for _, v := range voters {
		addr := "0x" + enode.NormalizeAddress(v)
		b := store.Ballot{Address: addr}
		if sv, ok := votes[addr]; ok {
			blk, at := sv.Block, sv.Time
			b.Block, b.At = &blk, &at
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}

func countVoted(ballots []store.Ballot) int {
	n := 0
	for _, b := range ballots {
		if b.Block != nil && b.WithdrawnAt == nil {
			n++
		}
	}
	return n
}

func firstVote(votes map[string]collector.SeenVote) (int64, time.Time) {
	var blk int64
	var at time.Time
	for _, sv := range votes {
		if blk == 0 || sv.Block < blk {
			blk, at = sv.Block, sv.Time
		}
	}
	return blk, at
}

// validatorsBefore returns the validator set before the change at block ch. It first reads the list
// from block ch-1's extraData; if that is unavailable (not in the cache, or the validator set lives
// in a contract) it derives it backwards from the current list and this address.
func validatorsBefore(ch int64, addr string, add bool, inSet map[string]bool, lookup blockSource) []string {
	if b, ok := lookup(ch - 1); ok && len(b.Validators) > 0 {
		return b.Validators
	}
	norm := enode.NormalizeAddress(addr)
	var out []string
	for a := range inSet {
		if a != norm {
			out = append(out, "0x"+a)
		}
	}
	if !add { // removal passed: it was in the set before
		out = append(out, "0x"+norm)
	}
	sort.Strings(out)
	return out
}

func inBlock(b collector.BlockStamp, addr string) bool {
	return slices.ContainsFunc(b.Validators, func(v string) bool { return enode.NormalizeAddress(v) == enode.NormalizeAddress(addr) })
}

// recordVoteHistory runs one recorder round (called by RunVoteCleanup). If the tally is incomplete
// (backfill in progress, epoch unknown) the whole round is skipped.
func (s *Server) recordVoteHistory(ctx context.Context, view collector.ChainView) error {
	if !view.VotesComplete() {
		return nil
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return err
	}
	set := view.LatestValidators()
	if set == nil { // validator set lives in a contract (no list in extraData): fall back to what the node reports; the cache and the node snapshot may differ by a block or two
		set, _ = s.chainConsensus(nodes)
	}
	if set == nil {
		return nil
	}
	open, err := s.store.ListOpenVoteRecords(ctx)
	if err != nil {
		return err
	}
	ended, err := s.store.ListEndedVoteRecords(ctx)
	if err != nil {
		return err
	}
	recorded := map[endedKey]bool{}
	for _, r := range ended {
		recorded[endedKey{voteKey{r.Address, r.Add}, *r.EndedBlock}] = true
	}
	lookup := func(n int64) (collector.BlockStamp, bool) {
		if b, ok := blockAt(view.Blocks, n); ok {
			return b, true
		}
		b, err := s.col.FetchBlock(ctx, n)
		return b, err == nil
	}
	own := ownValidators(nodes)
	recs := reconcileVotes(open, recorded, set, own, view, lookup)
	known := knownVotes(open, ended, recs)
	return s.store.SaveVoteRecords(ctx, append(recs, backfillZeroed(view, set, own, known)...))
}

// blockAt is a pure function returning block n from the cache (oldest to newest, increasing block
// numbers).
func blockAt(blocks []collector.BlockStamp, n int64) (collector.BlockStamp, bool) {
	i := sort.Search(len(blocks), func(i int) bool { return blocks[i].Number >= n })
	if i < len(blocks) && blocks[i].Number == n {
		return blocks[i], true
	}
	return collector.BlockStamp{}, false
}

// ── GET /validator-votes/history ────────────────────────────

func (s *Server) ListValidatorVoteHistory(ctx context.Context, _ api.ListValidatorVoteHistoryRequestObject) (api.ListValidatorVoteHistoryResponseObject, error) {
	recs, err := s.store.ListEndedVoteRecords(ctx)
	if err != nil {
		return nil, err
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	names, err := s.peerNames(ctx, nodes)
	if err != nil {
		return nil, err
	}
	fillOwnIDs(names, nodes)
	return api.ListValidatorVoteHistory200JSONResponse(voteHistory(recs, ownValidators(nodes), names)), nil
}

// fillOwnIDs is a pure function. For our own nodes without a name it fills in the node id as the
// name, matching what the in-progress votes view shows (the frontend falls back to the id).
// peerNames deliberately gives no name to our own unnamed nodes (other pages rely on that to fall
// back to the id), so the fill happens only here for vote history and peerNames is left alone.
func fillOwnIDs(names map[string]string, nodes []store.Node) {
	for addr, id := range ownAddresses(nodes) {
		if _, ok := names[addr]; !ok {
			names[addr] = id
		}
	}
}

// voteHistory is a pure function. own holds our own validator addresses (ownValidators); names maps
// address (NormalizeAddress'd) -> name.
func voteHistory(recs []store.VoteRecord, own map[string]bool, names map[string]string) []api.ValidatorVoteRecord {
	out := make([]api.ValidatorVoteRecord, 0, len(recs))
	for _, r := range recs {
		if r.EndedBlock == nil || r.EndedAt == nil {
			continue
		}
		rec := api.ValidatorVoteRecord{
			Address: r.Address, Add: r.Add, Result: api.ValidatorVoteOutcome(r.Result),
			FirstVoteAt: r.FirstAt, EndedAt: *r.EndedAt, EndedBlock: *r.EndedBlock,
			Votes: r.Votes, Needed: r.Needed, Ballots: make([]api.ValidatorBallot, 0, len(r.Ballots)),
		}
		for _, b := range r.Ballots {
			key := enode.NormalizeAddress(b.Address)
			ab := api.ValidatorBallot{Address: b.Address, Block: b.Block, VotedAt: b.At, WithdrawnAt: b.WithdrawnAt}
			if own["0x"+key] {
				t := true
				ab.Own = &t
			}
			if name, ok := names[key]; ok {
				ab.Name = &name
			}
			rec.Ballots = append(rec.Ballots, ab)
		}
		out = append(out, rec)
	}
	return out
}

// ownValidators returns the validator addresses of our own nodes (0x + lowercase), shared by vote
// history and withdrawal detection.
func ownValidators(nodes []store.Node) map[string]bool {
	out := map[string]bool{}
	for _, n := range nodes {
		if n.ValidatorAddress != nil {
			out["0x"+enode.NormalizeAddress(*n.ValidatorAddress)] = true
		}
	}
	return out
}

// backfillZeroed covers votes that started between two recorder rounds and then ended with the
// count dropping to zero. The recorder never saw them in progress, so the flow above cannot record
// them; view.Votes keeps only each voter's latest ballot, and a withdrawing counter-vote overwrites
// the original, so it cannot be seen afterwards either. Instead it replays Besu's tally (VoteTally)
// over the cached blocks in order to backfill them:
//   - Only the last contiguous run of cached blocks that is within this epoch is replayed (anything
//     before a fetch-failure gap is ignored); ballots cast before the cache range that still count
//     are used as the starting tally, so "someone is still voting" is not mistaken for zero.
//   - Only each voter's latest ballot counts; a voter not in that block's validator list has its
//     ballots dropped. When either direction reaches half of that block's list + 1, all ballots for
//     the address are voided: if the direction is opposite to the current state (the list really
//     changed) it passed, which is left to backfillPassed and the normal flow and not recorded
//     here; if the direction matches the current state (adding one already in, removing one not in)
//     the list did not change, so the vote ends here and is recorded as expired.
//   - One vote = (address, direction opposite to the current state). Counted ballots going from 0
//     to non-zero = start, back to 0 = end; the end block is the block that brought it to zero.
//   - known holds the DB rows (open and ended) plus the ones reconcileVotes just produced this
//     round; anything whose interval overlaps one with the same (address, direction) is not
//     backfilled (the normal flow already recorded it).
//
// ballots use the list at the end block; our own voters that switched to the opposite direction are
// marked withdrawn (time = the counter-vote's block), and everyone else's ballots are reset to not
// voted per the zero-count closing rule. It only reads the cache and never asks the node for
// blocks.
func backfillZeroed(view collector.ChainView, set []string, own map[string]bool, known []store.VoteRecord) []store.VoteRecord {
	tail := contiguousTail(view.Blocks, view.EpochStart)
	if len(tail) == 0 {
		return nil
	}
	norm := func(a string) string { return "0x" + enode.NormalizeAddress(a) }
	setNow := map[string]bool{}
	for _, a := range set {
		setNow[norm(a)] = true
	}
	membersAt := func(b collector.BlockStamp) map[string]bool {
		if len(b.Validators) == 0 { // validator set lives in a contract and extraData carries no list: fall back to the current one
			return setNow
		}
		m := make(map[string]bool, len(b.Validators))
		for _, a := range b.Validators {
			m[norm(a)] = true
		}
		return m
	}
	// tally[address][voter] = latest ballot
	tally := map[string]map[string]collector.SeenVote{}
	cutoffs := view.VoteCutoffs()
	for ref, sv := range view.Votes {
		if sv.Block < tail[0].Number && sv.Block >= view.EpochStart && view.Counts(ref, sv, cutoffs) {
			t := norm(ref.Target)
			if tally[t] == nil {
				tally[t] = map[string]collector.SeenVote{}
			}
			tally[t][norm(ref.Voter)] = sv
		}
	}
	type session struct {
		first     collector.SeenVote
		ballots   map[string]collector.SeenVote // voters in this direction in this vote, each one's first ballot
		withdrawn map[string]time.Time          // our own voters that switched to the opposite direction
	}
	sessions := map[string]*session{} // address -> the open vote (direction = opposite to the current state)
	count := func(t string, add bool) (n int) {
		for _, sv := range tally[t] {
			if sv.Add == add {
				n++
			}
		}
		return n
	}
	var out []store.VoteRecord
	end := func(t string, b collector.BlockStamp, members map[string]bool) {
		ss := sessions[t]
		delete(sessions, t)
		add := !members[t]
		ballots := ballotsFor(slices.Sorted(maps.Keys(members)), ss.ballots)
		for i, bl := range ballots {
			if at, ok := ss.withdrawn[bl.Address]; ok && bl.Block != nil {
				ballots[i].WithdrawnAt = &at
			}
		}
		blk, at := b.Number, b.Time
		r := store.VoteRecord{Address: t, Add: add, Result: store.VoteExpired, FirstBlock: ss.first.Block, FirstAt: ss.first.Time,
			EndedBlock: &blk, EndedAt: &at, Needed: voteNeeded(len(members)), Ballots: clearUncounted(ballots)}
		if !overlapsKnown(known, r) {
			out = append(out, r)
		}
	}
	// Votes already in progress in the starting tally (started before the cache range) count as a
	// vote too, and are checked against known when they end
	start := tail[0]
	for t, votes := range tally {
		add := !membersAt(start)[t]
		for voter, sv := range votes {
			if sv.Add != add {
				continue
			}
			ss := sessions[t]
			if ss == nil {
				ss = &session{first: sv, ballots: map[string]collector.SeenVote{}, withdrawn: map[string]time.Time{}}
				sessions[t] = ss
			}
			ss.ballots[voter] = sv
			if sv.Block < ss.first.Block {
				ss.first = sv
			}
		}
	}
	for _, b := range tail {
		members := membersAt(b)
		// The voter is no longer in the list: Besu drops its ballots when it is removed
		for t, votes := range tally {
			maps.DeleteFunc(votes, func(voter string, _ collector.SeenVote) bool { return !members[voter] })
			if sessions[t] != nil && count(t, !members[t]) == 0 {
				end(t, b, members)
			}
		}
		if b.Vote == nil || b.Proposer == "" || !members[norm(b.Proposer)] {
			continue
		}
		t, voter := norm(b.Vote.Target), norm(b.Proposer)
		sv := collector.SeenVote{Add: b.Vote.Add, Block: b.Number, Time: b.Time}
		if tally[t] == nil {
			tally[t] = map[string]collector.SeenVote{}
		}
		tally[t][voter] = sv
		add := !members[t] // the meaningful direction for this address
		if ss := sessions[t]; ss != nil {
			if sv.Add == add {
				if _, ok := ss.ballots[voter]; !ok {
					ss.ballots[voter] = sv
				}
				delete(ss.withdrawn, voter) // voted again after withdrawing
			} else if _, ok := ss.ballots[voter]; ok && own[voter] {
				ss.withdrawn[voter] = b.Time
			}
		}
		if quorum := voteNeeded(len(members)); count(t, add) >= quorum || count(t, !add) >= quorum {
			passed := count(t, add) >= quorum
			delete(tally, t)
			if passed {
				delete(sessions, t) // list changed: passed, left to backfillPassed and the normal flow
			} else if sessions[t] != nil {
				end(t, b, members) // opposite direction reached a majority and the list did not change: the whole vote is voided
			}
			continue
		}
		switch n := count(t, add); {
		case n > 0 && sessions[t] == nil:
			sessions[t] = &session{first: sv, ballots: map[string]collector.SeenVote{voter: sv}, withdrawn: map[string]time.Time{}}
		case n == 0 && sessions[t] != nil:
			end(t, b, members)
		}
	}
	sort.Slice(out, func(i, j int) bool { return *out[i].EndedBlock < *out[j].EndedBlock })
	return out
}

// contiguousTail is a pure function returning the BFT blocks in the cache (oldest to newest) that
// form the last run of contiguous block numbers within this epoch (after epochStart).
func contiguousTail(blocks []collector.BlockStamp, epochStart int64) []collector.BlockStamp {
	i := len(blocks)
	for i > 0 {
		b := blocks[i-1]
		if !b.BFT || b.Number <= epochStart || (i < len(blocks) && blocks[i].Number != b.Number+1) {
			break
		}
		i--
	}
	return blocks[i:]
}

// knownVotes is a pure function returning the known votes used to dedupe backfill: ended ones in
// the DB, this round's results, plus open rows not closed this round. For rows closed this round
// the result wins; using the old copy (with an end of infinity) would block a later vote on the
// same address and direction that started and dropped to zero again.
func knownVotes(open, ended, recs []store.VoteRecord) []store.VoteRecord {
	closed := map[int64]bool{}
	for _, r := range recs {
		if r.ID != 0 && r.EndedBlock != nil {
			closed[r.ID] = true
		}
	}
	known := slices.Concat(ended, recs)
	for _, r := range open {
		if !closed[r.ID] {
			known = append(known, r)
		}
	}
	return known
}

// overlapsKnown is a pure function reporting whether a row with the same (address, direction)
// already exists whose [first ballot, end] (end is infinity for open rows) overlaps r.
func overlapsKnown(known []store.VoteRecord, r store.VoteRecord) bool {
	for _, k := range known {
		if k.Address != r.Address || k.Add != r.Add {
			continue
		}
		kEnd := int64(math.MaxInt64)
		if k.EndedBlock != nil {
			kEnd = *k.EndedBlock
		}
		if k.FirstBlock <= *r.EndedBlock && r.FirstBlock <= kEnd {
			return true
		}
	}
	return false
}
