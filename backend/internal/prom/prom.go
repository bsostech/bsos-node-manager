// Package prom is the query client for this org's Prometheus: it turns GetNodeSeries requests into PromQL
// range queries over the history of bsos_node_*: the five host series are gauges emitted by the agent, and
// the chain and txpool series are recording rules Prometheus derives from Besu's native metrics
// (deploy/platform/docker-compose/rules.yml). When PROMETHEUS_URL is not set the caller does not create a
// Client (nil) and GetNodeSeries returns 503.
package prom

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"besu-manager/backend/internal/api"
)

// metric -> {Prometheus series name, unit, Chinese name, English name}. Names are fixed; see the file header
// for their source. The display names are used in alert notification condition text. They were copied by
// hand from the frontend chart titles (i18n detail.* / txpool.* / alerts.metric.*); change one and you must
// change the other.
var profiles = map[api.MetricId]struct{ name, unit, zh, en string }{
	api.Cpu:            {"bsos_node_cpu_percent", "%", "CPU", "CPU"},
	api.Mem:            {"bsos_node_mem_percent", "%", "記憶體", "Memory"},
	api.Disk:           {"bsos_node_disk_percent", "%", "磁碟", "Disk"},
	api.NetRx:          {"bsos_node_net_rx_bytes_per_sec", "B/s", "網路接收", "Net in"},
	api.NetTx:          {"bsos_node_net_tx_bytes_per_sec", "B/s", "網路傳送", "Net out"},
	api.PeerCount:      {"bsos_node_peer_count", "", "已連線 Peers", "Connected peers"},
	api.ChainHeight:    {"bsos_node_chain_height", "", "鏈高度", "Chain height"},
	api.BlocksBehind:   {"bsos_node_blocks_behind", "", "落後區塊", "Blocks behind"},
	api.BlockTime:      {"bsos_node_block_time_seconds", "s", "出塊時間", "Block time"},
	api.TxpoolPending:  {"bsos_node_txpool_pending", "", "交易池待處理", "Txpool pending"},
	api.Tps:            {"bsos_node_tps", "", "TPS", "TPS"},
	api.TxpoolAdded:    {"bsos_node_txpool_added", "tx/s", "進池", "Added to pool"},
	api.TxpoolRemoved:  {"bsos_node_txpool_removed", "tx/s", "出池", "Removed from pool"},
	api.TxpoolRejected: {"bsos_node_txpool_rejected", "tx/min", "被拒", "Rejected"},
	api.JvmHeap:        {"bsos_node_jvm_heap_percent", "%", "Besu 記憶體", "Besu memory"},
	api.TxpoolUsage:    {"bsos_node_txpool_usage_percent", "%", "交易池用量", "Pool usage"},
	api.TxpoolOldest:   {"bsos_node_txpool_oldest_seconds", "s", "交易池最舊等待", "Oldest pool transaction wait"},
}

// MetricName: MetricId -> Prometheus gauge name. ok=false means it is not a known metric. internal/alerting uses it
// as the allowlist when rendering custom alert rules; do not define a separate mapping that could drift.
func MetricName(m api.MetricId) (string, bool) {
	p, ok := profiles[m]
	return p.name, ok
}

// MetricLabel: MetricId -> display name in the notification language and unit, so alert notifications can
// phrase conditions readably like "CPU > 90%".
func MetricLabel(m api.MetricId, lang api.NotifyLanguage) (label, unit string) {
	p := profiles[m]
	if lang == api.NotifyLanguageZh {
		return p.zh, p.unit
	}
	return p.en, p.unit
}

// IsPercent: whether the metric is a percentage (0-100). Used to validate alert thresholds.
func IsPercent(m api.MetricId) bool { return profiles[m].unit == "%" }

// ParseDur: '15m' / '1h' / '3d' -> Duration. Returns (0,false) on failure.
func ParseDur(s string) (time.Duration, bool) {
	if len(s) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 0, false
	}
	switch s[len(s)-1] {
	case 'm':
		return time.Duration(n) * time.Minute, true
	case 'h':
		return time.Duration(n) * time.Hour, true
	case 'd':
		return time.Duration(n) * 24 * time.Hour, true
	}
	return 0, false
}

// Window: parses the time window from range (relative duration) or from/to (absolute epoch seconds). If both
// from and to are given and to>from -> absolute window, overriding range; otherwise relative (back from now,
// default 1h).
func Window(rangeStr string, from, to *int64, now time.Time) (time.Time, time.Time) {
	if from != nil && to != nil && *to > *from {
		return time.Unix(*from, 0).UTC(), time.Unix(*to, 0).UTC()
	}
	d := time.Hour
	if pd, ok := ParseDur(rangeStr); ok {
		d = pd
	}
	return now.Add(-d), now
}

