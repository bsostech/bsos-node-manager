package server

import (
	"context"
	"encoding/hex"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/enode"
	"besu-manager/backend/internal/store"
)

// Validator governance: the on-chain validator set is read-only; adding or removing one = one vote
// from each of our own validator nodes. The current validator set is public on-chain state; any
// reachable node of ours can query it, so reading needs no cross-org access.

// currentValidatorSet: the current on-chain validator set + consensus algorithm (merged with the
// identities of our known nodes). The address set and consensus algorithm come from the Consensus
// reported by any of our nodes (chain state is global, every node gives the same answer); identity
// (org/nodeId) is resolved against validator_address in the local nodes table (only our org's
// nodes). Addresses that cannot be resolved (other orgs' validators) are still listed, just with
// org/nodeId left empty (nil); nothing is invented or skipped: the validator set is public on-chain
// state and should not lose entries just because the platform cannot see the other side.
func (s *Server) currentValidatorSet(ctx context.Context) ([]api.ValidatorSetEntry, string, error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, "", err
	}
	addrs, consensus := s.chainConsensus(nodes)
	return validatorEntries(addrs, nodes), consensus, nil
}

// chainConsensus: validator addresses and consensus type. Chain state is global, so one copy of
// each is enough; they are looked up separately so a node that returns only one of them is not
// misread as "unknown".
func (s *Server) chainConsensus(nodes []store.Node) ([]string, string) {
	var addrs []string
	consensus := ""
	for _, n := range nodes {
		res, ok := s.col.Get(n.ID)
		if !ok || res.Snap == nil || res.Snap.Consensus == nil {
			continue
		}
		if addrs == nil && res.Snap.Consensus.Validators != nil {
			addrs = *res.Snap.Consensus.Validators
		}
		if consensus == "" && res.Snap.Consensus.Algorithm != nil {
			consensus = string(*res.Snap.Consensus.Algorithm)
		}
		if addrs != nil && consensus != "" {
			break
		}
	}
	return addrs, consensus
}

// validatorSet: the current validator addresses. Uses the chain cache first (block extraData,
// independent of which nodes are up), falling back to what nodes report.
func (s *Server) validatorSet(nodes []store.Node) []string {
	if set := s.col.Chain().LatestValidators(); set != nil {
		return set
	}
	set, _ := s.chainConsensus(nodes)
	return set
}

// knownValidator: pure function. When a node reported no consensus this round (Besu stopped, RPC
// unreachable), checks the node address the platform has on record against the validator set, so
// the "validator" tag does not vanish exactly when the node is in trouble and you most need to know
// it is a validator. Returns nil if the address or validator set is unknown (no guessing).
func knownValidator(addr *string, set []string) *bool {
	if addr == nil || set == nil {
		return nil
	}
	in := false
	for _, a := range set {
		if enode.NormalizeAddress(a) == enode.NormalizeAddress(*addr) {
			in = true
			break
		}
	}
	return &in
}

// validatorEntries: pure function. On-chain validator addresses -> list; our org's nodes (addresses
// computed in the same place as the peer page, ownAddresses) get org/nodeId filled in.
func validatorEntries(addrs []string, nodes []store.Node) []api.ValidatorSetEntry {
	byID := map[string]store.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	own := ownAddresses(nodes)
	out := make([]api.ValidatorSetEntry, 0, len(addrs))
	for _, addr := range addrs {
		entry := api.ValidatorSetEntry{Address: addr}
		if id, ok := own[enode.NormalizeAddress(addr)]; ok {
			org, nodeID := byID[id].Org, id
			entry.OrgId, entry.NodeId = &org, &nodeID
		}
		out = append(out, entry)
	}
	return out
}

// ── handlers ──

func (s *Server) ListValidatorSet(ctx context.Context, _ api.ListValidatorSetRequestObject) (api.ListValidatorSetResponseObject, error) {
	set, _, err := s.currentValidatorSet(ctx)
	if err != nil {
		return nil, err
	}
	// Connection state and names are filled in only here: they come from our nodes' admin_peers and
	// the names our org's users assigned (see peerobs.go).
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	perNode := s.observedPeers(nodes)
	names, err := s.peerNames(ctx, nodes)
	if err != nil {
		return nil, err
	}
	annotateValidators(set, connectedCounts(perNode), len(perNode) > 0, names)
	s.annotateTurns(ctx, set, s.col.Chain(), perNode)
	return api.ListValidatorSet200JSONResponse(set), nil
}

