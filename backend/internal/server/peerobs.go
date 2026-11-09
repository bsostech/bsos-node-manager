package server

import (
	"context"
	"strings"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/auth"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/enode"
	"besu-manager/backend/internal/store"
)

// Peer observation: the platform only labels what it can prove itself: that the other side holds a
// given public key (present in admin_peers = handshake succeeded), and whether the address derived
// from that key is an on-chain validator. It does not judge "whose node this is"; names are given
// by our own users (peer_names), and external organizations' lists play no part.

// pubAddress is the address derived from a public key (0x + lowercase); returns an empty string for
// an invalid key.
func pubAddress(pub string) string {
	addr, _ := enode.AddressFromPubKey(pub)
	return addr
}

// ownPubkeys maps our nodes' public keys -> node id (from the enode reported by net_enode).
func ownPubkeys(nodes []store.Node) map[string]string {
	out := map[string]string{}
	for _, n := range nodes {
		if n.ReportedEnode == nil {
			continue
		}
		if pub, err := enode.PubKeyHex(*n.ReportedEnode); err == nil {
			out[pub] = n.ID
		}
	}
	return out
}

// classifyPeers is a pure function. validators is the set of on-chain validator addresses
// (enode.NormalizeAddress'd); names maps address (normalized the same way) -> user-given name.
// Whether a peer is ours is shown by nodeId and whether it is a validator by validatorAddress, each
// in its own field (our own validators have both).
func classifyPeers(obs []collector.ObservedPeer, own map[string]string, validators map[string]bool, names map[string]string) []api.PeerInfo {
	out := make([]api.PeerInfo, 0, len(obs))
	for _, p := range obs {
		addr := pubAddress(p.Pubkey)
		pub, en := p.Pubkey, p.Enode
		info := api.PeerInfo{Pubkey: &pub, Address: addr, Enode: &en, Inbound: p.Inbound}
		if p.RemoteAddress != "" {
			ra := p.RemoteAddress
			info.RemoteAddress = &ra
		}
		if addr != "" && validators[enode.NormalizeAddress(addr)] {
			info.ValidatorAddress = &addr
		}
		if id := own[p.Pubkey]; id != "" {
			info.NodeId = &id
		}
		if name, ok := nameOfPub(names, p.Pubkey); ok {
			info.Name = &name
		}
		out = append(out, info)
	}
	return out
}

// connectedCounts is a pure function: for each address (NormalizeAddress'd), how many of our nodes
// list it in admin_peers (repeats on the same node count once).
func connectedCounts(perNode map[string][]collector.ObservedPeer) map[string]int {
	out := map[string]int{}
	for _, peers := range perNode {
		seen := map[string]bool{}
		for _, p := range peers {
			addr := enode.NormalizeAddress(pubAddress(p.Pubkey))
			if addr == "" || seen[addr] {
				continue
			}
			seen[addr] = true
			out[addr]++
		}
	}
	return out
}

// peerNames maps address (enode.NormalizeAddress'd) -> display name. Our own nodes use nodes.name
// (an unnamed node has no entry and the UI falls back to the id), others use peer_names; old names
// for our own nodes in peer_names are not used.
func (s *Server) peerNames(ctx context.Context, nodes []store.Node) (map[string]string, error) {
	list, err := s.store.ListPeerNames(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(list))
	for _, n := range list {
		out[enode.NormalizeAddress(n.Address)] = n.Name
	}
	overlayOwnNames(out, nodes)
	return out, nil
}

// overlayOwnNames is a pure function that replaces our nodes' addresses from peer_names with
// nodes.name (removing unnamed ones).
func overlayOwnNames(out map[string]string, nodes []store.Node) {
	nameOf := map[string]*string{}
	for _, n := range nodes {
		nameOf[n.ID] = n.Name
	}
	for addr, id := range ownAddresses(nodes) {
		delete(out, addr)
		if nm := nameOf[id]; nm != nil {
			out[addr] = *nm
		}
	}
}

// ownAddresses maps our nodes' addresses (NormalizeAddress'd) -> node id, derived from the enode
// public key. When the collector reads net_enode it writes validator_address together with the
// enode (collector.go SetNodeIdentity), so the addresses derived from both agree; the peer page,
// validator page and shared peers all use this so the logic is not written separately in each.
func ownAddresses(nodes []store.Node) map[string]string {
	out := map[string]string{}
	for pub, id := range ownPubkeys(nodes) {
		if addr := enode.NormalizeAddress(pubAddress(pub)); addr != "" {
			out[addr] = id
		}
	}
	return out
}

