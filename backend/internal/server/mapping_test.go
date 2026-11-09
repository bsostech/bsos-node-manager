package server

import (
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/store"
)

func ptr[T any](v T) *T { return &v }

// toAPIJoinToken: an expired pending token is presented as expired.
func TestToAPIJoinToken(t *testing.T) {
	now := time.Now().UTC()
	jt := store.JoinToken{
		ID: "jt-1", NodeName: ptr("n"), Status: "pending",
		CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour),
	}
	out := toAPIJoinToken(jt, now)
	if out.Status != api.JoinTokenStatus("expired") {
		t.Errorf("pending 已過期應為 expired,得 %v", out.Status)
	}
}