// observedPeers: the admin_peers our org's nodes read this round. Only actual reads count: Peers ==
// nil (ADMIN not enabled, offline, just restarted) means "unknown", not "zero peers".
func (s *Server) observedPeers(nodes []store.Node) map[string][]collector.ObservedPeer {
	perNode := map[string][]collector.ObservedPeer{}
	for _, n := range nodes {
		if res, ok := s.col.Get(n.ID); ok && res.Snap != nil && res.Snap.Peers != nil {
			perNode[n.ID] = res.Snap.Peers
		}
	}
	return perNode
}

// annotateTurns: fills in each validator's turn history and producing verdict. Shared by the
// validators page, chain nodes and the chain-level alert metrics (currentChainStats) so the verdict
// is consistent. perNode is the admin_peers of our org's nodes this round (observedPeers). Returns
// whether the chain is halted this round (chainHalted).
func (s *Server) annotateTurns(ctx context.Context, set []api.ValidatorSetEntry, view collector.ChainView, perNode map[string][]collector.ObservedPeer) (halted bool) {
	turns := validatorTurns(view.TurnBlocks(), setAddressList(set))
	for i := range set {
		set[i].Turns = turns[enode.NormalizeAddress(set[i].Address)]
		if set[i].Turns == nil {
			set[i].Turns = []api.ValidatorTurn{}
		}
	}
	// The producing verdict looks only at turn history, same rule for our org's and other orgs'
	// validators. With no healthy source node the block cache stops updating; do not judge from
	// stale data
	if view.SourceNodeID != "" {
		for i := range set {
			set[i].Producing = producing(set[i].Turns)
		}
	}
	// Once the chain halts there are no new blocks and the rule above can never decide, so look at
	// other evidence: for our own validators, the node itself (Besu unreachable counts any time,
	// agent disconnect only while halted); for other orgs', P2P only while halted: disconnected
	// from all our nodes means definitely not producing. While the chain is still producing, ignore
	// disconnects: the platform not seeing it does not mean it is not producing; turn history is
	// more accurate.
	halted = s.chainHalted(ctx, view)
	for i := range set {
		if id := set[i].NodeId; id != nil {
			if res, ok := s.col.Get(*id); ok && ownNodeNotProducing(res, halted) {
				set[i].Producing = new(false)
			}
		} else if halted && disconnectedFromAll(perNode, set[i].Address) {
			set[i].Producing = new(false)
		}
	}
	return halted
}

// haltMultiple: multiplier used when the "block production severely slowed or stopped" rule cannot
// be read (same as that rule's initial value).
const haltMultiple = 20

// chainHalted: latest block age reaching the "block production severely slowed or stopped" multiple
// = chain halted, turn history no longer trustworthy. The threshold is read from that rule (even
// when disabled), so the page, this code and alerts use the same standard. If the chain head or
// block period is unknown, treat as not halted (no guessing).
func (s *Server) chainHalted(ctx context.Context, view collector.ChainView) bool {
	head, ok := s.col.Head()
	if !ok {
		return false
	}
	multiple := float64(haltMultiple)
	if r, err := s.store.GetAlertRuleByPreset(ctx, "ChainSlowSevere"); err == nil && r != nil {
		multiple = r.Threshold
	}
	return isHalted(head.Time, time.Now(), s.expectedBlockPeriod(view), multiple)
}

// isHalted: pure function.
func isHalted(head, now time.Time, period, multiple float64) bool {
	return !head.IsZero() && period > 0 && now.Sub(head).Seconds() >= period*multiple
}

// disconnectedFromAll: pure function. None of our org's nodes is connected to this address, and at
// least one read a non-empty peer list. Returns false when all our nodes failed to read or all
// lists are empty (usually we ourselves are cut off): that is us not seeing, not the other side
// disconnecting.
func disconnectedFromAll(perNode map[string][]collector.ObservedPeer, addr string) bool {
	seesAny := false
	for _, peers := range perNode {
		if len(peers) > 0 {
			seesAny = true
			break
		}
	}
	return seesAny && connectedCounts(perNode)[enode.NormalizeAddress(addr)] == 0
}

