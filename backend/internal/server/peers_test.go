package server

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/big"
	"testing"
	"time"

	"besu-manager/backend/internal/enode"
	"besu-manager/backend/internal/store"
)

// secpP is secp256k1's field prime; mkPeer needs it to put the test keys on the curve.
var secpP, _ = new(big.Int).SetString("fffffffffffffffffffffffffffffffffffffffffffffffffffffffefffffc2f", 16)

// mkPeer builds a valid enode and the address derived from it. The public key is a real secp256k1 point
// derived from seed: x starts at sha256(seed) and moves up until x³ + 7 has a square root y.
func mkPeer(t *testing.T, seed string, hostPort string) (rawEnode, addr string) {
	t.Helper()
	h := sha256.Sum256([]byte(seed))
	x := new(big.Int).SetBytes(h[:])
	var y *big.Int
	for {
		rhs := new(big.Int).Mul(x, x)
		rhs.Mul(rhs, x).Add(rhs, big.NewInt(7)).Mod(rhs, secpP)
		if y = new(big.Int).ModSqrt(rhs, secpP); y != nil {
			break
		}
		x.Add(x, big.NewInt(1))
	}
	pub := fmt.Sprintf("%064x%064x", x, y)
	rawEnode = "enode://" + pub + "@" + hostPort
	addr, err := enode.Address(rawEnode)
	if err != nil {
		t.Fatalf("造測試 enode 失敗: %v", err)
	}
	return rawEnode, addr
}

// The shared list is sent when the toggle is on; self-added ones are sent regardless of the toggle;
// both exclude the node itself and quarantined nodes of ours; the same pubkey is sent only once, with
// the shared-list entry taking precedence. Our own nodes are never added to any node's targets
// automatically; they are sent only if the user lists them (giving the address).
func TestPushTargets(t *testing.T) {
	enA1, _ := mkPeer(t, "a1", "10.0.0.1:30303")
	enA2, _ := mkPeer(t, "a2", "10.0.0.2:30303")
	enA3, _ := mkPeer(t, "a3", "10.0.0.3:30303") // quarantined
	enB1, _ := mkPeer(t, "b1", "10.0.1.1:30303")
	enC1, _ := mkPeer(t, "c1", "10.0.2.1:30303")
	pubA1, _ := enode.PubKeyHex(enA1)
	pubA2, _ := enode.PubKeyHex(enA2)
	pubA3, _ := enode.PubKeyHex(enA3)
	pubB1, _ := enode.PubKeyHex(enB1)
	pubC1, _ := enode.PubKeyHex(enC1)
	nodes := []store.Node{
		{ID: "node-a1", ReportedEnode: &enA1, PeerShared: true},
		{ID: "node-a2", ReportedEnode: &enA2},
		{ID: "node-a3", ReportedEnode: &enA3, PeerShared: true, QuarantinedAt: ptr(time.Now())},
		{ID: "node-a4"},
	}
	innerA2 := "enode://" + pubA2 + "@192.168.1.2:30303" // one of our nodes entered by the user, with the address given
	manual := []store.ManualPeer{
		{Pubkey: pubB1, Enode: enB1},
		{Pubkey: pubA1, Enode: enA1}, // itself: not sent to a1
		{Pubkey: pubA2, Enode: innerA2},
		{Pubkey: pubA3, Enode: enA3}, // quarantined: not sent
	}
	custom := []store.NodePeer{
		{NodeID: "node-a1", Pubkey: pubC1, Enode: enC1},
		{NodeID: "node-a1", Pubkey: pubB1, Enode: "enode://" + pubB1 + "@172.16.0.1:30303"}, // also in the shared list: the shared entry wins
		{NodeID: "node-a2", Pubkey: pubB1, Enode: enB1},
		{NodeID: "node-a2", Pubkey: pubA2, Enode: enA2}, // itself: not sent
		{NodeID: "node-a2", Pubkey: pubA3, Enode: enA3}, // quarantined: not sent
	}
	got := pushTargets(nodes, manual, custom)
	if a1 := got["node-a1"]; len(a1) != 3 || !contains(a1, enB1) || !contains(a1, innerA2) || !contains(a1, enC1) {
		t.Fatalf("node-a1(開 + 自行加入) %v", a1)
	}
	if a2 := got["node-a2"]; len(a2) != 1 || a2[0] != enB1 {
		t.Fatalf("node-a2(關,只送自行加入) %v", a2)
	}
	if a4 := got["node-a4"]; a4 == nil || len(a4) != 0 {
		t.Fatalf("關、沒有自行加入:不送 %v", a4)
	}
	// No list at all: nothing is sent to anyone, and our own nodes are not pushed automatically
	for id, want := range pushTargets(nodes, nil, nil) {
		if len(want) != 0 {
			t.Fatalf("%s 沒有清單卻送了 %v", id, want)
		}
	}
}

// One push round reaches every node: a node that pushed successfully does not end the round for the
// nodes after it.
func TestPushPeersReachesEveryNode(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	enB1, _ := mkPeer(t, "b1", "10.0.1.1:30303")
	if _, _, err := s.store.AddManualPeer(ctx, store.ManualPeer{Pubkey: pubOfEnode(enB1), Enode: enB1, CreatedAt: time.Now()}, 10); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"node-a1", "node-a2"} {
		if err := s.store.UpsertOwnNode(ctx, id, "org-test", 8545, 9545, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := s.store.SetPeerShared(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	s.peersMissing = map[string]string{}
	ops := &peerOps{}
	s.ops = ops
	if err := s.pushPeers(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ops.calls) != 2 {
		t.Fatalf("兩台都該收到推送,得 %d 次 %+v", len(ops.calls), ops.calls)
	}
}
