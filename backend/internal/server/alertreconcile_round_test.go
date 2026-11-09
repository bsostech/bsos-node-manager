package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"besu-manager/backend/internal/prom"
	"besu-manager/backend/internal/store"
	"besu-manager/backend/internal/store/storetest"
)

// reconcileProm is one fake serving both Prometheus' /api/v1/alerts (the given label sets) and
// Alertmanager's /api/v2/alerts (recording each POST, answering amStatus).
type reconcileProm struct {
	mu       sync.Mutex
	active   string // JSON array of label maps
	amStatus int
	ended    [][]map[string]any
}

func (f *reconcileProm) server(t *testing.T) *prom.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/alerts":
			_, _ = w.Write([]byte(`{"status":"success","data":{"alerts":[` + f.active + `]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/alerts":
			var body []map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.ended = append(f.ended, body)
			w.WriteHeader(f.amStatus)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c := prom.New(srv.URL)
	c.SetAlertmanager(srv.URL + "/-/reload")
	return c
}

func (f *reconcileProm) posts() [][]map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ended
}

// One reconcile round: a firing alert that Prometheus has not had for alertStaleAfter is recorded
// in the audit, reported to Alertmanager as ended with its full labels, and written as resolved;
// one Prometheus still has (even with fewer labels) is left alone.
func TestReconcileAlertsResolvesStale(t *testing.T) {
	s, _ := accessTestServer(t)
	audited := recordAudit(t)
	ctx := context.Background()
	f := &reconcileProm{active: `{"labels":{"alertname":"NodeOffline","node":"node-a1"},"state":"firing"}`, amStatus: http.StatusOK}
	s.prom = f.server(t)
	start := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	for _, a := range []store.Alert{
		{Fingerprint: "fp-a1", Labels: map[string]string{"alertname": "NodeOffline", "node": "node-a1", "monitor": "x"}},
		{Fingerprint: "fp-b2", Labels: map[string]string{"alertname": "NodeOffline", "node": "node-b2", "monitor": "x"}},
	} {
		a.Alertname, a.Scope, a.Severity, a.Status, a.Annotations, a.StartsAt = "NodeOffline", "hw", "critical", "firing", map[string]string{}, start
		if err := s.store.UpsertAlert(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	missing := map[store.AlertKey]time.Time{}
	t0 := time.Now().UTC().Truncate(time.Microsecond)
	if err := s.reconcileAlerts(ctx, missing, t0); err != nil {
		t.Fatal(err)
	}
	if len(f.posts()) != 0 || len(audited("alerts_auto_resolved")) != 0 || alertsByFingerprint(t, s.store)["fp-b2"].Status != "firing" {
		t.Fatal("第一次對不到不該收")
	}

	now := t0.Add(alertStaleAfter)
	if err := s.reconcileAlerts(ctx, missing, now); err != nil {
		t.Fatal(err)
	}
	got := alertsByFingerprint(t, s.store)
	if b := got["fp-b2"]; b.Status != "resolved" || b.EndsAt == nil || !b.EndsAt.Equal(now) {
		t.Fatalf("b2 應寫成已恢復、結束時間為這輪: %+v", b)
	}
	if a := got["fp-a1"]; a.Status != "firing" || a.EndsAt != nil {
		t.Fatalf("Prometheus 還有的 a1 不該動: %+v", a)
	}
	posts := f.posts()
	if len(posts) != 1 || len(posts[0]) != 1 {
		t.Fatalf("應送 Alertmanager 一則結束: %v", posts)
	}
	sent := posts[0][0]
	sentStart, _ := time.Parse(time.RFC3339Nano, sent["startsAt"].(string))
	sentEnd, _ := time.Parse(time.RFC3339Nano, sent["endsAt"].(string))
	if l := sent["labels"].(map[string]any); l["node"] != "node-b2" || l["monitor"] != "x" || !sentStart.Equal(start) || !sentEnd.Equal(now) {
		t.Fatalf("送給 Alertmanager 的內容不對(要完整 labels 才對得到 fingerprint): %v", sent)
	}
	entries := audited("alerts_auto_resolved")
	if len(entries) != 1 {
		t.Fatalf("稽核應一筆: %v", entries)
	}
	if ids, _ := entries[0]["alerts"].([]string); len(ids) != 1 || ids[0] != alertID(got["fp-b2"]) {
		t.Fatalf("稽核要記收掉的告警 id: %v", entries[0])
	}
	if len(missing) != 0 {
		t.Fatalf("收掉的不該再留在 missing: %v", missing)
	}
}

// When Alertmanager refuses the end, or the audit cannot be written, or Prometheus is unreachable,
// the round returns an error and the database row stays firing (the next round tries again).
func TestReconcileAlertsFailuresLeaveRowFiring(t *testing.T) {
	ctx := context.Background()
	// setup leaves fp-b2 missing from Prometheus since one round alertStaleAfter ago, so the next
	// round at the returned time ends it
	setup := func(t *testing.T, f *reconcileProm) (*Server, map[store.AlertKey]time.Time, time.Time) {
		t.Helper()
		s, _ := accessTestServer(t)
		s.prom = f.server(t)
		start := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
		if err := s.store.UpsertAlert(ctx, store.Alert{Fingerprint: "fp-b2", Alertname: "NodeOffline", Scope: "hw", Severity: "critical", Status: "firing",
			Labels: map[string]string{"alertname": "NodeOffline", "node": "node-b2"}, Annotations: map[string]string{}, StartsAt: start}); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		missing := map[store.AlertKey]time.Time{}
		if err := s.reconcileAlerts(ctx, missing, now.Add(-alertStaleAfter)); err != nil || len(missing) != 1 {
			t.Fatalf("第一輪: %v %v", missing, err)
		}
		return s, missing, now
	}
	stillFiring := func(t *testing.T, s *Server) {
		t.Helper()
		if b := alertsByFingerprint(t, s.store)["fp-b2"]; b.Status != "firing" || b.EndsAt != nil {
			t.Fatalf("失敗時資料庫不該改: %+v", b)
		}
	}

	t.Run("Alertmanager 拒絕", func(t *testing.T) {
		recordAudit(t)
		f := &reconcileProm{amStatus: http.StatusInternalServerError}
		s, missing, now := setup(t, f)
		if err := s.reconcileAlerts(ctx, missing, now); err == nil {
			t.Fatal("Alertmanager 500 應回錯")
		}
		stillFiring(t, s)
	})
	t.Run("稽核寫不進去", func(t *testing.T) {
		f := &reconcileProm{amStatus: http.StatusOK}
		s, missing, now := setup(t, f)
		storetest.FailAuditForTest(t)
		if err := s.reconcileAlerts(ctx, missing, now); err == nil {
			t.Fatal("稽核失敗應回錯")
		}
		if len(f.posts()) != 0 {
			t.Fatal("稽核失敗不該通知 Alertmanager")
		}
		stillFiring(t, s)
	})
	t.Run("Prometheus 連不上", func(t *testing.T) {
		recordAudit(t)
		s, missing, now := setup(t, &reconcileProm{amStatus: http.StatusOK})
		s.prom = prom.New("http://127.0.0.1:1")
		if err := s.reconcileAlerts(ctx, missing, now); err == nil {
			t.Fatal("Prometheus 連不上應回錯")
		}
		stillFiring(t, s)
	})
}
