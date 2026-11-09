package server

import (
	"testing"

	"besu-manager/backend/internal/auth"
)

// /me's user id is the username when the principal has one; a principal without a username (no
// local or SSO account name) gets the fixed id "user".
func TestMeFromPrincipal(t *testing.T) {
	alice := "alice"
	if me := meFromPrincipal(&auth.Principal{Username: &alice, Role: "viewer"}); me.User.Id != "alice" {
		t.Errorf("有 username 應為 alice,得 %q", me.User.Id)
	}
	if me := meFromPrincipal(&auth.Principal{Role: "viewer"}); me.User.Id != "user" {
		t.Errorf("沒有 username 應為 user,得 %q", me.User.Id)
	}
}
