package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// The switch defaults to off and toggling it leaves the self-added list alone; one entry per public key,
// rejected at the cap; deleting a node removes its own list and also entries in other lists whose public key
// belongs to it (the shared list is user-entered and left alone). The sent list writes and reads back.
func TestNodePeers(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	pubA2, pubB1 := strings.Repeat("a2", 64), strings.Repeat("b1", 64)
	for _, q := range []string{
		`INSERT INTO nodes (id, org) VALUES ('node-a1', 'org-A')`,
		`INSERT INTO nodes (id, org, reported_enode) VALUES ('node-a2', 'org-A', 'enode://` + pubA2 + `@10.0.0.2:30303')`,
		`INSERT INTO nodes (id, org) VALUES ('node-a3', 'org-A')`,
	} {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := s.GetNode(ctx, "node-a1"); n == nil || n.PeerShared || n.PushedPeers == nil || len(n.PushedPeers) != 0 {
		t.Fatalf("預設應為不套用、沒送過: %+v", n)
	}
	now := time.Now()
	a2 := NodePeer{NodeID: "node-a1", Pubkey: pubA2, Enode: "enode://" + pubA2 + "@10.0.0.2:30303", CreatedAt: now}
	if added, _, err := s.AddNodePeer(ctx, a2, 10); err != nil || !added {
		t.Fatal(added, err)
	}
	for _, on := range []bool{true, false} {
		if ok, err := s.SetPeerShared(ctx, "node-a1", on); err != nil || !ok {
			t.Fatal(ok, err)
		}
		if n, _ := s.GetNode(ctx, "node-a1"); n == nil || n.PeerShared != on {
			t.Fatalf("開關沒寫入 %v: %+v", on, n)
		}
		if list, _ := s.ListNodePeers(ctx, "node-a1"); len(list) != 1 || list[0].Pubkey != pubA2 {
			t.Fatalf("切開關不該動自行加入的 %+v", list)
		}
	}
	if ok, _ := s.SetPeerShared(ctx, "node-x", true); ok {
		t.Fatal("不存在的節點")
	}
	b1 := NodePeer{NodeID: "node-a1", Pubkey: pubB1, Enode: "enode://" + pubB1 + "@10.0.1.1:30303", CreatedAt: now}
	if added, full, err := s.AddNodePeer(ctx, b1, 10); err != nil || !added || full {
		t.Fatal(added, full, err)
	}
	if added, full, err := s.AddNodePeer(ctx, b1, 10); err != nil || added || full {
		t.Fatalf("dup: %v %v %v", added, full, err)
	}
	if added, full, _ := s.AddNodePeer(ctx, NodePeer{NodeID: "node-a1", Pubkey: strings.Repeat("cc", 64), Enode: "x", CreatedAt: now}, 2); added || !full {
		t.Fatal("limit")
	}
	sent := []string{"enode://" + pubA2 + "@10.0.0.2:30303"}
	if err := s.SetPushedPeers(ctx, "node-a1", sent); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.GetNode(ctx, "node-a1"); n == nil || !slices.Equal(n.PushedPeers, sent) {
		t.Fatalf("pushed_peers 讀回 %+v", n)
	}
	// node-a3 also added node-a2 itself, and the shared list has node-a2 too; after deleting node-a2 the
	// self-added list no longer has it, but the shared list keeps it
	if _, _, err := s.AddNodePeer(ctx, NodePeer{NodeID: "node-a3", Pubkey: pubA2, Enode: a2.Enode, CreatedAt: now}, 10); err != nil {
		t.Fatal(err)
	}
	if added, _, err := s.AddManualPeer(ctx, ManualPeer{Pubkey: pubA2, Enode: "enode://" + pubA2 + "@1.2.3.4:30303", CreatedAt: now}, 10); err != nil || !added {
		t.Fatal(added, err)
	}
	if err := s.DeleteNode(ctx, "node-a2", nil); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListNodePeers(ctx, ""); len(list) != 1 || list[0].Pubkey != pubB1 {
		t.Fatalf("刪節點後應只剩 node-a1 的 b1: %+v", list)
	}
	if list, _ := s.ListManualPeers(ctx); len(list) != 1 {
		t.Fatalf("共用清單不該被刪 %+v", list)
	}
	if ok, _ := s.DeleteNodePeer(ctx, "node-a1", pubA2); ok {
		t.Fatal("已被清掉的條目不該再刪得到")
	}
}
