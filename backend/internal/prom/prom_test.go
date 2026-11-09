package prom

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
)

func TestSeriesParsesMatrix(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"node":"node-a1"},"values":[[1718600000,"42.3"],[1718600060,"43.8"]]}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL)
	now := time.Now()
	ms, err := c.Series(context.Background(), api.Cpu, "node-a1", now.Add(-time.Hour), now, 0)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if !strings.Contains(gotQuery, `bsos_node_cpu_percent{node="node-a1"}`) {
		t.Errorf("PromQL 不對: %q", gotQuery)
	}
	if len(ms.Points) != 2 {
		t.Fatalf("點數 = %d, 期望 2", len(ms.Points))
	}
	if ms.Points[0].V == nil || *ms.Points[0].V != 42.3 {
		t.Errorf("第一點值不對: %+v", ms.Points[0].V)
	}
	if ms.Unit == nil || *ms.Unit != "%" {
		t.Errorf("單位不對: %v", ms.Unit)
	}
}

func TestNewEmptyURLIsNil(t *testing.T) {
	if New("") != nil {
		t.Error("空 URL 應回 nil(未啟用)")
	}
}

func TestSeriesEmptyResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer srv.Close()
	now := time.Now()
	ms, err := c(t, srv.URL).Series(context.Background(), api.Tps, "node-x", now.Add(-6*time.Hour), now, 0)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(ms.Points) != 0 {
		t.Errorf("空結果應 0 點, 得 %d", len(ms.Points))
	}
}

// An explicitly given step is used as-is in query_range and not overridden by stepFor.
func TestSeriesUsesExplicitStep(t *testing.T) {
	var gotStep string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotStep = r.URL.Query().Get("step")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	now := time.Now()
	// 60s differs from the 37.5s (rounded to 37) auto-computed for 1h, so we can tell whether it was overridden.
	if _, err := c.Series(context.Background(), api.Cpu, "node-a1", now.Add(-time.Hour), now, 60*time.Second); err != nil {
		t.Fatalf("Series: %v", err)
	}
	if gotStep != "60" {
		t.Errorf("應直接用呼叫端指定的 step=60,得 %q", gotStep)
	}
	// step=0 is computed from the span: 24h / 96 = 15m.
	if _, err := c.Series(context.Background(), api.Cpu, "node-a1", now.Add(-24*time.Hour), now, 0); err != nil {
		t.Fatalf("Series: %v", err)
	}
	if gotStep != "900" {
		t.Errorf("step=0 跨度 24h 應自動算成 900,得 %q", gotStep)
	}
}

// stepFor: span / 96, with 15s used when below 15s.
func TestStepFor(t *testing.T) {
	for _, tc := range []struct{ span, want time.Duration }{
		{24 * time.Hour, 15 * time.Minute},
		{time.Hour, 37500 * time.Millisecond},
		{10 * time.Minute, 15 * time.Second},
	} {
		if got := stepFor(tc.span); got != tc.want {
			t.Errorf("stepFor(%v) = %v,want %v", tc.span, got, tc.want)
		}
	}
}

// Zero or negative is not a valid duration.
func TestParseDurRejectsZero(t *testing.T) {
	if d, ok := ParseDur("0h"); ok {
		t.Errorf("0h 應不合法,得 %v", d)
	}
}

// clampStep: 15s floor, and when (span/step) exceeds the cap the step is enlarged; this must block both tiny
// values passed carelessly by callers and cases that in theory should never happen.
func TestClampStep(t *testing.T) {
	if got := clampStep(1*time.Second, time.Hour); got != minStep {
		t.Errorf("低於下限應 clamp 到 %v,得 %v", minStep, got)
	}
	if got := clampStep(minStep, 24*time.Hour*365); got <= minStep {
		t.Errorf("span 極大時應把 step 拉大避免超過 maxPoints,得 %v", got)
	}
	if got := clampStep(37*time.Second, time.Hour); got != 37*time.Second {
		t.Errorf("合理範圍內的值不該被改動,得 %v", got)
	}
}

func TestWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	// Relative: range duration -> back from now.
	if s, e := Window("3d", nil, nil, now); !e.Equal(now) || now.Sub(s) != 72*time.Hour {
		t.Errorf("rel 3d 視窗不對: %v..%v", s, e)
	}
	// Unparsable -> default 1h.
	if s, _ := Window("bad", nil, nil, now); now.Sub(s) != time.Hour {
		t.Errorf("壞 range 應退回 1h")
	}
	// Absolute: from/to override range.
	from, to := int64(1_700_000_000), int64(1_700_003_600)
	if s, e := Window("3d", &from, &to, now); s.Unix() != from || e.Unix() != to {
		t.Errorf("abs 視窗應用 from/to: %v..%v", s, e)
	}
	// from==to has zero length, so it is not an absolute window; fall back to range.
	if s, e := Window("3d", &from, &from, now); !e.Equal(now) || now.Sub(s) != 72*time.Hour {
		t.Errorf("from==to 應退回相對視窗: %v..%v", s, e)
	}
	// 0h has zero length; fall back to the default 1h.
	if s, _ := Window("0h", nil, nil, now); now.Sub(s) != time.Hour {
		t.Errorf("0h 應退回 1h")
	}
}

func c(t *testing.T, url string) *Client {
	t.Helper()
	cl := New(url)
	if cl == nil {
		t.Fatal("client nil")
	}
	return cl
}

// One query for all nodes: the query has no node filter, and results are mapped back by metric.node.
func TestFetchBesuUp(t *testing.T) {
	var q string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query().Get("query")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[` +
			`{"metric":{"node":"node-x"},"value":[1700000000,"1"]},` +
			`{"metric":{"node":"node-y"},"value":[1700000000,"0"]}]}}`))
	}))
	defer srv.Close()
	m, err := c(t, srv.URL).fetchBesuUp(context.Background())
	if err != nil {
		t.Fatalf("fetchBesuUp: %v", err)
	}
	if !strings.Contains(q, `source="besu"`) || strings.Contains(q, "node=") {
		t.Errorf("query 應只限定 besu 來源、不篩節點: %s", q)
	}
	if len(m) != 2 || !m["node-x"] || m["node-y"] {
		t.Errorf("got %v", m)
	}
}

// Cache lookup: a fresh hit returns the value; too old (Prometheus down), never refreshed, or no such series
// all return nil = unknown.
func TestLookupUp(t *testing.T) {
	now := time.Now()
	m := map[string]bool{"node-x": true, "node-y": false}
	cases := []struct {
		name string
		at   time.Time
		id   string
		want *bool
	}{
		{"新鮮 up", now.Add(-besuUpInterval), "node-x", ptr(true)},
		{"新鮮 down", now.Add(-besuUpInterval), "node-y", ptr(false)},
		{"太舊", now.Add(-besuUpMaxAge - time.Second), "node-x", nil},
		{"從未刷新", time.Time{}, "node-x", nil},
		{"沒有這條序列", now, "node-z", nil},
	}
	for _, tc := range cases {
		got := lookupUp(m, tc.at, now, tc.id)
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func ptr(b bool) *bool { return &b }

// Rejected counts use "current value - offset 1h" rather than increase(), so the first occurrence of a reason
// that appears inside the window is not lost; when the difference is negative or offset is missing, the or
// falls back to the current value.
func TestTxPoolRejectedQuery(t *testing.T) {
	var q string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query().Get("query")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[` +
			`{"metric":{"reason":"NONCE_TOO_LOW"},"value":[1700000000,"1"]},` +
			`{"metric":{"reason":"GAS_PRICE_TOO_LOW"},"value":[1700000000,"3"]},` +
			`{"metric":{"reason":"UPFRONT_COST_EXCEEDS_BALANCE"},"value":[1700000000,"0"]}]}}`))
	}))
	defer srv.Close()
	got, err := c(t, srv.URL).TxPoolRejected(context.Background(), "node-a1")
	if err != nil {
		t.Fatalf("TxPoolRejected: %v", err)
	}
	series := `besu_transaction_pool_rejected_total{node="node-a1",layer="txpool"}`
	want := `(sum by (reason) (` + series + `) - sum by (reason) (` + series + ` offset 1h)) >= 0 or sum by (reason) (` + series + `)`
	if q != want {
		t.Errorf("PromQL 不對:\n got %s\nwant %s", q, want)
	}
	if len(got) != 2 || got[0] != (api.TxPoolRejected{Reason: "GAS_PRICE_TOO_LOW", Count: 3}) || got[1] != (api.TxPoolRejected{Reason: "NONCE_TOO_LOW", Count: 1}) {
		t.Errorf("got %v", got)
	}
}

