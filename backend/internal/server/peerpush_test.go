package server

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/rpcops"
	"besu-manager/backend/internal/store"
)

type syncCall struct{ want, drop []string }

// peerOps records only SyncPeers calls; down = node offline (the whole call fails).
type peerOps struct {
	NodeOps
	calls        []syncCall
	down         bool
	rejectRemove bool // node rejects admin_removePeer
}

func (o *peerOps) SyncPeers(_ context.Context, _ string, want, drop []string) (rpcops.PeerSync, error) {
	if o.down {
		return rpcops.PeerSync{}, errors.New("agent 未連線")
	}
	o.calls = append(o.calls, syncCall{want, drop})
	if o.rejectRemove {
		return rpcops.PeerSync{Unremoved: drop}, nil
	}
	return rpcops.PeerSync{}, nil
}

// The sent list is stored in the DB: after a platform restart (a new Server, memory empty) deleting
// an entry still removes it in the next round; a round where the node is offline leaves the record
// unchanged, and rejected removals wait for the next round; turning the toggle off removes
// everything the platform sent, and no RPC is made after that.
func TestPushPeersRemembersAcrossRestart(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	enB1, _ := mkPeer(t, "b1", "10.0.1.1:30303")
	enB2, _ := mkPeer(t, "b2", "10.0.1.2:30303")
	if err := s.store.UpsertOwnNode(ctx, "node-a1", "org-test", 8545, 9545, nil); err != nil {
		t.Fatal(err)
	}
	for _, en := range []string{enB1, enB2} {
		if _, _, err := s.store.AddManualPeer(ctx, store.ManualPeer{Pubkey: pubOfEnode(en), Enode: en, CreatedAt: time.Now()}, 10); err != nil {
			t.Fatal(err)
		}
	}
	round := func(srv *Server) []syncCall {
		t.Helper()
		ops := &peerOps{}
		srv.ops = ops
		if err := srv.pushPeers(ctx); err != nil {
			t.Fatal(err)
		}
		return ops.calls
	}
	fresh := func() *Server { return &Server{store: s.store, peersMissing: map[string]string{}} }

	if c := round(fresh()); len(c) != 0 {
		t.Fatalf("預設不套用:不該打 RPC %+v", c)
	}
	if _, err := s.store.SetPeerShared(ctx, "node-a1", true); err != nil {
		t.Fatal(err)
	}
	if c := round(fresh()); len(c) != 1 || len(c[0].want) != 2 || len(c[0].drop) != 0 {
		t.Fatalf("打開第一輪 %+v", c)
	}
	// b2 is deleted only after the platform restart
	if _, err := s.store.DeleteManualPeer(ctx, pubOfEnode(enB2)); err != nil {
		t.Fatal(err)
	}
	// Node offline: the record is unchanged, and the next round still knows to remove b2
	down := fresh()
	down.ops = &peerOps{down: true}
	if err := down.pushPeers(ctx); err != nil {
		t.Fatal(err)
	}
	if c := round(fresh()); len(c) != 1 || !slices.Equal(c[0].want, []string{enB1}) || !slices.Equal(c[0].drop, []string{enB2}) {
		t.Fatalf("重啟後刪除 %+v", c)
	}
	if _, err := s.store.SetPeerShared(ctx, "node-a1", false); err != nil {
		t.Fatal(err)
	}
	// Node rejects the removal: the record stays and removal is retried next round
	rej := fresh()
	rej.ops = &peerOps{rejectRemove: true}
	if err := rej.pushPeers(ctx); err != nil {
		t.Fatal(err)
	}
	if c := round(fresh()); len(c) != 1 || len(c[0].want) != 0 || !slices.Equal(c[0].drop, []string{enB1}) {
		t.Fatalf("關掉 %+v", c)
	}
	if c := round(fresh()); len(c) != 0 {
		t.Fatalf("關著且送過的都移除了:不該再打 RPC %+v", c)
	}
}

