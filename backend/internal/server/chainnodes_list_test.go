package server

import (
	"context"
	"testing"

	"besu-manager/backend/internal/api"
)

// GET /chain/nodes: the validator set, our own nodes and the peers they see, as one list. Our
// validators come first, then the other org's validator, then our non-validator node; the other
// org's lag is estimated from our nodes' admin_peers.
func TestListChainNodes(t *testing.T) {
	g := newChainFixture(t, true)
	g.poll(t)
	res, err := g.s.ListChainNodes(context.Background(), api.ListChainNodesRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	rows := res.(api.ListChainNodes200JSONResponse)
	own1, own2 := g.v1, g.v2
	if own2 < own1 {
		own1, own2 = own2, own1
	}
	var order []string
	for _, r := range rows {
		order = append(order, r.Address)
	}
	if want := []string{own1, own2, g.vb, g.a3}; len(order) != 4 || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] || order[3] != want[3] {
		t.Fatalf("order:\n got %v\nwant %v", order, want)
	}
	for _, r := range rows[:3] {
		if !r.IsValidator || r.LastProposed == nil || r.MissedTurns1000 == nil || *r.MissedTurns1000 != 0 || r.Producing == nil || !*r.Producing {
			t.Fatalf("validator row: %+v", r)
		}
	}
	if r := rows[0]; !r.IsOwn || r.NodeId == nil {
		t.Fatalf("own validator: %+v", r)
	}
	// height 12, peers report latestBlock 9: 12 - 1 - 9
	if r := rows[2]; r.IsOwn || r.NodeId != nil || r.BlocksBehind == nil || *r.BlocksBehind != 2 {
		t.Fatalf("other org's validator: %+v", r)
	}
	if r := rows[3]; !r.IsOwn || r.IsValidator || *r.NodeId != "node-a3" || r.LastProposed != nil {
		t.Fatalf("own non-validator: %+v", r)
	}
}