// stepFor: span -> step, targeting ~96 points with a 15s floor (to avoid pulling too finely).
func stepFor(span time.Duration) time.Duration {
	if step := span / 96; step > 15*time.Second {
		return step
	}
	return 15 * time.Second
}

const (
	minStep   = 15 * time.Second
	maxPoints = 1000 // conservative, well below Prometheus's own hard limit of 11000 points, so no caller can make Prometheus return a flood of points
)

// clampStep: whether step was computed by stepFor or explicitly given by the caller, it goes through the same
// safety floor. This is the single checkpoint; every query is protected through it indirectly.
func clampStep(step, span time.Duration) time.Duration {
	if step < minStep {
		step = minStep
	}
	if span > 0 && span/step > maxPoints {
		step = span / maxPoints
	}
	return step
}

type Client struct {
	base string
	hc   *http.Client

	// Cache of up{source="besu"} and Besu heap usage (written by RunBesuUp, read by BesuMetricsUp /
	// JvmHeapPercent).
	upMu sync.RWMutex
	up   map[string]bool
	jvm  map[string]float64
	upAt time.Time
	// Alertmanager address and the last time it reported ready (checked in the same RunBesuUp round); empty
	// am = not configured, not checked.
	am   string
	amAt time.Time
	// Cache of last-24-hours availability and disk days (written by RunHistory).
	hist history

	// RuleThreshold: the current threshold of a default alert rule (by code), read even when the rule is disabled
	// (the page uses the same standard as alerts). The availability bar reads NodeBehind's blocks-behind count, and
	// chain stability reads the ChainSlow / ChainSlowSevere multiples; nil or ok=false uses each one's initial
	// value. main wires it to the rules table, so prom does not depend on store.
	RuleThreshold func(ctx context.Context, preset string) (float64, bool)
}

// New: empty baseURL -> nil (meaning Prometheus is not enabled).
func New(baseURL string) *Client {
	if baseURL == "" {
		return nil
	}
	return &Client{base: baseURL, hc: &http.Client{Timeout: 10 * time.Second}}
}

