package server

import (
	"context"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/store"
)

// Errors for RPC users, token signing and join-code revocation return a reason; duplicate-name and
// not-found-on-delete audits store the code.
func TestRpcUserAndJoinTokenReasons(t *testing.T) {
	s, _ := accessTestServer(t)
	audited := recordAudit(t)
	ctx := asAdmin(context.Background())
	create := func(name string, perms ...string) api.CreateRpcUserResponseObject {
		r, err := s.CreateRpcUser(ctx, api.CreateRpcUserRequestObject{Body: &api.CreateRpcUserJSONRequestBody{Username: name, Permissions: perms}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	want400 := func(r api.CreateRpcUserResponseObject, reason api.ErrorReason) {
		t.Helper()
		if v, ok := r.(api.CreateRpcUser400JSONResponse); !ok || !hasReason(api.Error(v), reason) {
			t.Fatalf("要 400 %s,得 %#v", reason, r)
		}
	}
	want400(create("bsos-x", "eth:*"), api.ErrorReasonRpcUsernameReserved)
	want400(create("", "eth:*"), api.ErrorReasonRpcUsernameReserved)
	want400(create("a b", "eth:*"), api.ErrorReasonRpcUsernameFormat)
	r := create("alice", "eth:nope")
	if v, ok := r.(api.CreateRpcUser400JSONResponse); !ok || !hasReason(api.Error(v), api.ErrorReasonRpcPermissionUnknownMethod) || (*v.Params)["value"] != `"eth:nope"` {
		t.Fatalf("目錄外方法要帶 value: %#v", r)
	}
	created, ok := create("alice", "eth:*").(api.CreateRpcUser201JSONResponse)
	if !ok {
		t.Fatal("建立失敗")
	}
	if v, ok := create("alice", "eth:*").(api.CreateRpcUser409JSONResponse); !ok || !hasReason(api.Error(v), api.ErrorReasonRpcUserExists) {
		t.Fatalf("同名要 409 rpc_user_exists")
	}
	if d := audited("rpc_user_create"); len(d) == 0 || d[len(d)-1]["reason"] != string(api.ErrorReasonRpcUserExists) {
		t.Fatalf("同名稽核 %+v", d)
	}

	issue := func(id string, b api.IssueRpcUserTokenJSONRequestBody) api.IssueRpcUserTokenResponseObject {
		r, err := s.IssueRpcUserToken(ctx, api.IssueRpcUserTokenRequestObject{Id: id, Body: &b})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	ttl, past, far := api.RpcTokenTtl1h, time.Now().Add(-time.Hour), time.Now().Add(400*24*time.Hour)
	for name, c := range map[string]struct {
		b    api.IssueRpcUserTokenJSONRequestBody
		want api.ErrorReason
	}{
		"兩個都帶": {api.IssueRpcUserTokenJSONRequestBody{Ttl: &ttl, ExpiresAt: &past}, api.ErrorReasonRpcTokenTtlXor},
		"都沒帶":  {api.IssueRpcUserTokenJSONRequestBody{}, api.ErrorReasonRpcTokenTtlXor},
		"過去":   {api.IssueRpcUserTokenJSONRequestBody{ExpiresAt: &past}, api.ErrorReasonRpcTokenExpiryPast},
		"超過一年": {api.IssueRpcUserTokenJSONRequestBody{ExpiresAt: &far}, api.ErrorReasonRpcTokenExpiryTooFar},
	} {
		if v, ok := issue(created.Id, c.b).(api.IssueRpcUserToken400JSONResponse); !ok || !hasReason(api.Error(v), c.want) {
			t.Fatalf("%s 要 400 %s", name, c.want)
		}
	}
	if v, ok := issue("nope", api.IssueRpcUserTokenJSONRequestBody{Ttl: &ttl}).(api.IssueRpcUserToken404JSONResponse); !ok || !hasReason(api.Error(v), api.ErrorReasonRpcUserNotFound) {
		t.Fatal("不存在的使用者要 404 rpc_user_not_found")
	}

	// join code: not found 404, revoking an already revoked one 409
	revoke := func(id string) api.RevokeJoinTokenResponseObject {
		r, err := s.RevokeJoinToken(ctx, api.RevokeJoinTokenRequestObject{Id: id})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if v, ok := revoke("nope").(api.RevokeJoinToken404JSONResponse); !ok || !hasReason(api.Error(v), api.ErrorReasonJoinTokenNotFound) {
		t.Fatal("不存在的接入碼要 404 join_token_not_found")
	}
	node := "node-z"
	if err := s.store.CreateJoinToken(ctx, store.JoinToken{ID: "jt1", SecretHash: "h", Status: "revoked", NodeID: node, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), RpcPort: 8545, MetricsPort: 9545}); err != nil {
		t.Fatal(err)
	}
	if v, ok := revoke("jt1").(api.RevokeJoinToken409JSONResponse); !ok || !hasReason(api.Error(v), api.ErrorReasonJoinTokenNotRevocable) {
		t.Fatalf("已撤銷的要 409 join_token_not_revocable")
	}
}
