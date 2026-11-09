package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/enode"
	"besu-manager/backend/internal/store"
)

func pubOfEnode(en string) string { return strings.TrimPrefix(strings.Split(en, "@")[0], "enode://") }

// The list is the shared list (entered by users; our org's nodes may be entered too), and an entry
// whose public key is one of our nodes says which node. nodes lists each node's toggle, number of custom
// entries, and how many of everything it should be pushed (shared + custom) are not connected.
func TestBuildSharedPeersCounts(t *testing.T) {
	enA1, _ := mkPeer(t, "a1", "10.0.0.1:30303")
	enA2, _ := mkPeer(t, "a2", "10.0.0.2:30303")
	enA3, _ := mkPeer(t, "a3", "10.0.0.3:30303") // shared, not reporting
	enA4, _ := mkPeer(t, "a4", "10.0.0.4:30303") // toggle off, has custom entries
	enA5, _ := mkPeer(t, "a5", "10.0.0.5:30303") // quarantined
	enB1, _ := mkPeer(t, "b1", "172.16.0.1:30303")
	enC1, _ := mkPeer(t, "c1", "172.16.1.1:30303")
	now := time.Now()
	nodes := []store.Node{
		{ID: "node-a1", ReportedEnode: &enA1, PeerShared: true},
		{ID: "node-a2", ReportedEnode: &enA2, PeerShared: true},
		{ID: "node-a3", ReportedEnode: &enA3, PeerShared: true},
		{ID: "node-a4", ReportedEnode: &enA4},
		{ID: "node-a5", ReportedEnode: &enA5, PeerShared: true, QuarantinedAt: &now},
		{ID: "node-a6"},
	}
	innerA2 := "enode://" + pubOfEnode(enA2) + "@192.168.0.2:30303"
	manual := []store.ManualPeer{
		{Pubkey: pubOfEnode(enB1), Enode: enB1},
		{Pubkey: pubOfEnode(enA2), Enode: innerA2},
	}
	custom := []store.NodePeer{
		{NodeID: "node-a4", Pubkey: pubOfEnode(enB1), Enode: enB1},
		{NodeID: "node-a4", Pubkey: pubOfEnode(enC1), Enode: enC1},
	}
	perNode := map[string][]collector.ObservedPeer{
		"node-a1": {{Pubkey: pubOfEnode(enA2)}, {Pubkey: pubOfEnode(enB1)}},
		"node-a2": {{Pubkey: pubOfEnode(enA1)}},
		"node-a4": {{Pubkey: pubOfEnode(enB1)}},
	}
	got := buildSharedPeers(nodes, manual, custom, perNode, nil, nil)
	if len(got.Peers) != 2 || got.Peers[0].Enode != enB1 || got.Peers[1].Enode != innerA2 {
		t.Fatalf("清單照填的順序、位址照填的: %+v", got.Peers)
	}
	if b1 := got.Peers[0]; b1.NodeId != nil {
		t.Errorf("b1 %+v", b1)
	}
	if a2 := got.Peers[1]; a2.NodeId == nil || *a2.NodeId != "node-a2" {
		t.Errorf("a2 是本單位節點 %+v", a2)
	}
	if len(got.Nodes) != 6 {
		t.Fatalf("nodes %+v", got.Nodes)
	}
	missing := func(i int) any {
		if m := got.Nodes[i].Missing; m != nil {
			return *m
		}
		return nil
	}
	// a1 connected to all; a2 not connected to b1 (its own entry does not count); a4's custom c1
	// not connected; a3 not reporting, a5 quarantined, a6 not reporting are all nil
	for i, want := range []any{0, 1, nil, 1, nil, nil} {
		if missing(i) != want {
			t.Errorf("%s missing = %v, want %v", got.Nodes[i].Id, missing(i), want)
		}
	}
	if a4 := got.Nodes[3]; a4.UseShared || a4.Additions != 2 || !got.Nodes[4].Quarantined || got.Nodes[5].Additions != 0 || !got.Nodes[0].UseShared {
		t.Errorf("nodes %+v", got.Nodes)
	}
}

