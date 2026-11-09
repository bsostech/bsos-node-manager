package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/rpcops"
)

// Chain-level info: fetch recent block headers from our org's healthy node with the highest height, recording timestamp, proposer (miner), round and the vote each block carries.
// Kept in memory only, not in Prometheus; each round fetches only new blocks, and they are kept when the source node changes (same chain, same blocks). On platform restart a fresh batch is fetched, and this epoch's votes are backfilled in the background.

// BlockWindow: how many blocks the block-interval view covers. KeepBlocks: how many blocks to keep; the chain page's last-hour round timeouts need 1 hour of coverage (enough even at 1 second per block);
// a validator's turn record looks only at the last SignerWindow blocks, the same range as signer metrics.
// ponytail: fixed block count; chains faster than 1 second per block cover less than 1 hour. If that matters, compute the count from the block time.
const (
	BlockWindow = 36
	KeepBlocks  = 3600
)

// fetchBatch: how many blocks to fetch per batch (Besu's default batch limit is 1024; stay conservative).
const fetchBatch = 200

// defaultEpochLength: Besu's default epochlength for IBFT2 / QBFT. Applied only when admin_nodeInfo is readable and genesis does not set epochlength (see epochLength).
const defaultEpochLength = 30000

// BlockStamp: a block's number, timestamp, proposer (0x + lowercase), tx count, gas, round, carried vote (nil if none) and the validator set when it was produced.
// BFT = IBFT2 / QBFT fields were decoded from extraData; when false, round, vote and validator set are meaningless and the block is used for neither the production grid nor vote counting.
type BlockStamp struct {
	Number     int64
	Time       time.Time
	Proposer   string
	TxCount    int
	GasUsed    int64
	GasLimit   int64
	BFT        bool
	Round      int
	Vote       *BlockVote
	Validators []string
}

// VoteRef: which validator (Voter) voted on which address (Target); both 0x + lowercase.
type VoteRef struct {
	Target string
	Voter  string
}

// SeenVote: the block holding this validator's latest vote on this address in this epoch, and that block's time. In Besu's tally (VoteTally) a later vote from the same validator overrides the earlier one.
type SeenVote struct {
	Add   bool
	Block int64
	Time  time.Time
}

// ChainView: chain-level cache. Empty SourceNodeID = no healthy node this round.
// Votes: each (address, validator)'s latest vote in this epoch; Changed: address -> the latest block where the validator set changed because of it; Changes: every such block (oldest to newest, last = Changed). Counting rules in tallyBlocks and mergeTally.
// EpochLength 0 = unknown (not a BFT chain, not asked yet, or no node in our org has ADMIN enabled); when unknown, Votes and Changed cover only blocks in the current window and the tally is incomplete. VotesFrom: the block from which the tally is complete.
type ChainView struct {
	SourceNodeID string
	Blocks       []BlockStamp // oldest to newest
	Votes        map[VoteRef]SeenVote
	Changed      map[string]int64
	Changes      map[string][]int64
	EpochLength  int64
	EpochStart   int64
	VotesFrom    int64
	// BlockPeriod / EmptyBlockPeriod: seconds per block with / without transactions (see blockPeriods); 0 = unknown
	BlockPeriod      float64
	EmptyBlockPeriod float64
	GenesisAt        *time.Time // timestamp of block 0; nil = not fetched yet
	Forks            []Fork     // hard fork settings (admin_nodeInfo); nil = unknown
	tracking         bool       // VotesFrom has a value; when the epoch starts at 0, VotesFrom 0 means "complete", so 0 cannot mean "not started"
	gen              uint64     // incremented whenever the chain is replaced; an in-flight backfill stops when it sees a different value
}

// LatestVote: this validator's (voter) latest vote on target in this epoch, counting only votes not yet discarded (see VoteCutoffs); target and voter are both 0x + lowercase.
func (v ChainView) LatestVote(target, voter string) (SeenVote, bool) {
	sv, ok := v.Votes[VoteRef{Target: target, Voter: voter}]
	return sv, ok && v.Counts(VoteRef{Target: target, Voter: voter}, sv, v.VoteCutoffs())
}

