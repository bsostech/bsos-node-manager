package store

import (
	"testing"
	"time"
)

// JoinToken.EffectiveStatus: pending but past expiry -> expired; everything else unchanged.
func TestJoinTokenEffectiveStatus(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	cases := []struct {
		name   string
		status string
		exp    time.Time
		want   string
	}{
		{"pending 未過期", "pending", future, "pending"},
		{"pending 已過期 → expired", "pending", past, "expired"},
		{"pending 剛好到期 → expired", "pending", now, "expired"},
		{"used 不受到期影響", "used", past, "used"},
		{"revoked 不受到期影響", "revoked", past, "revoked"},
	}
	for _, c := range cases {
		jt := JoinToken{Status: c.status, ExpiresAt: c.exp}
		if got := jt.EffectiveStatus(now); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
