package server

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/enode"
	"besu-manager/backend/internal/store"
)

// Custom peers / manual peers that are rejected return reason and params with an English message;
// the audit stores reason and reason_params.
func TestPeerErrorReasons(t *testing.T) {
	s, _ := accessTestServer(t)
	audited := recordAudit(t)
	ctx := asAdmin(context.Background())
	if err := s.store.UpsertOwnNode(ctx, "node-a1", "org-test", 8545, 9545, nil); err != nil {
		t.Fatal(err)
	}
	s.col = collector.New(s.store, downRequester{}, allowRPC{})
	s.peersMissing = map[string]string{}
	s.ops = &peerOps{}
	enB1, addrB1 := mkPeer(t, "b1", "10.0.1.1:30303")
	add := func(en string) api.CreateNodeCustomPeerResponseObject {
		r, err := s.CreateNodeCustomPeer(ctx, api.CreateNodeCustomPeerRequestObject{Id: "node-a1", Body: &api.ManualPeerCreateRequest{Enode: en}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	reasonOf := func(r any) (api.Error, bool) {
		switch v := r.(type) {
		case api.CreateNodeCustomPeer400JSONResponse:
			return api.Error(v), true
		case api.CreateNodeCustomPeer409JSONResponse:
			return api.Error(v), true
		case api.CreateManualPeer400JSONResponse:
			return api.Error(v), true
		case api.CreateManualPeer409JSONResponse:
			return api.Error(v), true
		}
		return api.Error{}, false
	}
	expect := func(r any, want api.ErrorReason, params map[string]string) {
		t.Helper()
		e, ok := reasonOf(r)
		if !ok || e.Reason == nil || *e.Reason != want {
			t.Fatalf("要 %s,得 %#v", want, r)
		}
		for k, v := range params {
			if e.Params == nil || (*e.Params)[k] != v {
				t.Fatalf("%s params[%s] 要 %q,得 %#v", want, k, v, e.Params)
			}
		}
		if strings.ContainsFunc(e.Message, func(r rune) bool { return r > 0x2e80 }) {
			t.Fatalf("%s message 不該有中文:%q", want, e.Message)
		}
	}

	expect(add("enode://xyz@10.0.0.1:30303"), api.ErrorReasonEnodePubkeyLength, map[string]string{"got": "3"})
	expect(add(strings.Replace(enB1, "enode://", "", 1)), api.ErrorReasonEnodeScheme, nil)
	offCurve := "enode://" + strings.Repeat("ab", 64) + "@10.0.0.1:30303"
	expect(add(offCurve), api.ErrorReasonEnodePubkeyInvalid, map[string]string{"value": strconv.Quote(offCurve)})
	expect(add(enB1+"?foo=1"), api.ErrorReasonEnodeDiscport, map[string]string{"value": `"foo=1"`})
	self, selfAddr := mkPeer(t, "a1", "10.0.0.1:30303")
	if err := s.store.SetNodeIdentity(ctx, "node-a1", selfAddr, self); err != nil {
		t.Fatal(err)
	}
	expect(add(self), api.ErrorReasonPeerIsSelf, map[string]string{"nodeId": "node-a1"})
	enD1, _ := mkPeer(t, "d1", "10.0.3.1:30303")
	if _, _, err := s.store.AddManualPeer(ctx, store.ManualPeer{Pubkey: pubOfEnode(enD1), Enode: enD1, CreatedAt: time.Now()}, manualPeerLimit); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetNodePeerShared(ctx, api.SetNodePeerSharedRequestObject{Id: "node-a1", Body: &api.PeerSharedRequest{UseShared: true}}); err != nil {
		t.Fatal(err)
	}
	expect(add(enD1), api.ErrorReasonPeerInSharedList, nil)
	if _, ok := add(enB1).(api.CreateNodeCustomPeer201JSONResponse); !ok {
		t.Fatal("第一次加應成功")
	}
	expect(add(enB1), api.ErrorReasonPeerExists, nil)

	// Fill up to the cap
	for i := 0; i < manualPeerLimit; i++ {
		pub := strings.Repeat(fmt.Sprintf("%04x", 0x1000+i), 32)
		if _, _, err := s.store.AddNodePeer(ctx, store.NodePeer{NodeID: "node-a1", Pubkey: pub, Enode: "enode://" + pub + "@10.9.0.1:30303", CreatedAt: time.Now()}, manualPeerLimit+1); err != nil {
			t.Fatal(err)
		}
	}
	enC1, _ := mkPeer(t, "c1", "10.0.2.1:30303")
	limit := fmt.Sprint(manualPeerLimit)
	expect(add(enC1), api.ErrorReasonPeerListFull, map[string]string{"limit": limit})

	if r, _ := s.DeleteNodeCustomPeer(ctx, api.DeleteNodeCustomPeerRequestObject{Id: "node-a1", Pubkey: pubOfEnode(enC1)}); r == nil {
		t.Fatal("刪除不存在的")
	} else if v, ok := r.(api.DeleteNodeCustomPeer404JSONResponse); !ok || v.Reason == nil || *v.Reason != api.ErrorReasonPeerNotFound {
		t.Fatalf("刪除不存在的應 peer_not_found,得 %#v", r)
	}
	if r, _ := s.DeleteNodeCustomPeer(ctx, api.DeleteNodeCustomPeerRequestObject{Id: "node-x", Pubkey: pubOfEnode(enC1)}); r == nil {
		t.Fatal("不存在的節點")
	} else if v, ok := r.(api.DeleteNodeCustomPeer404JSONResponse); !ok || v.Reason == nil || *v.Reason != api.ErrorReasonNodeNotFound {
		t.Fatalf("不存在的節點應 node_not_found,得 %#v", r)
	}

	failed := func(event string) []map[string]any {
		var out []map[string]any
		for _, d := range audited(event) {
			if d["result"] == "failed" {
				out = append(out, d)
			}
		}
		return out
	}
	d := failed("node_peer_added")
	if len(d) != 2 || d[0]["reason"] != string(api.ErrorReasonPeerExists) || d[0]["reason_params"] != nil {
		t.Fatalf("稽核 node_peer_added %+v", d)
	}
	if p, _ := d[1]["reason_params"].(map[string]string); d[1]["reason"] != string(api.ErrorReasonPeerListFull) || p["limit"] != limit {
		t.Fatalf("稽核清單已滿要帶 limit %+v", d[1])
	}
	if d := failed("node_peer_deleted"); len(d) != 1 || d[0]["reason"] != string(api.ErrorReasonPeerNotFound) {
		t.Fatalf("稽核 node_peer_deleted %+v", d)
	}

	// Manual peers (shared list)
	addManual := func(en string) api.CreateManualPeerResponseObject {
		r, err := s.CreateManualPeer(ctx, api.CreateManualPeerRequestObject{Body: &api.ManualPeerCreateRequest{Enode: en}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	expect(addManual("enode://"+pubOfEnode(enB1)+"@example.com:30303"), api.ErrorReasonEnodeHostNotIp, nil)
	if r, ok := addManual(enB1).(api.CreateManualPeer201JSONResponse); !ok || enode.NormalizeAddress(r.Address) != enode.NormalizeAddress(addrB1) {
		t.Fatalf("第一次加手動 peer 應成功並帶地址(取名用): %#v", r)
	}
	expect(addManual(enB1), api.ErrorReasonManualPeerExists, nil)
	if r, _ := s.DeleteManualPeer(ctx, api.DeleteManualPeerRequestObject{Pubkey: pubOfEnode(enC1)}); r == nil {
		t.Fatal("刪除不存在的手動 peer")
	} else if v, ok := r.(api.DeleteManualPeer404JSONResponse); !ok || v.Reason == nil || *v.Reason != api.ErrorReasonManualPeerNotFound {
		t.Fatalf("應 manual_peer_not_found,得 %#v", r)
	}
	for i := 0; i < manualPeerLimit; i++ {
		pub := strings.Repeat(fmt.Sprintf("%04x", 0x2000+i), 32)
		if _, _, err := s.store.AddManualPeer(ctx, store.ManualPeer{Pubkey: pub, Enode: "enode://" + pub + "@10.9.0.2:30303", CreatedAt: time.Now()}, manualPeerLimit+10); err != nil {
			t.Fatal(err)
		}
	}
	expect(addManual(enC1), api.ErrorReasonManualPeerListFull, map[string]string{"limit": limit})
	if d := failed("manual_peer_added"); len(d) != 2 || d[1]["reason"] != string(api.ErrorReasonManualPeerListFull) {
		t.Fatalf("稽核手動清單已滿 %+v", d)
	}
	if d := failed("manual_peer_added"); len(d) != 2 || d[0]["reason"] != string(api.ErrorReasonManualPeerExists) {
		t.Fatalf("稽核 manual_peer_added %+v", d)
	}
}