// Counts: this vote still counts: it is after this address's cutoff (cutoffs is the result of VoteCutoffs, computed once by the caller and reused) and the voter has not been removed since.
func (v ChainView) Counts(ref VoteRef, sv SeenVote, cutoffs map[string]int64) bool {
	return sv.Block >= cutoffs[ref.Target] && !v.VoteDiscarded(ref.Voter, sv.Block, math.MaxInt64)
}

// VoteDiscarded: the voter was removed from the validator set (or removed and re-added) after block (where it cast this vote) and before to; when Besu removes it, that vote is dropped too.
// To ask "up to now", pass math.MaxInt64. voter is 0x + lowercase.
func (v ChainView) VoteDiscarded(voter string, block, to int64) bool {
	return slices.ContainsFunc(v.Changes[voter], func(n int64) bool { return n > block && n < to })
}

// VoteCutoffs: the block from which each address's votes count (earlier ones were discarded by Besu). Following Besu VoteTally.addVote: once votes in either direction reach validator count / 2 + 1,
// the list is updated (adding one already present or removing one absent is a no-op) and all votes for the address are discarded. If the list changed, that block is Changed; if not,
// e.g. retracting counter-votes reaching a majority, it can only be found by replaying each validator's latest vote in block order, and everything up to and including the discarding vote no longer counts.
// Votes from voters no longer in the list, or removed and re-added after voting (see VoteDiscarded), are not replayed.
// ponytail: only each validator's latest vote is known (older votes overridden by the same validator cannot be replayed); the threshold uses the current validator count, so it can be off by one if the count changed within this epoch.
func (v ChainView) VoteCutoffs() map[string]int64 {
	validators := v.LatestValidators()
	n := len(validators)
	member := map[string]bool{}
	for _, a := range validators {
		member[strings.ToLower(a)] = true
	}
	byTarget := map[string][]VoteRef{}
	for ref, sv := range v.Votes {
		if sv.Block >= v.Changed[ref.Target] && member[ref.Voter] && !v.VoteDiscarded(ref.Voter, sv.Block, math.MaxInt64) {
			byTarget[ref.Target] = append(byTarget[ref.Target], ref)
		}
	}
	out := make(map[string]int64, len(v.Changed)+len(byTarget))
	maps.Copy(out, v.Changed)
	if n == 0 {
		return out
	}
	need := n/2 + 1
	for target, refs := range byTarget {
		sort.Slice(refs, func(i, j int) bool { return v.Votes[refs[i]].Block < v.Votes[refs[j]].Block })
		add, remove := map[string]bool{}, map[string]bool{}
		for _, ref := range refs {
			sv := v.Votes[ref]
			if sv.Add {
				add[ref.Voter] = true
				delete(remove, ref.Voter)
			} else {
				remove[ref.Voter] = true
				delete(add, ref.Voter)
			}
			if len(add) >= need || len(remove) >= need {
				out[target] = sv.Block + 1
				add, remove = map[string]bool{}, map[string]bool{}
			}
		}
	}
	return out
}

// LatestValidators: the validator list of the latest cached block; nil when there is no list (extraData undecodable or validators kept in a contract).
func (v ChainView) LatestValidators() []string {
	for i := len(v.Blocks) - 1; i >= 0; i-- {
		if b := v.Blocks[i]; b.BFT {
			if len(b.Validators) == 0 {
				return nil
			}
			return b.Validators
		}
	}
	return nil
}

// TurnBlocks: the range the validator turn record looks at (the last SignerWindow cached blocks).
func (v ChainView) TurnBlocks() []BlockStamp {
	return v.Blocks[max(0, len(v.Blocks)-SignerWindow):]
}

// ExpectedBlockPeriod: how many seconds per block right now. Transactions waiting in the pool and the latest block has transactions = the busy period, otherwise = the empty-block period
// (as in Besu: if the pool is empty when its turn comes, it waits the full empty-block period after the previous block before producing an empty block). pending nil (TXPOOL not enabled) is treated as empty: lenient, no false alarms.
func (v ChainView) ExpectedBlockPeriod(latest BlockStamp, pending *int) float64 {
	if pending != nil && *pending > 0 && latest.TxCount > 0 {
		return v.BlockPeriod
	}
	return v.EmptyBlockPeriod
}

