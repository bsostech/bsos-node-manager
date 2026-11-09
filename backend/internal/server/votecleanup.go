package server

import (
	"context"
	"log"
	"sort"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/enode"
)

// Automatic withdrawal of leftover votes: votes whose direction the validator set already satisfies
// but that remain in a node's pending-vote list come back to life as soon as the set changes back.

const (
	// voteQuiet: addresses the platform voted on are not auto-withdrawn within this period. The
	// validator set and on-chain votes used for the decision come from the collector snapshot (one
	// round every 10 seconds), so a vote just cast may be misjudged as leftover before the snapshot
	// catches up (e.g. X was just removed, the snapshot still has it in the set, and someone votes
	// "add X").
	voteQuiet = 2 * time.Minute
	// cleanupBudget caps how long one round holds voteMu, so a stuck node does not block manual
	// voting for too long.
	cleanupBudget = 20 * time.Second
)

// pendingVote is one vote in the pending-vote list; Address is 0x + lowercase.
type pendingVote struct {
	Address string
	Add     bool
}

// residualVotes is a pure function returning the votes already left over in one node's pending-vote
// list (sorted by address). Leftover = the direction is already satisfied by validators, and this
// node's (voter's) latest on-chain ballot for that address in this epoch since Changed is not in
// the opposite direction; if it is opposite, the vote on the node is a cancelling vote that Besu
// will still cast, so it must not be withdrawn.
func residualVotes(pending map[string]bool, validators []string, voter string, view collector.ChainView) []pendingVote {
	inSet := make(map[string]bool, len(validators))
	for _, v := range validators {
		inSet[enode.NormalizeAddress(v)] = true
	}
	var out []pendingVote
	for addr, add := range pending {
		norm := enode.NormalizeAddress(addr)
		if inSet[norm] != add {
			continue // not yet satisfied: still being voted on
		}
		target := "0x" + norm
		if sv, ok := view.LatestVote(target, voter); ok && sv.Add != add {
			continue
		}
		out = append(out, pendingVote{target, add})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}

// residualCand is a node that appears to have leftover votes in the snapshot. voter is its
// validator address (0x + lowercase).
type residualCand struct {
	nodeID, consensus, voter string
	validators               []string
}

// residualPlan holds the votes confirmed for withdrawal after re-reading inside the lock.
type residualPlan struct {
	residualCand
	votes []pendingVote
}

// freshResiduals is called inside voteMu. It re-reads the pending-vote list via RPC directly (the
// snapshot can be a round old) and withdraws only votes whose direction is still leftover and that
// the platform has not voted on within voteQuiet. Nodes that cannot be read are skipped and checked
// next round.
func freshResiduals(ctx context.Context, ops NodeOps, cands []residualCand, view collector.ChainView, touched map[string]time.Time, now time.Time) []residualPlan {
	var out []residualPlan
	for _, c := range cands {
		fresh, err := ops.PendingVotes(ctx, c.nodeID, c.consensus)
		if err != nil {
			continue
		}
		var votes []pendingVote
		for _, v := range residualVotes(fresh, c.validators, c.voter, view) {
			// This node's same-direction vote is already in a block (e.g. a withdrawal
			// counter-vote): no need to wait for voteQuiet. If kept, once the list changes the
			// tally is cleared and it becomes a real vote
			sv, landed := view.LatestVote(v.Address, c.voter)
			if (landed && sv.Add == v.Add) || now.Sub(touched[v.Address]) >= voteQuiet {
				votes = append(votes, v)
			}
		}
		if len(votes) > 0 {
			out = append(out, residualPlan{c, votes})
		}
	}
	return out
}

// RunVoteCleanup records vote history each background round (votehistory.go), then withdraws
// leftover votes on our own nodes. Idempotent; a missed round is caught up by the next.
func (s *Server) RunVoteCleanup(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// Record vote history first, then withdraw leftover votes: both read the same
			// chain-level cache
			if err := s.recordVoteHistory(ctx, s.col.Chain()); err != nil {
				log.Printf("votes: record vote history: %v", err)
			}
			if err := s.discardResidualVotes(ctx); err != nil {
				log.Printf("votes: auto-discard leftover votes: %v", err)
			}
		}
	}
}

// discardResidualVotes uses the collector snapshot to pick nodes with leftover votes, then confirms
// by re-reading inside voteMu before withdrawing. The whole round is skipped while the tally is
// being backfilled (on-chain ballots are incomplete and cancelling votes cannot be recognized);
// nodes with an unknown validator address are skipped (same reason). Auditing follows fanOutVote:
// record which votes will be withdrawn first and do not withdraw if that cannot be written;
// afterwards add an entry with per-node results.
func (s *Server) discardResidualVotes(ctx context.Context) error {
	view := s.col.Chain()
	if !view.VotesComplete() {
		return nil
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return err
	}
	var cands []residualCand
	for _, n := range nodes {
		res, ok := s.col.Get(n.ID)
		if !ok || n.ValidatorAddress == nil || res.Snap == nil || res.Snap.Consensus == nil {
			continue
		}
		cs := res.Snap.Consensus
		if cs.Algorithm == nil || cs.Validators == nil || res.Snap.PendingVotes == nil {
			continue
		}
		c := residualCand{n.ID, string(*cs.Algorithm), "0x" + enode.NormalizeAddress(*n.ValidatorAddress), *cs.Validators}
		if len(residualVotes(res.Snap.PendingVotes, c.validators, c.voter, view)) > 0 {
			cands = append(cands, c)
		}
	}
	if len(cands) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, cleanupBudget)
	defer cancel()
	s.voteMu.Lock()
	defer s.voteMu.Unlock()
	plans := freshResiduals(ctx, s.ops, cands, view, s.voteTouched, time.Now())
	if len(plans) == 0 {
		return nil
	}
	var planned []map[string]any
	for _, p := range plans {
		for _, v := range p.votes {
			planned = append(planned, map[string]any{"nodeId": p.nodeID, "address": v.Address, "add": v.Add})
		}
	}
	const event = "validator_vote_auto_discarded"
	if err := s.audit(ctx, event, map[string]any{"votes": planned}); err != nil {
		return err
	}
	i := 0
	for _, p := range plans {
		for _, v := range p.votes {
			if err := s.ops.DiscardVote(ctx, p.nodeID, p.consensus, v.Address); err != nil { // same shape as sendEach: the cause's reason if it has one, else operation_failed + detail
				e := causeError(api.ErrorCodeInternal, api.ErrorReasonOperationFailed, err)
				planned[i]["ok"], planned[i]["error"], planned[i]["reason"] = false, e.Message, string(*e.Reason)
				if e.Params != nil {
					planned[i]["params"] = *e.Params
				}
			} else {
				planned[i]["ok"] = true
			}
			i++
		}
	}
	s.auditDone(ctx, event, map[string]any{"votes": planned})
	return nil
}