// chainStats: chain-level numbers needed by chain-wide alert rules.
type chainStats struct {
	blockPeriod  float64 // seconds; 0 = unknown
	validators   int
	notProducing int // count with producing == false; null does not count
	tolerance    int // fault tolerance f = N - ceil(2N/3)
	halted       bool
}

// currentChainStats: ok=false when there is no healthy source node or the validator set is unknown
// (no 0 pretending to be data).
func (s *Server) currentChainStats(ctx context.Context) (chainStats, bool) {
	view := s.col.Chain()
	if view.SourceNodeID == "" {
		return chainStats{}, false
	}
	set, _, err := s.currentValidatorSet(ctx)
	if err != nil || len(set) == 0 {
		return chainStats{}, false
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return chainStats{}, false
	}
	halted := s.annotateTurns(ctx, set, view, s.observedPeers(nodes))
	return newChainStats(set, s.expectedBlockPeriod(view), halted), true
}

// newChainStats: pure function. set already has producing verdicts (annotateTurns).
func newChainStats(set []api.ValidatorSetEntry, period float64, halted bool) chainStats {
	out := chainStats{blockPeriod: period, validators: len(set), tolerance: len(set) - blockQuorum(len(set)), halted: halted}
	for _, v := range set {
		if v.Producing != nil && !*v.Producing {
			out.notProducing++
		}
	}
	return out
}

// ownNodeNotProducing: pure function. Our org's node is definitely not producing: agent present but
// RPC unreachable (Besu down), or agent disconnected (after having connected) while the chain is
// halted. A disconnected agent does not count while the chain is producing: the platform just cannot
// see it, Besu may still be signing; leave it to turn history. RPC 401 does not count (Besu is
// still signing, the platform's credentials are just wrong); never-connected does not count either
// (that is normal right after a platform restart before agents connect).
func ownNodeNotProducing(res collector.Result, halted bool) bool {
	if res.Snap == nil {
		return halted && res.EverSeen
	}
	return res.Snap.Ledger == nil && !res.Snap.RpcUnauthorized
}

// annotateValidators: pure function. reporting = at least one of our nodes reported a peer list
// this round; otherwise leave null (unknown). Names are always user-assigned; when our own node has
// no name, the frontend falls back to nodeId.
func annotateValidators(set []api.ValidatorSetEntry, connected map[string]int, reporting bool, names map[string]string) {
	for i := range set {
		addr := enode.NormalizeAddress(set[i].Address)
		if reporting {
			n := connected[addr]
			set[i].ConnectedNodes = &n
		}
		if name, ok := names[addr]; ok {
			set[i].Name = &name
		}
	}
}

// validatorTurns: pure function. For each validator in blocks (NormalizeAddress'd), the outcome of
// every turn it had, oldest to newest. Besu rotates by address order: if a block's round is r, then
// sorting that block's validator set, the r validators just before the actual proposer had their
// turn and did not produce. The list comes from that block's extraData, so turns are inferred
// correctly across validator-set changes; if extraData carries no list (QBFT with the validator set
// in a contract), use current (the current list). If the proposer is not in the list (parse
// anomaly), nothing is marked. When r exceeds the validator count, each validator gets at most one
// miss recorded.
func validatorTurns(blocks []collector.BlockStamp, current []string) map[string][]api.ValidatorTurn {
	out := map[string][]api.ValidatorTurn{}
	for _, b := range blocks {
		if !b.BFT {
			continue // extraData cannot be decoded: round unknown, cannot count this as the validator having produced
		}
		names := b.Validators
		if len(names) == 0 {
			names = current
		}
		order := make([]string, len(names))
		for i, a := range names {
			order[i] = enode.NormalizeAddress(a)
		}
		sort.Strings(order)
		i := slices.Index(order, enode.NormalizeAddress(b.Proposer))
		if i < 0 {
			continue
		}
		n := len(order)
		for k := min(b.Round, n-1); k >= 1; k-- {
			a := order[(i-k+n)%n]
			out[a] = append(out[a], api.ValidatorTurn{Block: b.Number, Proposed: false})
		}
		at, tx, used, limit := b.Time, b.TxCount, b.GasUsed, b.GasLimit
		out[order[i]] = append(out[order[i]], api.ValidatorTurn{Block: b.Number, Proposed: true, At: &at, TxCount: &tx, GasUsed: &used, GasLimit: &limit})
	}
	return out
}

