package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/enode"
	"besu-manager/backend/internal/rpcops"
)

// The platform builds the snapshot by calling node RPC itself, replacing the agent's /api/v1/snapshot.
// Two HTTP requests per node per round: the first batch of 16 calls gets the height and everything else; the second batch of 2 fetches the two blocks needed for block time (the height must be known first). When the pool has transactions, one more request pulls the full pool listing (see poolOldest).
// Both the IBFT2 and QBFT method sets are called; whichever returns data is the chain's consensus (the other returns method not found, which does not affect the other calls).

// sampleWindow: blockTime is derived from timestamp differences over the latest N blocks.
const sampleWindow = 10

// RPC: calls JSON-RPC on a node (implemented by rpcops.Client).
type RPC interface {
	Batch(ctx context.Context, nodeID string, calls []rpcops.Call) ([]rpcops.Result, error)
	ProbeAuth(ctx context.Context, nodeID string) string
}

var firstBatch = []rpcops.Call{
	{Method: "eth_blockNumber"},
	{Method: "eth_syncing"},
	{Method: "net_peerCount"},
	{Method: "admin_nodeInfo"},
	{Method: "txpool_besuStatistics"},
	{Method: "ibft_getValidatorsByBlockNumber", Params: []any{"latest"}},
	{Method: "qbft_getValidatorsByBlockNumber", Params: []any{"latest"}},
	{Method: "ibft_getSignerMetrics"},
	{Method: "qbft_getSignerMetrics"},
	{Method: "ibft_getPendingVotes"},
	{Method: "qbft_getPendingVotes"},
	{Method: "perm_getAccountsAllowlist"},
	{Method: "admin_peers"},
	{Method: "web3_clientVersion"},
	{Method: "eth_chainId"},
	{Method: "net_enode"},
}

// txpoolListMax: above this many transactions the full pool listing is not fetched (about 130 bytes each; 20k is about 2.6MB, within the tunnel's 8MB response limit).
const txpoolListMax = 20000

// Matches the order of firstBatch.
const (
	iHeight = iota
	iSyncing
	iPeerCount
	iNodeInfo
	iTxpool
	iIbftValidators
	iQbftValidators
	iIbftSigners
	iQbftSigners
	iIbftPending
	iQbftPending
	iAllowlist
	iPeers
	iClientVersion
	iChainID
	iNetEnode
)

// SignerWindow: how many recent blocks the block-production distribution covers (*_getSignerMetrics without arguments makes Besu look at only 100 blocks).
const SignerWindow = 1000

// batchFor: the batch to send this round. The production distribution needs an explicit block range to go beyond Besu's default 100 blocks; the range is computed from the height read last round
// ([prevHeight-1000, prevHeight), end exclusive; genesis is excluded since it has no proposer). This round's height is only known once the same batch comes back.
// A node's first round (no previous height yet) has no production distribution; it appears from the next round (10 seconds) on.
func batchFor(prevHeight int64) []rpcops.Call {
	calls := append([]rpcops.Call(nil), firstBatch...)
	if prevHeight > 0 {
		r := []any{fmt.Sprintf("0x%x", max(1, prevHeight-SignerWindow)), fmt.Sprintf("0x%x", prevHeight)}
		calls[iIbftSigners].Params, calls[iQbftSigners].Params = r, r
	}
	return calls
}

