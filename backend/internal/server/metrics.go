// bsos_platform_*: the second data source for alert rules. The platform turns events/state in
// Postgres (certificate expiry, node quarantine) into Prometheus gauges, so Prometheus/Alertmanager
// rules cover both measurement alerts (bsos_node_*) and event alerts without a separate rule
// engine.
package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/prom"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	certExpiryDesc = prometheus.NewDesc(
		"bsos_platform_cert_expiry_seconds", "expiry time of the node's latest leaf certificate (unix)", []string{"node_id"}, nil)
	nodeQuarantinedDesc = prometheus.NewDesc(
		"bsos_platform_node_quarantined", "whether the node is quarantined because its mTLS certificate was rejected (1=yes; not a network problem)", []string{"node_id"}, nil)

	// Chain-level numbers for chain-wide alert rules (ChainSlow, ValidatorsMissing, QuorumLost in
	// alerting/defaults.go)
	chainBlockPeriodDesc  = prometheus.NewDesc("bsos_chain_block_period_seconds", "expected seconds per block now (block period when transactions are waiting in the pool, otherwise empty block period)", nil, nil)
	chainValidatorsDesc   = prometheus.NewDesc("bsos_chain_validators", "current number of validators", nil, nil)
	chainNotProducingDesc = prometheus.NewDesc("bsos_chain_validators_not_producing", "number of validators not producing blocks (same check as the validators page)", nil, nil)
	chainToleranceDesc    = prometheus.NewDesc("bsos_chain_fault_tolerance", "fault tolerance f: how many validators can stop producing before the chain halts", nil, nil)
	chainHaltedDesc       = prometheus.NewDesc("bsos_chain_halted", "whether the chain has halted (1=the latest block is older than the \"Blocks severely slow or stopped\" multiple; same halt check as the page)", nil, nil)

	// Transaction pool alerts (NodeTxpoolFull, NodeTxpoolStale). Besu's metrics do not have these
	// two numbers, so the platform computes and exports them by asking each node every round
	// (collector); the label is node, same as the bsos_node_* recording rules, so rules, charts and
	// alert grouping recognize them.
	txpoolUsageDesc  = prometheus.NewDesc(metricName(api.TxpoolUsage), "transaction pool usage in % (transactions in pool / max transactions; not exported for the layered pool, which has no limit)", []string{"node"}, nil)
	txpoolOldestDesc = prometheus.NewDesc(metricName(api.TxpoolOldest), "seconds the oldest transaction in the pool has waited (0 when empty)", []string{"node"}, nil)

	// Blocks behind as computed by the platform from our organization's chain head; rules.yml's
	// bsos_node_blocks_behind takes the larger of this and the node's self-reported value. Carries
	// org: the recording rule merges the two series with max by (node, org).
	headBehindDesc = prometheus.NewDesc("bsos_node_blocks_behind_head", "blocks behind this org's chain head as computed by the platform (0 within tolerance)", []string{"node", "org"}, nil)
)

// platformCollector is pull-based (prometheus.Collector): every scrape queries the DB live, no
// caching (same trade-off as install.go's AgentBinaryChecksumHandler: low volume, infrequent hits).
// If any query fails only that class of metrics is skipped, so a transient error in one subsystem
// does not take down all of /metrics. bsos_chain_* reads the collector cache and does not call RPC.
type platformCollector struct {
	srv *Server
}

func (c *platformCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- certExpiryDesc
	ch <- nodeQuarantinedDesc
	ch <- chainBlockPeriodDesc
	ch <- chainValidatorsDesc
	ch <- chainNotProducingDesc
	ch <- chainToleranceDesc
	ch <- chainHaltedDesc
	ch <- txpoolUsageDesc
	ch <- txpoolOldestDesc
	ch <- headBehindDesc
}

