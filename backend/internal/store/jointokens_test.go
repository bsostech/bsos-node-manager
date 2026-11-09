package store

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"
)

// captureAudit swaps the audit writer for one that records event + detail (fail = every write
// fails), restoring it at the end.
func captureAudit(t *testing.T, fail *bool) func() []map[string]any {
	t.Helper()
	saved := AuditSink
	var mu sync.Mutex
	var got []map[string]any
	AuditSink = func(_ context.Context, _ AuditDB, event string, detail map[string]any) error {
		if *fail {
			return errors.New("audit down")
		}
		mu.Lock()
		defer mu.Unlock()
		d := map[string]any{"event": event}
		for k, v := range detail {
			d[k] = v
		}
		got = append(got, d)
		return nil
	}
	t.Cleanup(func() { AuditSink = saved })
	return func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		out := got
		got = nil
		return out
	}
}

func joinTokenStatuses(t *testing.T, s *Store) map[string]string {
	t.Helper()
	list, err := s.ListJoinTokens(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, tk := range list {
		m[tk.ID] = tk.Status
	}
	return m
}

// A new code revokes the same node's unused codes (expired ones too) with one audit entry each,
// then records and inserts itself, all in one transaction; other nodes and used codes are left
// alone; if the audit cannot be written nothing changes; concurrent creates still leave one
// pending code, with one revocation entry per superseded code; deleting the node revokes its unused
// code in the same transaction (audit down keeps both); revoking only applies to pending codes.
func TestJoinTokenSingleUnused(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	fail := false
	audits := captureAudit(t, &fail)
	now := time.Now().UTC()
	tok := func(id, node, status string, expires time.Time) JoinToken {
		return JoinToken{ID: id, SecretHash: "h-" + id, Status: status, NodeID: node, CreatedAt: now, ExpiresAt: expires, RpcPort: 8545, MetricsPort: 9545}
	}
	for _, tk := range []JoinToken{
		tok("jt-old", "node-a", "pending", now.Add(time.Hour)),
		tok("jt-expired", "node-a", "pending", now.Add(-time.Minute)),
		tok("jt-used", "node-a", "used", now.Add(time.Hour)),
		tok("jt-other", "node-b", "pending", now.Add(time.Hour)),
	} {
		if err := s.CreateJoinToken(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.CreateJoinTokenSuperseding(ctx, tok("jt-new", "node-a", "pending", now.Add(time.Hour)),
		map[string]any{"code_id": "jt-new"}, map[string]any{"reason": "superseded", "superseded_by": "jt-new"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"jt-old": "revoked", "jt-expired": "revoked", "jt-used": "used", "jt-other": "pending", "jt-new": "pending"}
	if got := joinTokenStatuses(t, s); !maps.Equal(got, want) {
		t.Fatalf("statuses = %v, want %v", got, want)
	}
	a := audits()
	if len(a) != 3 || a[2]["event"] != "join_token_created" || a[2]["code_id"] != "jt-new" {
		t.Fatalf("audits = %v", a)
	}
	var revoked []string
	for _, e := range a[:2] {
		if e["event"] != "join_token_revoked" || e["reason"] != "superseded" || e["superseded_by"] != "jt-new" || e["node_id"] != "node-a" || e["token_hash"] != "h-"+e["code_id"].(string) {
			t.Fatalf("revocation entry = %v", e)
		}
		revoked = append(revoked, e["code_id"].(string))
	}
	if slices.Sort(revoked); !slices.Equal(revoked, []string{"jt-expired", "jt-old"}) {
		t.Fatalf("revoked = %v", revoked)
	}

	fail = true
	if err := s.CreateJoinTokenSuperseding(ctx, tok("jt-blocked", "node-a", "pending", now.Add(time.Hour)), nil, nil); !errors.Is(err, ErrAudit) {
		t.Fatalf("audit down: %v", err)
	}
	if got := joinTokenStatuses(t, s); got["jt-new"] != "pending" || got["jt-blocked"] != "" {
		t.Fatalf("audit down must change nothing: %v", got)
	}
	fail = false

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.CreateJoinTokenSuperseding(ctx, tok(fmt.Sprintf("jt-c%d", i), "node-c", "pending", now.Add(time.Hour)), nil, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var pendingC int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM join_tokens WHERE node_id = 'node-c' AND status = 'pending'`).Scan(&pendingC); err != nil || pendingC != 1 {
		t.Fatalf("concurrent creates left %d pending codes (%v)", pendingC, err)
	}
	var revokedC int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM join_tokens WHERE node_id = 'node-c' AND status = 'revoked'`).Scan(&revokedC); err != nil {
		t.Fatal(err)
	}
	counts := map[any]int{}
	for _, e := range audits() {
		counts[e["event"]]++
	}
	if revokedC != 7 || counts["join_token_revoked"] != revokedC || counts["join_token_created"] != 8 {
		t.Fatalf("concurrent creates: %d revoked codes, audits %v", revokedC, counts)
	}

	if _, err := s.pool.Exec(ctx, `INSERT INTO nodes (id, org) VALUES ('node-a', 'org-A')`); err != nil {
		t.Fatal(err)
	}
	fail = true
	if err := s.DeleteNode(ctx, "node-a", nil); !errors.Is(err, ErrAudit) {
		t.Fatalf("delete with audit down: %v", err)
	}
	if n, _ := s.GetNode(ctx, "node-a"); n == nil || joinTokenStatuses(t, s)["jt-new"] != "pending" {
		t.Fatal("audit down: the node and its code must both remain")
	}
	fail = false
	if err := s.DeleteNode(ctx, "node-a", map[string]any{"reason": "node_deleted"}); err != nil {
		t.Fatal(err)
	}
	if got := joinTokenStatuses(t, s); got["jt-new"] != "revoked" || got["jt-other"] != "pending" {
		t.Fatalf("after delete: %v", got)
	}
	if a := audits(); len(a) != 1 || a[0]["code_id"] != "jt-new" || a[0]["reason"] != "node_deleted" {
		t.Fatalf("delete audits = %v", a)
	}

	for id, want := range map[string]bool{"jt-other": true, "jt-used": false, "jt-old": false} {
		if ok, err := s.MarkJoinTokenRevoked(ctx, id); err != nil || ok != want {
			t.Errorf("MarkJoinTokenRevoked(%s) = %v %v, want %v", id, ok, err, want)
		}
	}
	if got := joinTokenStatuses(t, s); got["jt-used"] != "used" {
		t.Fatalf("a used code must stay used: %v", got)
	}
}
