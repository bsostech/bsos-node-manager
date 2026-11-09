// Package server implements the handlers of the Platform API (contract/openapi.yaml). The node
// inventory comes from PostgreSQL, live values from the Collector, and time series from our
// organization's Prometheus.
package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"besu-manager/backend/internal/alerting"
	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/auth"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/prom"
	"besu-manager/backend/internal/rpcops"
	"besu-manager/backend/internal/store"
)

type Server struct {
	store          *store.Store
	col            *collector.Collector   // live-value cache of our own agents
	auth           *auth.Service          // AuthN/RBAC
	org            string                 // our organization's org id
	platformAddr   string                 // address the agent dials back to on join (returned with a new join token and in /me)
	publicAPIBase  string                 // address nodes use to reach the platform over HTTP (PUBLIC_API_BASE); empty = unset
	agentBinaryDir string                 // AGENT_BINARY_DIR: the offline install bundle is packed from here; empty = no agent binary
	rootDigest     string                 // SHA-256 of the embedded CA root certificate (join token anti-MITM)
	prom           *prom.Client           // our organization's Prometheus queries (nil = PROMETHEUS_URL unset, time series return 503)
	ops            NodeOps                // the platform issues JSON-RPC to nodes directly (internal/rpcops)
	alertSvc       *alerting.Service      // alert notification settings (SMTP/Webhook, see internal/alerting)
	rulesSvc       *alerting.RulesService // custom alert rules (see internal/alerting/rules.go)
	rpcSigner      *auth.RpcSigner        // node RPC token signing (public key goes to nodes, private key stays on the platform)

	// voteMu makes manual propose/withdraw mutually exclusive with the automatic withdrawal of
	// leftover votes. A node's pending-vote list is keyed by address; if someone votes a new
	// direction for the same address after the automatic withdrawal reads the list but before it
	// withdraws, the new vote is what gets withdrawn. voteTouched: address (0x + lowercase) -> the
	// last time the platform voted or withdrew on it; automatic withdrawal skips recently touched
	// ones (votecleanup.go).
	voteMu      sync.Mutex
	voteTouched map[string]time.Time

	// peersMissing: per node, the ones sent last round that did not connect (only used to log on
	// change); what was sent is stored in nodes.pushed_peers.
	peerMu       sync.Mutex
	peersMissing map[string]string

	// pushedAllowlists: node id -> pushedAllowlist, the list read back after the last successful push.
	pushedAllowlists sync.Map

	// agentOutdated: the last time this node was rejected because its agent protocol version was
	// too old (pki.Service.AgentOutdatedAt); nil = not wired.
	agentOutdated func(nodeID string) *time.Time

	// sendAccountMail sends the temporary-password mail (ACCOUNT_SMTP_*, see Deps.AccountMail); nil
	// = not configured at deploy time, cannot send.
	sendAccountMail func(ctx context.Context, to, subject, body string) error
}

// NodeOps is the JSON-RPC operations the platform issues to nodes (implemented by rpcops.Client).
// It is an interface so server tests do not need to bring up a tunnel.
type NodeOps interface {
	ProposeVote(ctx context.Context, nodeID, consensus, address string, add bool) error
	DiscardVote(ctx context.Context, nodeID, consensus, address string) error
	PendingVotes(ctx context.Context, nodeID, consensus string) (map[string]bool, error)
	ReadAllowlist(ctx context.Context, nodeID string) ([]string, error)
	SyncAllowlist(ctx context.Context, nodeID string, want []string) (have []string, err error)
	SyncPeers(ctx context.Context, nodeID string, want, drop []string) (rpcops.PeerSync, error)
	SetLogLevel(ctx context.Context, nodeID, level string) error
	TxPool(ctx context.Context, nodeID, from string) (rpcops.TxPool, error)
}

var _ api.StrictServerInterface = (*Server)(nil)

