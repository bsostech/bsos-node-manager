// Package collector implements Collector: at a fixed interval it assembles a snapshot for each of our own nodes
// and caches the latest live values for handlers. Unreachable = Agent/node offline.
//
// The Besu layers are built by the platform calling JSON-RPC itself (rpcsnap.go); the host layer is requested from the agent's /api/v1/host-metrics.
// Both go as reverse requests over the mTLS WebSocket the agent dials out; transport details live on the pki side.
package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/enode"
	"besu-manager/backend/internal/pki"
	"besu-manager/backend/internal/rpcops"
	"besu-manager/backend/internal/store"
)

// Snapshot: live values for one node in one round (field shape = the contract component schemas).
type Snapshot struct {
	Hardware  *api.HardwareMetrics `json:"hardware"`
	P2p       *api.P2PStatus       `json:"p2p"`
	Ledger    *api.LedgerSummary   `json:"ledger"`
	Consensus *api.ConsensusStatus `json:"consensus"`
	// RpcAuth: the platform probes node RPC without a token to see whether auth is enabled ("enabled" / "disabled"; empty if the probe failed).
	// Auth is managed by the node operator; the platform only displays it and does not enforce it.
	RpcAuth string    `json:"rpcAuth,omitempty"`
	AsOf    time.Time `json:"asOf"`
	// Allowlist: the node's current account allowlist (perm_getAccountsAllowlist); nil = unreadable, reason in AllowlistStatus.
	// Used only for drift comparison and "import from node" on the platform; not part of the snapshot JSON.
	Allowlist       *[]string       `json:"-"`
	AllowlistStatus AllowlistStatus `json:"-"`
	// PendingVotes: the node's local pending validator votes (*_getPendingVotes), address -> true add / false remove; nil = unreadable.
	// An entry leaves only when the node discards it or Besu restarts, not when the vote takes effect. Used by the vote list and
	// residual-vote cleanup; not part of the snapshot JSON.
	PendingVotes map[string]bool `json:"-"`
	// RpcUnauthorized: RPC calls this round were rejected by the node with 401 (so Ledger is nil). Lets the capability list tell "rejected" from "no response".
	RpcUnauthorized bool `json:"-"`
	// MissingAPIs: RPC namespaces the platform needs that the node has not enabled (see missingAPIs); nil when RPC is unreachable.
	MissingAPIs []string `json:"-"`
	// Peers: connections seen in admin_peers (public keys proven by handshake). Used for the peer list and connection status on the validators page; not part of the snapshot JSON.
	Peers []ObservedPeer `json:"-"`
	// LogsConnected: whether Besu is shipping logs to the agent; nil = could not ask (the /logs request failed or got no reply this round).
	LogsConnected *bool `json:"-"`
	// AgentVersion: build version reported by the agent (/agent-status); nil = not asked this round.
	AgentVersion *string `json:"-"`
	// AgentRuntime: how the agent runs, as it reported (/agent-status); nil = not asked this round.
	AgentRuntime *api.AgentRuntime `json:"-"`
	// P2PPort / P2POpen: the Besu P2P port the agent was asked to probe this round (from the enode; 0 = unknown, not probed) and the result (nil = not probed or no reply).
	// Whether Besu is running is decided by Result.BesuRunning from this together with RPC.
	P2PPort int   `json:"-"`
	P2POpen *bool `json:"-"`
	// ClientVersion: web3_clientVersion (e.g. besu/v25.4.1/...); nil = not asked this round. Only for display in the node list.
	ClientVersion *string `json:"-"`
	// ChainID: eth_chainId; nil = not asked this round.
	ChainID *int64 `json:"-"`
	// TxpoolMax: txpool transaction cap (only the sequenced txpool has one; layered is sized by capacity, so nil).
	// TxpoolListed / TxpoolOldest: whether the full pool listing was read this round, and the entry time of its oldest transaction (nil for an empty pool). Only for the platform's exported txpool alert metrics (server/metrics.go).
	TxpoolMax    *int       `json:"-"`
	TxpoolListed bool       `json:"-"`
	TxpoolOldest *time.Time `json:"-"`
	// EpochLength: IBFT2 / QBFT epoch length (from the admin_nodeInfo chain config); nil = could not ask. On-chain vote tallies reset at each epoch boundary.
	EpochLength *int64 `json:"-"`
	// BlockPeriod: block period set in genesis (seconds, read from the same place); nil = could not ask or not set in genesis.
	BlockPeriod *float64 `json:"-"`
	// Forks: hard forks present in the chain config (read from the same place, see forkSchedule); nil = could not ask.
	Forks *[]Fork `json:"-"`
	// EnodeErr: net_enode was unreadable or undecodable this round (so P2p.Enode is nil); the collector logs once per node and clears it on recovery.
	EnodeErr error `json:"-"`
	// HeadBehind: blocks behind computed by the platform from our org's chain head (see headBehind); nil = not computed this round (RPC unreachable, no chain head).
	// The node's self-reported value is in Ledger.BlocksBehind; they are stored separately because chain-head selection looks only at self-reported values.
	HeadBehind *int `json:"-"`
}

