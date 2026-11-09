package prom

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
)

// Per-cell decision: timestamp alignment (still aligned when only some timestamps come back), no host point =
// unknown, host or besu ever 0 = down, lagging >= 5 = degraded.
func TestHourStates(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	at := func(i int) int64 { return start.Add(time.Duration(i) * time.Hour).Unix() }
	host := map[int64]float64{}
	for i := 3; i < availHours; i++ { // the first 3 cells have no data (just registered)
		host[at(i)] = 1
	}
	host[at(5)] = 0
	besu := map[int64]float64{at(6): 0, at(7): 1}
	behind := map[int64]float64{at(8): 5, at(9): 4}
	got := hourStates(start, host, besu, behind, 5)
	if len(got) != availHours {
		t.Fatalf("len %d", len(got))
	}
	want := map[int]api.HourState{0: api.HourUnknown, 2: api.HourUnknown, 3: api.HourOk, 5: api.HourDown, 6: api.HourDown, 7: api.HourOk, 8: api.HourDegraded, 9: api.HourOk, 23: api.HourOk}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("格 %d = %s, want %s", i, got[i], w)
		}
	}
	// The lag threshold follows the "節點落後" (node lagging) rule: set to 6 blocks, the cell lagging 5 blocks no
	// longer counts
	if got := hourStates(start, host, besu, behind, 6); got[8] != api.HourOk {
		t.Errorf("門檻 6:格 8 = %s", got[8])
	}
}

// Chain stability: no point = unknown; reaching the severe-slowdown multiple = down, reaching slow =
// degraded; the multiples follow the alert rules.
func TestChainHourStates(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	at := func(i int) int64 { return start.Add(time.Duration(i) * time.Hour).Unix() }
	ratio := map[int64]float64{at(1): 1.2, at(2): 5, at(3): 19.9, at(4): 20, at(23): 0.8}
	got := chainHourStates(start, ratio, 5, 20)
	want := map[int]api.HourState{0: api.HourUnknown, 1: api.HourOk, 2: api.HourDegraded, 3: api.HourDegraded, 4: api.HourDown, 5: api.HourUnknown, 23: api.HourOk}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("格 %d = %s, want %s", i, got[i], w)
		}
	}
	if got := chainHourStates(start, ratio, 10, 20); got[2] != api.HourOk {
		t.Errorf("倍數 10:格 2 = %s", got[2])
	}
}

// Disk days: computable with positive growth; less than 7 days of data, no growth, or over 365 days are not
// computable, each returning its reason. Insufficient data takes precedence (a new node's growth rate is
// unreliable).
func TestDiskDaysLeft(t *testing.T) {
	if e := diskDaysLeft(70, 1.5, 168); e.days == nil || *e.days != 20 || e.reason != "" {
		t.Fatalf("70%% 每天 +1.5%%: %+v", e)
	}
	// Exactly 365 days is still computable (91.25 / 0.25 is exact in floating point).
	if e := diskDaysLeft(8.75, 0.25, 168); e.days == nil || *e.days != 365 {
		t.Fatalf("剛好 365 天應回天數: %+v", e)
	}
	for _, c := range []struct {
		name               string
		pct, perDay, count float64
		want               api.HardwareMetricsDiskDaysLeftReason
	}{
		{"沒成長", 70, 0, 168, api.DiskDaysLeftNotGrowing},
		{"變少", 70, -1, 168, api.DiskDaysLeftNotGrowing},
		{"資料不足", 70, 1.5, 100, api.DiskDaysLeftInsufficientData},
		{"資料不足也沒成長", 70, 0, 3, api.DiskDaysLeftInsufficientData},
		{"超過一年", 10, 0.1, 168, api.DiskDaysLeftOverAYear},
	} {
		if e := diskDaysLeft(c.pct, c.perDay, c.count); e.days != nil || e.reason != c.want {
			t.Errorf("%s 應為 %s,得 %+v", c.name, c.want, e)
		}
	}
}