// Chain: a copy of the chain-level cache.
func (c *Collector) Chain() ChainView {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v := c.chain
	v.Blocks = append([]BlockStamp(nil), c.chain.Blocks...)
	v.Votes = maps.Clone(c.chain.Votes)
	v.Changed = maps.Clone(c.chain.Changed)
	v.Changes = make(map[string][]int64, len(c.chain.Changes))
	for a, ns := range c.chain.Changes {
		v.Changes[a] = slices.Clone(ns) // the copy does not share the backing array: later merges will not modify what the caller holds
	}
	return v
}

// chainSource: pure function. The highest node among those reporting themselves healthy (ignoring the platform-computed HeadBehind, see Snapshot.Status); on equal height take the smaller id so the source does not jump every round.
func chainSource(cache map[string]Result) (string, int64) {
	id, best := "", int64(-1)
	for nid, r := range cache {
		if r.Snap == nil || r.Snap.reportedStatus() != api.NodeStatusHealthy || r.Snap.Ledger.ChainHeight == nil {
			continue
		}
		h := *r.Snap.Ledger.ChainHeight
		if h > best || (h == best && nid < id) {
			id, best = nid, h
		}
	}
	return id, best
}

// missingBlocks: pure function. Keeps known blocks within [height-window, height] and returns the block numbers still to fetch (ascending).
// When the source switches to a node slightly behind, known blocks a few above height are normal, so only those extra blocks are dropped;
// if the latest known block is more than a whole window above height, the chain was replaced (network rebuilt) and everything is refetched. Block 0 is excluded: the genesis timestamp is not a production time.
// ponytail: if the rebuilt chain has already grown near the old height, old and new blocks mix until they roll out of the window; being strict would require comparing parentHash.
func missingBlocks(have []BlockStamp, height, window int64) (keep []BlockStamp, fetch []int64) {
	from := max(height-window, 1)
	if len(have) > 0 && have[len(have)-1].Number > height+window {
		have = nil
	}
	known := map[int64]bool{}
	for _, b := range have {
		if b.Number >= from && b.Number <= height {
			keep = append(keep, b)
			known[b.Number] = true
		}
	}
	for n := from; n <= height; n++ {
		if !known[n] {
			fetch = append(fetch, n)
		}
	}
	return keep, fetch
}

// tallyBlocks: pure function. Extracts votes and validator-set change points from a run of blocks ordered oldest to newest.
// Change points are detected only between adjacent blocks (consecutive numbers, both with a decodable list); addresses added or removed are recorded on the later block. Each address's change points are oldest to newest.
func tallyBlocks(blocks []BlockStamp) (map[VoteRef]SeenVote, map[string][]int64) {
	votes, changed := map[VoteRef]SeenVote{}, map[string][]int64{}
	for i, b := range blocks {
		if !b.BFT {
			continue
		}
		if b.Vote != nil && b.Proposer != "" {
			k := VoteRef{Target: b.Vote.Target, Voter: b.Proposer}
			if old, ok := votes[k]; !ok || b.Number > old.Block {
				votes[k] = SeenVote{Add: b.Vote.Add, Block: b.Number, Time: b.Time}
			}
		}
		if i == 0 || !blocks[i-1].BFT || blocks[i-1].Number != b.Number-1 || len(b.Validators) == 0 || len(blocks[i-1].Validators) == 0 {
			continue
		}
		prev := map[string]bool{}
		for _, a := range blocks[i-1].Validators {
			prev[a] = true
		}
		for _, a := range b.Validators {
			if !prev[a] {
				changed[a] = append(changed[a], b.Number)
			}
			delete(prev, a)
		}
		for a := range prev {
			changed[a] = append(changed[a], b.Number)
		}
	}
	return votes, changed
}

