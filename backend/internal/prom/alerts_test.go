package prom

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ActiveAlerts reads Prometheus' /api/v1/alerts (firing and pending alike) and returns only the
// labels of each; a 5xx is reported as unreachable rather than as an empty list, so the reconcile
// round never mistakes an outage for "nothing is firing".
func TestActiveAlerts(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/alerts" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"status":"success","data":{"alerts":[
			{"labels":{"alertname":"NodeOffline","node":"node-a1"},"state":"firing","value":"0"},
			{"labels":{"alertname":"NodeCpuHigh","node":"node-a2"},"state":"pending","value":"95"}]}}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	got, err := c.ActiveAlerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0]["node"] != "node-a1" || got[1]["alertname"] != "NodeCpuHigh" || len(got[0]) != 2 {
		t.Fatalf("labels 不對: %v", got)
	}
	status = http.StatusServiceUnavailable
	if got, err := c.ActiveAlerts(context.Background()); !errors.Is(err, ErrUnreachable) || got != nil {
		t.Fatalf("Prometheus 5xx 應回 unreachable,得 %v %v", got, err)
	}
}

// HasAlertmanager is true only once an address is configured; a nil client (Prometheus not
// configured) has none.
func TestHasAlertmanager(t *testing.T) {
	var none *Client
	c := New("http://prom.invalid")
	if none.HasAlertmanager() || c.HasAlertmanager() {
		t.Fatal("沒設 Alertmanager 應為 false")
	}
	c.SetAlertmanager("")
	if c.HasAlertmanager() {
		t.Fatal("空網址不算設定")
	}
	c.SetAlertmanager("http://am:9093/-/reload")
	if !c.HasAlertmanager() {
		t.Fatal("設了應為 true")
	}
}
