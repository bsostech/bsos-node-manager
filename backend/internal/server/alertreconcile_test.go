package server

import (
	"testing"
	"time"

	"besu-manager/backend/internal/store"
)

func TestStaleAlerts(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC)
	b2 := store.Alert{Fingerprint: "b2", StartsAt: t0, Labels: map[string]string{"alertname": "NodeOffline", "node": "node-b2", "monitor": "x"}}
	a1 := store.Alert{Fingerprint: "a1", StartsAt: t0, Labels: map[string]string{"alertname": "NodeOffline", "node": "node-a1"}}
	open := []store.Alert{b2, a1}
	// Prometheus only has a1 left (pending also counts); b2's labels lack the external label and
	// must still match
	active := []map[string]string{{"alertname": "NodeOffline", "node": "node-a1"}}
	missing := map[store.AlertKey]time.Time{}

	if got := staleAlerts(open, active, missing, t0); len(got) != 0 {
		t.Fatalf("第一次對不到就收: %v", got)
	}
	if got := staleAlerts(open, active, missing, t0.Add(alertStaleAfter-time.Second)); len(got) != 0 {
		t.Fatalf("未滿 %v 就收: %v", alertStaleAfter, got)
	}
	got := staleAlerts(open, active, missing, t0.Add(alertStaleAfter))
	if len(got) != 1 || got[0].Fingerprint != "b2" {
		t.Fatalf("滿 %v 要收 b2,得到 %v", alertStaleAfter, got)
	}

	// Matched again midway: the count restarts
	missing = map[store.AlertKey]time.Time{}
	staleAlerts(open, active, missing, t0)
	back := append(active, map[string]string{"alertname": "NodeOffline", "node": "node-b2"})
	staleAlerts(open, back, missing, t0.Add(time.Minute))
	if got := staleAlerts(open, active, missing, t0.Add(alertStaleAfter)); len(got) != 0 {
		t.Fatalf("中途對到過要重新起算: %v", got)
	}

	// Ones no longer firing in the database are not kept in missing
	staleAlerts([]store.Alert{a1}, active, missing, t0)
	if len(missing) != 0 {
		t.Fatalf("missing 留著已結束的: %v", missing)
	}
}
