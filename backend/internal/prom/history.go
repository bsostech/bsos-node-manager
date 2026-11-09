package prom

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"sync"
	"time"

	"besu-manager/backend/internal/api"
)

// The node list's "Besu 記憶體" (Besu memory), the last-24-hours availability, and the node detail's "磁碟還能撐"
// (disk days left).
// Like up{source="besu"}, these are queried in the background and the API only reads the cache: the list never
// waits on Prometheus.

const (
	availHours = 24
	// Values used when the rule thresholds cannot be read (same as those rules' initial values): blocks
	// behind for a lagging node, and the multipliers for slow / severely slow block production.
	defaultBehind     = 5
	defaultSlowX      = 5
	defaultSlowSevere = 20
	// Apart from the 15-second up round, these two are fresh enough when queried every 5 minutes (one cell
	// per hour, disk counted in days); not refreshed successfully for more than 3 rounds means unknown.
	historyInterval = 5 * time.Minute
	historyMaxAge   = 3 * historyInterval
	diskWindowHours = 7 * 24
	diskMaxDays     = 365
)

// offlineFor: a node counts as down only after being unscrapable this long (same as the default durations of
// NodeOffline and BesuMetricsDown; hard-coded, not tied to the rules).
const offlineFor = "2m"

// qUp: a node counts as down only if it was unscrapable over that window; if the platform was down during the
// same window, it does not count. Prometheus scrapes nodes through the platform proxy, so while the platform
// is down every node reads 0, and it stays 0 for the few dozen seconds after recovery while agents reconnect.
// Both windows must be the same length: for samples after the platform recovers, the node's look-back window
// must not still contain the zeros from the outage. Not shared with alerting's unlessPlatformDown: that one
// only looks at the present, and after recovery relies on the rule duration to hold off.
func qUp(source string) string {
	return `min by (node) (min_over_time((max_over_time(up{job="bsos-nodes",source="` + source + `"}[` + offlineFor + `])` +
		` unless on () min_over_time(up{job="bsos-platform"}[` + offlineFor + `]) == 0)[1h:15s]))`
}

var (
	qHostUp = qUp("host")
	qBesuUp = qUp("besu")
)

const (
	qBehind   = `max by (node) (max_over_time(bsos_node_blocks_behind[1h]))`
	qDiskNow  = `max by (node) (bsos_node_disk_percent)`
	qDiskRate = `deriv((max by (node) (bsos_node_disk_percent))[7d:1h]) * 86400`
	qDiskN    = `count_over_time((max by (node) (bsos_node_disk_percent))[7d:1h])`
	qJvmHeap  = `max by (node) (bsos_node_jvm_heap_percent)`
	// The hourly multiple of the oldest chain head age over the normal block time; same quantity as the
	// ChainSlow alert (alerting/defaults.go chainSlowExpr). One difference: block 0 does not count. At that
	// point the chain head is the genesis block, whose timestamp was set when genesis was written; using it
	// would mark a new chain as stalled for its first hour. The alert does not exclude it: a chain stuck at
	// block 0 (not enough validators) is a real reason to page someone.
	qChainSlow = `max_over_time(((min(timestamp(besu_blockchain_chain_head_timestamp{job="bsos-nodes"}) - besu_blockchain_chain_head_timestamp{job="bsos-nodes"}) / on () bsos_chain_block_period_seconds) and on () max(ethereum_blockchain_height{job="bsos-nodes"}) > 0)[1h:15s])`
)

type history struct {
	mu      sync.RWMutex
	avail   map[string][]api.HourState
	disk    map[string]diskEstimate
	at      time.Time
	chain   []api.HourState
	chainAt time.Time
}