// mergeTally: merges newly extracted votes and change points into v (for the same key keep the newer; change points are unioned), dropping anything before the epoch start. Order-independent, so a newest-to-oldest backfill can merge too.
// Changed and Changes are modified only here and in startEpoch, always together.
func mergeTally(v *ChainView, votes map[VoteRef]SeenVote, changed map[string][]int64) {
	if v.Votes == nil {
		v.Votes = map[VoteRef]SeenVote{}
	}
	if v.Changed == nil {
		v.Changed = map[string]int64{}
	}
	if v.Changes == nil {
		v.Changes = map[string][]int64{}
	}
	for k, sv := range votes {
		if old, ok := v.Votes[k]; sv.Block >= v.EpochStart && (!ok || sv.Block > old.Block) {
			v.Votes[k] = sv
		}
	}
	for a, ns := range changed {
		all := slices.Concat(v.Changes[a], slices.DeleteFunc(slices.Clone(ns), func(n int64) bool { return n < v.EpochStart }))
		if len(all) == 0 {
			continue
		}
		slices.Sort(all)
		v.Changes[a] = slices.Compact(all)
		v.Changed[a] = v.Changes[a][len(v.Changes[a])-1]
	}
}

// startEpoch: sets the start when entering a new epoch (or learning the epoch for the first time) and clears the old epoch's votes; VotesFrom is not below the start.
// ponytail: assumes epochs count from block 0; when QBFT migrates from IBFT2 (startblock) Besu counts from startblock-1, so epoch boundaries will be misaligned.
func startEpoch(v *ChainView, epoch, height int64) {
	v.EpochLength = epoch
	start := int64(0)
	if epoch > 0 {
		start = height - height%epoch
	}
	if start != v.EpochStart {
		v.EpochStart = start
		maps.DeleteFunc(v.Votes, func(_ VoteRef, sv SeenVote) bool { return sv.Block < start })
		maps.DeleteFunc(v.Changed, func(_ string, n int64) bool { return n < start })
		for a, ns := range v.Changes {
			if kept := slices.DeleteFunc(slices.Clone(ns), func(n int64) bool { return n < start }); len(kept) > 0 {
				v.Changes[a] = kept
			} else {
				delete(v.Changes, a)
			}
		}
	}
	v.VotesFrom = max(v.VotesFrom, start)
}

// votesComplete: the tally covers the whole epoch. Block 0 is genesis and never carries votes.
func votesComplete(v ChainView) bool {
	return v.VotesFrom <= max(v.EpochStart, 1)
}

// VotesComplete: the epoch is known and all of this epoch's votes have been read in (no backfill in progress).
func (v ChainView) VotesComplete() bool {
	return v.EpochLength > 0 && v.tracking && votesComplete(v)
}

// chainSetting: pure function. Epoch length and block period are chain-wide settings, so any of our nodes that can read admin_nodeInfo counts;
// prefer the source node, then the smallest id (so the result does not jump every round). No node reporting = zero value (unknown; no fallback to defaults).
func chainSetting[T any](cache map[string]Result, source string, get func(*Snapshot) *T) T {
	if r, ok := cache[source]; ok && r.Snap != nil && get(r.Snap) != nil {
		return *get(r.Snap)
	}
	best, out := "", *new(T)
	for id, r := range cache {
		if r.Snap != nil && get(r.Snap) != nil && (best == "" || id < best) {
			best, out = id, *get(r.Snap)
		}
	}
	return out
}

func chainEpoch(cache map[string]Result, source string) int64 {
	return chainSetting(cache, source, func(s *Snapshot) *int64 { return s.EpochLength })
}

// blockPeriods: pure function. Seconds per block with transactions (busy) and without (empty); 0 = unknown.
// busy = the genesis setting; if unreadable, the median interval between blocks with transactions in the latest BlockWindow blocks and their preceding block.
// empty = the median interval of blocks with 0 transactions: Besu does not report the empty-block period, but empty blocks are produced only after it elapses, and chains without it still produce empty blocks every busy seconds;
// with no empty blocks to go on it equals busy. Each is the other's fallback. The median is unaffected by a few slow blocks (round timeouts when a validator is missing).
// ponytail: the empty-block period is inferred from observation, so after a transitions change it takes BlockWindow blocks to catch up; if Besu ever reports it over RPC, read the setting instead.
func blockPeriods(configured float64, blocks []BlockStamp) (busy, empty float64) {
	recent := blocks[max(0, len(blocks)-BlockWindow-1):]
	var full, idle []float64
	for i := 1; i < len(recent); i++ {
		if recent[i].Number != recent[i-1].Number+1 {
			continue
		}
		gap := recent[i].Time.Sub(recent[i-1].Time).Seconds()
		if recent[i].TxCount > 0 {
			full = append(full, gap)
		} else {
			idle = append(idle, gap)
		}
	}
	busy, empty = configured, median(idle)
	if busy == 0 {
		busy = median(full)
	}
	if busy == 0 {
		busy = empty
	}
	if empty == 0 {
		empty = busy
	}
	return busy, empty
}

