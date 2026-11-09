package pki

import (
	"crypto/rand"
	"testing"

	"besu-manager/backend/internal/store"
)

func testMaster(t *testing.T) []byte {
	t.Helper()
	m := make([]byte, 32)
	if _, err := rand.Read(m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestReEnrollCheck(t *testing.T) {
	const self = "org-A"
	own := func() *store.Node { return &store.Node{ID: "node-a1", Org: self} }

	cases := []struct {
		name   string
		node   *store.Node
		online bool
		want   reEnrollReject
	}{
		{"自有節點且離線 → 放行", own(), false, reEnrollOK},
		{"自有節點但仍在線 → 擋(這是取代 join「id 不得存在」的那道)", own(), true, reEnrollOnline},
		{"節點不存在 → 擋(該走 join)", nil, false, reEnrollNotFound},
		{"org 不是本單位 → 擋(資料不一致時仍不放行)", &store.Node{ID: "node-b1", Org: "org-B"}, false, reEnrollNotOwn},
		{"org 不是本單位且在線 → 先擋在 org,不洩漏在線與否", &store.Node{ID: "node-b1", Org: "org-B"}, true, reEnrollNotOwn},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := reEnrollCheck(c.node, self, c.online); got != c.want {
				t.Errorf("reEnrollCheck() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSourceAllowed(t *testing.T) {
	cases := []struct {
		name    string
		ip      string
		allowed []string
		want    bool
	}{
		{"名單空 → 不限制", "203.0.113.9", nil, true},
		{"名單空且取不到來源 → 仍放行", "", []string{}, true},
		{"單一 IP 相符 → 放行", "10.0.0.31", []string{"10.0.0.31/32"}, true},
		{"在網段內 → 放行", "10.0.0.77", []string{"192.168.1.0/24", "10.0.0.0/24"}, true},
		{"不在名單 → 擋", "10.0.1.5", []string{"10.0.0.0/24"}, false},
		{"有名單但取不到來源 IP → 擋(不可放行)", "", []string{"10.0.0.0/24"}, false},
		{"IPv6 相符 → 放行", "2001:db8::5", []string{"2001:db8::/64"}, true},
		{"IPv4 不吃 IPv6 網段 → 擋", "10.0.0.5", []string{"2001:db8::/64"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sourceAllowed(c.ip, c.allowed); got != c.want {
				t.Errorf("sourceAllowed(%q, %v) = %v, want %v", c.ip, c.allowed, got, c.want)
			}
		})
	}
}
