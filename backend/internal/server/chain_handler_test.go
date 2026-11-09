package server

import (
	"context"
	"slices"
	"testing"

	"besu-manager/backend/internal/api"
)

// GET /chain over a live source node: height, chain id, consensus, quorum and epoch come from the
// source (ties go to the smallest node id); one round change in the last hour is charged to the
// validator whose turn was skipped, with its node id when it is ours.
func TestGetChainInfo(t *testing.T) {
	g := newChainFixture(t, true)
	blk := g.turnOf(g.vb) + 3 // vb proposes again 3 blocks later, after one round change
	g.rpc.rounds[blk] = 1
	g.poll(t)

	res, err := g.s.GetChainInfo(context.Background(), api.GetChainInfoRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	c := res.(api.GetChainInfo200JSONResponse)
	if c.SourceNodeId == nil || *c.SourceNodeId != "node-a1" || *c.Height != 12 || *c.ChainId != 1337 || *c.Consensus != api.Qbft ||
		*c.ValidatorCount != 3 || *c.BlockQuorum != 2 || *c.EpochLength != 30000 || *c.EpochStart != 0 || !c.LatestBlockAt.Equal(g.rpc.blockTime(12)) {
		t.Fatalf("chain info: %+v", c)
	}
	if len(c.BlockIntervals) != 11 || len(c.Stability24h) != 24 {
		t.Fatalf("intervals %v, stability %d slots", c.BlockIntervals, len(c.Stability24h))
	}
	missed := g.set[(slices.Index(g.set, g.vb)+len(g.set)-1)%len(g.set)]
	rc := c.RoundChanges1h
	if rc == nil || rc.Total != 1 || len(rc.ByProposer) != 1 || rc.ByProposer[0].Address != missed || rc.ByProposer[0].Count != 1 {
		t.Fatalf("round changes: %+v", rc)
	}
	if own := missed != g.vb && rc.ByProposer[0].NodeId != nil; own != (missed == g.v1 || missed == g.v2) {
		t.Fatalf("node id of the skipped validator: %+v", rc.ByProposer[0])
	}
}

// No healthy node: no source, and no round-change figure pretending to be zero.
func TestGetChainInfoNoSource(t *testing.T) {
	g := newChainFixture(t, true)
	g.rpc.down = map[string]bool{"node-a1": true, "node-a2": true, "node-a3": true}
	g.poll(t)
	res, err := g.s.GetChainInfo(context.Background(), api.GetChainInfoRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if c := res.(api.GetChainInfo200JSONResponse); c.SourceNodeId != nil || c.Height != nil || c.RoundChanges1h != nil || len(c.Stability24h) != 24 {
		t.Fatalf("want empty chain info, got %+v", c)
	}
}