// producingMissedInARow: missing this many turns in a row counts as not producing (rule: see
// ValidatorSetEntry.producing in the contract).
const producingMissedInARow = 2

// missedTurns: pure function. Number of missed turns in the turn history.
func missedTurns(turns []api.ValidatorTurn) int {
	n := 0
	for _, t := range turns {
		if !t.Proposed {
			n++
		}
	}
	return n
}

// producing: pure function. Missed the last producingMissedInARow turns = false; no turn history =
// nil. Does not count misses across the whole window: a validator that resumes producing flips back
// on its next turn without waiting for old misses to roll out of the window.
func producing(turns []api.ValidatorTurn) *bool {
	if len(turns) == 0 {
		return nil
	}
	last := turns[max(0, len(turns)-producingMissedInARow):]
	allMissed := len(last) == producingMissedInARow && !slices.ContainsFunc(last, func(t api.ValidatorTurn) bool { return t.Proposed })
	return new(!allMissed)
}

// ── Voting: one RPC to each of our validator nodes, nothing stored, vote status is read from the
// chain ──

// setAddressList: validator set addresses, as-is and in order (the current for validatorTurns).
func setAddressList(set []api.ValidatorSetEntry) []string {
	out := make([]string, len(set))
	for i, v := range set {
		out[i] = v.Address
	}
	return out
}

// setAddresses: validator set addresses (NormalizeAddress'd).
func setAddresses(set []api.ValidatorSetEntry) map[string]bool {
	out := make(map[string]bool, len(set))
	for _, v := range set {
		out[enode.NormalizeAddress(v.Address)] = true
	}
	return out
}

// voteTargets: pure function. Our org's nodes that receive votes = nodes whose validator address is
// in the current validator set (sorted).
func voteTargets(set []api.ValidatorSetEntry, nodes []store.Node) []string {
	inSet := setAddresses(set)
	var out []string
	for _, n := range nodes {
		if n.ValidatorAddress != nil && inSet[enode.NormalizeAddress(*n.ValidatorAddress)] {
			out = append(out, n.ID)
		}
	}
	sort.Strings(out)
	return out
}

