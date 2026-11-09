package server

import (
	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/auth"
	"besu-manager/backend/internal/store"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func accessTestServer(t *testing.T) (*Server, *pgxpool.Pool) {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("isolated TEST_DATABASE_URL unavailable")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || !strings.Contains(u.Path, "test") {
		t.Fatal("expected local test database")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("access_test_%s", strings.ToLower(rand.Text()))
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`); admin.Close() })
	st := store.New(pool)
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return &Server{store: st}, pool
}

// RPC users are named permission groups shared across the organization: duplicate name gives 409;
// issuing a token writes a record; nonexistent user gives 404.
func TestRpcUserTokenIssue(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	signer, err := loadOrCreateRpcSigner(ctx, s.store, bytesRepeat(32))
	if err != nil {
		t.Fatal(err)
	}
	s.rpcSigner = signer
	body := api.CreateRpcUserJSONRequestBody{Username: "alice", Permissions: []string{"eth:*"}}
	result, err := s.CreateRpcUser(ctx, api.CreateRpcUserRequestObject{Body: &body})
	if err != nil {
		t.Fatal(err)
	}
	created, ok := result.(api.CreateRpcUser201JSONResponse)
	if !ok || created.Username != "alice" || created.Id == "" {
		t.Fatalf("建立回應不對: %#v", result)
	}
	if r, _ := s.CreateRpcUser(ctx, api.CreateRpcUserRequestObject{Body: &body}); r == nil {
		t.Fatal("同名應回 409")
	} else if _, ok := r.(api.CreateRpcUser409JSONResponse); !ok {
		t.Fatalf("同名應回 409,得 %#v", r)
	}

	ttl := api.RpcTokenTtl1h
	issued, err := s.IssueRpcUserToken(ctx, api.IssueRpcUserTokenRequestObject{Id: created.Id, Body: &api.IssueRpcUserTokenJSONRequestBody{Ttl: &ttl}})
	if err != nil {
		t.Fatal(err)
	}
	tok, ok := issued.(api.IssueRpcUserToken200JSONResponse)
	if !ok || tok.Token == "" || len(tok.Permissions) != 1 || time.Until(tok.ExpiresAt) < 50*time.Minute {
		t.Fatalf("簽發回應不對: %#v", issued)
	}
	evs, err := s.ListRpcUserEvents(ctx, api.ListRpcUserEventsRequestObject{Id: created.Id})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := evs.(api.ListRpcUserEvents200JSONResponse)
	if !ok || len(got) != 2 || got[0].Kind != api.RpcUserEventTokenIssued || got[0].ExpiresAt == nil || !got[0].ExpiresAt.Equal(tok.ExpiresAt) || got[1].Kind != api.RpcUserEventCreated {
		t.Fatalf("紀錄應為 簽發、建立 兩筆: %#v", evs)
	}

	if r, _ := s.IssueRpcUserToken(ctx, api.IssueRpcUserTokenRequestObject{Id: "nobody", Body: &api.IssueRpcUserTokenJSONRequestBody{Ttl: &ttl}}); r == nil {
		t.Fatal("不存在的使用者應回 404")
	} else if _, ok := r.(api.IssueRpcUserToken404JSONResponse); !ok {
		t.Fatalf("不存在的使用者應回 404,得 %#v", r)
	}
	if r, _ := s.ListRpcUserEvents(ctx, api.ListRpcUserEventsRequestObject{Id: "nobody"}); r == nil {
		t.Fatal("不存在的使用者紀錄應回 404")
	} else if _, ok := r.(api.ListRpcUserEvents404JSONResponse); !ok {
		t.Fatalf("不存在的使用者紀錄應回 404,得 %#v", r)
	}
}

// Label and purpose: response, record and audit carry the same label and purpose; empty or
// all-whitespace gives null (the key is present in the JSON); more than 64 characters gives 400 and
// nothing is issued or recorded.
func TestRpcTokenIDAndLabel(t *testing.T) {
	s, _ := accessTestServer(t)
	audited := recordAudit(t)
	ctx := context.Background()
	signer, err := loadOrCreateRpcSigner(ctx, s.store, bytesRepeat(32))
	if err != nil {
		t.Fatal(err)
	}
	s.rpcSigner = signer
	r, err := s.CreateRpcUser(ctx, api.CreateRpcUserRequestObject{Body: &api.CreateRpcUserJSONRequestBody{Username: "bob", Permissions: []string{"eth:*"}}})
	if err != nil {
		t.Fatal(err)
	}
	id := r.(api.CreateRpcUser201JSONResponse).Id
	ttl := api.RpcTokenTtl1h
	issue := func(label *string) api.IssueRpcUserTokenResponseObject {
		t.Helper()
		r, err := s.IssueRpcUserToken(ctx, api.IssueRpcUserTokenRequestObject{Id: id, Body: &api.IssueRpcUserTokenJSONRequestBody{Ttl: &ttl, Label: label}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	padded, blank, long := "  交給 A 廠商 ", "   ", strings.Repeat("字", 65)
	with, ok := issue(&padded).(api.IssueRpcUserToken200JSONResponse)
	if !ok || with.Label == nil || *with.Label != "交給 A 廠商" || !strings.HasPrefix(with.TokenId, "tk_") {
		t.Fatalf("帶用途的回應不對: %#v", with)
	}
	without, ok := issue(&blank).(api.IssueRpcUserToken200JSONResponse)
	if !ok || without.Label != nil || without.TokenId == with.TokenId {
		t.Fatalf("全空白應當沒填: %#v", without)
	}
	if b, _ := json.Marshal(without); !strings.Contains(string(b), `"label":null`) {
		t.Fatalf("沒填時 JSON 要有 label:null: %s", b)
	}
	if _, ok := issue(&long).(api.IssueRpcUserToken400JSONResponse); !ok {
		t.Fatal("65 字應 400")
	}
	if _, ok := issue(ptr(strings.Repeat("字", 64))).(api.IssueRpcUserToken200JSONResponse); !ok {
		t.Fatal("64 個中文字應可以")
	}
	evs, err := s.ListRpcUserEvents(ctx, api.ListRpcUserEventsRequestObject{Id: id})
	if err != nil {
		t.Fatal(err)
	}
	got := evs.(api.ListRpcUserEvents200JSONResponse)
	if len(got) != 4 { // three issued (the 65-character one is not recorded) + creation
		t.Fatalf("紀錄筆數 %d: %#v", len(got), got)
	}
	if e := got[2]; e.TokenId == nil || *e.TokenId != with.TokenId || e.Label == nil || *e.Label != "交給 A 廠商" {
		t.Fatalf("帶用途那張的紀錄不對: %#v", e)
	}
	if e := got[1]; e.TokenId == nil || *e.TokenId != without.TokenId || e.Label != nil {
		t.Fatalf("沒填用途那張的紀錄不對: %#v", e)
	}
	if e := got[3]; e.Kind != api.RpcUserEventCreated || e.TokenId != nil || e.Label != nil {
		t.Fatalf("建立紀錄不該有代號與用途: %#v", e)
	}
	a := audited("rpc_token_issue")
	if len(a) != 3 || a[0]["jti"] != with.TokenId || *(a[0]["purpose"].(*string)) != "交給 A 廠商" || a[1]["purpose"].(*string) != nil {
		t.Fatalf("稽核不對: %v", a)
	}
}

// Validity: one of five enum values or an explicit expiry time (later than now, at most 1 year
// out); exactly one of the two must be given.
func TestRpcTokenExpiry(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	signer, err := loadOrCreateRpcSigner(ctx, s.store, bytesRepeat(32))
	if err != nil {
		t.Fatal(err)
	}
	s.rpcSigner = signer
	if err := s.store.CreateRpcUser(ctx, store.RpcUser{ID: "u1", Username: "alice", Permissions: []string{"eth:*"}}, ""); err != nil {
		t.Fatal(err)
	}
	ttl, bad := api.RpcTokenTtl1h, api.RpcTokenRequestTtl("forever")
	at := func(d time.Duration) *time.Time { v := time.Now().Add(d); return &v }
	cases := []struct {
		name string
		body api.RpcTokenRequest
		ok   bool
	}{
		{"ttl", api.RpcTokenRequest{Ttl: &ttl}, true},
		{"自訂到期", api.RpcTokenRequest{ExpiresAt: at(90 * 24 * time.Hour)}, true},
		{"自訂到期超過 1 年", api.RpcTokenRequest{ExpiresAt: at(366 * 24 * time.Hour)}, false},
		{"自訂到期已過", api.RpcTokenRequest{ExpiresAt: at(-time.Minute)}, false},
		{"兩個都給", api.RpcTokenRequest{Ttl: &ttl, ExpiresAt: at(time.Hour)}, false},
		{"都沒給", api.RpcTokenRequest{}, false},
		{"ttl 不合法", api.RpcTokenRequest{Ttl: &bad}, false},
	}
	for _, c := range cases {
		body := c.body
		r, err := s.IssueRpcUserToken(ctx, api.IssueRpcUserTokenRequestObject{Id: "u1", Body: &body})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if _, got := r.(api.IssueRpcUserToken200JSONResponse); got != c.ok {
			t.Errorf("%s: 預期成功=%v,得 %#v", c.name, c.ok, r)
		}
		if _, is400 := r.(api.IssueRpcUserToken400JSONResponse); !c.ok && !is400 {
			t.Errorf("%s: 應回 400,得 %#v", c.name, r)
		}
	}
	want := at(30 * 24 * time.Hour)
	issued, err := s.IssueRpcUserToken(ctx, api.IssueRpcUserTokenRequestObject{Id: "u1", Body: &api.RpcTokenRequest{ExpiresAt: want}})
	if err != nil {
		t.Fatal(err)
	}
	if tok, ok := issued.(api.IssueRpcUserToken200JSONResponse); !ok || tok.ExpiresAt.Sub(*want).Abs() > 2*time.Second {
		t.Fatalf("自訂到期沒照指定時間: %#v", issued)
	}
}

// loadOrCreateRpcSigner wraps the db backend into an RpcSigner, as the server does at startup when the backend is db.
func loadOrCreateRpcSigner(ctx context.Context, st auth.RpcSigningKeyStore, master []byte) (*auth.RpcSigner, error) {
	key, err := auth.LoadOrCreateRpcKey(ctx, st, master)
	if err != nil {
		return nil, err
	}
	return auth.NewRpcSigner(key)
}

func bytesRepeat(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func TestRpcUserDelete(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	if err := s.store.CreateRpcUser(ctx, store.RpcUser{ID: "u1", Username: "alice", Permissions: []string{"eth:*"}}, ""); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.DeleteRpcUser(ctx, api.DeleteRpcUserRequestObject{Id: "u1"}); r == nil {
		t.Fatal("delete 回 nil")
	} else if _, ok := r.(api.DeleteRpcUser204Response); !ok {
		t.Fatalf("delete 應回 204,得 %#v", r)
	}
	if r, _ := s.DeleteRpcUser(ctx, api.DeleteRpcUserRequestObject{Id: "u1"}); r == nil {
		t.Fatal("delete 回 nil")
	} else if _, ok := r.(api.DeleteRpcUser404JSONResponse); !ok {
		t.Fatalf("重複刪除應回 404,得 %#v", r)
	}
}
