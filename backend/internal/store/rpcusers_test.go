package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func rpcEventKinds(t *testing.T, s *Store, id string) []string {
	t.Helper()
	evs, err := s.ListRpcUserEvents(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	kinds := make([]string, len(evs))
	for i, e := range evs {
		kinds[i] = e.Kind
	}
	return kinds
}

// Users are shared across the org: duplicate names are blocked; creation records one event, issuance records
// the expiry and the permissions at the time; records are newest first.
func TestRpcUserLifecycle(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	if err := s.CreateRpcUser(ctx, RpcUser{ID: "u1", Username: "alice", Permissions: []string{"eth:*"}}, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRpcUser(ctx, RpcUser{ID: "u2", Username: "alice", Permissions: []string{"net:*"}}, "admin"); !errors.Is(err, ErrRpcUserExists) {
		t.Fatalf("同名應回 ErrRpcUserExists,得 %v", err)
	}
	if u, _ := s.GetRpcUser(ctx, "u2"); u != nil {
		t.Fatal("同名被擋下時不該留下使用者")
	}
	// Permissions are stored as JSONB and decoded on read; tokens are signed with what comes back here
	if u, err := s.GetRpcUser(ctx, "u1"); err != nil || u == nil || !slices.Equal(u.Permissions, []string{"eth:*"}) {
		t.Fatalf("GetRpcUser 權限不對: %+v err=%v", u, err)
	}
	if us, err := s.ListRpcUsers(ctx); err != nil || len(us) != 1 || !slices.Equal(us[0].Permissions, []string{"eth:*"}) {
		t.Fatalf("ListRpcUsers 權限不對: %+v err=%v", us, err)
	}

	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	label := "正式環境"
	if err := s.RecordRpcToken(ctx, "u1", []string{"eth:*", "net:peerCount"}, exp, "carol", "tk_000000000001", &label); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRpcToken(ctx, "nobody", []string{"eth:*"}, exp, "carol", "tk_000000000002", nil); err == nil {
		t.Fatal("不存在的使用者不該記到 token")
	}
	if err := s.RecordRpcToken(ctx, "u1", []string{"eth:*"}, exp, "carol", "tk_000000000001", nil); err == nil {
		t.Fatal("代號重複應寫不進去")
	}

	evs, err := s.ListRpcUserEvents(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if got := rpcEventKinds(t, s, "u1"); len(got) != 2 || got[0] != RpcEventTokenIssued || got[1] != RpcEventCreated {
		t.Fatalf("紀錄應為 簽發/建立 各一筆(新到舊),得 %v", got)
	}
	if e := evs[0]; e.Actor != "carol" || e.ExpiresAt == nil || !e.ExpiresAt.Equal(exp) || len(e.Permissions) != 2 ||
		e.TokenID == nil || *e.TokenID != "tk_000000000001" || e.Label == nil || *e.Label != label {
		t.Fatalf("簽發紀錄不對: %+v", e)
	}
	if e := evs[1]; e.Actor != "admin" || len(e.Permissions) != 1 || e.Permissions[0] != "eth:*" || e.TokenID != nil || e.Label != nil {
		t.Fatalf("建立紀錄不對: %+v", e)
	}
}

// Deleting a user deletes its records; recreating with the same name gets a new id and does not inherit old
// records.
func TestRpcUserDeleteDropsEvents(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	if err := s.CreateRpcUser(ctx, RpcUser{ID: "old", Username: "alice", Permissions: []string{"eth:*"}}, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRpcToken(ctx, "old", []string{"eth:*"}, time.Now().Add(time.Hour), "admin", "tk_00000000000a", nil); err != nil {
		t.Fatal(err)
	}
	if name, err := s.DeleteRpcUser(ctx, "old"); err != nil || name != "alice" {
		t.Fatalf("delete: %q %v", name, err)
	}
	if name, _ := s.DeleteRpcUser(ctx, "old"); name != "" {
		t.Fatal("重複刪除應回空字串")
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM rpc_user_events WHERE rpc_user_id = 'old'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("刪除使用者後紀錄應一起刪,剩 %d 筆 (%v)", n, err)
	}
	if err := s.CreateRpcUser(ctx, RpcUser{ID: "new", Username: "alice", Permissions: []string{"net:*"}}, "admin"); err != nil {
		t.Fatalf("刪除後同名應可重建: %v", err)
	}
	if got := rpcEventKinds(t, s, "new"); len(got) != 1 || got[0] != RpcEventCreated {
		t.Fatalf("重建的使用者只該有自己的建立紀錄,得 %v", got)
	}
}

// RPC users are not bound to nodes: deleting a node does not affect users.
func TestDeleteNodeKeepsRpcUsers(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO nodes(id,org) VALUES('node-x','test')`); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRpcUser(ctx, RpcUser{ID: "u1", Username: "alice", Permissions: []string{"eth:*"}}, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNode(ctx, "node-x", nil); err != nil {
		t.Fatal(err)
	}
	if u, err := s.GetRpcUser(ctx, "u1"); err != nil || u == nil {
		t.Fatalf("刪節點不該刪 RPC 使用者: %v %v", u, err)
	}
	if got := rpcEventKinds(t, s, "u1"); len(got) != 1 {
		t.Fatalf("刪節點不該動到紀錄,得 %v", got)
	}
}

// The signing key is a single row: written the first time, then ON CONFLICT DO NOTHING, so reading back
// always returns the first key.
func TestRpcSigningKeyWriteOnce(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	if k, pub, err := s.GetRpcSigningKey(ctx); err != nil || k != nil || pub != "" {
		t.Fatalf("空表應回 nil: %v %v %q", k, pub, err)
	}
	if err := s.SaveRpcSigningKey(ctx, []byte("enc-1"), "pem-1", "RS256"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRpcSigningKey(ctx, []byte("enc-2"), "pem-2", "RS256"); err != nil {
		t.Fatal(err)
	}
	k, pub, err := s.GetRpcSigningKey(ctx)
	if err != nil || string(k) != "enc-1" || pub != "pem-1" {
		t.Fatalf("第二次寫入不該蓋掉第一把: %q %q %v", k, pub, err)
	}
}