// RunHistory: queries availability and disk every historyInterval in the background and caches the result,
// until ctx ends.
func (c *Client) RunHistory(ctx context.Context) {
	ticker := time.NewTicker(historyInterval)
	defer ticker.Stop()
	for {
		if avail, disk, err := c.fetchHistory(ctx, time.Now()); err == nil {
			c.hist.mu.Lock()
			c.hist.avail, c.hist.disk, c.hist.at = avail, disk, time.Now()
			c.hist.mu.Unlock()
		}
		if chain, err := c.fetchChainStability(ctx, time.Now()); err == nil {
			c.hist.mu.Lock()
			c.hist.chain, c.hist.chainAt = chain, time.Now()
			c.hist.mu.Unlock()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Availability24h: one cell per hour for this node over the last 24 hours; if the cache is too old or lacks
// this node, 24 unknown cells. No I/O. Same when c is nil (Prometheus not configured).
func (c *Client) Availability24h(nodeID string) []api.HourState {
	if c != nil {
		c.hist.mu.RLock()
		defer c.hist.mu.RUnlock()
		if a, ok := c.hist.avail[nodeID]; ok && fresh(c.hist.at, time.Now(), historyMaxAge) {
			return a
		}
	}
	return unknownHours()
}

func unknownHours() []api.HourState {
	out := make([]api.HourState, availHours)
	for i := range out {
		out[i] = api.HourUnknown
	}
	return out
}

// ChainStability24h: the chain's block production per hour over the last 24 hours; if the cache is too old or
// Prometheus is not configured, 24 unknown cells. No I/O.
func (c *Client) ChainStability24h() []api.HourState {
	if c != nil {
		c.hist.mu.RLock()
		defer c.hist.mu.RUnlock()
		if c.hist.chain != nil && fresh(c.hist.chainAt, time.Now(), historyMaxAge) {
			return c.hist.chain
		}
	}
	return unknownHours()
}

// threshold: the rule's current threshold; def if it cannot be read.
func (c *Client) threshold(ctx context.Context, preset string, def float64) float64 {
	if c.RuleThreshold != nil {
		if v, ok := c.RuleThreshold(ctx, preset); ok {
			return v
		}
	}
	return def
}

func (c *Client) fetchChainStability(ctx context.Context, now time.Time) ([]api.HourState, error) {
	start := now.Add(-(availHours - 1) * time.Hour)
	m, err := c.queryMatrix(ctx, qChainSlow, start, now)
	if err != nil {
		return nil, err
	}
	return chainHourStates(start, m[""], c.threshold(ctx, "ChainSlow", defaultSlowX), c.threshold(ctx, "ChainSlowSevere", defaultSlowSevere)), nil
}

// chainHourStates: pure function. The hourly multiple of the oldest chain head (timestamp -> value, aligned
// like hourStates): reaching severe = down, reaching slow = degraded, no point = unknown.
func chainHourStates(start time.Time, ratio map[int64]float64, slow, severe float64) []api.HourState {
	out := make([]api.HourState, availHours)
	for i := range out {
		v, ok := ratio[start.Add(time.Duration(i)*time.Hour).Unix()]
		switch {
		case !ok:
			out[i] = api.HourUnknown
		case v >= severe:
			out[i] = api.HourDown
		case v >= slow:
			out[i] = api.HourDegraded
		default:
			out[i] = api.HourOk
		}
	}
	return out
}

// diskEstimate: how many days the disk has left; when the days cannot be computed, reason explains why
// (exactly one of the two is set).
type diskEstimate struct {
	days   *float64
	reason api.HardwareMetricsDiskDaysLeftReason
}

// DiskDaysLeft: how many days this node's disk has left, or the reason when it cannot be computed; both nil
// when unknown (no data, cache too old). No I/O.
func (c *Client) DiskDaysLeft(nodeID string) (*float64, *api.HardwareMetricsDiskDaysLeftReason) {
	if c == nil {
		return nil, nil
	}
	c.hist.mu.RLock()
	defer c.hist.mu.RUnlock()
	e, ok := c.hist.disk[nodeID]
	if !ok || !fresh(c.hist.at, time.Now(), historyMaxAge) {
		return nil, nil
	}
	if e.days != nil {
		return e.days, nil
	}
	return nil, &e.reason
}

// JvmHeapPercent: what % of this Besu's Java heap is used (cache from the 15-second round); nil when unknown.
// No I/O.
func (c *Client) JvmHeapPercent(nodeID string) *float64 {
	if c == nil {
		return nil
	}
	c.upMu.RLock()
	defer c.upMu.RUnlock()
	if v, ok := c.jvm[nodeID]; ok && upFresh(c.upAt, time.Now()) {
		return &v
	}
	return nil
}

func fresh(at, now time.Time, maxAge time.Duration) bool {
	return !at.IsZero() && now.Sub(at) <= maxAge
}

func (c *Client) fetchHistory(ctx context.Context, now time.Time) (map[string][]api.HourState, map[string]diskEstimate, error) {
	start := now.Add(-(availHours - 1) * time.Hour)
	host, err := c.queryMatrix(ctx, qHostUp, start, now)
	if err != nil {
		return nil, nil, err
	}
	besu, err := c.queryMatrix(ctx, qBesuUp, start, now)
	if err != nil {
		return nil, nil, err
	}
	behind, err := c.queryMatrix(ctx, qBehind, start, now)
	if err != nil {
		return nil, nil, err
	}
	// The lag threshold is shared with alerting (the page uses the same standard as alerts): read the current
	// threshold of the "節點落後" (node lagging) rule, even if the rule is disabled
	th := c.threshold(ctx, "NodeBehind", defaultBehind)
	avail := map[string][]api.HourState{}
	for node, h := range host {
		avail[node] = hourStates(start, h, besu[node], behind[node], th)
	}
	// A failed disk query does not affect availability: that round just has no day count
	disk := map[string]diskEstimate{}
	var vec [3]map[string]float64
	for i, q := range []string{qDiskNow, qDiskRate, qDiskN} {
		if vec[i], err = c.queryVector(ctx, q); err != nil {
			return avail, disk, nil
		}
	}
	for node, pct := range vec[0] {
		disk[node] = diskDaysLeft(pct, vec[1][node], vec[2][node])
	}
	return avail, disk, nil
}

// hourStates: pure function. Points from three range queries (unix-second timestamp -> value) mapped by time
// onto 24 cells: cell i is the point at timestamp start + i hours, representing the hour up to that moment. A
// range query returns only timestamps that have data, so it cannot be filled in order. No point on the host
// query = unknown. Lagging >= behindAt blocks = degraded.
func hourStates(start time.Time, host, besu, behind map[int64]float64, behindAt float64) []api.HourState {
	out := make([]api.HourState, availHours)
	for i := range out {
		ts := start.Add(time.Duration(i) * time.Hour).Unix()
		h, ok := host[ts]
		switch {
		case !ok:
			out[i] = api.HourUnknown
		case h == 0:
			out[i] = api.HourDown
		case besuDown(besu, ts):
			out[i] = api.HourDown
		case behind[ts] >= behindAt:
			out[i] = api.HourDegraded
		default:
			out[i] = api.HourOk
		}
	}
	return out
}

func besuDown(besu map[int64]float64, ts int64) bool {
	v, ok := besu[ts]
	return ok && v == 0
}

// diskDaysLeft: pure function. (100 - current %) / daily growth %; returns a reason when there is less than 7
// days of data (one point per hour, one missing point tolerated), no growth, or more than 365 days.
// Insufficient data is checked first: a new node that has run only a few hours has no reliable growth rate
// yet, so we cannot say it is "not growing".
func diskDaysLeft(percent, perDay, points float64) diskEstimate {
	switch {
	case points < diskWindowHours-1:
		return diskEstimate{reason: api.DiskDaysLeftInsufficientData}
	case perDay <= 0:
		return diskEstimate{reason: api.DiskDaysLeftNotGrowing}
	}
	d := max((100-percent)/perDay, 0) // usage over 100% (measurement error) is treated as full
	if d > diskMaxDays {
		return diskEstimate{reason: api.DiskDaysLeftOverAYear}
	}
	return diskEstimate{days: &d}
}

// queryVector: instant query, returns node -> value.
func (c *Client) queryVector(ctx context.Context, q string) (map[string]float64, error) {
	var res []struct {
		Metric struct {
			Node string `json:"node"`
		} `json:"metric"`
		Value [2]any `json:"value"`
	}
	if err := c.get(ctx, "/api/v1/query?query="+url.QueryEscape(q), &res); err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(res))
	for _, r := range res {
		if v, ok := sampleValue(r.Value); ok {
			out[r.Metric.Node] = v
		}
	}
	return out, nil
}

// queryMatrix: range query (step 1h), returns node -> timestamp -> value.
func (c *Client) queryMatrix(ctx context.Context, q string, start, end time.Time) (map[string]map[int64]float64, error) {
	var res []struct {
		Metric struct {
			Node string `json:"node"`
		} `json:"metric"`
		Values [][2]any `json:"values"`
	}
	path := fmt.Sprintf("/api/v1/query_range?query=%s&start=%d&end=%d&step=3600", url.QueryEscape(q), start.Unix(), end.Unix())
	if err := c.get(ctx, path, &res); err != nil {
		return nil, err
	}
	out := make(map[string]map[int64]float64, len(res))
	for _, r := range res {
		pts := make(map[int64]float64, len(r.Values))
		for _, p := range r.Values {
			ts, _ := p[0].(float64)
			if v, ok := sampleValue(p); ok {
				pts[int64(ts)] = v
			}
		}
		out[r.Metric.Node] = pts
	}
	return out, nil
}

// sampleValue: the numeric value of a sample. NaN / Inf (e.g. division by a heap max of 0) count as no value:
// they cannot be JSON-encoded and would fail the whole list.
func sampleValue(p [2]any) (float64, bool) {
	s, _ := p[1].(string)
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil && !math.IsNaN(v) && !math.IsInf(v, 0)
}

// get: GETs the Prometheus API and decodes data.result into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	resp, err := c.do(ctx, c.base+path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var body struct {
		Data struct {
			Result json.RawMessage `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return err
	}
	return json.Unmarshal(body.Data.Result, out)
}