// groupVotes: pure function. Pending changes = (address, direction) pairs not yet reflected in the
// validator set: votes counted on-chain this epoch (network-wide, per Besu VoteTally), plus those
// in our nodes' pendingVotes not yet written to a block. Sorted by address, add before remove for
// the same address. For an address only the direction that would change the set is listed, so one
// node never appears in both the add and remove rows: a pending vote in the opposite direction is a
// no-op that Besu does not cast, and the on-chain vote keeps counting until replaced by this node's
// next vote or the address's tally is cleared. Withdrawal: this node of ours sent a counter vote
// not yet in a block = withdrawing (still counted); counter vote already in a block = withdrawnAt
// (no longer counted). voter.pending: true = the node's pending-vote list still has this vote;
// false = another org, or the node reported a pending list without this vote; nil = no snapshot for
// our node (agent offline etc.), unknown whether it is still there. pending is keyed by nodes that
// reported a pending list. voterAddr maps node id -> its validator address (0x + lowercase).
func groupVotes(set []api.ValidatorSetEntry, pending map[string]map[string]bool, voterAddr map[string]string, view collector.ChainView) []api.ValidatorVote {
	inSet := setAddresses(set)
	nodeOf := map[string]string{}
	for id, a := range voterAddr {
		nodeOf[a] = id
	}
	type key struct {
		addr string // without 0x
		add  bool
	}
	rows := map[key]map[string]api.ValidatorVoter{} // validator address -> voter
	put := func(k key, voter string, countedAt *int64, countedAtTime *time.Time, pending bool) {
		if inSet[k.addr] == k.add {
			return
		}
		if rows[k] == nil {
			rows[k] = map[string]api.ValidatorVoter{}
		}
		v, ok := rows[k][voter]
		if !ok {
			v = api.ValidatorVoter{Address: voter, CountedAt: countedAt, CountedAtTime: countedAtTime}
		}
		if pending { // already counted on-chain, may still be left on the node too
			v.Pending = &pending
		}
		if ok {
			rows[k][voter] = v
			return
		}
		if id, ok := nodeOf[voter]; ok {
			v.NodeId = &id
		}
		rows[k][voter] = v
	}
	cutoff := view.VoteCutoffs()
	for ref, sv := range view.Votes {
		if !inSet[enode.NormalizeAddress(ref.Voter)] || !view.Counts(ref, sv, cutoff) {
			continue
		}
		blk, at := sv.Block, sv.Time
		put(key{enode.NormalizeAddress(ref.Target), sv.Add}, ref.Voter, &blk, &at, false)
	}
	for id, votes := range pending {
		voter, ok := voterAddr[id]
		if !ok {
			continue
		}
		for addr, add := range votes {
			k := key{enode.NormalizeAddress(addr), add}
			if inSet[k.addr] == add {
				// A pending vote whose direction is already the status quo: if this node's on-chain
				// vote is still the opposite direction (the pending row), it is the counter vote
				// sent by a withdrawal -> mark this voter in that row as withdrawing
				orig := key{k.addr, !add}
				if v, ok := rows[orig][voter]; ok && v.CountedAt != nil {
					v.Withdrawing = new(true)
					rows[orig][voter] = v
				}
				continue
			}
			put(k, voter, nil, nil, true)
		}
	}
	// Withdrawn: this node's latest on-chain vote is a counter vote (direction is already the
	// status quo). If the same change still has other counted votes, list it back in its original
	// row with the time the counter vote was written to a block
	for ref, sv := range view.Votes {
		target := enode.NormalizeAddress(ref.Target)
		id, own := nodeOf[ref.Voter]
		// The latest on-chain vote is a counter vote and is after the reset point (a withdrawal
		// from a previous round does not count for this one)
		if !own || !inSet[enode.NormalizeAddress(ref.Voter)] || !view.Counts(ref, sv, cutoff) || inSet[target] != sv.Add {
			continue
		}
		orig := key{target, !sv.Add}
		if rows[orig] == nil {
			continue
		}
		if _, listed := rows[orig][ref.Voter]; listed { // voted back in the original direction after withdrawing and still in the pending list: treat as a normal pending vote
			continue
		}
		at, nodeID := sv.Time, id
		rows[orig][ref.Voter] = api.ValidatorVoter{Address: ref.Voter, NodeId: &nodeID, Pending: new(false), WithdrawnAt: &at}
	}
	keys := slices.Collect(maps.Keys(rows))
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].addr != keys[j].addr {
			return keys[i].addr < keys[j].addr
		}
		return keys[i].add && !keys[j].add
	})
	out := make([]api.ValidatorVote, 0, len(keys))
	for _, k := range keys {
		voters := slices.Collect(maps.Values(rows[k]))
		for i, v := range voters {
			if v.Pending != nil {
				continue
			}
			if v.NodeId == nil {
				voters[i].Pending = new(bool)
			} else if _, reported := pending[*v.NodeId]; reported {
				voters[i].Pending = new(bool)
			}
		}
		sort.Slice(voters, func(i, j int) bool { return voters[i].Address < voters[j].Address })
		out = append(out, api.ValidatorVote{Address: "0x" + k.addr, Add: k.add, Voters: voters})
	}
	return out
}

// voteContext: input shared by the three vote handlers: current validator set, consensus type, our
// org's nodes that receive votes, and node id -> validator address.
func (s *Server) voteContext(ctx context.Context) (set []api.ValidatorSetEntry, consensus string, targets []string, voterAddr map[string]string, err error) {
	if set, consensus, err = s.currentValidatorSet(ctx); err != nil {
		return nil, "", nil, nil, err
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, "", nil, nil, err
	}
	voterAddr = map[string]string{}
	for _, n := range nodes {
		if n.ValidatorAddress != nil {
			voterAddr[n.ID] = "0x" + enode.NormalizeAddress(*n.ValidatorAddress)
		}
	}
	return set, consensus, voteTargets(set, nodes), voterAddr, nil
}

// voteBlocked: pure function. Reason voting is not allowed (returns 409); empty = may vote.
func voteBlocked(consensus string, targets []string) api.ErrorReason {
	switch {
	case consensus == "":
		return api.ErrorReasonVoteNoConsensus
	case len(targets) == 0:
		return api.ErrorReasonNoValidatorNodes
	}
	return ""
}

