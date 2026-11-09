package store

import (
	"context"
	"testing"
	"time"
)

// Fill in missing rules on every start: missing ones are added, existing (user-edited) ones are left alone;
// restoring defaults resets edited ones, including the resolve wait and resolve threshold.
func TestSeedDefaultAlertRulesFillsMissing(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	cpu, jvm := "NodeCpuHigh", "NodeJvmHeapHigh"
	clear := 85.0
	rule := func(id string, preset *string) AlertRule {
		return AlertRule{ID: id, Name: *preset, Preset: preset, Metric: "cpu", Operator: "gt", Threshold: 90, ClearThreshold: &clear,
			ForDuration: "5m", KeepFiringFor: "2m", Severity: "warning", Enabled: true, CreatedBy: "system"}
	}
	if n, err := s.SeedDefaultAlertRules(ctx, []AlertRule{rule("a", &cpu)}); err != nil || n != 1 {
		t.Fatalf("first seed: n=%d err=%v", n, err)
	}
	r, _ := s.GetAlertRule(ctx, "a")
	r.Threshold, r.KeepFiringFor, r.ClearThreshold = 50, "0s", nil
	if ok, err := s.UpdateAlertRule(ctx, *r); err != nil || !ok {
		t.Fatalf("update existing: ok=%v err=%v", ok, err)
	}
	// An unknown id must report not-found so the API answers 404 instead of a silent success
	missing := *r
	missing.ID = "nope"
	if ok, err := s.UpdateAlertRule(ctx, missing); err != nil || ok {
		t.Fatalf("update unknown id: ok=%v err=%v", ok, err)
	}
	// Restart with one more default rule: only the new one is added, edited ones are kept
	if n, err := s.SeedDefaultAlertRules(ctx, []AlertRule{rule("b", &cpu), rule("c", &jvm)}); err != nil || n != 1 {
		t.Fatalf("second seed: n=%d err=%v", n, err)
	}
	if r, _ := s.GetAlertRule(ctx, "a"); r.Threshold != 50 || r.KeepFiringFor != "0s" || r.ClearThreshold != nil {
		t.Fatalf("改過的被覆蓋了: %+v", r)
	}
	if r, _ := s.GetAlertRuleByPreset(ctx, jvm); r == nil || r.ID != "c" {
		t.Fatalf("新的預設規則沒補上: %+v", r)
	}
	if err := s.RestoreDefaultAlertRules(ctx, []AlertRule{rule("x", &cpu)}, "alice"); err != nil {
		t.Fatal(err)
	}
	r, _ = s.GetAlertRule(ctx, "a")
	if r.Threshold != 90 || r.KeepFiringFor != "2m" || r.ClearThreshold == nil || *r.ClearThreshold != 85 || !r.Enabled {
		t.Fatalf("還原應改回預設值(保留原 id): %+v", r)
	}
	if rules, _ := s.ListAlertRules(ctx); len(rules) != 2 {
		t.Fatalf("還原不該多一列: %d", len(rules))
	}
}

func TestAlertsKeepOneRowPerFiring(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	node := "node-a1"
	t0 := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Microsecond)
	end0 := t0.Add(10 * time.Minute)
	t1 := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	a := Alert{Fingerprint: "fp", Alertname: "NodeOffline", NodeID: &node, Scope: "net", Severity: "critical",
		Status: "firing", Labels: map[string]string{}, Annotations: map[string]string{}, StartsAt: t0}
	if err := s.UpsertAlert(ctx, a); err != nil {
		t.Fatal(err)
	}
	a.Status, a.EndsAt = "resolved", &end0
	if err := s.UpsertAlert(ctx, a); err != nil {
		t.Fatal(err)
	}
	a.Status, a.EndsAt, a.StartsAt = "firing", nil, t1
	if err := s.UpsertAlert(ctx, a); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListAlerts(ctx, AlertFilter{})
	if err != nil || len(all) != 2 {
		t.Fatalf("want 2 firings, got %d (%v)", len(all), err)
	}
	if all[0].Status != "firing" || all[1].Status != "resolved" {
		t.Fatalf("want newest first with old one resolved, got %+v", all)
	}
	name := "NodeOffline"
	since := time.Now().Add(-24 * time.Hour)
	recent, _ := s.ListAlerts(ctx, AlertFilter{Alertname: &name, Since: &since})
	if len(recent) != 1 || !recent[0].StartsAt.Equal(t1) {
		t.Fatalf("since filter should drop the firing that ended 2 days ago, got %+v", recent)
	}
	other := "NodeCpuHigh"
	if none, _ := s.ListAlerts(ctx, AlertFilter{Alertname: &other}); len(none) != 0 {
		t.Fatalf("alertname filter leaked: %+v", none)
	}
	if err := s.PruneAlerts(ctx, time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.ListAlerts(ctx, AlertFilter{}); len(left) != 1 || left[0].Status != "firing" {
		t.Fatalf("prune should drop only the old resolved firing, got %+v", left)
	}
}

// After a Prometheus/Alertmanager restart, a still-firing alert gets a new starts_at: the old row must be
// closed, not stay firing forever.
func TestNewFiringClosesStaleOpenRow(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	t0 := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Microsecond)
	t1 := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	a := Alert{Fingerprint: "fp", Alertname: "NodeOffline", Scope: "net", Severity: "critical",
		Status: "firing", Labels: map[string]string{}, Annotations: map[string]string{}, StartsAt: t0}
	if err := s.UpsertAlert(ctx, a); err != nil {
		t.Fatal(err)
	}
	a.StartsAt = t1
	if err := s.UpsertAlert(ctx, a); err != nil {
		t.Fatal(err)
	}
	firing := "firing"
	open, _ := s.ListAlerts(ctx, AlertFilter{Status: &firing})
	if len(open) != 1 || !open[0].StartsAt.Equal(t1) {
		t.Fatalf("only the newest firing should stay open, got %+v", open)
	}
}