func median(vs []float64) float64 {
	if len(vs) == 0 {
		return 0
	}
	sort.Float64s(vs)
	return vs[len(vs)/2]
}

// updateChain: fetches new blocks after each pollAll. On fetch failure the source still switches to this round's node and only known blocks are kept; the next round fills in.
// Only pollAll calls this; the backfill (backfillVotes) runs in another goroutine touching only Votes, Changed and VotesFrom. Both merge incrementally under mu, never replacing wholesale.
func (c *Collector) updateChain(ctx context.Context) {
	c.mu.RLock()
	id, height := chainSource(c.cache)
	have := c.chain.Blocks
	epoch := chainEpoch(c.cache, id)
	period := chainSetting(c.cache, id, func(s *Snapshot) *float64 { return s.BlockPeriod })
	forks := chainSetting(c.cache, id, func(s *Snapshot) *[]Fork { return s.Forks })
	needGenesis := c.chain.GenesisAt == nil
	c.mu.RUnlock()
	if id == "" {
		c.mu.Lock()
		c.chain.SourceNodeID = ""
		c.mu.Unlock()
		return
	}
	const window = KeepBlocks
	from := max(height-window, 1)
	keep, fetch := missingBlocks(have, height, window)
	c.mu.Lock()
	if len(have) > 0 && have[len(have)-1].Number > height+window {
		// latest known block more than a whole window above now = the chain was replaced, and the old chain's votes do not count (same check as missingBlocks)
		c.chain.Votes, c.chain.Changed, c.chain.Changes, c.chain.VotesFrom, c.chain.EpochStart, c.chain.tracking = nil, nil, nil, 0, 0, false
		c.chain.GenesisAt, needGenesis = nil, true
		c.chain.gen++
		c.noOlder = -1
	}
	c.chain.SourceNodeID, c.chain.Blocks = id, keep
	c.mu.Unlock()
	got := make([]BlockStamp, 0, len(fetch))
	for len(fetch) > 0 {
		n := min(len(fetch), fetchBatch)
		part, err := c.fetchBlocks(ctx, id, fetch[:n])
		got = append(got, part...)
		if err != nil {
			break
		}
		fetch = fetch[n:]
	}
	failed := len(fetch) > 0 // blocks not fetched are refetched next round via missingBlocks
	var genesisAt *time.Time
	if needGenesis {
		if g, err := c.fetchBlocks(ctx, id, []int64{0}); err == nil && len(g) == 1 {
			genesisAt = &g[0].Time
		}
	}
	blocks := append(keep, got...)
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].Number < blocks[j].Number })
	votes, changed := tallyBlocks(blocks)
	c.mu.Lock()
	defer c.mu.Unlock()
	v := &c.chain
	v.Blocks, v.Forks = blocks, forks
	v.BlockPeriod, v.EmptyBlockPeriod = blockPeriods(period, blocks)
	if genesisAt != nil {
		v.GenesisAt = genesisAt
	}
	switch {
	case failed && !v.tracking:
		v.VotesFrom = height // incomplete on the very first round: tracking has not started and no backfill runs (the backfill start cannot be an unfetched block)
	case failed:
		// already tracking: VotesFrom stays; missing blocks are filled next round
	case !v.tracking || from < v.VotesFrom:
		v.VotesFrom, v.tracking = from, true
	}
	startEpoch(v, epoch, height)
	if v.tracking && votesComplete(*v) {
		v.VotesFrom = v.EpochStart // the cache already covers the whole epoch: same as when a backfill completes
	}
	if epoch > 0 {
		mergeTally(v, votes, changed)
	} else {
		// Without the epoch we do not know at which block the tally resets: keep only votes in the current window (no backfill); proposals are still listed and can be voted on,
		// but the counts are unreliable and the API marks them unknown.
		// tracking is cleared: once the epoch is known, VotesFrom is recomputed from the window start at that time and the backfill fills the rest; gen is incremented so an in-flight backfill stops and does not write old-epoch votes back
		v.Votes, v.Changed, v.Changes, v.tracking = nil, nil, nil, false
		mergeTally(v, votes, changed) // with the epoch unknown the start is 0, so everything is accepted
		v.gen++
	}
	if !failed && epoch > 0 && !votesComplete(*v) && !c.backfilling && c.noOlder != v.EpochStart {
		c.backfilling = true
		go c.backfillVotes(ctx, id, v.gen, v.EpochStart, v.VotesFrom)
	}
}