// The alert list uses this to tell whether the monitoring source is down: never refreshed successfully, or
// refreshes stopped too long ago, both count as unreachable.
func TestReachable(t *testing.T) {
	c := &Client{}
	if c.Reachable() {
		t.Error("從未刷新應為 false")
	}
	c.upAt = time.Now().Add(-besuUpInterval)
	if !c.Reachable() {
		t.Error("剛刷新應為 true")
	}
	c.upAt = time.Now().Add(-besuUpMaxAge - time.Second)
	if c.Reachable() {
		t.Error("刷新停太久應為 false")
	}
}

// Classifying a monitoring-source outage: unreachable and 5xx are wrapped as ErrUnreachable; 4xx (bad query)
// and caller cancellation do not count.
func TestDoClassifiesUnreachable(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	defer srv.Close()
	c := New(srv.URL)
	for _, tc := range []struct {
		status int
		want   bool
	}{{http.StatusInternalServerError, true}, {http.StatusBadGateway, true}, {http.StatusServiceUnavailable, true}, {http.StatusBadRequest, false}, {http.StatusUnprocessableEntity, false}} {
		status = tc.status
		_, err := c.do(context.Background(), srv.URL)
		if got := errors.Is(err, ErrUnreachable); err == nil || got != tc.want {
			t.Errorf("狀態 %d: err=%v 算中斷=%v,want %v", tc.status, err, got, tc.want)
		}
	}
	if _, err := New("http://127.0.0.1:1").do(context.Background(), "http://127.0.0.1:1/x"); !errors.Is(err, ErrUnreachable) {
		t.Errorf("連不上要算中斷: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.do(ctx, srv.URL); errors.Is(err, ErrUnreachable) || !errors.Is(err, context.Canceled) {
		t.Errorf("呼叫端取消不算中斷: %v", err)
	}
}

// Whether alerts update in real time: the Prometheus round is fresh, and when Alertmanager is configured it
// must also report ready.
func TestAlertsLiveWatchesAlertmanager(t *testing.T) {
	prom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
	}))
	defer prom.Close()
	amUp := true
	am := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/-/ready" || !amUp {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer am.Close()

	run := func(c *Client) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		go c.RunBesuUp(ctx)
		for i := 0; i < 50 && !c.Reachable(); i++ {
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
		cancel()
	}
	c := New(prom.URL)
	c.SetAlertmanager(am.URL + "/-/reload")
	run(c)
	if !c.AlertsLive() {
		t.Fatal("兩邊都正常應為 true")
	}
	amUp = false
	c2 := New(prom.URL)
	c2.SetAlertmanager(am.URL + "/-/reload")
	run(c2)
	if c2.AlertsLive() {
		t.Fatal("Alertmanager 不 ready 應為 false")
	}
	c3 := New(prom.URL) // no Alertmanager configured: only Prometheus matters
	run(c3)
	if !c3.AlertsLive() {
		t.Fatal("沒設 Alertmanager 只看 Prometheus")
	}
	if !(*Client)(nil).AlertsLive() {
		t.Fatal("沒設 Prometheus 當作可用")
	}
}

func TestEndAlertsPostsToAlertmanager(t *testing.T) {
	var got []map[string]any
	am := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/alerts" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
	}))
	defer am.Close()
	c := New("http://prom.invalid")
	c.SetAlertmanager(am.URL + "/-/reload")
	start := time.Date(2026, 11, 2, 13, 30, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	if err := c.EndAlerts(context.Background(), []EndedAlert{{Labels: map[string]string{"alertname": "NodeOffline", "node": "node-b2"}, StartsAt: start}}, end); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["endsAt"] != end.Format(time.RFC3339) || got[0]["labels"].(map[string]any)["node"] != "node-b2" {
		t.Fatalf("送給 Alertmanager 的內容不對: %v", got)
	}
}

// When Alertmanager returns non-200 we must return an error, not treat it as delivered.
func TestEndAlertsReportsAlertmanagerError(t *testing.T) {
	am := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer am.Close()
	c := New("http://prom.invalid")
	c.SetAlertmanager(am.URL + "/-/reload")
	if err := c.EndAlerts(context.Background(), []EndedAlert{{Labels: map[string]string{"alertname": "NodeOffline"}}}, time.Now()); err == nil {
		t.Fatal("Alertmanager 回 500 應回錯")
	}
}
