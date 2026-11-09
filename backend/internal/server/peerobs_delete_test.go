package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/store"
	"besu-manager/backend/internal/store/storetest"
)

// Deleting a peer name: the address is matched case-insensitively and audited in lowercase; an
// address that is not 0x + 40 hex is a 400 with no audit; when the audit cannot be written
// the name stays.
func TestDeletePeerName(t *testing.T) {
	s, _ := accessTestServer(t)
	audited := recordAudit(t)
	ctx := asAdmin(context.Background())
	_, b2 := pubOf(t, "b2")
	_, c3 := pubOf(t, "c3")
	for _, n := range []store.PeerName{{Address: b2, Name: "B 單位"}, {Address: c3, Name: "C 單位"}} {
		if err := s.store.SetPeerName(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	names := func() map[string]string {
		t.Helper()
		list, err := s.store.ListPeerNames(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, n := range list {
			out[n.Address] = n.Name
		}
		return out
	}
	del := func(c context.Context, addr string) (api.DeletePeerNameResponseObject, error) {
		return s.DeletePeerName(c, api.DeletePeerNameRequestObject{Address: addr})
	}

	if r, err := del(ctx, "not-an-address"); err != nil {
		t.Fatal(err)
	} else if e, ok := r.(api.DeletePeerName400JSONResponse); !ok || e.Reason == nil || *e.Reason != api.ErrorReasonFieldFormat || len(audited("peer_name_deleted")) != 0 || len(names()) != 2 {
		t.Fatalf("不合法的地址應 400 field_format、不稽核、不動資料: %#v", r)
	}

	if r, err := del(ctx, "0x"+strings.ToUpper(b2[2:])); err != nil {
		t.Fatal(err)
	} else if _, ok := r.(api.DeletePeerName204Response); !ok {
		t.Fatalf("%#v", r)
	}
	if got := names(); got[b2] != "" || got[c3] != "C 單位" {
		t.Fatalf("只刪 b2(大小寫不分): %v", got)
	}
	if e := audited("peer_name_deleted"); len(e) != 1 || e[0]["address"] != b2 || e[0]["actor"] != "root" {
		t.Fatalf("稽核記小寫地址與操作者: %v", e)
	}

	storetest.FailAuditForTest(t)
	if _, err := del(ctx, c3); !errors.Is(err, errAudit) {
		t.Fatalf("稽核失敗應回錯: %v", err)
	}
	if got := names(); got[c3] != "C 單位" {
		t.Fatal("稽核失敗不該刪")
	}
}