// BlocksBehind: the larger of the node's self-reported value and the platform's chain-head value; nil if neither exists.
func (s *Snapshot) BlocksBehind() *int {
	var self *int
	if s.Ledger != nil {
		self = s.Ledger.BlocksBehind
	}
	if self == nil || (s.HeadBehind != nil && *s.HeadBehind > *self) {
		return s.HeadBehind
	}
	return self
}

// RpcAuthEnabled: converts the probe result into the contract's tri-state (true / false / nil = unknown).
func (s *Snapshot) RpcAuthEnabled() *bool {
	switch s.RpcAuth {
	case "enabled":
		v := true
		return &v
	case "disabled":
		v := false
		return &v
	}
	return nil
}

// AllowlistStatus: the result of reading the account allowlist this round. Zero value AllowlistUnknown = not read (whole RPC batch unreachable, Ledger is nil).
type AllowlistStatus int

const (
	AllowlistUnknown      AllowlistStatus = iota
	AllowlistReadable                     // Allowlist has a value
	AllowlistPermMissing                  // PERM namespace not enabled (rpcops.ErrPermUnavailable)
	AllowlistFileDisabled                 // account allowlist file not enabled (rpcops.ErrAllowlistDisabled)
	AllowlistReadFailed                   // any other error
)

// DisabledErr: returns the matching rpcops error when the account allowlist is not enabled (either reason counts), otherwise nil.
func (e AllowlistStatus) DisabledErr() error {
	switch e {
	case AllowlistPermMissing:
		return rpcops.ErrPermUnavailable
	case AllowlistFileDisabled:
		return rpcops.ErrAllowlistDisabled
	}
	return nil
}

// AllowlistEnabled: whether the account allowlist file is enabled; nil = unknown (RPC unreachable or PERM namespace not enabled).
func (s *Snapshot) AllowlistEnabled() *bool {
	var v bool
	switch {
	case s.Allowlist != nil:
		v = true
	case s.AllowlistStatus == AllowlistFileDisabled:
		v = false
	default:
		return nil
	}
	return &v
}

// ObservedPeer: one entry from the node's admin_peers.
type ObservedPeer struct {
	Pubkey        string // lowercase hex, 128 characters
	Enode         string
	RemoteAddress string
	Inbound       bool
	// LatestBlock: Besu's estimate of the peer's head block number (protocols.eth.latestBlock); nil = field absent or not estimated yet (0 right after connecting).
	// It is a +/-1 estimate: on receiving a broadcast new block it records the block's parent.
	LatestBlock *int64
}

// Result: the latest fetch result for a node; Snap == nil means unreachable.
// EverSeen distinguishes "never fetched successfully (unknown)" from "was online, now unreachable (offline)". FailSince marks the start of the current unreachable stretch (see GetFleetStatus); the timer is not reset within the same outage and is cleared on recovery.
// Whether a node is quarantined (cert revoked but still dialing in) is not tracked here: the platform never dials nodes, so that is decided on the handshake
// when the agent dials in and written to nodes.quarantined_at. Readers always consult the DB, not this cache.
type Result struct {
	Snap      *Snapshot
	At        time.Time
	EverSeen  bool
	FailSince *time.Time
	// P2PSeenPort: the P2P port most recently found listening (0 = never found). Kept through rounds where the agent is offline; reset when the platform restarts.
	P2PSeenPort int
}

