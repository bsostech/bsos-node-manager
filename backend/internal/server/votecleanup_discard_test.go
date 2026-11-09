package server

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"besu-manager/backend/internal/store/storetest"
)

// A background round withdraws only leftover votes (direction already satisfied by the set) and
// leaves votes still in progress alone; an address the platform just voted on waits out voteQuiet;
// one node failing is recorded per vote. Audit: the planned votes first, then each one's result.
func TestDiscardResidualVotes(t *testing.T) {
	g := newChainFixture(t, true)
	audited := recordAudit(t)
	g.rpc.pending["node-a1"] = map[string]bool{g.vb: true, g.x: true} // vb already in: leftover; x being voted on
	g.rpc.pending["node-a2"] = map[string]bool{g.x: false}            // x already out: leftover
	g.poll(t)
	g.ops.fail["node-a2"] = errors.New("agent offline")

	if err := g.s.discardResidualVotes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"node-a1 qbft discard " + g.vb, "node-a2 qbft discard " + g.x}; !slices.Equal(g.ops.calls, want) {
		t.Fatalf("RPC calls:\n got %v\nwant %v", g.ops.calls, want)
	}
	entries := audited("validator_vote_auto_discarded")
	if len(entries) != 2 || entries[0]["result"] != nil || entries[1]["result"] != "done" {
		t.Fatalf("audit: %+v", entries)
	}
	done := entries[1]["votes"].([]map[string]any)
	if len(done) != 2 || done[0]["nodeId"] != "node-a1" || done[0]["address"] != g.vb || done[0]["add"] != true || done[0]["ok"] != true ||
		done[1]["nodeId"] != "node-a2" || done[1]["ok"] != false || done[1]["reason"] != "operation_failed" {
		t.Fatalf("per-vote results: %+v", done)
	}
	// The failure carries a reason the audit page translates; the raw text goes in params.detail
	if p, _ := done[1]["params"].(map[string]string); p["detail"] != "agent offline" {
		t.Fatalf("per-vote results: %+v", done)
	}

	// vb was just voted on through the platform: this round leaves it for later.
	g.ops.calls = nil
	g.s.voteTouched[g.vb] = time.Now()
	if err := g.s.discardResidualVotes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{"node-a2 qbft discard " + g.x}; !slices.Equal(g.ops.calls, want) {
		t.Fatalf("recently touched address withdrawn: %v", g.ops.calls)
	}
}

// Epoch unknown (ADMIN off): on-chain ballots may be incomplete, so the whole round does nothing.
func TestDiscardResidualVotesSkipsIncompleteTally(t *testing.T) {
	g := newChainFixture(t, true)
	audited := recordAudit(t)
	g.rpc.epoch = 0
	g.rpc.pending["node-a1"] = map[string]bool{g.vb: true}
	g.poll(t)
	if err := g.s.discardResidualVotes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(g.ops.calls) != 0 || len(audited("validator_vote_auto_discarded")) != 0 {
		t.Fatalf("incomplete tally still withdrew: %v", g.ops.calls)
	}
}

// The plan cannot be audited: nothing is withdrawn.
func TestDiscardResidualVotesAuditFailClosed(t *testing.T) {
	g := newChainFixture(t, true)
	g.rpc.pending["node-a1"] = map[string]bool{g.vb: true}
	g.poll(t)
	events := storetest.FailAuditForTest(t)
	if err := g.s.discardResidualVotes(context.Background()); !errors.Is(err, errAudit) {
		t.Fatalf("want errAudit, got %v", err)
	}
	if len(g.ops.calls) != 0 || !slices.Equal(*events, []string{"validator_vote_auto_discarded"}) {
		t.Fatalf("calls %v, audit attempts %v", g.ops.calls, *events)
	}
}
