package server

import (
	"context"
	"sort"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/enode"
)

// ── GET /chain ──────────────────────────────────────────────

func (s *Server) GetChainInfo(ctx context.Context, _ api.GetChainInfoRequestObject) (api.GetChainInfoResponseObject, error) {
	view := s.col.Chain()
	head, hasHead := s.col.Head()
	now := time.Now().UTC()
	out := chainInfo(view, s.sourceSnap(view), head, hasHead, now)
	out.Stability24h = s.prom.ChainStability24h()
	if out.SourceNodeId != nil {
		rc, err := s.roundChanges1h(ctx, view, now)
		if err != nil {
			return nil, err
		}
		out.RoundChanges1h = rc
	}
	return api.GetChainInfo200JSONResponse(out), nil
}

// sourceSnap is the source node's snapshot this round; nil if there is no source.
func (s *Server) sourceSnap(view collector.ChainView) *collector.Snapshot {
	if view.SourceNodeID == "" {
		return nil
	}
	if res, ok := s.col.Get(view.SourceNodeID); ok {
		return res.Snap
	}
	return nil
}

// expectedBlockPeriod is how many seconds per block there should be now (shared by the page and
// alerts, see collector.ChainView.ExpectedBlockPeriod); 0 = unknown.
func (s *Server) expectedBlockPeriod(view collector.ChainView) float64 {
	head, _ := s.col.Head()
	return view.ExpectedBlockPeriod(head, txpoolPending(s.sourceSnap(view)))
}

func txpoolPending(src *collector.Snapshot) *int {
	if src == nil || src.Ledger == nil {
		return nil
	}
	return src.Ledger.TxpoolPending
}

// chainInfo is a pure function. src is the source node's snapshot this round and head is the latest
// block (collector.Head: the newer of the 2-second lightweight update and the block cache); without
// a source it returns only empty block intervals and 24 unknown slots (stability is filled in by
// the caller).
func chainInfo(view collector.ChainView, src *collector.Snapshot, head collector.BlockStamp, hasHead bool, now time.Time) api.ChainInfo {
	out := api.ChainInfo{BlockIntervals: []float32{}, Stability24h: []api.HourState{}, AsOf: now}
	if view.SourceNodeID == "" || src == nil || src.Ledger == nil {
		return out
	}
	id := view.SourceNodeID
	out.DataAgeSec = dataAge(src.AsOf, now)
	out.SourceNodeId, out.ChainId, out.Height = &id, src.ChainID, src.Ledger.ChainHeight
	if cs := src.Consensus; cs != nil {
		out.Consensus = cs.Algorithm
		if cs.Validators != nil {
			n, q := len(*cs.Validators), blockQuorum(len(*cs.Validators))
			out.ValidatorCount, out.BlockQuorum = &n, &q
		}
	}
	// The cache keeps more blocks for the validator production grid; block intervals look only at
	// the last BlockWindow
	recent := view.Blocks[max(0, len(view.Blocks)-collector.BlockWindow-1):]
	for i := 1; i < len(recent); i++ {
		out.BlockIntervals = append(out.BlockIntervals, float32(recent[i].Time.Sub(recent[i-1].Time).Seconds()))
	}
	// Height and latest block time come from the same block: the frontend uses both to compute
	// "time since last block"
	if hasHead {
		h, t := head.Number, head.Time
		out.Height, out.LatestBlockAt = &h, &t
		if view.Forks != nil {
			v := collector.EvmVersion(view.Forks, h, t)
			out.EvmVersion = &v
		}
	}
	positive := func(v float64) *float32 {
		if v <= 0 {
			return nil
		}
		f := float32(v)
		return &f
	}
	out.BlockPeriodSeconds, out.EmptyBlockPeriodSeconds = positive(view.BlockPeriod), positive(view.EmptyBlockPeriod)
	out.ExpectedBlockPeriodSeconds = positive(view.ExpectedBlockPeriod(head, txpoolPending(src)))
	out.GenesisAt = view.GenesisAt
	if view.EpochLength > 0 {
		l, st, from := view.EpochLength, view.EpochStart, view.VotesFrom
		out.EpochLength, out.EpochStart, out.VotesFrom = &l, &st, &from
	}
	return out
}

// roundChanges1h: how many times each scheduled proposer was skipped in the last hour (relative to
// now, so after a long halt nothing from before the halt is kept). Names and own-organization
// identity are the same as GET /validators.
func (s *Server) roundChanges1h(ctx context.Context, view collector.ChainView, now time.Time) (*api.RoundChanges, error) {
	set, _, err := s.currentValidatorSet(ctx)
	if err != nil {
		return nil, err
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	names, err := s.peerNames(ctx, nodes)
	if err != nil {
		return nil, err
	}
	return roundChangesOf(validatorTurns(lastHour(view.Blocks, now), setAddressList(set)), set, names), nil
}

// lastHour is a pure function returning the part of blocks (oldest to newest) within one hour
// before now.
func lastHour(blocks []collector.BlockStamp, now time.Time) []collector.BlockStamp {
	from := now.Add(-time.Hour)
	return blocks[sort.Search(len(blocks), func(i int) bool { return !blocks[i].Time.Before(from) }):]
}

// roundChangesOf is a pure function. turns is the result of validatorTurns; returns each
// validator's missed count, most to least (ties by address). set fills in org / nodeId for our own
// nodes.
func roundChangesOf(turns map[string][]api.ValidatorTurn, set []api.ValidatorSetEntry, names map[string]string) *api.RoundChanges {
	bySet := map[string]api.ValidatorSetEntry{}
	for _, v := range set {
		bySet[enode.NormalizeAddress(v.Address)] = v
	}
	out := &api.RoundChanges{ByProposer: []api.RoundChangeCount{}}
	for addr, ts := range turns {
		n := missedTurns(ts)
		if n == 0 {
			continue
		}
		e := api.RoundChangeCount{Address: "0x" + addr, Count: n}
		if v, ok := bySet[addr]; ok {
			e.Address, e.OrgId, e.NodeId = v.Address, v.OrgId, v.NodeId
		}
		if name, ok := names[addr]; ok {
			e.Name = &name
		}
		out.Total += n
		out.ByProposer = append(out.ByProposer, e)
	}
	sort.Slice(out.ByProposer, func(i, j int) bool {
		a, b := out.ByProposer[i], out.ByProposer[j]
		return a.Count > b.Count || a.Count == b.Count && a.Address < b.Address
	})
	return out
}

// blockQuorum is the block production quorum ceil(2N/3), the same as Besu's
// BftHelpers.calculateRequiredValidatorQuorum. The frontend's proposal confirmation dialog has its
// own copy (frontend/src/lib/validators.ts setAfter); change both together.
func blockQuorum(n int) int {
	return (2*n + 2) / 3
}