// BesuRunning: whether Besu is running. RPC reachable or P2P port listening = true; neither reachable and this port was listening before = false;
// anything else (agent offline, port unknown, this port never connected) = nil: with setups like a docker internal network that does not publish the port, a failed probe does not mean it stopped.
func (r Result) BesuRunning() *bool {
	s := r.Snap
	if s == nil {
		return nil
	}
	switch {
	case s.Ledger != nil, s.P2POpen != nil && *s.P2POpen:
		return new(true)
	case s.P2POpen != nil && s.P2PPort == r.P2PSeenPort:
		return new(false)
	}
	return nil
}

// nextResult: pure function computing the new cache value from the previous result + this round's fetch (split out for unit tests without spinning up HTTP).
func nextResult(prev Result, snap *Snapshot, now time.Time) Result {
	seen := snap != nil || prev.EverSeen // once seen, stays true
	var failSince *time.Time
	if snap == nil {
		if prev.Snap == nil && prev.FailSince != nil {
			failSince = prev.FailSince // continue the same outage; do not reset the timer
		} else {
			failSince = &now
		}
	}
	seenPort := prev.P2PSeenPort
	if snap != nil && snap.P2POpen != nil && *snap.P2POpen {
		seenPort = snap.P2PPort
	}
	return Result{Snap: snap, At: now, EverSeen: seen, FailSince: failSince, P2PSeenPort: seenPort}
}

// Requester: asks the online agent of a given node for data. Implemented by pki.Service (reverse requests over the WebSocket the agent dials out);
// an interface instead of depending on that package directly, because the collector only needs "ask a question, get an answer".
type Requester interface {
	Request(ctx context.Context, nodeID, path string) (status int, body string, err error)
}

// probeEvery: nodes with auth enabled have their RPC auth probed every this many rounds. Probing every round leaves a 401 line in the Besu log, which is needlessly frequent; in between the last result is reused.
// Nodes without auth (or unreadable) are probed every round: a 200 leaves no 401, and the setup guide needs to see auth turn on within seconds.
const probeEvery = 12

// requestTimeout: timeout for a single fetch.
const requestTimeout = 5 * time.Second

// hostMetricsTimeout: cap for asking the agent for host metrics (gopsutil reads a cache; normally microseconds).
const hostMetricsTimeout = 2 * time.Second

type Collector struct {
	store *store.Store
	req   Requester
	rpc   RPC
	round int // incremented by the single pollAll goroutine

	mu    sync.RWMutex
	cache map[string]Result // node id -> latest result
	// known: reported enodes read from the DB in the latest round. Re-read from the DB every round, so when a node is deleted and rejoins, the next round sees the mismatch and rewrites it.
	known map[string]string
	// knownRuntime: agent runtimes stored in the DB, refreshed with known; a reported runtime is written only when it differs.
	knownRuntime map[string]string
	// lastNodes: the node list from the last successful read; when the DB is temporarily unreadable, requests continue from it, since a DB hiccup should not abort the whole round of fetches.
	lastNodes []string
	chain     ChainView // chain-level cache (see chain.go)
	// backfilling: a backfill of this epoch's votes is running; noOlder: the epoch start at which a backfill found the source node lacked older blocks (-1 = none); that epoch is not retried. Both are read and written under mu.
	backfilling bool
	noOlder     int64
	// head / headSource: the latest block from the lightweight chain-head update and the source node at that time (see RunHead); read and written under mu.
	head       *BlockStamp
	headSource string
	// enodeWarned: nodes already logged for unreadable net_enode, logged once per failure stretch; read and written under mu.
	enodeWarned map[string]bool
}

