package server

import (
	"sort"
	"strings"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/enode"
)

// Chain node list: one entry per address; validator, own, connection count, address ordering; our
// own validators carry both identities; production verdict and missed rounds same as the validators
// page; last proposal; blocks behind (own nodes use blocks_behind, others use the latestBlock - 1
// seen in the same round by our highest node).
func TestChainNodes(t *testing.T) {
	pubOf := func(c string) string { return strings.Repeat(c, 128) }
	addrOf := func(pub string) string { return enode.NormalizeAddress(pubAddress(pub)) }
	pa1, pa2, pb1, pb2, px := pubOf("1"), pubOf("2"), pubOf("3"), pubOf("4"), pubOf("5")
	a1, a2, b1, b2, x := addrOf(pa1), addrOf(pa2), addrOf(pb1), addrOf(pb2), addrOf(px)
	i64 := func(v int64) *int64 { return &v }
	one := 1

	no, yes := false, true
	miss, hit := api.ValidatorTurn{Proposed: false}, api.ValidatorTurn{Proposed: true}
	set := []api.ValidatorSetEntry{
		{Address: "0x" + strings.ToUpper(a1), Producing: &yes, Turns: []api.ValidatorTurn{hit, hit}}, // validator set addresses may be checksummed
		{Address: "0x" + b1, Producing: &no, Turns: []api.ValidatorTurn{hit, miss, miss}},
		{Address: "0x" + b2, Turns: []api.ValidatorTurn{}},
	}
	own := []ownNodeView{
		// a1: height 100, sees b1 latestBlock 95, x (a regular node) 99, a2 (own)
		{id: "node-a1", addr: a1, height: i64(100), behind: &one, peers: []collector.ObservedPeer{
			{Pubkey: pb1, LatestBlock: i64(95)}, {Pubkey: px, LatestBlock: i64(99)}, {Pubkey: pa2, LatestBlock: i64(10)}}},
		// a2: behind (height 90), sees b1 latestBlock 89 -> use the entry from a1, which is higher;
		// b2 not estimated yet
		{id: "node-a2", addr: a2, height: i64(90), peers: []collector.ObservedPeer{{Pubkey: pb1, LatestBlock: i64(89)}, {Pubkey: pb2}}},
	}
	at := time.Unix(1000, 0)
	blocks := []collector.BlockStamp{{Number: 7, Proposer: "0x" + b1, Time: at}, {Number: 8, Proposer: "0x" + a1, Time: at.Add(2 * time.Second)}, {Number: 9, Proposer: "0x" + x}}
	got := chainNodes(set, own, map[string]string{b1: "北區"}, blocks)

	var order []string
	byAddr := map[string]api.ChainNode{}
	for _, n := range got {
		order = append(order, n.Address)
		byAddr[n.Address] = n
	}
	// Validators: own a1; others b1 (2 nodes connected), b2 (1 node connected). Then own
	// non-validator a2, and finally x
	if want := []string{"0x" + a1, "0x" + b1, "0x" + b2, "0x" + a2, "0x" + x}; strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("排序:\n got %v\nwant %v", order, want)
	}
	if n := byAddr["0x"+a1]; !n.IsOwn || !n.IsValidator || *n.NodeId != "node-a1" || *n.BlocksBehind != 1 || *n.MissedTurns1000 != 0 || n.LastProposed.Height != 8 {
		t.Fatalf("自家驗證者:%+v", n)
	}
	if n := byAddr["0x"+b1]; n.IsOwn || *n.Producing || *n.MissedTurns1000 != 2 || *n.Name != "北區" || *n.BlocksBehind != 4 || n.LastProposed.Height != 7 {
		t.Fatalf("別人的驗證者(用 a1 的高度:100 − 1 − 95):%+v", n)
	}
	if n := byAddr["0x"+b2]; n.BlocksBehind != nil || n.Producing != nil || n.LastProposed != nil || n.MissedTurns1000 != nil {
		t.Fatalf("還沒估到、沒有輪到紀錄、沒出過:%+v", n)
	}
	if n := byAddr["0x"+a2]; !n.IsOwn || n.IsValidator || n.BlocksBehind != nil || n.MissedTurns1000 != nil {
		t.Fatalf("自家非驗證者不採用別人看到的 latestBlock:%+v", n)
	}
	if n := byAddr["0x"+x]; *n.BlocksBehind != 0 || n.LastProposed != nil || n.Producing != nil {
		t.Fatalf("一般節點(100 − 1 − 99 = 0;不是驗證者不記提案):%+v", n)
	}
}

// Does not go negative when exactly caught up (latestBlock = height) or ahead.
func TestChainNodesBehindNotNegative(t *testing.T) {
	pub := strings.Repeat("6", 128)
	h, lb := int64(50), int64(52)
	got := chainNodes(nil, []ownNodeView{{id: "node-a1", addr: "aa", height: &h, peers: []collector.ObservedPeer{{Pubkey: pub, LatestBlock: &lb}}}}, nil, nil)
	for _, n := range got {
		if !n.IsOwn && (n.BlocksBehind == nil || *n.BlocksBehind != 0) {
			t.Fatalf("%+v", n)
		}
	}
}

// Other organizations' regular nodes are ordered by how many of our nodes connect to them, most
// first, then by address. The highest address has the most connections, so the test can tell the
// order comes from connection count; the other two tie on count and must come lowest address first.
func TestChainNodesOrderByConnections(t *testing.T) {
	pubs := []string{strings.Repeat("7", 128), strings.Repeat("8", 128), strings.Repeat("9", 128)}
	addr := func(pub string) string { return "0x" + enode.NormalizeAddress(pubAddress(pub)) }
	sort.Slice(pubs, func(i, j int) bool { return addr(pubs[i]) < addr(pubs[j]) })
	lo, mid, hi := pubs[0], pubs[1], pubs[2]
	own := []ownNodeView{
		{id: "node-a1", addr: "a1", peers: []collector.ObservedPeer{{Pubkey: hi}, {Pubkey: lo}}},
		{id: "node-a2", addr: "a2", peers: []collector.ObservedPeer{{Pubkey: hi}, {Pubkey: mid}}},
	}
	got := chainNodes(nil, own, nil, nil)
	if len(got) != 5 || got[2].Address != addr(hi) || got[3].Address != addr(lo) || got[4].Address != addr(mid) {
		t.Fatalf("應依連線數再依地址排:%s, %s, %s;得 %+v", addr(hi), addr(lo), addr(mid), got)
	}
}
