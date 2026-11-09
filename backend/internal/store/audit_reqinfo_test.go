package store

import (
	"context"
	"testing"

	"besu-manager/backend/internal/reqinfo"
)

// Audit entries from requests carry ip and ua; background jobs have no request and carry neither; the
// caller's map is not modified.
func TestWithRequestInfo(t *testing.T) {
	in := map[string]any{"username": "alice"}
	if got := withRequestInfo(context.Background(), in); got["ip"] != nil || got["ua"] != nil {
		t.Fatalf("沒有請求不該帶: %v", got)
	}
	ctx := reqinfo.With(context.Background(), reqinfo.Info{IP: "198.51.100.7", UA: "curl/8"})
	got := withRequestInfo(ctx, in)
	if got["ip"] != "198.51.100.7" || got["ua"] != "curl/8" || got["username"] != "alice" {
		t.Fatalf("detail 不對: %v", got)
	}
	if _, ok := in["ip"]; ok {
		t.Fatal("不該改到呼叫端的 map")
	}
}
