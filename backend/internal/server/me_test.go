package server

import (
	"context"
	"testing"

	"besu-manager/backend/internal/api"
)

// The platform address /me gives the frontend: included only if set; a PLATFORM_ADDR still at the
// default localhost:8443 is treated as unset (nodes cannot reach it).
func TestMePlatformAddresses(t *testing.T) {
	s := New(Deps{PlatformAddr: "localhost:8443"})
	if me := s.meWithFlags(context.Background(), nil, api.Me{}); me.PlatformAddr != nil || me.PublicApiBase != nil {
		t.Fatalf("沒設應為 null: %+v", me)
	}
	s = New(Deps{PlatformAddr: "platform.org-a:8443", PublicAPIBase: "https://platform.org-a/"})
	me := s.meWithFlags(context.Background(), nil, api.Me{})
	if me.PlatformAddr == nil || *me.PlatformAddr != "platform.org-a:8443" || me.PublicApiBase == nil || *me.PublicApiBase != "https://platform.org-a" {
		t.Fatalf("有設應帶上(結尾 / 去掉): %+v", me)
	}
}
