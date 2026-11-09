package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/prom"
	"besu-manager/backend/internal/store"
)

// A monitoring source outage must not hide alerts already fired: the alerts in the database are
// returned as usual, with monitoringAvailable=false.
func TestAlertsServedWhileMonitoringDown(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	if err := s.store.UpsertOwnNode(ctx, "node-a1", "org-test", 8545, 9545, nil); err != nil {
		t.Fatal(err)
	}
	node := "node-a1"
	if err := s.store.UpsertAlert(ctx, store.Alert{Fingerprint: "fp-a", Alertname: "NodeOffline", NodeID: &node, Scope: "node", Severity: "critical",
		Status: "firing", Labels: map[string]string{}, Annotations: map[string]string{}, StartsAt: time.Now().Add(-time.Minute).Truncate(time.Microsecond)}); err != nil {
		t.Fatal(err)
	}
	check := func(name string, wantLive bool) {
		t.Helper()
		r, err := s.ListAlerts(ctx, api.ListAlertsRequestObject{})
		if err != nil {
			t.Fatal(err)
		}
		l := r.(api.ListAlerts200JSONResponse)
		if len(l.Alerts) != 1 || l.MonitoringAvailable != wantLive {
			t.Fatalf("%s: /alerts %+v", name, l)
		}
		nr, err := s.GetNodeAlerts(ctx, api.GetNodeAlertsRequestObject{Id: "node-a1"})
		if err != nil {
			t.Fatal(err)
		}
		if nl := nr.(api.GetNodeAlerts200JSONResponse); len(nl.Alerts) != 1 || nl.MonitoringAvailable != wantLive {
			t.Fatalf("%s: /nodes/{id}/alerts %+v", name, nl)
		}
	}
	check("沒設 Prometheus", true)
	s.prom = prom.New("http://127.0.0.1:1") // the background round has never succeeded
	check("Prometheus 連不上", false)
	if r, _ := s.GetNodeAlerts(ctx, api.GetNodeAlertsRequestObject{Id: "nope"}); r == nil {
		t.Fatal("nil")
	} else if _, ok := r.(api.GetNodeAlerts404JSONResponse); !ok {
		t.Fatalf("不存在的節點仍 404: %#v", r)
	}
}

// Time series: a monitoring source outage returns 503 monitoring_unavailable. If the background
// round is still fresh but the query itself cannot connect (just went down), that is also 503;
// Prometheus returning 4xx (a malformed query) is still a generic error (500).
func TestGetNodeSeriesMonitoringDown(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	if err := s.store.UpsertOwnNode(ctx, "node-a1", "org-test", 8545, 9545, nil); err != nil {
		t.Fatal(err)
	}
	rangeStatus := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/query_range" && rangeStatus != http.StatusOK {
			w.WriteHeader(rangeStatus)
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
	}))
	defer srv.Close()
	s.prom = prom.New(srv.URL)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.prom.RunBesuUp(runCtx)
	for i := 0; i < 50 && !s.prom.Reachable(); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	series := func() (api.GetNodeSeriesResponseObject, error) {
		return s.GetNodeSeries(ctx, api.GetNodeSeriesRequestObject{Id: "node-a1", Params: api.GetNodeSeriesParams{Metric: "cpu"}})
	}
	if r, err := series(); err != nil {
		t.Fatal(err)
	} else if _, ok := r.(api.GetNodeSeries200JSONResponse); !ok {
		t.Fatalf("正常應 200: %#v", r)
	}
	rangeStatus = http.StatusInternalServerError
	if r, err := series(); err != nil {
		t.Fatal(err)
	} else if e, ok := r.(api.GetNodeSeries503JSONResponse); !ok || e.Code != api.ErrorCodeMonitoringUnavailable || !hasReason(api.Error(e), api.ErrorReasonMonitoringDown) {
		t.Fatalf("Prometheus 5xx 應 503: %#v", r)
	}
	rangeStatus = http.StatusBadRequest
	if _, err := series(); err == nil {
		t.Fatal("Prometheus 4xx(查詢寫錯)應是一般錯誤")
	}
	s.prom = prom.New("http://127.0.0.1:1") // the background round has never succeeded
	if r, err := series(); err != nil {
		t.Fatal(err)
	} else if _, ok := r.(api.GetNodeSeries503JSONResponse); !ok {
		t.Fatalf("快取過期應 503: %#v", r)
	}
	// The query itself would succeed, but the background round has not: still 503 rather than a
	// chart drawn from a source the platform considers down
	rangeStatus = http.StatusOK
	s.prom = prom.New(srv.URL)
	if r, err := series(); err != nil {
		t.Fatal(err)
	} else if _, ok := r.(api.GetNodeSeries503JSONResponse); !ok {
		t.Fatalf("background round not alive should be 503: %#v", r)
	}
}

// Prometheus not configured: time series return 503 rather than drawing fake data (the node must
// exist, otherwise 404 comes first).
func TestGetNodeSeriesWithoutPromIs503(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	if err := s.store.UpsertOwnNode(ctx, "node-a1", "org-test", 8545, 9545, nil); err != nil {
		t.Fatal(err)
	}
	resp, err := s.GetNodeSeries(ctx, api.GetNodeSeriesRequestObject{Id: "node-a1", Params: api.GetNodeSeriesParams{Metric: "cpu"}})
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := resp.(api.GetNodeSeries503JSONResponse); !ok || r.Code != api.ErrorCodeMonitoringUnavailable {
		t.Fatalf("得 %#v,應為 503 monitoring_unavailable", resp)
	}
}