// Deps is what building a Server needs. Anything not given is nil / empty: tests fill in only what
// they use.
type Deps struct {
	Store          *store.Store
	Collector      *collector.Collector
	Auth           *auth.Service
	Org            string
	PlatformAddr   string // address the agent dials back to on join (returned with a new join token and in /me)
	PublicAPIBase  string // PUBLIC_API_BASE; a trailing / is stripped
	RootDigest     string
	AgentBinaryDir string
	Prom           *prom.Client // nil = PROMETHEUS_URL unset
	Ops            NodeOps
	AlertSvc       *alerting.Service
	RulesSvc       *alerting.RulesService
	RpcSigner      *auth.RpcSigner
	AccountMail    *alerting.MailConfig // mail server for temporary-password mail (ACCOUNT_SMTP_*); nil = unset, cannot send
	// AgentOutdated: the last time this node was rejected because its agent protocol version was
	// too old (pki.Service.AgentOutdatedAt); nil = not wired
	AgentOutdated func(nodeID string) *time.Time
}

func New(d Deps) *Server {
	s := &Server{
		store: d.Store, col: d.Collector, auth: d.Auth, org: d.Org, platformAddr: d.PlatformAddr,
		publicAPIBase: strings.TrimRight(d.PublicAPIBase, "/"), rootDigest: d.RootDigest, agentBinaryDir: d.AgentBinaryDir,
		prom: d.Prom, ops: d.Ops, alertSvc: d.AlertSvc, rulesSvc: d.RulesSvc, rpcSigner: d.RpcSigner, agentOutdated: d.AgentOutdated,
		peersMissing: map[string]string{}, voteTouched: map[string]time.Time{},
	}
	if c := d.AccountMail; c != nil {
		cfg := *c
		s.sendAccountMail = func(ctx context.Context, to, subject, body string) error {
			return alerting.SendMail(ctx, cfg, to, subject, body)
		}
	}
	return s
}

// ── GET /nodes ──────────────────────────────────────────────

func (s *Server) ListNodes(ctx context.Context, req api.ListNodesRequestObject) (api.ListNodesResponseObject, error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	additions, err := s.additionCounts(ctx)
	if err != nil {
		return nil, err
	}
	set := s.validatorSet(nodes)
	out := make([]api.NodeSummary, 0, len(nodes))
	var oldest time.Time // the oldest collection; the frontend's "updated N seconds ago" is based on this, not on when the browser received the response
	for _, n := range nodes {
		res, _ := s.col.Get(n.ID)
		if res.Snap != nil && (oldest.IsZero() || res.Snap.AsOf.Before(oldest)) {
			oldest = res.Snap.AsOf
		}
		sum := summarize(n, res, now)
		sum.AgentOutdatedAt = s.agentOutdatedAt(n.ID)
		if sum.IsValidator == nil {
			sum.IsValidator = knownValidator(n.ValidatorAddress, set)
		}
		mode := peerModeOf(n, additions[n.ID])
		sum.PeerMode = &mode
		// Taken from the Prometheus background cache (not waiting on Prometheus); heap usage is
		// given only when the agent answered this round, so stale values are not used while
		// disconnected
		avail := s.prom.Availability24h(n.ID)
		sum.Availability24h = &avail
		if res.Snap != nil {
			sum.JvmHeapPercent = f32(s.prom.JvmHeapPercent(n.ID))
		}
		sum.Capabilities = s.capabilities(n.ID, n.AgentRuntime, res.Snap)
		sum.BesuRunning = res.BesuRunning()
		out = append(out, sum)
	}
	return api.ListNodes200JSONResponse{Nodes: out, AsOf: now, DataAgeSec: dataAge(oldest, now)}, nil
}

// dataAge is how many seconds passed from collection to the response; both times are server clock,
// so the frontend using it is unaffected by browser clock skew. Returns nil if never collected.
func dataAge(at, now time.Time) *float32 {
	if at.IsZero() {
		return nil
	}
	age := float32(max(0, now.Sub(at).Seconds()))
	return &age
}

func (s *Server) agentOutdatedAt(nodeID string) *time.Time {
	if s.agentOutdated == nil {
		return nil
	}
	return s.agentOutdated(nodeID)
}

