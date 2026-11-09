package server

import (
	"testing"
	"time"

	"besu-manager/backend/internal/api"
)

// Two-state verdict used by /nodes/{id}/cert.
func TestCertStatus(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { t := now.Add(d); return &t }

	cases := []struct {
		name     string
		notAfter *time.Time
		revoked  bool
		want     api.CertStatus
	}{
		{"無紀錄(CA 重置後)", nil, false, api.Untrusted},
		{"已撤銷但還沒到期", at(48 * time.Hour), true, api.Untrusted},
		{"已過期", at(-time.Minute), false, api.Untrusted},
		{"48 小時後到期 → 有效", at(48 * time.Hour), false, api.Valid},
		// Less than 24 hours left is the renewal point, not an anomaly
		{"23 小時後到期 → 仍有效", at(23 * time.Hour), false, api.Valid},
		{"1 小時後到期 → 仍有效", at(time.Hour), false, api.Valid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := certStatus(c.notAfter, c.revoked, now); got != c.want {
				t.Errorf("certStatus() = %q, want %q", got, c.want)
			}
		})
	}
}