// Backfill parameters. ponytail: the backfill covers a whole epoch (default 30000 blocks, under about 1 minute); an epoch of hundreds of thousands of blocks takes minutes to scan,
// at which point consider skipping votes before the change point or scanning on demand.
const backfillBatch = 500 // Besu's default batch limit is 1024

var backfillPause = 200 * time.Millisecond // brief pause between batches so we do not compete with the collector and logs for the agent tunnel

// backfillVotes: after a platform restart, backfills from VotesFrom back to this epoch's start, keeping only votes, not blocks.
// Each batch fetches one extra block overlapping the previous batch so validator-set change points are not lost between batches. Stops on an epoch change or chain replacement midway; fetch failures are left for the next round to restart;
// if the source node lacks earlier block headers (checkpoint sync) it stops there and this epoch is not retried.
func (c *Collector) backfillVotes(ctx context.Context, id string, gen uint64, start, hi int64) {
	defer func() {
		c.mu.Lock()
		c.backfilling = false
		c.mu.Unlock()
	}()
	low := max(start, 1)
	for hi > low {
		lo := max(low, hi-backfillBatch+1)
		nums := make([]int64, 0, hi-lo+1)
		for n := lo; n <= hi; n++ {
			nums = append(nums, n)
		}
		part, err := c.fetchBlocks(ctx, id, nums)
		if err != nil && !errors.Is(err, errNoBlock) {
			return
		}
		missing := errors.Is(err, errNoBlock)
		if missing && (len(part) == 0 || part[len(part)-1].Number != hi) {
			part = nil // accept only the contiguous run going down from hi
		}
		for i := len(part) - 1; i > 0; i-- {
			if part[i-1].Number != part[i].Number-1 {
				part = part[i:]
				break
			}
		}
		votes, changed := tallyBlocks(part)
		c.mu.Lock()
		v := &c.chain
		if v.gen != gen || v.EpochStart != start {
			c.mu.Unlock()
			return
		}
		mergeTally(v, votes, changed)
		if len(part) > 0 && part[0].Number < v.VotesFrom {
			v.VotesFrom = part[0].Number
		}
		if votesComplete(*v) {
			v.VotesFrom = start
		}
		if missing {
			c.noOlder = start
		}
		c.mu.Unlock()
		if missing || len(part) == 0 {
			return
		}
		hi = part[0].Number
		select {
		case <-ctx.Done():
			return
		case <-time.After(backfillPause):
		}
	}
}

// FetchBlock: queries one block header from the source node (for blocks not in the cache, e.g. a list change point from long before a platform outage). Returns an error if there is no source node.
func (c *Collector) FetchBlock(ctx context.Context, n int64) (BlockStamp, error) {
	c.mu.RLock()
	id := c.chain.SourceNodeID
	c.mu.RUnlock()
	if id == "" {
		return BlockStamp{}, errors.New("no source node")
	}
	b, err := c.fetchBlocks(ctx, id, []int64{n})
	if err != nil {
		return BlockStamp{}, err
	}
	if len(b) != 1 {
		return BlockStamp{}, errNoBlock
	}
	return b[0], nil
}

// errNoBlock: the node returned null for a block number (it has no header for that block).
var errNoBlock = errors.New("node does not have this block")