// rpcLayers: the Besu layers. error = the whole batch was unreachable (agent offline, node RPC not up, 401); the caller leaves the Besu layers empty.
func rpcLayers(ctx context.Context, rpc RPC, nodeID string, snap *Snapshot, prevHeight int64) error {
	res, err := rpc.Batch(ctx, nodeID, batchFor(prevHeight))
	if err != nil {
		return err
	}
	var heightHex string
	if err := res[iHeight].Decode(&heightHex); err != nil {
		return err // if even the height cannot be read, treat Besu as not up; same semantics as the agent version
	}
	height, _ := hexInt64(heightHex)
	sync := syncStatus(res[iSyncing])

	snap.Ledger = &api.LedgerSummary{ChainHeight: &height}
	behind := sync.blocksBehind
	snap.Ledger.BlocksBehind = &behind
	var stats struct {
		LocalCount  int `json:"localCount"`
		RemoteCount int `json:"remoteCount"`
		MaxSize     int `json:"maxSize"` // the layered txpool returns -1
	}
	if res[iTxpool].Decode(&stats) == nil { // skip if TXPOOL is not enabled
		pending := stats.LocalCount + stats.RemoteCount
		snap.Ledger.TxpoolPending = &pending
		if stats.MaxSize > 0 {
			snap.TxpoolMax = &stats.MaxSize
		}
		snap.TxpoolOldest, snap.TxpoolListed = poolOldest(ctx, rpc, nodeID, pending)
	}
	blockTiming(ctx, rpc, nodeID, height, snap.Ledger)

	p := &api.P2PStatus{}
	inSync := !sync.syncing
	p.InSync = &inSync
	var peerHex string
	if res[iPeerCount].Decode(&peerHex) == nil {
		n, _ := hexInt64(peerHex)
		peers := int(n)
		p.PeerCount = &peers
	}
	// Node identity (enode -> validator address) comes from net_enode: NET is a required namespace, so we can map which local node is which validator even without ADMIN.
	var self string
	if err := res[iNetEnode].Decode(&self); err != nil {
		snap.EnodeErr = err
		self = ""
	} else if _, err := enode.Address(self); err != nil {
		snap.EnodeErr = err
		self = ""
	} else {
		p.Enode = &self
	}
	snap.P2p = p
	snap.Consensus, snap.PendingVotes = consensus(res, self)
	if prevHeight == 0 && snap.Consensus != nil {
		// Without a range Besu returns only 100 blocks in the first round; treating that as 1000 blocks would make the validators page misjudge those outside the window as not producing, so skip it this round
		snap.Consensus.ProposerMetrics = nil
	}
	snap.EpochLength, snap.BlockPeriod = bftConfig(res[iNodeInfo], snap.Consensus != nil)
	snap.Forks = forkSchedule(res[iNodeInfo])

	var allow []string
	if err := res[iAllowlist].Decode(&allow); err != nil {
		switch perr := rpcops.PermErr(err); {
		case errors.Is(perr, rpcops.ErrPermUnavailable):
			snap.AllowlistStatus = AllowlistPermMissing
		case errors.Is(perr, rpcops.ErrAllowlistDisabled):
			snap.AllowlistStatus = AllowlistFileDisabled
		default:
			snap.AllowlistStatus = AllowlistReadFailed
		}
	} else {
		if allow == nil {
			allow = []string{} // Besu returns null = empty list; nil is reserved for "could not read"
		}
		snap.Allowlist, snap.AllowlistStatus = &allow, AllowlistReadable
	}
	snap.MissingAPIs = missingAPIs(res)
	snap.Peers = observedPeers(res[iPeers])
	var ver string
	if res[iClientVersion].Decode(&ver) == nil && ver != "" {
		snap.ClientVersion = &ver
	}
	var chainHex string
	if res[iChainID].Decode(&chainHex) == nil {
		if id, err := hexInt64(chainHex); err == nil {
			snap.ChainID = &id
		}
	}
	return nil
}

// observedPeers: every peer in admin_peers that completed the handshake. The handshake itself proves the peer holds this public key,
// so the public key is trustworthy. The enode port is the listening port the peer announced in the handshake (the ephemeral inbound port is in network.remoteAddress);
// host is the peer's actual connection IP; when the peer is behind NAT this IP may not be dialable back.
// nil when ADMIN is not enabled (or the response shape is wrong); does not affect the other layers.
func observedPeers(r rpcops.Result) []ObservedPeer {
	var raw []struct {
		Enode   string `json:"enode"`
		Network struct {
			RemoteAddress string `json:"remoteAddress"`
			Inbound       bool   `json:"inbound"`
		} `json:"network"`
		Protocols struct {
			Eth struct {
				LatestBlock int64 `json:"latestBlock"`
			} `json:"eth"`
		} `json:"protocols"`
	}
	if r.Decode(&raw) != nil {
		return nil
	}
	out := make([]ObservedPeer, 0, len(raw))
	for _, p := range raw {
		pub, err := enode.PubKeyHex(p.Enode)
		if err != nil {
			continue
		}
		op := ObservedPeer{Pubkey: pub, Enode: p.Enode, RemoteAddress: p.Network.RemoteAddress, Inbound: p.Network.Inbound}
		if b := p.Protocols.Eth.LatestBlock; b > 0 {
			op.LatestBlock = &b
		}
		out = append(out, op)
	}
	return out
}