// Background round against a fake Prometheus: query parsing and cache hits; cache too old, node missing, or
// Prometheus not configured all count as unknown.
func TestFetchHistoryAndCache(t *testing.T) {
	now := time.Unix(1_700_100_000, 0)
	start := now.Add(-(availHours - 1) * time.Hour).Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/query_range") && strings.Contains(q, `source="host"`):
			fmt.Fprintf(w, `{"data":{"result":[{"metric":{"node":"n1"},"values":[[%d,"1"],[%d,"0"]]}]}}`, start+22*3600, start+23*3600)
		case strings.HasPrefix(r.URL.Path, "/api/v1/query_range"):
			fmt.Fprint(w, `{"data":{"result":[]}}`)
		case strings.Contains(q, "deriv"):
			fmt.Fprint(w, `{"data":{"result":[{"metric":{"node":"n1"},"value":[0,"2"]}]}}`)
		case strings.Contains(q, "count_over_time"):
			fmt.Fprint(w, `{"data":{"result":[{"metric":{"node":"n1"},"value":[0,"168"]}]}}`)
		default: // current disk value
			fmt.Fprint(w, `{"data":{"result":[{"metric":{"node":"n1"},"value":[0,"60"]}]}}`)
		}
	}))
	defer srv.Close()
	c := New(srv.URL)
	avail, disk, err := c.fetchHistory(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	a := avail["n1"]
	if a[21] != api.HourUnknown || a[22] != api.HourOk || a[23] != api.HourDown {
		t.Fatalf("對位錯: %v", a[20:])
	}
	if d := disk["n1"].days; d == nil || *d != 20 {
		t.Fatalf("磁碟天數 %+v", disk["n1"])
	}
	c.hist.avail, c.hist.disk, c.hist.at = avail, disk, time.Now()
	if got := c.Availability24h("n1"); !slices.Equal(got, a) {
		t.Fatal("快取沒命中")
	}
	if d, r := c.DiskDaysLeft("n1"); d == nil || *d != 20 || r != nil {
		t.Fatalf("DiskDaysLeft %v %v", d, r)
	}
	if got := c.Availability24h("other"); got[0] != api.HourUnknown || len(got) != availHours {
		t.Fatal("沒有這台應 24 格 unknown")
	}
	c.hist.at = time.Now().Add(-historyMaxAge - time.Second)
	if got := c.Availability24h("n1"); got[23] != api.HourUnknown || !noDisk(c, "n1") {
		t.Fatal("快取太舊應當不知道")
	}
	var none *Client
	if got := none.Availability24h("n1"); len(got) != availHours || got[0] != api.HourUnknown || !noDisk(none, "n1") || none.JvmHeapPercent("n1") != nil {
		t.Fatal("沒設 Prometheus 應當不知道")
	}
}

// Disk queries fail: availability still updates, there is just no day count.
func TestFetchHistoryDiskFailureKeepsAvailability(t *testing.T) {
	now := time.Unix(1_700_100_000, 0)
	start := now.Add(-(availHours - 1) * time.Hour).Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/query_range") {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, `{"data":{"result":[{"metric":{"node":"n1"},"values":[[%d,"1"]]}]}}`, start+23*3600)
	}))
	defer srv.Close()
	avail, disk, err := New(srv.URL).fetchHistory(context.Background(), now)
	if err != nil || avail["n1"][23] != api.HourOk || len(disk) != 0 {
		t.Fatalf("avail=%v disk=%v err=%v", avail["n1"], disk, err)
	}
}

// The instant query for Besu heap usage parses into node -> percent; NaN (max of 0) counts as no value.
func TestQueryVectorJvmHeap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") != qJvmHeap {
			t.Errorf("query = %s", r.URL.Query().Get("query"))
		}
		fmt.Fprint(w, `{"data":{"result":[{"metric":{"node":"n1"},"value":[0,"5.25"]},{"metric":{"node":"n2"},"value":[0,"NaN"]}]}}`)
	}))
	defer srv.Close()
	v, err := New(srv.URL).queryVector(context.Background(), qJvmHeap)
	if _, nan := v["n2"]; err != nil || v["n1"] != 5.25 || nan {
		t.Fatalf("v=%v err=%v", v, err)
	}
}

// noDisk: disk unknown (neither days nor reason).
func noDisk(c *Client, node string) bool {
	d, r := c.DiskDaysLeft(node)
	return d == nil && r == nil
}