// New: req asks the agent for host metrics (pki.Service); rpc calls JSON-RPC on nodes (rpcops.Client).
func New(st *store.Store, req Requester, rpc RPC) *Collector {
	return &Collector{store: st, req: req, rpc: rpc, cache: map[string]Result{}, noOlder: -1, enodeWarned: map[string]bool{}}
}

// Get returns the node's latest cached result; ok=false means the node has not been asked yet.
func (c *Collector) Get(nodeID string) (Result, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.cache[nodeID]
	return r, ok
}

// Run asks the agents of all own nodes for snapshots at a fixed interval until ctx ends.
func (c *Collector) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	c.pollAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.pollAll(ctx)
		}
	}
}

func (c *Collector) pollAll(ctx context.Context) {
	ids := c.nodeIDs(ctx)
	probe := c.round%probeEvery == 0
	c.round++
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			prev, _ := c.Get(id)
			snap := c.fetch(ctx, id, prev, probe)
			if snap != nil && snap.Ledger != nil && prev.Snap != nil {
				// This round's HeadBehind is computed only after all nodes are asked (markHeadBehind); for the few seconds in between keep last round's value,
				// otherwise the status flashes back to healthy, /metrics misses a round, and NodeBehind's duration gets reset
				snap.HeadBehind = prev.Snap.HeadBehind
			}
			c.warnEnode(id, snap)
			c.recordIdentity(ctx, id, snap)
			now := time.Now().UTC()
			c.mu.Lock()
			defer c.mu.Unlock()
			c.cache[id] = nextResult(c.cache[id], snap, now)
		}(id)
	}
	wg.Wait()
	c.markHeadBehind()
	c.updateChain(ctx)
}

// markHeadBehind: after this round's queries, fills in each node's lag (HeadBehind) from our org's chain head. Snapshots are replaced with new copies before being put back,
// so old pointers held by other goroutines are not modified.
func (c *Collector) markHeadBehind() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, v := range headBehind(c.cache, c.chain.BlockPeriod) {
		r := c.cache[id]
		snap := *r.Snap
		snap.HeadBehind = &v
		r.Snap = &snap
		c.cache[id] = r
	}
}

// headBehind: pure function. Chain head = the highest height among nodes reporting themselves healthy (same as chainSource); each node's lag = chain head - its own height,
// treated as 0 below the tolerance. Within a round, eth_blockNumber is not called on all nodes at the same moment (host metrics, logs and agent status are asked first),
// so the skew can be several seconds; faster block production means more blocks of skew, so the tolerance scales with the block period.
func headBehind(cache map[string]Result, blockPeriod float64) map[string]int {
	_, head := chainSource(cache)
	if head < 0 {
		return nil
	}
	tol := 2
	if blockPeriod > 0 {
		tol = max(tol, int(math.Ceil(headSkew.Seconds()/blockPeriod)))
	}
	out := map[string]int{}
	for id, r := range cache {
		if r.Snap == nil || r.Snap.Ledger == nil || r.Snap.Ledger.ChainHeight == nil {
			continue
		}
		d := int(head - *r.Snap.Ledger.ChainHeight)
		if d < tol {
			d = 0
		}
		out[id] = d
	}
	return out
}

// headSkew: upper bound on how far apart heights within a round can be in time (host metrics timeout 2 seconds, plus logs, agent status and RPC itself).
const headSkew = 5 * time.Second