func (s *Server) ListValidatorVotes(ctx context.Context, _ api.ListValidatorVotesRequestObject) (api.ListValidatorVotesResponseObject, error) {
	set, _, targets, voterAddr, err := s.voteContext(ctx)
	if err != nil {
		return nil, err
	}
	pending := map[string]map[string]bool{}
	for _, id := range targets {
		if res, ok := s.col.Get(id); ok && res.Snap != nil && res.Snap.PendingVotes != nil {
			pending[id] = res.Snap.PendingVotes
		}
	}
	view := s.col.Chain()
	return api.ListValidatorVotes200JSONResponse{
		ValidatorCount: len(set), Needed: voteNeeded(len(set)), OwnValidators: append([]string{}, targets...),
		Votes: groupVotes(set, pending, voterAddr, view),
		// With epoch unknown (no node of ours has ADMIN enabled) voters come only from the current
		// window, the epoch boundary is unclear and the tally is not trustworthy
		TallyKnown: view.EpochLength > 0,
	}, nil
}

// fanOutVote: sends once to each of our validators and reports per node (individual failures do not
// fail the whole request). An audit entry is written first (which nodes will be sent to); if it
// cannot be written nothing is sent. Afterwards another entry records each node's result. A
// non-empty reason = no node to send to or consensus type unknown (409). Takes voteMu, mutually
// exclusive with auto-withdrawing leftover votes (votecleanup.go).
func (s *Server) fanOutVote(ctx context.Context, event, address string, send func(nodeID, consensus string) error, detail map[string]any) (api.ValidatorVoteResult, api.ErrorReason, error) {
	s.voteMu.Lock()
	defer s.voteMu.Unlock()
	s.voteTouched["0x"+enode.NormalizeAddress(address)] = time.Now()
	_, consensus, targets, _, err := s.voteContext(ctx)
	if err != nil {
		return api.ValidatorVoteResult{}, "", err
	}
	if reason := voteBlocked(consensus, targets); reason != "" {
		return api.ValidatorVoteResult{}, reason, nil
	}
	d := map[string]any{"address": address, "nodes": targets}
	for k, v := range detail {
		d[k] = v
	}
	if err := s.audit(ctx, event, d); err != nil {
		return api.ValidatorVoteResult{}, "", err
	}
	out := api.ValidatorVoteResult{Results: sendEach(targets, func(id string) error { return send(id, consensus) })}
	done := map[string]any{"address": address, "nodes": out.Results}
	for k, v := range detail {
		done[k] = v
	}
	s.auditDone(ctx, event, done)
	return out, "", nil
}

// sendEach: pure function. Sends to each node in turn; one failure does not affect the others, the
// failure reason goes into the result.
func sendEach(targets []string, send func(nodeID string) error) []api.ValidatorVoteNodeResult {
	out := make([]api.ValidatorVoteNodeResult, 0, len(targets))
	for _, id := range targets {
		r := api.ValidatorVoteNodeResult{NodeId: id, Ok: true}
		if err := send(id); err != nil { // use the reason carried by the underlying error if any (e.g. agent not connected); unrecognized raw text goes into detail
			e := causeError(api.ErrorCodeInternal, api.ErrorReasonOperationFailed, err)
			r.Ok, r.Error, r.Reason, r.Params = false, &e.Message, e.Reason, e.Params
		}
		out = append(out, r)
	}
	return out
}

func (s *Server) ProposeValidatorVote(ctx context.Context, req api.ProposeValidatorVoteRequestObject) (api.ProposeValidatorVoteResponseObject, error) {
	if req.Body == nil || !validAddress(req.Body.Address) {
		return api.ProposeValidatorVote400JSONResponse{Code: api.ErrorCodeInvalid, Message: "address must be 0x followed by 40 hex digits"}, nil // the contract's pattern rejects this first; unreachable
	}
	addr, add := strings.ToLower(req.Body.Address), req.Body.Add
	set, _, err := s.currentValidatorSet(ctx)
	if err != nil {
		return nil, err
	}
	if reason := noopVote(set, addr, add); reason != "" {
		return api.ProposeValidatorVote409JSONResponse(apierr.New(api.ErrorCodeConflict, reason, nil)), nil
	}
	res, reason, err := s.fanOutVote(ctx, "validator_vote_proposed", addr, func(id, consensus string) error {
		return s.ops.ProposeVote(ctx, id, consensus, addr, add)
	}, map[string]any{"add": add})
	switch {
	case err != nil:
		return nil, err
	case reason != "":
		return api.ProposeValidatorVote409JSONResponse(apierr.New(api.ErrorCodeConflict, reason, nil)), nil
	}
	return api.ProposeValidatorVote200JSONResponse(res), nil
}