// Toggling the toggle: save first, then push to this node immediately (a failed push still returns
// 200, leaves the record unchanged and is pushed next round); self-added entries are untouched.
// While on, self-adding a pubkey already in the shared list returns 409; while off it can be added.
func TestSetNodePeerShared(t *testing.T) {
	s, _ := accessTestServer(t)
	audited := recordAudit(t)
	ctx := asAdmin(context.Background())
	if err := s.store.UpsertOwnNode(ctx, "node-a1", "org-test", 8545, 9545, nil); err != nil {
		t.Fatal(err)
	}
	s.col = collector.New(s.store, downRequester{}, allowRPC{})
	s.peersMissing = map[string]string{}
	enB1, _ := mkPeer(t, "b1", "10.0.1.1:30303")
	enC1, _ := mkPeer(t, "c1", "10.0.2.1:30303")
	if _, _, err := s.store.AddManualPeer(ctx, store.ManualPeer{Pubkey: pubOfEnode(enB1), Enode: enB1, CreatedAt: time.Now()}, 10); err != nil {
		t.Fatal(err)
	}
	add := func(en string) api.CreateNodeCustomPeerResponseObject {
		r, err := s.CreateNodeCustomPeer(ctx, api.CreateNodeCustomPeerRequestObject{Id: "node-a1", Body: &api.ManualPeerCreateRequest{Enode: en}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	set := func(on bool) api.NodeCustomPeers {
		r, err := s.SetNodePeerShared(ctx, api.SetNodePeerSharedRequestObject{Id: "node-a1", Body: &api.PeerSharedRequest{UseShared: on}})
		if err != nil {
			t.Fatal(err)
		}
		v, ok := r.(api.SetNodePeerShared200JSONResponse)
		if !ok {
			t.Fatalf("%#v", r)
		}
		return api.NodeCustomPeers(v)
	}
	if _, ok := add(enC1).(api.CreateNodeCustomPeer201JSONResponse); !ok {
		t.Fatal("關著可以自行加入")
	}
	ops := &peerOps{}
	s.ops = ops
	if v := set(true); !v.UseShared || len(v.Peers) != 1 {
		t.Fatalf("打開 %+v", v)
	}
	if len(ops.calls) != 1 || !slices.Equal(ops.calls[0].want, []string{enB1, enC1}) {
		t.Fatalf("打開應立即推共用 + 自行加入 %+v", ops.calls)
	}
	if r, ok := add(enB1).(api.CreateNodeCustomPeer409JSONResponse); !ok {
		t.Fatalf("開著加共用清單裡的應 409,得 %#v", r)
	}
	// Push failed: still saved and returns 200, the sent record is unchanged
	s.ops = &peerOps{down: true}
	if v := set(false); v.UseShared || len(v.Peers) != 1 {
		t.Fatalf("關掉 %+v", v)
	}
	if n, _ := s.store.GetNode(ctx, "node-a1"); n.PeerShared || !slices.Equal(n.PushedPeers, []string{enB1, enC1}) {
		t.Fatalf("推不成功應已存開關、記錄不動 %+v", n)
	}
	ops = &peerOps{}
	s.ops = ops
	if err := s.pushPeers(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ops.calls) != 1 || !slices.Equal(ops.calls[0].want, []string{enC1}) || !slices.Equal(ops.calls[0].drop, []string{enB1}) {
		t.Fatalf("下一輪補上移除 %+v", ops.calls)
	}
	if _, ok := add(enB1).(api.CreateNodeCustomPeer201JSONResponse); !ok {
		t.Fatal("關著可以加共用清單裡也有的")
	}
	if d := audited("node_peer_shared_set"); len(d) != 2 || d[0]["use_shared"] != true || d[1]["use_shared"] != false {
		t.Fatalf("稽核 %+v", d)
	}
	if r, _ := s.SetNodePeerShared(ctx, api.SetNodePeerSharedRequestObject{Id: "node-x", Body: &api.PeerSharedRequest{}}); r == nil {
		t.Fatal("不存在的節點")
	} else if _, ok := r.(api.SetNodePeerShared404JSONResponse); !ok {
		t.Fatalf("不存在的節點應 404,得 %#v", r)
	}
}
