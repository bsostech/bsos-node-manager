package auth

import (
	"context"
	"fmt"
	"testing"
	"time"

	"besu-manager/backend/internal/store"
	"besu-manager/backend/internal/store/storetest"
)

// The same account + permission is written only once per window; a denial after the window is written again, carrying the number merged in between.
func TestDeniedThrottle(t *testing.T) {
	var d deniedLog
	t0 := time.Unix(1_000_000, 0)
	if w, _ := d.admit("k", t0); !w {
		t.Fatal("第一次應寫")
	}
	for i := 1; i <= 3; i++ {
		if w, _ := d.admit("k", t0.Add(time.Duration(i)*time.Second)); w {
			t.Fatal("視窗內不該再寫")
		}
	}
	if w, _ := d.admit("other", t0.Add(time.Second)); !w {
		t.Fatal("不同 key 各自計")
	}
	w, n := d.admit("k", t0.Add(deniedWindow+time.Second))
	if !w || n != 3 {
		t.Fatalf("過了視窗應寫且帶 suppressed=3: write=%v n=%d", w, n)
	}
}

// A missing permission writes access_denied with the account, role, permission, method and path; the HTTP method cannot use the key method (the login method already uses it).
func TestAuditDeniedDetail(t *testing.T) {
	got := storetest.CaptureAuditForTest(t, func(event string, _ map[string]any) bool { return event == "access_denied" })

	s := New(store.New(nil), "test", false)
	name := "viewer1"
	p := &Principal{Username: &name, Role: "viewer"}
	s.auditDenied(context.Background(), p, PermNodesOnboard, "GET", "/api/v1/nodes/n1/logs")
	s.auditDenied(context.Background(), p, PermNodesOnboard, "GET", "/api/v1/nodes/n2/logs") // a different path does not bypass the throttle
	if len(*got) != 1 {
		t.Fatalf("節流後應只有一筆: %d", len(*got))
	}
	d := (*got)[0]
	if d["actor"] != "viewer1" || d["role"] != "viewer" || d["permission"] != string(PermNodesOnboard) ||
		d["http_method"] != "GET" || d["path"] != "/api/v1/nodes/n1/logs" || d["method"] != nil {
		t.Fatalf("detail 不對: %v", d)
	}
}

// The throttle table has a hard cap: flooding it with unexpired keys does not make it grow without bound.
func TestDeniedBounded(t *testing.T) {
	var d deniedLog
	now := time.Unix(1_000_000, 0)
	for i := 0; i < deniedMax*2; i++ {
		d.admit(fmt.Sprint(i), now)
	}
	if len(d.seen) > deniedMax {
		t.Fatalf("節流表超過上限: %d", len(d.seen))
	}
}