func summarize(n store.Node, res collector.Result, now time.Time) api.NodeSummary {
	sum := api.NodeSummary{
		Id:            n.ID,
		Name:          n.Name,
		Org:           n.Org,
		Address:       n.ValidatorAddress, // the node's own address, whether or not it is a validator
		AsOf:          now,
		QuarantinedAt: n.QuarantinedAt,
		ReportedEnode: n.ReportedEnode,
	}
	sum.RpcPort, sum.MetricsPort = &n.RpcPort, &n.MetricsPort
	// res is the true live value from the Collector cache. A joined node always has a collection
	// source (the WS the agent dials in on), so there is no "no source" state to distinguish.
	sum.Status = ownStatus(res)
	if res.Snap == nil {
		return sum
	}
	snap := res.Snap
	sum.AsOf = snap.AsOf
	lastSeen := snap.AsOf
	sum.LastSeen = &lastSeen
	// Host metrics are unrelated to RPC: when RPC is unreachable the agent can still report CPU and
	// disk
	if h := snap.Hardware; h != nil {
		sum.CpuPercent, sum.DiskUsedPercent = h.CpuPercent, h.DiskUsedPercent
		if h.MemUsedBytes != nil && h.MemTotalBytes != nil && *h.MemTotalBytes > 0 {
			p := float32(float64(*h.MemUsedBytes) / float64(*h.MemTotalBytes) * 100)
			sum.MemPercent = &p
		}
	}
	if snap.Ledger == nil {
		return sum
	}
	sum.ChainHeight = snap.Ledger.ChainHeight
	sum.TxpoolPending = snap.Ledger.TxpoolPending
	sum.BlocksBehind = snap.BlocksBehind()
	sum.ClientVersion = snap.ClientVersion
	if snap.P2p != nil {
		sum.PeerCount = snap.P2p.PeerCount
	}
	if snap.Consensus != nil {
		sum.IsValidator = snap.Consensus.IsValidator
	}
	sum.RpcAuthEnabled = snap.RpcAuthEnabled()
	return sum
}

// ownStatus is the status of an own node. A node the collector has not polled yet has a zero-value
// Result, which also lands on unknown.
func ownStatus(res collector.Result) api.NodeStatus {
	switch {
	case res.Snap == nil && !res.EverSeen:
		return api.NodeStatusUnknown
	case res.Snap == nil:
		return api.NodeStatusOffline
	}
	return res.Snap.Status()
}

// capabilities is the capability list. agent / RPC come from the collector's result this round;
// metrics looks at the platform Prometheus's latest scrape (Prometheus unset, not in the cache or
// too old, or agent offline = null; no separate probing).
// storedRuntime is the agent runtime last saved in the DB, used while the agent is offline (that is when a re-join is needed).
func (s *Server) capabilities(nodeID string, storedRuntime *string, snap *collector.Snapshot) *api.NodeCapabilities {
	c := &api.NodeCapabilities{MissingRpcApis: []string{}}
	if storedRuntime != nil {
		rt := api.AgentRuntime(*storedRuntime)
		c.AgentRuntime = &rt
	}
	if snap != nil {
		c.Agent = true
		reachable := snap.Ledger != nil
		c.RpcReachable = &reachable
		c.RpcUnauthorized = snap.RpcUnauthorized
		if snap.MissingAPIs != nil {
			c.MissingRpcApis = snap.MissingAPIs
		}
		c.LogsConnected = snap.LogsConnected
		c.AgentVersion = snap.AgentVersion
		if snap.AgentRuntime != nil {
			c.AgentRuntime = snap.AgentRuntime
		}
		c.AllowlistEnabled = snap.AllowlistEnabled()
	}
	// When the agent is offline the platform cannot scrape metrics anyway; what is left in
	// Prometheus are pre-disconnect values, which are not shown. Reads the cache refreshed in the
	// background by prom (RunBesuUp); no I/O.
	if s.prom != nil && snap != nil {
		c.MetricsReachable = s.prom.BesuMetricsUp(nodeID)
	}
	return c
}

// ── GET /nodes/{id} ─────────────────────────────────────────

