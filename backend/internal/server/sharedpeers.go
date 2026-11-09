package server

import (
	"context"
	"sort"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/enode"
	"besu-manager/backend/internal/store"
)

// ListSharedPeers: GET /shared-peers. For each shared-list entry, how many nodes are connected to
// it, each node's usage, and candidates that our nodes are connected to but are not in the list.
// All data comes from the admin_peers the collector read this round; no extra RPC.
func (s *Server) ListSharedPeers(ctx context.Context, _ api.ListSharedPeersRequestObject) (api.ListSharedPeersResponseObject, error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	manual, err := s.store.ListManualPeers(ctx)
	if err != nil {
		return nil, err
	}
	custom, err := s.store.ListNodePeers(ctx, "")
	if err != nil {
		return nil, err
	}
	set, _, err := s.currentValidatorSet(ctx)
	if err != nil {
		return nil, err
	}
	validators := map[string]bool{}
	for _, v := range set {
		validators[enode.NormalizeAddress(v.Address)] = true
	}
	names, err := s.peerNames(ctx, nodes)
	if err != nil {
		return nil, err
	}
	// Count only nodes whose admin_peers was actually read: Peers being nil (ADMIN not enabled,
	// offline, just restarted) means "unknown", not "zero peers".
	perNode := map[string][]collector.ObservedPeer{}
	for _, n := range nodes {
		if res, ok := s.col.Get(n.ID); ok && res.Snap != nil && res.Snap.Peers != nil {
			perNode[n.ID] = res.Snap.Peers
		}
	}
	return api.ListSharedPeers200JSONResponse(buildSharedPeers(nodes, manual, custom, perNode, validators, names)), nil
}

// buildSharedPeers is a pure function.
//   - Each node's missing count is against everything it should push (shared + self-added),
//     regardless of the toggle, and only for nodes that reported admin_peers this round.
//   - Candidates come from all of our reporting nodes (regardless of the toggle), most widely seen
//     first: each extra node that sees one adds information.
//
// validators and names are both keyed by NormalizeAddress'd addresses.
func buildSharedPeers(nodes []store.Node, manual []store.ManualPeer, custom []store.NodePeer, perNode map[string][]collector.ObservedPeer,
	validators map[string]bool, names map[string]string) api.SharedPeers {
	own := ownPubkeys(nodes)
	connectedTo := map[string]map[string]bool{} // node id -> the pubkeys it is connected to
	for id, peers := range perNode {
		m := map[string]bool{}
		for _, p := range peers {
			m[p.Pubkey] = true
		}
		connectedTo[id] = m
	}
	out := api.SharedPeers{Peers: []api.SharedPeer{}, Candidates: []api.PeerCandidate{}, Nodes: []api.SharedPeerNode{}}

	// Push targets of the shared list: toggle on, not quarantined; for each, also count how many of
	// everything it should push are not connected
	targets := pushTargets(nodes, manual, custom)
	additions := countByNode(custom)
	for _, n := range nodes { // ListNodes is already sorted by id
		sn := api.SharedPeerNode{Id: n.ID, UseShared: n.PeerShared, Additions: additions[n.ID], Quarantined: n.QuarantinedAt != nil}
		_, seen := perNode[n.ID]
		if n.QuarantinedAt == nil && seen {
			missing := 0
			for _, e := range targets[n.ID] {
				if pub, _ := enode.PubKeyHex(e); !connectedTo[n.ID][pub] {
					missing++
				}
			}
			sn.Missing = &missing
		}
		out.Nodes = append(out.Nodes, sn)
	}
	nameOf := func(pub string) *string {
		if n, ok := nameOfPub(names, pub); ok {
			return &n
		}
		return nil
	}

	inList := map[string]bool{}
	for _, m := range manual {
		inList[m.Pubkey] = true
		p := api.SharedPeer{Pubkey: m.Pubkey, Enode: m.Enode, Address: pubAddress(m.Pubkey), Name: nameOf(m.Pubkey)}
		if self := own[m.Pubkey]; self != "" {
			p.NodeId = &self
		}
		out.Peers = append(out.Peers, p)
	}

	// Candidates: walk in node id order and take the enode from the first node that sees it, so the
	// output is stable
	ids := make([]string, 0, len(perNode))
	for id := range perNode {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	byPub := map[string]*api.PeerCandidate{}
	seenBy := map[string]int{} // pubkey -> how many of our nodes see it
	var order []string
	for _, id := range ids {
		seen := map[string]bool{}
		for _, p := range perNode[id] {
			if own[p.Pubkey] != "" || inList[p.Pubkey] || seen[p.Pubkey] {
				continue
			}
			seen[p.Pubkey] = true
			seenBy[p.Pubkey]++
			if byPub[p.Pubkey] != nil {
				continue
			}
			addr := pubAddress(p.Pubkey)
			c := &api.PeerCandidate{Pubkey: p.Pubkey, Address: addr, Enode: p.Enode, Name: nameOf(p.Pubkey)}
			if addr != "" && validators[enode.NormalizeAddress(addr)] {
				c.ValidatorAddress = &addr
			}
			byPub[p.Pubkey] = c
			order = append(order, p.Pubkey)
		}
	}
	for _, pub := range order {
		out.Candidates = append(out.Candidates, *byPub[pub])
	}
	sort.SliceStable(out.Candidates, func(i, j int) bool { return seenBy[out.Candidates[i].Pubkey] > seenBy[out.Candidates[j].Pubkey] })
	return out
}
