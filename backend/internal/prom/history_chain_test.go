package prom

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
)

// Chain stability round against a fake Prometheus: it sends the chain-slow range query over the
// last 24 hours, grades each hour with the thresholds of the ChainSlow / ChainSlowSevere rules
// (falling back to the defaults when a rule cannot be read), and ChainStability24h serves the
// cached result only while it is fresh.
func TestFetchChainStabilityAndCache(t *testing.T) {
	now := time.Unix(1_700_100_000, 0)
	start := now.Add(-(availHours - 1) * time.Hour).Unix()
	var gotQuery, gotStart, gotEnd string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query_range" {
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		q := r.URL.Query()
		gotQuery, gotStart, gotEnd = q.Get("query"), q.Get("start"), q.Get("end")
		// ChainSlow has no node label: the series comes back keyed by ""
		fmt.Fprintf(w, `{"data":{"result":[{"metric":{},"values":[[%d,"1"],[%d,"6"],[%d,"25"]]}]}}`,
			start+21*3600, start+22*3600, start+23*3600)
	}))
	defer srv.Close()
	c := New(srv.URL)

	got, err := c.fetchChainStability(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != qChainSlow || gotStart != fmt.Sprint(start) || gotEnd != fmt.Sprint(now.Unix()) {
		t.Fatalf("查詢不對: query=%q start=%s end=%s", gotQuery, gotStart, gotEnd)
	}
	if got[0] != api.HourUnknown || got[21] != api.HourOk || got[22] != api.HourDegraded || got[23] != api.HourDown {
		t.Fatalf("預設門檻 5/20 判定錯: %v", got[20:])
	}

	// Thresholds follow the rules: ChainSlow raised to 10 makes the 6x hour ok; ChainSlowSevere
	// unreadable keeps 20
	var asked []string
	c.RuleThreshold = func(_ context.Context, preset string) (float64, bool) {
		asked = append(asked, preset)
		if preset == "ChainSlow" {
			return 10, true
		}
		return 0, false
	}
	got, err = c.fetchChainStability(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(asked, []string{"ChainSlow", "ChainSlowSevere"}) || got[22] != api.HourOk || got[23] != api.HourDown {
		t.Fatalf("門檻跟規則: asked=%v got=%v", asked, got[20:])
	}

	c.hist.chain, c.hist.chainAt = got, time.Now()
	if cached := c.ChainStability24h(); !slices.Equal(cached, got) {
		t.Fatal("快取沒命中")
	}
	c.hist.chainAt = time.Now().Add(-historyMaxAge - time.Second)
	if stale := c.ChainStability24h(); len(stale) != availHours || stale[23] != api.HourUnknown {
		t.Fatalf("快取太舊應 24 格 unknown: %v", stale)
	}
	var none *Client
	if n := none.ChainStability24h(); len(n) != availHours || n[0] != api.HourUnknown {
		t.Fatal("沒設 Prometheus 應 24 格 unknown")
	}
	if fresh := New(srv.URL).ChainStability24h(); fresh[23] != api.HourUnknown {
		t.Fatal("還沒跑過一輪應 unknown")
	}
}

// Prometheus down: the round returns an error and leaves the cache alone instead of overwriting it
// with unknowns.
func TestFetchChainStabilityError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if got, err := New(srv.URL).fetchChainStability(context.Background(), time.Now()); err == nil || got != nil {
		t.Fatalf("Prometheus 500 應回錯: %v %v", got, err)
	}
}