// fetchBlocks: fetches a range of block headers in one batch; if decoding fails midway, returns the part decoded so far and the error.
// Blocks whose extraData cannot be decoded (not IBFT2 / QBFT) still keep their timestamp, just without round and vote.
func (c *Collector) fetchBlocks(ctx context.Context, id string, nums []int64) ([]BlockStamp, error) {
	tags := make([]string, len(nums))
	for i, n := range nums {
		tags[i] = fmt.Sprintf("0x%x", n)
	}
	return c.fetchTags(ctx, id, tags)
}

// fetchTags: like fetchBlocks, with blocks given as eth_getBlockByNumber parameters (hex block number or "latest").
func (c *Collector) fetchTags(ctx context.Context, id string, tags []string) ([]BlockStamp, error) {
	calls := make([]rpcops.Call, len(tags))
	for i, t := range tags {
		calls[i] = rpcops.Call{Method: "eth_getBlockByNumber", Params: []any{t, false}}
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	res, err := c.rpc.Batch(ctx, id, calls)
	if err != nil {
		return nil, err
	}
	out := make([]BlockStamp, 0, len(res))
	var missing bool
	for i, r := range res {
		if r.Err == nil && string(r.Raw) == "null" {
			missing = true
			continue
		}
		var blk struct {
			Number       string            `json:"number"`
			Timestamp    string            `json:"timestamp"`
			Miner        string            `json:"miner"`
			ExtraData    string            `json:"extraData"`
			GasUsed      string            `json:"gasUsed"`
			GasLimit     string            `json:"gasLimit"`
			Transactions []json.RawMessage `json:"transactions"` // without full transactions this is a list of hashes; only the length is used
		}
		if err := r.Decode(&blk); err != nil {
			return out, err
		}
		ts, err := hexInt64(blk.Timestamp)
		if err != nil {
			return out, err
		}
		if blk.Number == "" {
			blk.Number = tags[i] // when a block number was requested, the requested one wins
		}
		num, err := hexInt64(blk.Number)
		if err != nil {
			return out, err
		}
		b := BlockStamp{Number: num, Time: time.Unix(ts, 0).UTC(), Proposer: strings.ToLower(blk.Miner), TxCount: len(blk.Transactions)}
		b.GasUsed, _ = hexInt64(blk.GasUsed) // undecodable counts as 0: only for display, does not affect production or vote counting
		b.GasLimit, _ = hexInt64(blk.GasLimit)
		if x, err := parseBftExtra(blk.ExtraData); err == nil {
			b.BFT, b.Round, b.Vote, b.Validators = true, x.Round, x.Vote, x.Validators
		}
		out = append(out, b)
	}
	if missing {
		return out, errNoBlock
	}
	return out, nil
}

// headInterval: interval of the lightweight chain-head update. With 10-second collection rounds on a 2-second-block chain, the "time since last block" on the page is routinely 5x the block time.
const headInterval = 2 * time.Second

// RunHead: every headInterval asks only the source node for the latest block, for GET /chain's height and latest block time; does not touch the block cache (new blocks are still filled by the collection round).
// Cleared when unreachable or when the source changes, without keeping the old value (it would be older than the collection round's).
func (c *Collector) RunHead(ctx context.Context) {
	t := time.NewTicker(headInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c.mu.RLock()
		id := c.chain.SourceNodeID
		c.mu.RUnlock()
		var head *BlockStamp
		if id != "" {
			if b, err := c.fetchTags(ctx, id, []string{"latest"}); err == nil && len(b) == 1 {
				head = &b[0]
			}
		}
		c.mu.Lock()
		c.head, c.headSource = head, id
		c.mu.Unlock()
	}
}

// Head: the latest block. The newer of the lightweight update and the block cache (height and time come from the same block, never going backwards); ok=false if neither exists.
func (c *Collector) Head() (BlockStamp, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out BlockStamp
	ok := false
	if n := len(c.chain.Blocks); n > 0 {
		out, ok = c.chain.Blocks[n-1], true
	}
	if h := c.head; h != nil && c.headSource == c.chain.SourceNodeID && (!ok || h.Number > out.Number) {
		out, ok = *h, true
	}
	return out, ok
}
