package server

import (
	"context"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/enode"
	"besu-manager/backend/internal/rpcops"
	"besu-manager/backend/internal/store"
)

// Toggle off: the shared-list entry sent last round goes to admin_removePeer, custom ones stay;
// toggle back on: the shared entry is added back, custom ones untouched.
func TestPushTargetsSwitch(t *testing.T) {
	enA1, _ := mkPeer(t, "a1", "10.0.0.1:30303")
	enB1, _ := mkPeer(t, "b1", "10.0.1.1:30303")
	enB2, _ := mkPeer(t, "b2", "10.0.1.2:30303")
	pubB1, _ := enode.PubKeyHex(enB1)
	pubB2, _ := enode.PubKeyHex(enB2)
	manual := []store.ManualPeer{{Pubkey: pubB1, Enode: enB1}}
	custom := []store.NodePeer{{NodeID: "node-a1", Pubkey: pubB2, Enode: enB2}}
	at := func(on bool) []string {
		return pushTargets([]store.Node{{ID: "node-a1", ReportedEnode: &enA1, PeerShared: on}}, manual, custom)["node-a1"]
	}
	on, off := at(true), at(false)
	if len(on) != 2 || len(off) != 1 || off[0] != enB2 {
		t.Fatalf("on %v off %v", on, off)
	}
	if drop := rpcops.PeerDiff(on, off); len(drop) != 1 || drop[0] != enB1 {
		t.Fatalf("關掉時 drop %v", drop)
	}
	if drop := rpcops.PeerDiff(off, on); len(drop) != 0 {
		t.Fatalf("打開時不該移除 %v", drop)
	}
}

// Each custom entry is tagged with which of our nodes it belongs to, whether it is quarantined, and
// its display name; the entries are returned whether or not the node uses the shared list.
func TestBuildCustomPeers(t *testing.T) {
	enA1, _ := mkPeer(t, "a1", "10.0.0.1:30303")
	enA2, _ := mkPeer(t, "a2", "10.0.0.2:30303")
	enB1, addrB1 := mkPeer(t, "b1", "10.0.1.1:30303")
	pubA2, _ := enode.PubKeyHex(enA2)
	pubB1, _ := enode.PubKeyHex(enB1)
	nodes := []store.Node{
		{ID: "node-a1", ReportedEnode: &enA1},
		{ID: "node-a2", ReportedEnode: &enA2, QuarantinedAt: ptr(time.Now())},
	}
	list := []store.NodePeer{
		{NodeID: "node-a1", Pubkey: pubA2, Enode: "enode://" + pubA2 + "@192.168.1.2:30303"},
		{NodeID: "node-a1", Pubkey: pubB1, Enode: enB1},
	}
	v := buildCustomPeers(nodes[0], nodes, list, map[string]string{enode.NormalizeAddress(addrB1): "org-B 1"})
	if v.UseShared || len(v.Peers) != 2 {
		t.Fatalf("%+v", v)
	}
	a2, b1 := v.Peers[0], v.Peers[1]
	if !a2.Quarantined || a2.NodeId == nil || *a2.NodeId != "node-a2" {
		t.Fatalf("a2 %+v", a2)
	}
	if b1.Quarantined || b1.NodeId != nil || b1.Name == nil || *b1.Name != "org-B 1" {
		t.Fatalf("b1 %+v", b1)
	}
	if on := buildCustomPeers(store.Node{ID: "node-a1", ReportedEnode: &enA1, PeerShared: true}, nodes, list, nil); !on.UseShared || len(on.Peers) != 2 {
		t.Fatalf("開著也回自行加入的 %+v", on)
	}
	if none := buildCustomPeers(store.Node{ID: "node-a3"}, nodes, nil, nil); none.UseShared || len(none.Peers) != 0 {
		t.Fatalf("都沒有 %+v", none)
	}
}

