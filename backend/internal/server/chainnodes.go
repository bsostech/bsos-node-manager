package server

import (
	"context"
	"sort"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/enode"
)

// ── GET /chain/nodes ────────────────────────────────────────
// Chain nodes: validator set union our own nodes union the nodes seen in admin_peers of each of our nodes.

func (s *Server) ListChainNodes(ctx context.Context, _ api.ListChainNodesRequestObject) (api.ListChainNodesResponseObject, error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	names, err := s.peerNames(ctx, nodes)
	if err != nil {
		return nil, err
	}
	view := s.col.Chain()
	addrs, _ := s.chainConsensus(nodes)
	set := validatorEntries(addrs, nodes)
	s.annotateTurns(ctx, set, view, s.observedPeers(nodes))
	idOf := ownAddresses(nodes)
	own := make([]ownNodeView, 0, len(idOf))
	for addr, id := range idOf {
		o := ownNodeView{id: id, addr: addr}
		if res, ok := s.col.Get(id); ok && res.Snap != nil {
			o.peers = res.Snap.Peers
			if l := res.Snap.Ledger; l != nil {
				o.height, o.behind = l.ChainHeight, res.Snap.BlocksBehind()
			}
		}
		own = append(own, o)
	}
	return api.ListChainNodes200JSONResponse(chainNodes(set, own, names, view.Blocks)), nil
}

// ownNodeView is what one of our nodes reported this round: address (NormalizeAddress'd), height
// and blocks behind, admin_peers. Height and peers come from the same batch.
type ownNodeView struct {
	id, addr string
	height   *int64
	behind   *int
	peers    []collector.ObservedPeer
}

// chainNodes is a pure function. set already has turn history and production verdicts
// (annotateTurns); names maps address (NormalizeAddress'd) -> name; blocks is the block cache
// (oldest to newest).
func chainNodes(set []api.ValidatorSetEntry, own []ownNodeView, names map[string]string, blocks []collector.BlockStamp) []api.ChainNode {
	rows := map[string]*api.ChainNode{}
	row := func(key string) *api.ChainNode {
		if r, ok := rows[key]; ok {
			return r
		}
		r := &api.ChainNode{Address: "0x" + key}
		if name, ok := names[key]; ok {
			r.Name = &name
		}
		rows[key] = r
		return r
	}
	for _, v := range set {
		r := row(enode.NormalizeAddress(v.Address))
		r.IsValidator, r.Producing = true, v.Producing
		if len(v.Turns) > 0 { // no turn history (cache not filled yet, or no source node) means unknown, not 0 misses; same as producing being null
			missed := missedTurns(v.Turns)
			r.MissedTurns1000 = &missed
		}
	}
	for _, o := range own {
		r := row(o.addr)
		id := o.id
		r.IsOwn, r.NodeId = true, &id
		if o.behind != nil {
			b := int64(*o.behind)
			r.BlocksBehind = &b
		}
	}
	// Other organizations' nodes: the latestBlock seen in the same round by the highest of our
	// nodes connected to it (Besu usually records the previous block, so subtract 1, minimum 0)
	connected, reporterHeight := map[string]int{}, map[string]int64{}
	for _, o := range own {
		seen := map[string]bool{}
		for _, p := range o.peers {
			key := enode.NormalizeAddress(pubAddress(p.Pubkey))
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			connected[key]++
			r := row(key)
			if r.IsOwn || o.height == nil || p.LatestBlock == nil { // our own nodes use the same lag as NodeSummary (the larger of the self-reported value and the one the platform computes from the chain head)
				continue
			}
			if h, ok := reporterHeight[key]; ok && h >= *o.height {
				continue
			}
			reporterHeight[key] = *o.height
			b := max(0, *o.height-1-*p.LatestBlock)
			r.BlocksBehind = &b
		}
	}
	// Last time as proposer: search from newest to oldest for the first block each validator
	// appears in; stop once all are found
	left := len(set)
	for i := len(blocks) - 1; i >= 0 && left > 0; i-- {
		r, ok := rows[enode.NormalizeAddress(blocks[i].Proposer)]
		if !ok || !r.IsValidator || r.LastProposed != nil {
			continue
		}
		r.LastProposed = &api.ProposedBlock{Height: blocks[i].Number, At: blocks[i].Time}
		left--
	}
	out := make([]api.ChainNode, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.IsValidator != b.IsValidator {
			return a.IsValidator
		}
		if a.IsOwn != b.IsOwn {
			return a.IsOwn
		}
		if ca, cb := connected[enode.NormalizeAddress(a.Address)], connected[enode.NormalizeAddress(b.Address)]; ca != cb {
			return ca > cb
		}
		return a.Address < b.Address
	})
	return out
}