func (s *Server) GetNode(ctx context.Context, req api.GetNodeRequestObject) (api.GetNodeResponseObject, error) {
	n, err := s.store.GetNode(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return api.GetNode404JSONResponse(nodeNotFoundError), nil
	}

	now := time.Now().UTC()
	detail := api.NodeDetail{
		Id:              n.ID,
		Name:            n.Name,
		Org:             n.Org,
		AsOf:            now,
		QuarantinedAt:   n.QuarantinedAt,
		AgentOutdatedAt: s.agentOutdatedAt(n.ID),
	}

	detail.ValidatorAddress = n.ValidatorAddress // written by the platform after reading net_enode (null before Besu comes up)
	detail.ReportedEnode = n.ReportedEnode
	detail.RpcPort, detail.MetricsPort = &n.RpcPort, &n.MetricsPort
	res, _ := s.col.Get(n.ID)
	detail.Status = ownStatus(res)
	detail.Capabilities = s.capabilities(n.ID, n.AgentRuntime, res.Snap)
	detail.BesuRunning = res.BesuRunning()
	switch {
	case res.Snap == nil:
	case res.Snap.Ledger == nil: // agent present, RPC unreachable: hardware layer only
		detail.AsOf = res.Snap.AsOf
		detail.Hardware = res.Snap.Hardware
	default:
		snap := res.Snap
		detail.AsOf = snap.AsOf
		detail.Hardware = snap.Hardware
		detail.P2p = snap.P2p
		if snap.Ledger != nil {
			l := *snap.Ledger
			l.BlocksBehind = snap.BlocksBehind() // same as the list: the larger of the self-reported value and the one the platform computes from the chain head
			detail.Ledger = &l
		}
		detail.Consensus = snap.Consensus
		detail.ClientVersion = snap.ClientVersion
		detail.RpcAuthEnabled = snap.RpcAuthEnabled()
	}
	if detail.Consensus == nil || detail.Consensus.IsValidator == nil { // consensus unreadable (Besu stopped): decide validator status from the validator set
		nodes, err := s.store.ListNodes(ctx)
		if err != nil {
			return nil, err
		}
		if v := knownValidator(n.ValidatorAddress, s.validatorSet(nodes)); v != nil {
			c := api.ConsensusStatus{}
			if detail.Consensus != nil {
				c = *detail.Consensus // cache is shared; copy before modifying
			}
			c.IsValidator = v
			detail.Consensus = &c
		}
	}
	if detail.Hardware != nil { // cache is shared; copy before modifying
		hw := *detail.Hardware
		days, reason := s.prom.DiskDaysLeft(n.ID)
		hw.DiskDaysLeft, hw.DiskDaysLeftReason = f32(days), reason
		detail.Hardware = &hw
	}
	return api.GetNode200JSONResponse(detail), nil
}

// f32 converts a number from the Prometheus cache to the contract's float32; nil stays nil.
func f32(v *float64) *float32 {
	if v == nil {
		return nil
	}
	f := float32(*v)
	return &f
}

// DELETE /nodes/{id}: platform-side removal: revoke all of the node's valid certificates (the deny
// watcher closes its connection within 5s) + delete the node inventory entry and settings. besu on
// the VM is unaffected (full teardown is bsos-uninstall).
func (s *Server) DeleteNode(ctx context.Context, req api.DeleteNodeRequestObject) (api.DeleteNodeResponseObject, error) {
	n, err := s.store.GetNode(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return api.DeleteNode404JSONResponse(nodeNotFoundError), nil
	}

	d := map[string]any{"node_id": req.Id}
	if err := s.audit(ctx, "node_delete", d); err != nil {
		return nil, err
	}
	revoked, err := s.store.RevokeNodeCerts(ctx, req.Id, "node deleted", "platform")
	if err != nil {
		s.auditFailedInternal(ctx, "node_delete", d, err)
		return nil, err
	}
	// Its unused join codes are revoked with it: once the id is free, such a code would join it again
	if err := s.store.DeleteNode(ctx, req.Id, withActor(ctx, map[string]any{"reason": "node_deleted"})); err != nil {
		// The certificates are already revoked; this entry must state how many were revoked
		s.auditFailedInternal(ctx, "node_delete", map[string]any{"node_id": req.Id, "revoked_certs": revoked}, err)
		return nil, err
	}
	// How many were revoked is only known afterwards, so add a completion entry
	s.auditDone(ctx, "node_delete", map[string]any{"node_id": req.Id, "revoked_certs": revoked})
	return api.DeleteNode204Response{}, nil
}

// certStatus reports whether the latest certificate **issued** by the platform is still trusted.
// notAfter == nil = no record (e.g. after a CA reset). There is no "expiring soon": 1/3 of validity
// remaining is the renewal point, not an anomaly.
func certStatus(notAfter *time.Time, revoked bool, now time.Time) api.CertStatus {
	if notAfter == nil || revoked || notAfter.Before(now) {
		return api.Untrusted
	}
	return api.Valid
}