// bftConfig: epoch length and block period for IBFT2 / QBFT. Besu reports them only in the chain config of admin_nodeInfo, and only the keys present in genesis:
// without epochlength Besu uses its default; without blockperiodseconds we do not guess (nil; the caller uses the actual block interval instead). The empty-block period (xemptyblockperiodseconds)
// is not reported. Not a BFT chain, or ADMIN not enabled (admin_nodeInfo unreadable) = both nil: unknown, no default guessed.
func bftConfig(r rpcops.Result, bft bool) (epoch *int64, period *float64) {
	type options struct {
		EpochLength        int64 `json:"epochLength"`
		BlockPeriodSeconds int64 `json:"blockPeriodSeconds"`
		BlockPeriodMillis  int64 `json:"xBlockPeriodMilliSeconds"`
	}
	var info struct {
		Protocols struct {
			Eth struct {
				Config struct {
					Ibft2 *options `json:"ibft2"`
					Qbft  *options `json:"qbft"`
				} `json:"config"`
			} `json:"eth"`
		} `json:"protocols"`
	}
	if !bft || r.Decode(&info) != nil {
		return nil, nil
	}
	e := int64(defaultEpochLength)
	for _, o := range []*options{info.Protocols.Eth.Config.Qbft, info.Protocols.Eth.Config.Ibft2} {
		if o == nil {
			continue
		}
		if o.EpochLength > 0 {
			e = o.EpochLength
		}
		if p := float64(o.BlockPeriodSeconds); o.BlockPeriodMillis > 0 {
			p = float64(o.BlockPeriodMillis) / 1000
			period = &p
		} else if p > 0 {
			period = &p
		}
		break
	}
	return &e, period
}

// evmForks: hard forks that change the EVM, in activation order; keys are the names in the admin_nodeInfo chain config (Besu JsonGenesisConfigOptions); ...Block compares against height, ...Time against timestamp.
// Forks that only adjust the difficulty bomb (muirGlacier, arrowGlacier, grayGlacier) or only blob parameters (bpo1-5) are not EVM versions and are not listed.
var evmForks = []struct{ key, name string }{
	{"homesteadBlock", "Homestead"}, {"eip150Block", "Tangerine Whistle"}, {"eip158Block", "Spurious Dragon"},
	{"byzantiumBlock", "Byzantium"}, {"constantinopleBlock", "Constantinople"}, {"petersburgBlock", "Petersburg"},
	{"istanbulBlock", "Istanbul"}, {"berlinBlock", "Berlin"}, {"londonBlock", "London"}, {"mergeNetSplitBlock", "Paris"},
	{"shanghaiTime", "Shanghai"}, {"cancunTime", "Cancun"}, {"pragueTime", "Prague"}, {"osakaTime", "Osaka"}, {"amsterdamTime", "Amsterdam"},
}

// Fork: one hard fork present in the chain config. AtTime = the activation point is a timestamp (...Time), otherwise a block number.
type Fork struct {
	Name   string
	At     int64
	AtTime bool
}

// forkSchedule: hard forks present in the admin_nodeInfo chain config, in activation order. admin_nodeInfo unreadable (ADMIN not enabled) = nil.
func forkSchedule(r rpcops.Result) *[]Fork {
	var info struct {
		Protocols struct {
			Eth struct {
				Config map[string]json.RawMessage `json:"config"`
			} `json:"eth"`
		} `json:"protocols"`
	}
	if r.Decode(&info) != nil || info.Protocols.Eth.Config == nil {
		return nil
	}
	out := []Fork{}
	for _, f := range evmForks {
		var at int64
		if raw, ok := info.Protocols.Eth.Config[f.key]; ok && json.Unmarshal(raw, &at) == nil {
			out = append(out, Fork{Name: f.name, At: at, AtTime: strings.HasSuffix(f.key, "Time")})
		}
	}
	return &out
}

// EvmVersion: pure function. The latest hard fork already active at the chain head (height, time); none active = Frontier.
func EvmVersion(forks []Fork, height int64, at time.Time) string {
	name := "Frontier"
	for _, f := range forks {
		if f.AtTime && at.Unix() >= f.At || !f.AtTime && height >= f.At {
			name = f.Name
		}
	}
	return name
}

// missingAPIs: namespaces the platform needs that the node has not enabled. ETH is not included: if even the height cannot be read, the whole batch is already treated as unreachable.
// Only one consensus method set returns data, so it counts as missing only when both IBFT and QBFT are unavailable.
func missingAPIs(res []rpcops.Result) []string {
	missing := []string{}
	for _, c := range []struct {
		name string
		i    int
	}{{"NET", iPeerCount}, {"ADMIN", iNodeInfo}, {"TXPOOL", iTxpool}, {"PERM", iAllowlist}} {
		if rpcops.MethodUnavailable(res[c.i].Err) {
			missing = append(missing, c.name)
		}
	}
	if rpcops.MethodUnavailable(res[iIbftValidators].Err) && rpcops.MethodUnavailable(res[iQbftValidators].Err) {
		missing = append(missing, "IBFT/QBFT")
	}
	return missing
}

type syncInfo struct {
	syncing      bool
	blocksBehind int
}

