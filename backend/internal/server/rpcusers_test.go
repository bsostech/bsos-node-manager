package server

import (
	"testing"
	"time"

	"besu-manager/backend/internal/api"
)

// validatePermissions: a permission string must be <ns>:* or <ns>:<catalog method>; rejects typos /
// bare * / *:* / missing colon.
func TestValidatePermissions(t *testing.T) {
	cases := []struct {
		name    string
		perms   []string
		wantErr bool
		reason  api.ErrorReason // empty = no reason (the case the contract rejects first, unreachable)
	}{
		{"整個 namespace", []string{"eth:*"}, false, ""},
		{"兩個單一方法", []string{"eth:blockNumber", "net:peerCount"}, false, ""},
		{"空清單", nil, true, ""},
		{"未知 namespace", []string{"foo:*"}, true, api.ErrorReasonRpcPermissionUnknownNamespace},
		{"目錄外方法", []string{"eth:notARealMethod"}, true, api.ErrorReasonRpcPermissionUnknownMethod},
		{"裸 *", []string{"*"}, true, api.ErrorReasonRpcPermissionFormat},
		{"*:*", []string{"*:*"}, true, api.ErrorReasonRpcPermissionUnknownNamespace},
		{"缺冒號", []string{"ethblockNumber"}, true, api.ErrorReasonRpcPermissionFormat},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validatePermissions(c.perms)
			if (err != nil) != c.wantErr {
				t.Fatalf("perms=%v:want err=%v,得 %v", c.perms, c.wantErr, err)
			}
			if c.reason != "" && !hasReason(*err, c.reason) {
				t.Fatalf("perms=%v:要 %s,得 %#v", c.perms, c.reason, err)
			}
		})
	}
}

// expiresAt must be strictly in the future and at most rpcTokenMaxTTL out: now itself is past, and
// exactly the maximum is still accepted.
func TestRpcTokenTTLExpiresAtBounds(t *testing.T) {
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	at := func(d time.Duration) (time.Duration, *api.Error) {
		exp := now.Add(d)
		return rpcTokenTTL(api.RpcTokenRequest{ExpiresAt: &exp}, now)
	}
	if _, err := at(0); err == nil || !hasReason(*err, api.ErrorReasonRpcTokenExpiryPast) {
		t.Errorf("到期 = now 應為 expiry past,得 %#v", err)
	}
	if ttl, err := at(rpcTokenMaxTTL); err != nil || ttl != rpcTokenMaxTTL {
		t.Errorf("剛好上限應通過,得 %v %#v", ttl, err)
	}
	if _, err := at(rpcTokenMaxTTL + time.Second); err == nil || !hasReason(*err, api.ErrorReasonRpcTokenExpiryTooFar) {
		t.Errorf("超過上限一秒應為 too far,得 %#v", err)
	}
}