// revokedSet turns the revoked serials into a set. Callers may compare against the whole node list,
// and a linear scan would be O(nodes x revocations).
func (s *Server) revokedSet(ctx context.Context) (map[string]bool, error) {
	serials, err := s.store.RevokedSerials(ctx)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(serials))
	for _, sr := range serials {
		set[sr] = true
	}
	return set, nil
}

// GET /nodes/{id}/cert: node certificate / connection status (top of the settings page). Connection
// = whether the collector can reach the agent; certificate = the latest in issued_certificates (no
// record/revoked/expired -> untrusted, which directly surfaces "old certificate does not match
// after a CA reset": only a certificate signed by the current CA is valid).
func (s *Server) GetNodeCert(ctx context.Context, req api.GetNodeCertRequestObject) (api.GetNodeCertResponseObject, error) {
	n, err := s.store.GetNode(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return api.GetNodeCert404JSONResponse(nodeNotFoundError), nil
	}

	out := api.NodeCertStatus{Connection: api.NodeCertStatusConnectionLost, Cert: api.Untrusted}

	if res, ok := s.col.Get(n.ID); ok && res.Snap != nil {
		out.Connection = api.NodeCertStatusConnectionConnected
		ls := res.Snap.AsOf
		out.LastSeen = &ls
	}

	cert, err := s.store.LatestCertForNode(ctx, n.ID)
	if err != nil {
		return nil, err
	}
	if cert != nil {
		revoked, err := s.revokedSet(ctx)
		if err != nil {
			return nil, err
		}
		serial, na := cert.Serial, cert.NotAfter.UTC()
		out.Serial, out.NotAfter = &serial, &na
		out.Cert = certStatus(&na, revoked[cert.Serial], time.Now().UTC())
	}
	// cert == nil -> stays untrusted (no certificate record for the current CA, e.g.
	// issued_certificates cleared after a CA reset)

	return api.GetNodeCert200JSONResponse(out), nil
}

// ── GET /nodes/{id}/series ──────────────────────────────────

// monitoringDownError: the monitoring source is down (found by the background round, or the query
// itself cannot connect).
var monitoringDownError = apierr.New(api.ErrorCodeMonitoringUnavailable, api.ErrorReasonMonitoringDown, nil)

func (s *Server) GetNodeSeries(ctx context.Context, req api.GetNodeSeriesRequestObject) (api.GetNodeSeriesResponseObject, error) {
	n, err := s.store.GetNode(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return api.GetNodeSeries404JSONResponse(nodeNotFoundError), nil
	}

	now := time.Now().UTC()
	rng := "1h"
	if req.Params.Range != nil {
		rng = string(*req.Params.Range)
	}
	// Parse the time window: range (relative) or from/to (absolute, drag-to-zoom).
	start, end := prom.Window(rng, req.Params.From, req.Params.To, now)
	// step: used as given when the fleet overlay chart specifies it (so multiple nodes share the
	// same time grid); otherwise 0, and prom.Series computes it from the span.
	var step time.Duration
	if req.Params.Step != nil {
		step = time.Duration(*req.Params.Step) * time.Second
	}

	// No Prometheus means no time series: return 503 rather than drawing fake data (a
	// realistic-looking fake chart from a misconfigured deployment is worse than a blank one).
	// Frontend development uses MSW.
	if s.prom == nil {
		return api.GetNodeSeries503JSONResponse(apierr.New(api.ErrorCodeMonitoringUnavailable, api.ErrorReasonMonitoringNotConfigured, nil)), nil
	}
	// A down monitoring source returns 503, same as the "監控來源中斷" (monitoring source down) alert:
	// check the background round first (so each chart does not wait for the full timeout when the
	// host is unresponsive), and rely on the query's own error for the window right after a
	// disconnect that the round has not caught yet
	if !s.promReachable() {
		return api.GetNodeSeries503JSONResponse(monitoringDownError), nil
	}
	ms, err := s.prom.Series(ctx, req.Params.Metric, n.ID, start, end, step)
	if errors.Is(err, prom.ErrUnreachable) {
		return api.GetNodeSeries503JSONResponse(monitoringDownError), nil
	}
	if err != nil {
		return nil, err
	}
	return api.GetNodeSeries200JSONResponse(ms), nil
}