// nodeIDs: nodes to ask this round. Offline nodes are not filtered out here: Request fails immediately when the
// agent is not connected.
func (c *Collector) nodeIDs(ctx context.Context) []string {
	nodes, err := c.store.ListNodes(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastNodes = pollTargets(nodes, err, c.lastNodes)
	if err == nil {
		c.known = make(map[string]string, len(nodes))
		c.knownRuntime = make(map[string]string, len(nodes))
		for _, n := range nodes {
			if n.ReportedEnode != nil {
				c.known[n.ID] = *n.ReportedEnode
			}
			if n.AgentRuntime != nil {
				c.knownRuntime[n.ID] = *n.AgentRuntime
			}
		}
	}
	return c.lastNodes
}

// pollTargets: use the new list when the DB is readable, otherwise keep last round's (reason under lastNodes).
func pollTargets(nodes []store.Node, err error, last []string) []string {
	if err != nil {
		return last
	}
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	return ids
}

// warnEnode: logs once when net_enode is unreadable or undecodable (otherwise node identity would silently stay empty); resets once readable, so the next failure is logged again.
// A whole RPC batch being unreachable (Ledger is nil) is not logged here; that is an RPC issue on the capability list.
func (c *Collector) warnEnode(nodeID string, snap *Snapshot) {
	if snap == nil || snap.Ledger == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if snap.EnodeErr == nil {
		delete(c.enodeWarned, nodeID)
		return
	}
	if !c.enodeWarned[nodeID] {
		c.enodeWarned[nodeID] = true
		log.Printf("collector: node %s: cannot read or parse net_enode, node identity (enode, validator address) not updated: %v", nodeID, snap.EnodeErr)
	}
}

// recordIdentity: writes the enode from net_enode and the derived validator address back to nodes.
// Join does not carry these two values, so this is where they come from; write only when they differ from the DB (first read, node changed address, deleted and rejoined).
func (c *Collector) recordIdentity(ctx context.Context, nodeID string, snap *Snapshot) {
	if c.store == nil || snap == nil || snap.P2p == nil || snap.P2p.Enode == nil || *snap.P2p.Enode == "" {
		return
	}
	en := *snap.P2p.Enode
	c.mu.RLock()
	same := c.known[nodeID] == en
	c.mu.RUnlock()
	if same {
		return
	}
	addr, err := enode.Address(en)
	if err != nil {
		return
	}
	if err := c.store.SetNodeIdentity(ctx, nodeID, addr, en); err != nil {
		return // retry next round
	}
	c.mu.Lock()
	c.known[nodeID] = en // the next round's nodeIDs re-reads from the DB and overwrites this
	c.mu.Unlock()
}

// fetch: assembles one node's snapshot for this round; returns nil (= unreachable; the caller treats it as offline) only when the host layer fails and RPC gives no response at all.
// No ledger layer = agent present, RPC unreachable (Ledger is nil; the caller shows rpc_unreachable).
func (c *Collector) fetch(ctx context.Context, nodeID string, prev Result, probe bool) *Snapshot {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	snap := &Snapshot{AsOf: time.Now().UTC()}
	// The host layer gets its own short timeout: it being slow should not eat into the RPC layer's time and get a node with a healthy Besu marked offline.
	hctx, hcancel := context.WithTimeout(ctx, hostMetricsTimeout)
	defer hcancel()
	if status, body, err := c.req.Request(hctx, nodeID, pki.HostMetricsPath); err == nil && status == http.StatusOK {
		var hw api.HardwareMetrics
		if json.Unmarshal([]byte(body), &hw) == nil {
			snap.Hardware = &hw
		}
	}
	// Whether Besu is connected to the log ingest: only the latest line is needed to check connected (the host layer's short timeout applies too).
	if l, err := c.Logs(hctx, nodeID, nil, 1); err == nil {
		snap.LogsConnected = &l.Connected
	}
	c.agentStatus(hctx, nodeID, snap)
	var prevHeight int64
	if prev.Snap != nil && prev.Snap.Ledger != nil && prev.Snap.Ledger.ChainHeight != nil {
		prevHeight = *prev.Snap.Ledger.ChainHeight
	}
	if err := rpcLayers(ctx, c.rpc, nodeID, snap, prevHeight); err != nil {
		// When the host layer fails, it counts as unreachable only if RPC also gave no response at all. An RPC error (401, a 502 because Besu has no RPC) means
		// the tunnel works and the agent is online; a host metrics failure that just happened (timeout, agent just started and has not sampled yet) must not mark it offline.
		if snap.Hardware == nil && errors.Is(err, rpcops.ErrNoResponse) {
			return nil
		}
		snap.RpcUnauthorized = errors.Is(err, rpcops.ErrUnauthorized)
		return snap
	}
	if probe || prev.Snap == nil || prev.Snap.RpcAuth != "enabled" {
		snap.RpcAuth = c.rpc.ProbeAuth(ctx, nodeID)
	} else {
		snap.RpcAuth = prev.Snap.RpcAuth
	}
	return snap
}

// saveRuntime stores a newly reported agent runtime; a failed write is retried next round.
func (c *Collector) saveRuntime(ctx context.Context, nodeID, rt string) {
	c.mu.RLock()
	same := c.knownRuntime[nodeID] == rt
	c.mu.RUnlock()
	if same || c.store == nil {
		return
	}
	if c.store.SetNodeAgentRuntime(ctx, nodeID, rt) != nil {
		return
	}
	c.mu.Lock()
	if c.knownRuntime == nil {
		c.knownRuntime = map[string]string{}
	}
	c.knownRuntime[nodeID] = rt
	c.mu.Unlock()
}

// agentStatus: asks the agent for its version and to probe the P2P port in this node's enode (no port sent if the enode is not read yet). Leaves everything nil if it cannot ask.
func (c *Collector) agentStatus(ctx context.Context, nodeID string, snap *Snapshot) {
	c.mu.RLock()
	en := c.known[nodeID]
	c.mu.RUnlock()
	path := "/agent-status"
	if port, err := enode.Port(en); err == nil {
		snap.P2PPort = port
		path += "?p2pPort=" + strconv.Itoa(port)
	}
	status, body, err := c.req.Request(ctx, nodeID, path)
	if err != nil || status != http.StatusOK {
		return
	}
	var st api.AgentStatus
	if json.Unmarshal([]byte(body), &st) != nil {
		return
	}
	if st.Version != "" {
		snap.AgentVersion = &st.Version
	}
	if st.Runtime != nil && st.Runtime.Valid() {
		snap.AgentRuntime = st.Runtime
		c.saveRuntime(ctx, nodeID, string(*st.Runtime))
	}
	if snap.P2PPort != 0 { // BesuRunning relies on this: no port probe means no P2POpen
		snap.P2POpen = st.P2pOpen
	}
}

// Logs: asks the agent for a slice of its log buffer. after nil = the latest limit lines; limit 0 = the agent's default.
// The platform does not store logs; this only relays and validates the shape.
func (c *Collector) Logs(ctx context.Context, nodeID string, after *int64, limit int) (api.NodeLogs, error) {
	q := url.Values{}
	if after != nil {
		q.Set("after", strconv.FormatInt(*after, 10))
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	status, body, err := c.req.Request(ctx, nodeID, "/logs?"+q.Encode())
	if err != nil {
		return api.NodeLogs{}, err
	}
	if status != http.StatusOK {
		return api.NodeLogs{}, fmt.Errorf("agent /logs returned HTTP %d", status)
	}
	var l api.NodeLogs
	if err := json.Unmarshal([]byte(body), &l); err != nil {
		return api.NodeLogs{}, fmt.Errorf("agent /logs: decode response: %w", err)
	}
	if l.Lines == nil {
		l.Lines = []api.NodeLogLine{}
	}
	return l, nil
}

// Status derives the node status from the snapshot.
// No ledger layer = agent present, RPC unreachable.
func (s *Snapshot) Status() api.NodeStatus {
	st := s.reportedStatus()
	if st == api.NodeStatusHealthy && s.HeadBehind != nil && *s.HeadBehind > 0 {
		return api.NodeStatusSyncing
	}
	return st
}

// reportedStatus: only the node's self-reported status (excluding HeadBehind). Chain-head selection uses it so it never loops back to values computed by us.
func (s *Snapshot) reportedStatus() api.NodeStatus {
	if s.Ledger == nil {
		return api.NodeStatusRpcUnreachable
	}
	if s.P2p != nil && s.P2p.InSync != nil && !*s.P2p.InSync {
		return api.NodeStatusSyncing
	}
	if s.Ledger != nil && s.Ledger.BlocksBehind != nil && *s.Ledger.BlocksBehind > 0 {
		return api.NodeStatusSyncing
	}
	return api.NodeStatusHealthy
}
