package server

import (
	"context"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/auth"
	"besu-manager/backend/internal/store"
)

func asUserRole(name, role string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Username: &name, Role: role})
}

// Read state is per user: marking read while firing marks only the "firing" entry; after recovery
// it becomes unread again, then marking reads both; read-all marks everything; malformed and
// nonexistent ids are ignored; node alerts carry read state too; when an alert is cleared its read
// state is deleted with it.
func TestAlertReads(t *testing.T) {
	s, pool := accessTestServer(t)
	ctx := context.Background()
	if err := s.store.UpsertOwnNode(ctx, "node-a1", "org-test", 8545, 9545, nil); err != nil {
		t.Fatal(err)
	}
	node := "node-a1"
	start := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	up := func(fp string, ends *time.Time) {
		t.Helper()
		status := "firing"
		if ends != nil {
			status = "resolved"
		}
		if err := s.store.UpsertAlert(ctx, store.Alert{Fingerprint: fp, Alertname: "NodeCpuHigh", NodeID: &node, Scope: "hw", Severity: "warning", Status: status,
			Labels: map[string]string{}, Annotations: map[string]string{"value": "91.5"}, StartsAt: start, EndsAt: ends}); err != nil {
			t.Fatal(err)
		}
	}
	up("fp-a", nil)
	up("fp-b", nil)
	ops, view := asUserRole("ops", "operator"), asUserRole("vic", "viewer")
	list := func(c context.Context) map[string]api.Alert {
		t.Helper()
		r, err := s.ListAlerts(c, api.ListAlertsRequestObject{})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]api.Alert{}
		for _, a := range r.(api.ListAlerts200JSONResponse).Alerts {
			out[a.Fingerprint] = a
		}
		return out
	}
	mark := func(c context.Context, ids ...string) {
		t.Helper()
		if r, err := s.MarkAlertsRead(c, api.MarkAlertsReadRequestObject{Body: &api.AlertReadRequest{Ids: ids}}); err != nil {
			t.Fatal(err)
		} else if _, ok := r.(api.MarkAlertsRead204Response); !ok {
			t.Fatalf("%T", r)
		}
	}
	a := list(ops)["fp-a"]
	if a.Id == "" || a.ReadFire || a.ReadResolve || a.Annotations["value"] != "91.5" {
		t.Fatalf("初始: %+v", a)
	}
	mark(ops, a.Id, "garbage", "fp-x|2020-01-01T00:00:00Z")
	if a := list(ops)["fp-a"]; !a.ReadFire || a.ReadResolve {
		t.Fatalf("觸發中標已讀只標觸發那則: %+v", a)
	}
	if a := list(view)["fp-a"]; a.ReadFire {
		t.Fatal("另一個使用者不該跟著讀過")
	}
	if b := list(ops)["fp-b"]; b.ReadFire {
		t.Fatal("沒標的不該變已讀")
	}

	ended := time.Now().Truncate(time.Microsecond)
	up("fp-a", &ended)
	a = list(ops)["fp-a"]
	if !a.ReadFire || a.ReadResolve || a.Id != list(view)["fp-a"].Id {
		t.Fatalf("恢復後重新變未讀、id 不變: %+v", a)
	}
	mark(ops, a.Id)
	if a := list(ops)["fp-a"]; !a.ReadFire || !a.ReadResolve {
		t.Fatalf("恢復後標已讀兩則都讀過: %+v", a)
	}

	if r, err := s.MarkAllAlertsRead(view, api.MarkAllAlertsReadRequestObject{}); err != nil {
		t.Fatal(err)
	} else if _, ok := r.(api.MarkAllAlertsRead204Response); !ok {
		t.Fatalf("%T", r)
	}
	for fp, a := range list(view) {
		if !a.ReadFire || a.ReadResolve != (a.Status == api.AlertStatusResolved) {
			t.Fatalf("read-all 後 %s: %+v", fp, a)
		}
	}
	r, err := s.GetNodeAlerts(view, api.GetNodeAlertsRequestObject{Id: "node-a1"})
	if err != nil {
		t.Fatal(err)
	}
	if na := r.(api.GetNodeAlerts200JSONResponse).Alerts; len(na) != 2 || !na[0].ReadFire {
		t.Fatalf("節點告警也帶已讀: %+v", na)
	}

	if err := s.store.PruneAlerts(ctx, time.Now().Add(time.Hour)); err != nil { // clear the recovered fp-a
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM alert_reads WHERE fingerprint = 'fp-a'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("告警清掉後已讀要跟著刪: %d %v", n, err)
	}
}