// syncStatus: eth_syncing returns false (synced) or {currentBlock, highestBlock}.
func syncStatus(r rpcops.Result) syncInfo {
	var obj struct {
		CurrentBlock string `json:"currentBlock"`
		HighestBlock string `json:"highestBlock"`
	}
	if r.Decode(&obj) != nil || obj.HighestBlock == "" {
		return syncInfo{}
	}
	cur, _ := hexInt64(obj.CurrentBlock)
	high, _ := hexInt64(obj.HighestBlock)
	return syncInfo{syncing: true, blocksBehind: int(high - cur)}
}

// poolOldest: entry time of the oldest transaction in the pool, for the "transaction stuck too long" alert (listed=false = unknown; that metric is not emitted).
// The full pool listing is a separate request: if it is too large, unreachable or over the response limit, only this item is lost and the whole batch is not dragged down. An empty pool needs no query.
// ponytail: above txpoolListMax transactions the oldest age is unknown (metric not emitted, the alert recovers per keep_firing_for); for better accuracy have the agent compute it on the node side and return it
func poolOldest(ctx context.Context, rpc RPC, nodeID string, pending int) (oldest *time.Time, listed bool) {
	if pending == 0 {
		return nil, true
	}
	if pending > txpoolListMax {
		return nil, false
	}
	res, err := rpc.Batch(ctx, nodeID, []rpcops.Call{{Method: "txpool_besuTransactions"}})
	if err != nil || len(res) == 0 {
		return nil, false
	}
	return rpcops.PoolOldest(res[0])
}

// blockTiming: blockTime is derived from timestamp differences over the latest sampleWindow blocks; tps is approximated from the latest block.
func blockTiming(ctx context.Context, rpc RPC, nodeID string, height int64, l *api.LedgerSummary) {
	from := height - sampleWindow
	if from < 1 {
		from = 1 // never use genesis (height 0) as the reference block: its timestamp is fixed at 1970 and blockTime would come out astronomical
	}
	if height <= from {
		return
	}
	res, err := rpc.Batch(ctx, nodeID, []rpcops.Call{
		{Method: "eth_getBlockByNumber", Params: []any{fmt.Sprintf("0x%x", height), false}},
		{Method: "eth_getBlockByNumber", Params: []any{fmt.Sprintf("0x%x", from), false}},
	})
	if err != nil {
		return
	}
	var latest, earlier struct {
		Timestamp    string   `json:"timestamp"`
		Transactions []string `json:"transactions"`
	}
	if res[0].Decode(&latest) != nil || res[1].Decode(&earlier) != nil {
		return
	}
	tsL, _ := hexInt64(latest.Timestamp)
	tsE, _ := hexInt64(earlier.Timestamp)
	if span := tsL - tsE; span > 0 {
		bt := float32(span) / float32(height-from)
		tps := float32(len(latest.Transactions)) / bt
		l.Tps = &tps
	}
}

// consensus: whichever method set returns validators is the chain's consensus; neither returns any = nil (not IBFT2/QBFT, or namespace not enabled).
// The node's pending votes come back separately (nil = unreadable).
func consensus(res []rpcops.Result, selfEnode string) (*api.ConsensusStatus, map[string]bool) {
	alg, iVal, iSig, iPend := api.Ibft2, iIbftValidators, iIbftSigners, iIbftPending
	var validators []string
	if res[iVal].Decode(&validators) != nil {
		alg, iVal, iSig, iPend = api.Qbft, iQbftValidators, iQbftSigners, iQbftPending
		if res[iVal].Decode(&validators) != nil {
			return nil, nil
		}
	}
	cs := &api.ConsensusStatus{Algorithm: &alg, Validators: &validators}
	count := len(validators)
	cs.ValidatorCount = &count
	if selfEnode != "" {
		if addr, err := enode.Address(selfEnode); err == nil {
			isVal := false
			for _, v := range validators {
				if strings.EqualFold(v, addr) {
					isVal = true
					break
				}
			}
			cs.IsValidator = &isVal
		}
	}
	var signers []struct {
		Address                 string `json:"address"`
		ProposedBlockCount      string `json:"proposedBlockCount"`
		LastProposedBlockNumber string `json:"lastProposedBlockNumber"`
	}
	if res[iSig].Decode(&signers) == nil {
		out := make([]api.ProposerMetric, 0, len(signers))
		for _, r := range signers {
			count, _ := hexInt64(r.ProposedBlockCount)
			last, _ := hexInt64(r.LastProposedBlockNumber)
			out = append(out, api.ProposerMetric{Address: r.Address, ProposedBlockCount: int(count), LastProposedBlockNumber: int(last)})
		}
		cs.ProposerMetrics = &out
	}
	var votes map[string]bool
	if res[iPend].Decode(&votes) != nil {
		votes = nil
	} else if votes == nil {
		votes = map[string]bool{}
	}
	return cs, votes
}

func hexInt64(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimPrefix(s, "0x"), 16, 64)
}
