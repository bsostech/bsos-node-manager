package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/store"
)

// Extra peers per node: the toggle for applying the shared list + the ones added individually; push
// rules are in peers.go.

// peerModeOf is the summary used by the UI, derived from the toggle and individual additions (the
// "custom peer" marker in the node list).
func peerModeOf(n store.Node, additions int) api.PeerMode {
	switch {
	case n.PeerShared:
		return api.PeerModeShared
	case additions > 0:
		return api.PeerModeCustom
	}
	return api.PeerModeOff
}

// additionCounts: how many peers each node added individually.
func (s *Server) additionCounts(ctx context.Context) (map[string]int, error) {
	list, err := s.store.ListNodePeers(ctx, "")
	if err != nil {
		return nil, err
	}
	return countByNode(list), nil
}

// countByNode is a pure function: how many entries each node added individually.
func countByNode(list []store.NodePeer) map[string]int {
	out := map[string]int{}
	for _, p := range list {
		out[p.NodeID]++
	}
	return out
}

// quarantinedPubkeys: public keys of our quarantined nodes.
func quarantinedPubkeys(nodes []store.Node) map[string]bool {
	out := map[string]bool{}
	for _, n := range nodes {
		if n.QuarantinedAt == nil {
			continue
		}
		for pub := range ownPubkeys([]store.Node{n}) {
			out[pub] = true
		}
	}
	return out
}

// customPeersView builds the NodeCustomPeers response (shared by GET, toggling, and add/delete).
func (s *Server) customPeersView(ctx context.Context, n *store.Node) (api.NodeCustomPeers, error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return api.NodeCustomPeers{}, err
	}
	list, err := s.store.ListNodePeers(ctx, n.ID)
	if err != nil {
		return api.NodeCustomPeers{}, err
	}
	names, err := s.peerNames(ctx, nodes)
	if err != nil {
		return api.NodeCustomPeers{}, err
	}
	return buildCustomPeers(*n, nodes, list, names), nil
}

// buildCustomPeers is a pure function that marks each individually added entry with which of our
// nodes it belongs to and whether it is quarantined. names maps normalized address -> name, as
// peerNames returns it.
func buildCustomPeers(n store.Node, nodes []store.Node, list []store.NodePeer, names map[string]string) api.NodeCustomPeers {
	own, quarantined := ownPubkeys(nodes), quarantinedPubkeys(nodes)
	view := api.NodeCustomPeers{UseShared: n.PeerShared, Peers: []api.CustomPeer{}}
	for _, p := range list {
		c := api.CustomPeer{Pubkey: p.Pubkey, Enode: p.Enode, Quarantined: quarantined[p.Pubkey]}
		if id := own[p.Pubkey]; id != "" {
			c.NodeId = &id
		}
		if name, ok := nameOfPub(names, p.Pubkey); ok {
			c.Name = &name
		}
		view.Peers = append(view.Peers, c)
	}
	return view
}

// ── GET /nodes/{id}/custom-peers ────────────────────────────

func (s *Server) GetNodeCustomPeers(ctx context.Context, req api.GetNodeCustomPeersRequestObject) (api.GetNodeCustomPeersResponseObject, error) {
	n, err := s.requireOwnNode(ctx, req.Id)
	if errors.Is(err, ErrNodeNotFound) {
		return api.GetNodeCustomPeers404JSONResponse(nodeNotFoundError), nil
	}
	if err != nil {
		return nil, err
	}
	view, err := s.customPeersView(ctx, n)
	if err != nil {
		return nil, err
	}
	return api.GetNodeCustomPeers200JSONResponse(view), nil
}

// ── PUT /nodes/{id}/peer-shared ─────────────────────────────

// SetNodePeerShared saves the toggle first, then pushes to this node once immediately; a failed
// push returns no error, the next background resend round fills it in. It pushes once even if the
// value did not change.
func (s *Server) SetNodePeerShared(ctx context.Context, req api.SetNodePeerSharedRequestObject) (api.SetNodePeerSharedResponseObject, error) {
	n, err := s.requireOwnNode(ctx, req.Id)
	if errors.Is(err, ErrNodeNotFound) {
		return api.SetNodePeerShared404JSONResponse(nodeNotFoundError), nil
	}
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return api.SetNodePeerShared400JSONResponse{Code: api.ErrorCodeInvalid, Message: "empty body"}, nil // the contract's required check rejects this first; unreachable
	}
	on := req.Body.UseShared
	d := map[string]any{"node_id": n.ID, "use_shared": on}
	if err := s.audit(ctx, "node_peer_shared_set", d); err != nil {
		return nil, err
	}
	ok, err := s.store.SetPeerShared(ctx, n.ID, on)
	if err != nil {
		s.auditFailedInternal(ctx, "node_peer_shared_set", d, err)
		return nil, err
	}
	if !ok {
		s.auditFailedReason(ctx, "node_peer_shared_set", d, api.ErrorReasonNodeNotFound, nil)
		return api.SetNodePeerShared404JSONResponse(nodeNotFoundError), nil
	}
	n.PeerShared = on
	if err := s.pushNode(ctx, n.ID); err != nil {
		log.Printf("peers: node %s: push after toggle (retrying next round): %v", n.ID, err)
	}
	view, err := s.customPeersView(ctx, n)
	if err != nil {
		return nil, err
	}
	return api.SetNodePeerShared200JSONResponse(view), nil
}