// Series: sends a range query to this org's Prometheus and returns a node's metric time series over [start,end].
// step: 0 = computed from the span (existing behavior, used by single-node charts); non-zero = explicitly given by
// the caller (used by the fleet overlay chart so all nodes share the same time grid). Either way it goes through
// clampStep, so no caller can pass an overly fine step and make Prometheus return a flood of points.
func (c *Client) Series(ctx context.Context, metric api.MetricId, nodeID string, start, end time.Time, step time.Duration) (api.MetricSeries, error) {
	prof, ok := profiles[metric]
	if !ok {
		return api.MetricSeries{}, fmt.Errorf("prom: unknown metric %s", metric)
	}
	span := end.Sub(start)
	if step <= 0 {
		step = stepFor(span)
	}
	step = clampStep(step, span)
	// max by (node): after the scrape target switched to the platform proxy, the same node has series with
	// two sets of instance labels from before and after the switch; they are all gauges, so max folds them
	// into one and the chart is not misread because of an extra series. The old series disappear once past
	// retention (15d), and this wrapper can be removed then.
	q := fmt.Sprintf("max by (node) (%s{node=%q})", prof.name, nodeID)
	u := fmt.Sprintf("%s/api/v1/query_range?query=%s&start=%d&end=%d&step=%d",
		c.base, url.QueryEscape(q), start.Unix(), end.Unix(), int(step.Seconds()))

	resp, err := c.do(ctx, u)
	if err != nil {
		return api.MetricSeries{}, err
	}
	defer resp.Body.Close()

	var body struct {
		Data struct {
			Result []struct {
				Values [][2]any `json:"values"` // [ unixSec(number), value(string) ]
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return api.MetricSeries{}, err
	}

	unit := prof.unit
	out := api.MetricSeries{Metric: metric, Unit: &unit, Points: []api.MetricPoint{}}
	if len(body.Data.Result) == 0 {
		return out, nil // no data (node not scraped yet) = empty series, not an error
	}
	for _, pair := range body.Data.Result[0].Values {
		ts, _ := pair[0].(float64)
		t := time.Unix(int64(ts), 0).UTC()
		vs, _ := pair[1].(string)
		if v, perr := strconv.ParseFloat(vs, 64); perr == nil {
			vv := v
			out.Points = append(out.Points, api.MetricPoint{T: t, V: &vv})
		} else {
			out.Points = append(out.Points, api.MetricPoint{T: t, V: nil})
		}
	}
	return out, nil
}

// ErrUnreachable: Prometheus is unreachable (transport error, timeout) or failing itself (5xx). Callers return 503
// "監控來源中斷" (monitoring source down) for it;
// 4xx (bad query) and undecodable responses do not count and remain ordinary errors.
var ErrUnreachable = errors.New("prom: monitoring source unreachable")

// do: GETs the Prometheus API and returns only 200 responses (the caller must Close). If the caller cancels
// (page change) it returns ctx.Err(), which does not count as unreachable.
func (c *Client) do(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("%w: status %d", ErrUnreachable, resp.StatusCode)
	}
	return nil, fmt.Errorf("prom: status %d", resp.StatusCode)
}

// besuUpInterval: interval for refreshing up{source="besu"} in the background, aligned with prometheus.yml's
// scrape_interval (15s).
// besuUpMaxAge: if not refreshed successfully for longer than this (Prometheus down), stop trusting the cache
// and return nil = unknown.
// ponytail: both constants are hard-coded; change them together if Prometheus's scrape interval changes.
const (
	besuUpInterval = 15 * time.Second
	besuUpMaxAge   = 3 * besuUpInterval
)

// RunBesuUp: every besuUpInterval in the background, fetches all nodes' Besu metrics scrape results with one
// query and caches them, until ctx ends. The API only reads the cache (BesuMetricsUp), so a slow or dead
// Prometheus never slows down node details.
func (c *Client) RunBesuUp(ctx context.Context) {
	ticker := time.NewTicker(besuUpInterval)
	defer ticker.Stop()
	for {
		if m, err := c.fetchBesuUp(ctx); err == nil {
			jvm, _ := c.queryVector(ctx, qJvmHeap) // if this fails the field is just missing; it does not block the up refresh
			c.upMu.Lock()
			c.up, c.jvm, c.upAt = m, jvm, time.Now()
			c.upMu.Unlock()
		}
		if c.am != "" && c.alertmanagerReady(ctx) {
			c.upMu.Lock()
			c.amAt = time.Now()
			c.upMu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// BesuMetricsUp: whether this node's Besu metrics were scraped on the latest attempt, from the cache; nil =
// unknown (never refreshed successfully, cache too old, or Prometheus has no such series). No I/O.
func (c *Client) BesuMetricsUp(nodeID string) *bool {
	c.upMu.RLock()
	defer c.upMu.RUnlock()
	return lookupUp(c.up, c.upAt, time.Now(), nodeID)
}

// Reachable: whether Prometheus answered a background refresh within the last besuUpMaxAge. No I/O. The alert
// list relies on it to tell whether "no alerts" really means healthy, or the monitoring source is down and
// alerts cannot get through at all.
func (c *Client) Reachable() bool {
	c.upMu.RLock()
	defer c.upMu.RUnlock()
	return upFresh(c.upAt, time.Now())
}

// SetAlertmanager: the Alertmanager that alert notifications pass through; /-/ready is derived from its
// reload address; empty = not checked. Call before RunBesuUp.
func (c *Client) SetAlertmanager(reloadURL string) {
	if c != nil && reloadURL != "" {
		c.am = strings.TrimSuffix(reloadURL, "/-/reload")
	}
}

func (c *Client) alertmanagerReady(ctx context.Context) bool {
	resp, err := c.do(ctx, c.am+"/-/ready")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// AlertsLive: whether alerts update in real time: Prometheus is reachable, and Alertmanager (if configured)
// is reachable. If either is down, the platform receives no new firings or resolutions and alerts stay as
// they were before the outage. No I/O.
func (c *Client) AlertsLive() bool {
	if c == nil {
		return true
	}
	c.upMu.RLock()
	defer c.upMu.RUnlock()
	now := time.Now()
	return upFresh(c.upAt, now) && (c.am == "" || upFresh(c.amAt, now))
}

// ActiveAlerts: labels of alerts Prometheus currently has firing or pending (including ones picked back up
// after a restart that are still in their grace period).
func (c *Client) ActiveAlerts(ctx context.Context) ([]map[string]string, error) {
	resp, err := c.do(ctx, c.base+"/api/v1/alerts")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Data struct {
			Alerts []struct {
				Labels map[string]string `json:"labels"`
			} `json:"alerts"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]map[string]string, len(body.Data.Alerts))
	for i, a := range body.Data.Alerts {
		out[i] = a.Labels
	}
	return out, nil
}

// HasAlertmanager: whether an Alertmanager address is configured (SetAlertmanager).
func (c *Client) HasAlertmanager() bool { return c != nil && c.am != "" }

// EndedAlert: one firing to report to Alertmanager as ended; Labels must be the full set Alertmanager
// originally sent, or the fingerprint will not match.
type EndedAlert struct {
	Labels   map[string]string
	StartsAt time.Time
}

// EndAlerts: tells Alertmanager these firings ended at at. It still sends the "resolved" webhook to the
// platform as usual and updates its own record of what it has notified; if the platform changed the database
// on its own, Alertmanager would think it is still firing and, when the same alert fires again, treat it as
// already notified and not send it.
func (c *Client) EndAlerts(ctx context.Context, alerts []EndedAlert, at time.Time) error {
	type amAlert struct {
		Labels   map[string]string `json:"labels"`
		StartsAt time.Time         `json:"startsAt"`
		EndsAt   time.Time         `json:"endsAt"`
	}
	body := make([]amAlert, len(alerts))
	for i, a := range alerts {
		body[i] = amAlert{a.Labels, a.StartsAt, at}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.am+"/api/v2/alerts", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("alertmanager: status %d", resp.StatusCode)
	}
	return nil
}

func upFresh(at, now time.Time) bool { return fresh(at, now, besuUpMaxAge) }

func lookupUp(m map[string]bool, at, now time.Time, nodeID string) *bool {
	if !upFresh(at, now) {
		return nil
	}
	up, ok := m[nodeID]
	if !ok {
		return nil
	}
	return &up
}

// fetchBesuUp: one instant query for every node's up{source="besu"} (node -> whether it was scraped). The job
// and source labels match prometheus.yml in deploy/platform/docker-compose.
func (c *Client) fetchBesuUp(ctx context.Context) (map[string]bool, error) {
	v, err := c.queryVector(ctx, `max by (node) (up{job="bsos-nodes",source="besu"})`)
	if err != nil {
		return nil, err
	}
	m := make(map[string]bool, len(v))
	for node, up := range v {
		m[node] = up == 1
	}
	return m, nil
}

// rejectedWindow: the fixed window for "被拒原因" (rejection reasons) on the txpool tab.
const rejectedWindow = time.Hour

// TxPoolRejected: a node's rejected transaction counts over the last rejectedWindow by reason, most to least;
// zeros are omitted. Only layer="txpool": Besu counts once at the txpool entry (both txpool implementations
// do), and layered counts again in each layer, so summing everything would double count.
func (c *Client) TxPoolRejected(ctx context.Context, nodeID string) ([]api.TxPoolRejected, error) {
	// No increase(): when a series first appears inside the window, its first sample is taken as the
	// baseline, so the 0->1 jump is not counted and the first occurrence of each reason is lost. Use "current
	// value - value before the window" instead; fall back to the current value when the series did not exist
	// before the window or Besu restarted and reset to zero (negative difference).
	series := fmt.Sprintf(`besu_transaction_pool_rejected_total{node=%q,layer="txpool"}`, nodeID)
	q := fmt.Sprintf(`(sum by (reason) (%[1]s) - sum by (reason) (%[1]s offset %[2]s)) >= 0 or sum by (reason) (%[1]s)`,
		series, promDur(rejectedWindow))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v1/query?query="+url.QueryEscape(q), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prom: query status %d", resp.StatusCode)
	}
	var body struct {
		Data struct {
			Result []struct {
				Metric struct {
					Reason string `json:"reason"`
				} `json:"metric"`
				Value [2]any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := []api.TxPoolRejected{}
	for _, r := range body.Data.Result {
		vs, _ := r.Value[1].(string)
		v, err := strconv.ParseFloat(vs, 64)
		// Counter differences are integers; Round only guards against float error. Omit zero counts
		if n := int(math.Round(v)); err == nil && n > 0 && r.Metric.Reason != "" {
			out = append(out, api.TxPoolRejected{Reason: r.Metric.Reason, Count: n})
		}
	}
	slices.SortFunc(out, func(a, b api.TxPoolRejected) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Reason, b.Reason))
	})
	return out, nil
}

// promDur: Duration -> PromQL duration (1h -> "1h", 90m -> "90m").
func promDur(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dm", int(d/time.Minute))
}