// nameOfPub looks up a name by public key (names are stored by address).
func nameOfPub(names map[string]string, pub string) (string, bool) {
	addr := pubAddress(pub)
	if addr == "" {
		return "", false
	}
	n, ok := names[enode.NormalizeAddress(addr)]
	return n, ok
}

func (s *Server) ListNodePeers(ctx context.Context, req api.ListNodePeersRequestObject) (api.ListNodePeersResponseObject, error) {
	n, err := s.store.GetNode(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return api.ListNodePeers404JSONResponse(nodeNotFoundError), nil
	}
	nodes, err := s.store.ListNodes(ctx)
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
	var obs []collector.ObservedPeer
	reported := false
	if res, ok := s.col.Get(n.ID); ok && res.Snap != nil && res.Snap.Peers != nil {
		obs, reported = res.Snap.Peers, true
	}
	peers := visiblePeers(ctx, classifyPeers(obs, ownPubkeys(nodes), validators, names))
	return api.ListNodePeers200JSONResponse{Reported: reported, Peers: peers}, nil
}

// visiblePeers: public keys, enodes and remote addresses are shown to those who can see monitoring;
// requests without a logged-in user (forwarded cross-organization via 8544) get only the allowlisted
// fields.
func visiblePeers(ctx context.Context, peers []api.PeerInfo) []api.PeerInfo {
	if !auth.PrincipalFrom(ctx).Can(auth.PermMonitorView) {
		for i, p := range peers {
			peers[i] = publicPeer(p)
		}
	}
	return peers
}

// publicPeer is a pure function returning the fields visible to requests without monitor:view; it
// copies an allowlist, so fields added to PeerInfo later do not leak by default.
func publicPeer(p api.PeerInfo) api.PeerInfo {
	return api.PeerInfo{Address: p.Address, Inbound: p.Inbound, NodeId: p.NodeId, Name: p.Name, ValidatorAddress: p.ValidatorAddress}
}

func (s *Server) SetPeerName(ctx context.Context, req api.SetPeerNameRequestObject) (api.SetPeerNameResponseObject, error) {
	if !validAddress(req.Address) {
		return api.SetPeerName400JSONResponse{Code: api.ErrorCodeInvalid, Message: "address must be 0x followed by 40 hex digits"}, nil // the contract's pattern rejects this first; unreachable
	}
	addr := strings.ToLower(req.Address)
	name := ""
	if req.Body != nil {
		name = strings.TrimSpace(req.Body.Name)
	}
	if name == "" || len([]rune(name)) > nodeNameMax {
		return api.SetPeerName400JSONResponse(nameRequiredError), nil
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	if ownAddresses(nodes)[enode.NormalizeAddress(addr)] != "" {
		return api.SetPeerName409JSONResponse(apierr.New(api.ErrorCodeConflict, api.ErrorReasonPeerIsOwnNode, nil)), nil
	}
	d := map[string]any{"address": addr, "name": name}
	if err := s.audit(ctx, "peer_name_set", d); err != nil {
		return nil, err
	}
	if err := s.store.SetPeerName(ctx, store.PeerName{Address: addr, Name: name}); err != nil {
		s.auditFailedInternal(ctx, "peer_name_set", d, err)
		return nil, err
	}
	return api.SetPeerName204Response{}, nil
}

func (s *Server) DeletePeerName(ctx context.Context, req api.DeletePeerNameRequestObject) (api.DeletePeerNameResponseObject, error) {
	if !validAddress(req.Address) {
		// the contract's pattern rejects this first with the same error; no audit, so arbitrary strings are not written into the record
		return api.DeletePeerName400JSONResponse(apierr.New(api.ErrorCodeInvalid, api.ErrorReasonFieldFormat, map[string]string{"field": "address"})), nil
	}
	addr := strings.ToLower(req.Address)
	d := map[string]any{"address": addr}
	if err := s.audit(ctx, "peer_name_deleted", d); err != nil {
		return nil, err
	}
	if err := s.store.DeletePeerName(ctx, addr); err != nil {
		s.auditFailedInternal(ctx, "peer_name_deleted", d, err)
		return nil, err
	}
	return api.DeletePeerName204Response{}, nil
}