// ── POST /nodes/{id}/custom-peers ───────────────────────────

func (s *Server) CreateNodeCustomPeer(ctx context.Context, req api.CreateNodeCustomPeerRequestObject) (api.CreateNodeCustomPeerResponseObject, error) {
	n, err := s.requireOwnNode(ctx, req.Id)
	if errors.Is(err, ErrNodeNotFound) {
		return api.CreateNodeCustomPeer404JSONResponse(nodeNotFoundError), nil
	}
	if err != nil {
		return nil, err
	}
	bad := func(e api.Error) (api.CreateNodeCustomPeerResponseObject, error) {
		return api.CreateNodeCustomPeer400JSONResponse(e), nil
	}
	if req.Body == nil {
		return bad(api.Error{Code: api.ErrorCodeInvalid, Message: "missing enode"}) // the contract's required check rejects this first; unreachable
	}
	en, pub, err := parseManualEnode(req.Body.Enode)
	if err != nil {
		return bad(enodeError(err))
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	if ownPubkeys(nodes)[pub] == n.ID {
		return bad(apierr.New(api.ErrorCodeInvalid, api.ErrorReasonPeerIsSelf, map[string]string{"nodeId": n.ID}))
	}
	if n.PeerShared && !quarantinedPubkeys(nodes)[pub] { // quarantined nodes are never pushed, whichever list they are on, so they do not count as "this node will connect"
		manual, err := s.store.ListManualPeers(ctx)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(manual, func(m store.ManualPeer) bool { return m.Pubkey == pub }) {
			return api.CreateNodeCustomPeer409JSONResponse(apierr.New(api.ErrorCodeConflict, api.ErrorReasonPeerInSharedList, nil)), nil
		}
	}
	d := map[string]any{"node_id": n.ID, "pubkey": pub, "enode": en}
	if err := s.audit(ctx, "node_peer_added", d); err != nil {
		return nil, err
	}
	added, full, err := s.store.AddNodePeer(ctx, store.NodePeer{NodeID: n.ID, Pubkey: pub, Enode: en, CreatedAt: time.Now().UTC()}, manualPeerLimit)
	switch {
	case err != nil:
		s.auditFailedInternal(ctx, "node_peer_added", d, err)
		return nil, err
	case full:
		p := map[string]string{"limit": strconv.Itoa(manualPeerLimit)}
		s.auditFailedReason(ctx, "node_peer_added", d, api.ErrorReasonPeerListFull, p)
		return bad(apierr.New(api.ErrorCodeInvalid, api.ErrorReasonPeerListFull, p))
	case !added:
		s.auditFailedReason(ctx, "node_peer_added", d, api.ErrorReasonPeerExists, nil)
		return api.CreateNodeCustomPeer409JSONResponse(apierr.New(api.ErrorCodeConflict, api.ErrorReasonPeerExists, nil)), nil
	}
	c, err := s.customPeerFromView(ctx, n, pub)
	if err != nil {
		return nil, err
	}
	return api.CreateNodeCustomPeer201JSONResponse(c), nil
}

// customPeerFromView returns the entry just written (with derived fields such as nodeId and name).
func (s *Server) customPeerFromView(ctx context.Context, n *store.Node, pub string) (api.CustomPeer, error) {
	view, err := s.customPeersView(ctx, n)
	if err != nil {
		return api.CustomPeer{}, err
	}
	for _, c := range view.Peers {
		if c.Pubkey == pub {
			return c, nil
		}
	}
	return api.CustomPeer{}, fmt.Errorf("custom peer %s not found after write", pub)
}

// ── DELETE /nodes/{id}/custom-peers/{pubkey} ────────────────

func (s *Server) DeleteNodeCustomPeer(ctx context.Context, req api.DeleteNodeCustomPeerRequestObject) (api.DeleteNodeCustomPeerResponseObject, error) {
	n, err := s.requireOwnNode(ctx, req.Id)
	if errors.Is(err, ErrNodeNotFound) {
		return api.DeleteNodeCustomPeer404JSONResponse(nodeNotFoundError), nil
	}
	if err != nil {
		return nil, err
	}
	pub := strings.ToLower(req.Pubkey)
	// A string that is not a pubkey format cannot be in the list, so return 404 directly instead of
	// writing arbitrary strings into the audit
	if !pubkeyHexRe.MatchString(pub) {
		return api.DeleteNodeCustomPeer404JSONResponse(apierr.New(api.ErrorCodeNotFound, api.ErrorReasonPeerNotFound, nil)), nil
	}
	d := map[string]any{"node_id": n.ID, "pubkey": pub}
	if err := s.audit(ctx, "node_peer_deleted", d); err != nil {
		return nil, err
	}
	ok, err := s.store.DeleteNodePeer(ctx, n.ID, pub)
	if err != nil {
		s.auditFailedInternal(ctx, "node_peer_deleted", d, err)
		return nil, err
	}
	if !ok {
		s.auditFailedReason(ctx, "node_peer_deleted", d, api.ErrorReasonPeerNotFound, nil)
		return api.DeleteNodeCustomPeer404JSONResponse(apierr.New(api.ErrorCodeNotFound, api.ErrorReasonPeerNotFound, nil)), nil
	}
	return api.DeleteNodeCustomPeer204Response{}, nil
}
