package server

import (
	"context"
	"sort"
	"testing"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/collector"
)

// Both our validators vote to add x and the set grows at block 8: the recorder writes one passed
// record, and the history lists it with our ballots marked own and names filled in (a named node by
// its name, an unnamed one by its id). A second recorder round adds nothing.
func TestListValidatorVoteHistory(t *testing.T) {
	g := newChainFixture(t, true)
	grown := append([]string{g.x}, g.set...)
	sort.Strings(grown)
	g.rpc.sets[8] = grown
	b1, b2 := g.turnOf(g.v1), g.turnOf(g.v2)
	g.rpc.votes[b1] = collector.BlockVote{Target: g.x, Add: true}
	g.rpc.votes[b2] = collector.BlockVote{Target: g.x, Add: true}
	g.poll(t)
	ctx := context.Background()
	for range 2 {
		if err := g.s.recordVoteHistory(ctx, g.s.col.Chain()); err != nil {
			t.Fatal(err)
		}
	}

	res, err := g.s.ListValidatorVoteHistory(ctx, api.ListValidatorVoteHistoryRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	got := res.(api.ListValidatorVoteHistory200JSONResponse)
	if len(got) != 1 {
		t.Fatalf("want one record, got %+v", got)
	}
	r := got[0]
	if r.Address != g.x || !r.Add || r.Result != api.ValidatorVoteOutcome("passed") || r.EndedBlock != 8 || r.Votes != 2 || r.Needed != 2 {
		t.Fatalf("record: %+v", r)
	}
	// Ballots list every validator at the time; only ours are marked own and carry a block.
	names := map[string]string{}
	for _, b := range r.Ballots {
		if b.Address == g.vb {
			if b.Own != nil || b.Block != nil {
				t.Fatalf("other org's ballot: %+v", b)
			}
			continue
		}
		if b.Own == nil || !*b.Own || b.Block == nil || b.Name == nil {
			t.Fatalf("own ballot: %+v", b)
		}
		names[b.Address] = *b.Name
	}
	if len(r.Ballots) != 3 || len(names) != 2 || names[g.v1] != "north-1" || names[g.v2] != "node-a2" {
		t.Fatalf("ballots %+v, names %v", r.Ballots, names)
	}
}

func TestBlockAt(t *testing.T) {
	blocks := []collector.BlockStamp{{Number: 3}, {Number: 4}, {Number: 7}}
	for _, c := range []struct {
		n  int64
		ok bool
	}{{3, true}, {7, true}, {5, false}, {2, false}, {8, false}} {
		b, ok := blockAt(blocks, c.n)
		if ok != c.ok || (ok && b.Number != c.n) {
			t.Errorf("blockAt(%d) = %+v, %v", c.n, b, ok)
		}
	}
}
