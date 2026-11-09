package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/auth"
	"besu-manager/backend/internal/store"
)

// The account list is sorted by username and carries role, disabled, the password-change flag,
// email and last login (UTC); the password hash never appears in the response.
func TestListLocalUsers(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	hash, _ := auth.HashPassword("bob-password-123")
	email := "carol@example.com"
	for _, u := range []store.LocalUser{
		{Username: "carol", PasswordHash: hash, Role: "viewer", Disabled: true, Email: &email},
		{Username: "bob", PasswordHash: hash, Role: "admin", MustChangePassword: true},
	} {
		if err := s.store.CreateLocalUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	login := time.Date(2026, 11, 1, 9, 30, 0, 0, time.FixedZone("TPE", 8*3600))
	if err := s.store.SetLastLogin(ctx, "carol", login); err != nil {
		t.Fatal(err)
	}
	r, err := s.ListLocalUsers(asAdmin(ctx), api.ListLocalUsersRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	list := r.(api.ListLocalUsers200JSONResponse)
	if len(list) != 2 || list[0].Username != "bob" || list[1].Username != "carol" {
		t.Fatalf("應依帳號排序: %+v", list)
	}
	bob, carol := list[0], list[1]
	if bob.Role != api.Admin || !bob.MustChangePassword || bob.Disabled || bob.LastLoginAt != nil || bob.Email != nil {
		t.Fatalf("bob: %+v", bob)
	}
	if carol.Role != api.Viewer || !carol.Disabled || carol.Email == nil || *carol.Email != email ||
		carol.LastLoginAt == nil || !carol.LastLoginAt.Equal(login) || carol.LastLoginAt.Location() != time.UTC {
		t.Fatalf("carol: %+v", carol)
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), hash) || strings.Contains(strings.ToLower(string(raw)), "password_hash") {
		t.Fatalf("回應不該帶密碼雜湊: %s", raw)
	}
}