func (c *platformCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if certs, err := c.srv.store.LatestCertPerNode(ctx); err == nil {
		for _, cert := range certs {
			if cert.NodeID == nil {
				continue
			}
			ch <- prometheus.MustNewConstMetric(certExpiryDesc, prometheus.GaugeValue,
				float64(cert.NotAfter.Unix()), *cert.NodeID)
		}
	}

	if nodes, err := c.srv.store.ListNodes(ctx); err == nil {
		for _, n := range nodes {
			if n.QuarantinedAt != nil {
				ch <- prometheus.MustNewConstMetric(nodeQuarantinedDesc, prometheus.GaugeValue, 1, n.ID)
			}
			if r, ok := c.srv.col.Get(n.ID); ok {
				txpoolMetrics(ch, n.ID, r.Snap, time.Now())
				if r.Snap != nil && r.Snap.HeadBehind != nil { // not computed in a round where RPC was unreachable; not emitted
					ch <- prometheus.MustNewConstMetric(headBehindDesc, prometheus.GaugeValue, float64(*r.Snap.HeadBehind), n.ID, n.Org)
				}
			}
		}
	}

	if cs, ok := c.srv.currentChainStats(ctx); ok {
		if cs.blockPeriod > 0 {
			ch <- prometheus.MustNewConstMetric(chainBlockPeriodDesc, prometheus.GaugeValue, cs.blockPeriod)
		}
		ch <- prometheus.MustNewConstMetric(chainValidatorsDesc, prometheus.GaugeValue, float64(cs.validators))
		ch <- prometheus.MustNewConstMetric(chainNotProducingDesc, prometheus.GaugeValue, float64(cs.notProducing))
		ch <- prometheus.MustNewConstMetric(chainToleranceDesc, prometheus.GaugeValue, float64(cs.tolerance))
		halted := 0.0
		if cs.halted {
			halted = 1
		}
		ch <- prometheus.MustNewConstMetric(chainHaltedDesc, prometheus.GaugeValue, halted)
	}
}

// metricName takes the metric name from prom's mapping table (the name rules and charts query),
// instead of keeping a separate copy.
func metricName(m api.MetricId) string {
	name, _ := prom.MetricName(m)
	return name
}

// txpoolMetrics returns one node's transaction pool metrics; if RPC was unreachable this round or
// TXPOOL is not enabled (TxpoolPending nil), none are emitted.
func txpoolMetrics(ch chan<- prometheus.Metric, nodeID string, snap *collector.Snapshot, now time.Time) {
	if snap == nil || snap.Ledger == nil || snap.Ledger.TxpoolPending == nil {
		return
	}
	if snap.TxpoolMax != nil {
		ch <- prometheus.MustNewConstMetric(txpoolUsageDesc, prometheus.GaugeValue, float64(*snap.Ledger.TxpoolPending)/float64(*snap.TxpoolMax)*100, nodeID)
	}
	if snap.TxpoolListed {
		age := 0.0
		if snap.TxpoolOldest != nil {
			age = max(0, now.Sub(*snap.TxpoolOldest).Seconds()) // no negative values when the node clock is ahead of the platform
		}
		ch <- prometheus.MustNewConstMetric(txpoolOldestDesc, prometheus.GaugeValue, age, nodeID)
	}
}

// PlatformMetricsHandler: GET /metrics. Protected by the SD_TOKEN bearer (same as /sd/targets, same
// token, disabled if unset). This is an internal endpoint for Prometheus, not dashboard data for
// humans, and uses a different trust model from the public API.
func PlatformMetricsHandler(srv *Server, token string) http.HandlerFunc {
	reg := prometheus.NewRegistry()
	reg.MustRegister(&platformCollector{srv: srv}, extraCollectors{})
	h := promhttp.HandlerFor(reg, promhttp.HandlerOpts{DisableCompression: true})
	return func(w http.ResponseWriter, r *http.Request) {
		if !sdBearerOK(w, r, token) {
			return
		}
		h.ServeHTTP(w, r)
	}
}

var (
	extraMu sync.Mutex
	extra   []prometheus.Collector
)

// AddPlatformCollector lets Enterprise add its own platform metrics during Wire (e.g. node logs
// failing to store). The /metrics handler is built before Wire, so this does not register directly
// into its registry but goes through extraCollectors.
func AddPlatformCollector(c prometheus.Collector) {
	extraMu.Lock()
	defer extraMu.Unlock()
	extra = append(extra, c)
}

// extraCollectors declares no Desc (unchecked collector); each collector is responsible for keeping
// its metric names unique.
type extraCollectors struct{}

func (extraCollectors) Describe(chan<- *prometheus.Desc) {}

func (extraCollectors) Collect(ch chan<- prometheus.Metric) {
	extraMu.Lock()
	cs := append([]prometheus.Collector(nil), extra...)
	extraMu.Unlock()
	for _, c := range cs {
		c.Collect(ch)
	}
}