// noopVote: pure function. Returns a reason for a vote whose direction matches the validator set's
// status quo (adding one already in the set, removing one not in it); empty = may vote. Besu
// accepts such a vote but never puts it in a block (it only casts it when the opposite direction
// was voted this epoch, which is a withdrawal and goes through DiscardValidatorVote), yet the UI
// would look like it succeeded. Not blocked when the validator set is unknown (set is empty).
func noopVote(set []api.ValidatorSetEntry, addr string, add bool) api.ErrorReason {
	if len(set) == 0 {
		return ""
	}
	in := setAddresses(set)[enode.NormalizeAddress(addr)]
	switch {
	case add && in:
		return api.ErrorReasonAlreadyValidator
	case !add && !in:
		return api.ErrorReasonNotValidator
	}
	return ""
}

func (s *Server) DiscardValidatorVote(ctx context.Context, req api.DiscardValidatorVoteRequestObject) (api.DiscardValidatorVoteResponseObject, error) {
	if !validAddress(req.Address) {
		return api.DiscardValidatorVote400JSONResponse{Code: api.ErrorCodeInvalid, Message: "address must be 0x followed by 40 hex digits"}, nil // the contract's pattern rejects this first; unreachable
	}
	addr := strings.ToLower(req.Address)
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	plan := withdrawPlan(nodes, s.col.Chain(), addr)
	res, reason, err := s.fanOutVote(ctx, "validator_vote_discarded", addr, func(id, consensus string) error {
		if add, ok := plan[id]; ok {
			return s.ops.ProposeVote(ctx, id, consensus, addr, !add)
		}
		return s.ops.DiscardVote(ctx, id, consensus, addr)
	}, map[string]any{"cancel": plan})
	switch {
	case err != nil:
		return nil, err
	case reason != "":
		return api.DiscardValidatorVote409JSONResponse(apierr.New(api.ErrorCodeConflict, reason, nil)), nil
	}
	return api.DiscardValidatorVote200JSONResponse(res), nil
}

// voteNeeded: vote majority threshold (Besu VoteTally: more than half the validators).
func voteNeeded(validators int) int {
	return validators/2 + 1
}

// withdrawPlan: pure function. On withdrawal, which of our validators must cast an opposite vote to
// cancel (node id -> original direction): those whose latest on-chain vote this epoch for this
// address (after the last list change) still counts in the pending row. Others only have it removed
// from the pending list, which equals never having voted. ponytail: on-chain votes come from the
// collector cache, up to one round (10 s) behind; a vote just written to a block is treated as not
// yet written and only removed from pending, the UI still shows it counted, and withdrawing again
// cancels it. For more accuracy, catch the source node up to the chain head before deciding on
// withdrawal.
func withdrawPlan(nodes []store.Node, view collector.ChainView, addr string) map[string]bool {
	target := "0x" + enode.NormalizeAddress(addr)
	out := map[string]bool{}
	for _, n := range nodes {
		if n.ValidatorAddress == nil {
			continue
		}
		voter := "0x" + enode.NormalizeAddress(*n.ValidatorAddress)
		// When the list cannot be read (cache has no block yet, or validator set lives in a
		// contract), counter votes cannot be told apart: only remove pending votes, do not risk an
		// opposite vote
		if sv, ok := view.LatestVote(target, voter); ok && view.LatestValidators() != nil && !isCounterVote(view, target, sv) {
			out[n.ID] = sv.Add
		}
	}
	return out
}

// isCounterVote: whether this on-chain vote is a counter vote: its direction is already the
// validator set's status quo (adding one already in, removing one already out). Besu only casts
// such a vote when this node voted the opposite direction this epoch (VoteProposer), so it is
// evidence of a withdrawal (or change of mind). The validator set is read from the cache's latest
// block list.
func isCounterVote(view collector.ChainView, target string, sv collector.SeenVote) bool {
	set := view.LatestValidators()
	in := slices.ContainsFunc(set, func(v string) bool { return enode.NormalizeAddress(v) == enode.NormalizeAddress(target) })
	return set != nil && in == sv.Add
}

// validAddress: 0x + 40 hex digits.
func validAddress(a string) bool {
	if len(a) != 42 || !strings.HasPrefix(a, "0x") {
		return false
	}
	_, err := hex.DecodeString(a[2:])
	return err == nil
}
