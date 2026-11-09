package server

import (
	"context"
	"testing"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/store"
)

// /shared-peers end to end: the shared list comes from the database with names from peer_names,
// each node's missing count comes from this round's admin_peers (null for a node that did not
// answer), and a peer seen by our nodes but not in the list is offered as a candidate, tagged as a
// validator when it is in the on-chain validator set.
func TestListSharedPeers(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	enA1, addrA1 := mkPeer(t, "a1", "10.0.0.1:30303")
	enA2, addrA2 := mkPeer(t, "a2", "10.0.0.2:30303")
	enB1, addrB1 := mkPeer(t, "b1", "172.16.0.1:30303")
	enC1, addrC1 := mkPeer(t, "c1", "172.16.1.1:30303")
	for _, n := range []struct{ id, en, addr string }{{"node-a1", enA1, addrA1}, {"node-a2", enA2, addrA2}} {
		if err := s.store.UpsertOwnNode(ctx, n.id, "org-test", 8545, 9545, nil); err != nil {
			t.Fatal(err)
		}
		if err := s.store.SetNodeIdentity(ctx, n.id, n.addr, n.en); err != nil {
			t.Fatal(err)
		}
		if _, err := s.store.SetPeerShared(ctx, n.id, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.store.AddManualPeer(ctx, store.ManualPeer{Pubkey: pubOfEnode(enB1), Enode: enB1}, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetPeerName(ctx, store.PeerName{Address: addrB1, Name: "B 單位"}); err != nil {
		t.Fatal(err)
	}
	pollCollector(t, s, snapRPC{
		down:       map[string]bool{"node-a2": true},
		validators: []string{addrA1, addrC1},
		peers:      map[string][]string{"node-a1": {enB1, enC1}},
	}, "node-a1", "node-a2")

	r, err := s.ListSharedPeers(ctx, api.ListSharedPeersRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	got := api.SharedPeers(r.(api.ListSharedPeers200JSONResponse))
	if len(got.Peers) != 1 {
		t.Fatalf("共用清單一筆: %+v", got.Peers)
	}
	if b := got.Peers[0]; b.Enode != enB1 || b.Name == nil || *b.Name != "B 單位" || b.NodeId != nil {
		t.Fatalf("b1 應帶名稱: %+v", b)
	}
	if len(got.Candidates) != 1 || got.Candidates[0].Enode != enC1 || got.Candidates[0].ValidatorAddress == nil {
		t.Fatalf("c1 應為候選並標驗證者: %+v", got.Candidates)
	}
	if len(got.Nodes) != 2 || got.Nodes[0].Missing == nil || *got.Nodes[0].Missing != 0 || got.Nodes[1].Missing != nil {
		t.Fatalf("a1 沒缺、a2 沒回報 missing 為 null: %+v", got.Nodes)
	}
}