// Through the handlers: adding a custom peer returns the entry just written; the node list marks a
// node with additions as custom and one without as off; GET lists this node's additions with their
// names; the observed peer list carries the name given to the peer.
func TestNodeCustomPeersHandlers(t *testing.T) {
	s, _ := accessTestServer(t)
	recordAudit(t)
	ctx := asAdmin(context.Background())
	for _, id := range []string{"node-a1", "node-a2", "node-a3"} {
		if err := s.store.UpsertOwnNode(ctx, id, "org-test", 8545, 9545, nil); err != nil {
			t.Fatal(err)
		}
	}
	enB1, addrB1 := mkPeer(t, "b1", "10.0.1.1:30303")
	enC1, _ := mkPeer(t, "c1", "10.0.2.1:30303")
	pubB1, _ := enode.PubKeyHex(enB1)
	s.col = collector.New(s.store, downRequester{}, allowRPC{})
	s.peersMissing = map[string]string{}
	s.ops = &peerOps{}
	add := func(id, en string) api.CustomPeer {
		t.Helper()
		r, err := s.CreateNodeCustomPeer(ctx, api.CreateNodeCustomPeerRequestObject{Id: id, Body: &api.ManualPeerCreateRequest{Enode: en}})
		if err != nil {
			t.Fatal(err)
		}
		c, ok := r.(api.CreateNodeCustomPeer201JSONResponse)
		if !ok {
			t.Fatalf("%s 加 %s:%#v", id, en, r)
		}
		return api.CustomPeer(c)
	}
	if c := add("node-a1", enB1); c.Pubkey != pubB1 || c.Enode != enB1 {
		t.Fatalf("回應應是剛寫入的那筆:%+v", c)
	}
	add("node-a1", enC1)
	add("node-a2", enB1)
	if r, err := s.SetPeerName(ctx, api.SetPeerNameRequestObject{Address: addrB1, Body: &api.PeerNameRequest{Name: "B 一號"}}); err != nil {
		t.Fatal(err)
	} else if _, ok := r.(api.SetPeerName204Response); !ok {
		t.Fatalf("取名:%#v", r)
	}
	pollCollector(t, s, snapRPC{down: map[string]bool{"node-a2": true}, peers: map[string][]string{"node-a1": {enB1}}}, "node-a1", "node-a2")

	r, err := s.ListNodes(ctx, api.ListNodesRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	mode := map[string]api.PeerMode{}
	for _, n := range r.(api.ListNodes200JSONResponse).Nodes {
		if n.PeerMode == nil {
			t.Fatalf("%s: peerMode missing", n.Id)
		}
		mode[n.Id] = *n.PeerMode
	}
	if mode["node-a1"] != api.PeerModeCustom || mode["node-a3"] != api.PeerModeOff {
		t.Errorf("節點清單 peerMode:%v", mode)
	}

	get := func(id string) map[string]api.CustomPeer {
		t.Helper()
		r, err := s.GetNodeCustomPeers(ctx, api.GetNodeCustomPeersRequestObject{Id: id})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]api.CustomPeer{}
		for _, p := range r.(api.GetNodeCustomPeers200JSONResponse).Peers {
			out[p.Enode] = p
		}
		return out
	}
	a1 := get("node-a1")
	if b1 := a1[enB1]; b1.Name == nil || *b1.Name != "B 一號" {
		t.Errorf("a1 的 b1 應帶名稱:%+v", b1)
	}
	if _, ok := a1[enC1]; !ok || len(a1) != 2 {
		t.Errorf("a1 應列出自行加入的兩筆:%+v", a1)
	}

	// The observed peer list carries the name given to the peer's address
	pr, err := s.ListNodePeers(ctx, api.ListNodePeersRequestObject{Id: "node-a1"})
	if err != nil {
		t.Fatal(err)
	}
	if ps := pr.(api.ListNodePeers200JSONResponse).Peers; len(ps) != 1 || ps[0].Name == nil || *ps[0].Name != "B 一號" {
		t.Errorf("a1 的 peer 應帶名稱:%+v", ps)
	}
}