// Candidates: peers seen by our nodes (regardless of toggle) that are neither in the list nor ours;
// sorted by how many nodes see them.
func TestBuildSharedPeersCandidates(t *testing.T) {
	enA1, _ := mkPeer(t, "a1", "10.0.0.1:30303")
	enA2, _ := mkPeer(t, "a2", "10.0.0.2:30303")
	enB1, _ := mkPeer(t, "b1", "172.16.0.1:30303") // already in the list
	enC1, addrC1 := mkPeer(t, "c1", "172.16.1.1:30303")
	enD1, _ := mkPeer(t, "d1", "192.168.0.1:41234")
	nodes := []store.Node{{ID: "node-a1", ReportedEnode: &enA1, PeerShared: true}, {ID: "node-a2", ReportedEnode: &enA2}}
	manual := []store.ManualPeer{{Pubkey: pubOfEnode(enB1), Enode: enB1}}
	perNode := map[string][]collector.ObservedPeer{
		"node-a1": {{Pubkey: pubOfEnode(enA2)}, {Pubkey: pubOfEnode(enB1)}, {Pubkey: pubOfEnode(enD1), Enode: enD1}, {Pubkey: pubOfEnode(enC1), Enode: enC1}},
		"node-a2": {{Pubkey: pubOfEnode(enC1), Enode: enC1}, {Pubkey: pubOfEnode(enC1), Enode: enC1}}, // the same node listing it twice counts once
	}
	validators := map[string]bool{enode.NormalizeAddress(addrC1): true}
	names := map[string]string{enode.NormalizeAddress(addrC1): "org-C 驗證者"}
	got := buildSharedPeers(nodes, manual, nil, perNode, validators, names).Candidates
	if len(got) != 2 {
		t.Fatalf("candidates = %+v", got)
	}
	if got[0].Pubkey != pubOfEnode(enC1) || got[0].ValidatorAddress == nil || got[0].Name == nil || *got[0].Name != "org-C 驗證者" {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Pubkey != pubOfEnode(enD1) || got[1].ValidatorAddress != nil || got[1].Enode != enD1 {
		t.Errorf("second = %+v", got[1])
	}
}

// Vote history: our org's nodes without a name get the node id; named ones and other orgs' are left alone.
func TestFillOwnIDs(t *testing.T) {
	enA1, addrA1 := mkPeer(t, "a1", "10.0.0.1:30303")
	enA2, addrA2 := mkPeer(t, "a2", "10.0.0.2:30303")
	_, addrB1 := mkPeer(t, "b1", "172.16.0.1:30303")
	nm := "台北主驗證者"
	nodes := []store.Node{{ID: "node-a1", ReportedEnode: &enA1, Name: &nm}, {ID: "node-a2", ReportedEnode: &enA2}}
	names := map[string]string{enode.NormalizeAddress(addrA1): nm, enode.NormalizeAddress(addrB1): "org-B"}
	fillOwnIDs(names, nodes)
	if names[enode.NormalizeAddress(addrA1)] != nm {
		t.Errorf("取了名的不該被蓋掉: %q", names[enode.NormalizeAddress(addrA1)])
	}
	if names[enode.NormalizeAddress(addrA2)] != "node-a2" {
		t.Errorf("沒取名的該補 id: %q", names[enode.NormalizeAddress(addrA2)])
	}
	if names[enode.NormalizeAddress(addrB1)] != "org-B" {
		t.Error("非本單位的名字要保留")
	}
}

// Names of our org's nodes come only from nodes.name: old names in peer_names are not used, and an
// unnamed node has no entry.
func TestOverlayOwnNames(t *testing.T) {
	enA1, addrA1 := mkPeer(t, "a1", "10.0.0.1:30303")
	enA2, addrA2 := mkPeer(t, "a2", "10.0.0.2:30303")
	_, addrB1 := mkPeer(t, "b1", "172.16.0.1:30303")
	nm := "台北主驗證者"
	nodes := []store.Node{{ID: "node-a1", ReportedEnode: &enA1, Name: &nm}, {ID: "node-a2", ReportedEnode: &enA2}}
	names := map[string]string{
		enode.NormalizeAddress(addrA1): "舊名字",
		enode.NormalizeAddress(addrA2): "舊名字",
		enode.NormalizeAddress(addrB1): "org-B",
	}
	overlayOwnNames(names, nodes)
	if names[enode.NormalizeAddress(addrA1)] != nm {
		t.Errorf("a1 = %q", names[enode.NormalizeAddress(addrA1)])
	}
	if _, ok := names[enode.NormalizeAddress(addrA2)]; ok {
		t.Error("a2 沒取名,不該沿用 peer_names 的舊名字")
	}
	if names[enode.NormalizeAddress(addrB1)] != "org-B" {
		t.Error("非本單位的名字要保留")
	}
	if ownAddresses(nodes)[enode.NormalizeAddress(addrA2)] != "node-a2" || ownAddresses(nodes)[enode.NormalizeAddress(addrB1)] != "" {
		t.Error("ownAddresses 判斷錯誤")
	}
}

func TestNormNodeName(t *testing.T) {
	s := func(v string) *string { return &v }
	if n, msg := normNodeName(s("  台北主驗證者 ")); msg != "" || n == nil || *n != "台北主驗證者" {
		t.Errorf("trim: %v %q", n, msg)
	}
	if n, msg := normNodeName(s("   ")); msg != "" || n != nil {
		t.Errorf("空白應視為清除: %v %q", n, msg)
	}
	if n, msg := normNodeName(nil); msg != "" || n != nil {
		t.Errorf("nil: %v %q", n, msg)
	}
	if n, msg := normNodeName(s(strings.Repeat("名", nodeNameMax))); msg != "" || n == nil {
		t.Errorf("剛好 %d 字應通過:%q", nodeNameMax, msg)
	}
	if _, msg := normNodeName(s(strings.Repeat("名", nodeNameMax+1))); msg == "" {
		t.Error("65 字應拒絕")
	}
}

func TestNormNodeID(t *testing.T) {
	for in, want := range map[string]string{"Node-A8": "node-a8", " VN-01 ": "vn-01", "vn-01": "vn-01"} {
		if got, ok := normNodeID(in); !ok || got != want {
			t.Errorf("normNodeID(%q) = %q,%v", in, got, ok)
		}
	}
	for _, bad := range []string{"-vn", "vn_01", "台北", ""} {
		if _, ok := normNodeID(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}

// peer_names does not accept addresses of our org's nodes (rename the node itself instead); other
// addresses are written as usual.
func TestSetPeerNameRejectsOwnNode(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	enA1, addrA1 := mkPeer(t, "a1", "10.0.0.1:30303")
	_, addrB1 := mkPeer(t, "b1", "172.16.0.1:30303")
	if err := s.store.UpsertOwnNode(ctx, "node-a1", "org-test", 8545, 9545, nil); err != nil {
		t.Fatal(err)
	}
	// only an enode, no validator_address yet (RPC down): the address must be derived from the public key
	if err := s.store.SetNodeIdentity(ctx, "node-a1", "", enA1); err != nil {
		t.Fatal(err)
	}
	set := func(addr string) api.SetPeerNameResponseObject {
		t.Helper()
		res, err := s.SetPeerName(ctx, api.SetPeerNameRequestObject{Address: addr, Body: &api.SetPeerNameJSONRequestBody{Name: "取名"}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if _, ok := set(addrA1).(api.SetPeerName409JSONResponse); !ok {
		t.Error("本單位節點的地址應回 409")
	}
	if _, ok := set(addrB1).(api.SetPeerName204Response); !ok {
		t.Error("非本單位的地址應可取名")
	}
}
