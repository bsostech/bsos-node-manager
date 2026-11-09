package server

import (
	"context"
	"log"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"besu-manager/backend/internal/rpcops"
	"besu-manager/backend/internal/store"
)

// Extra peers: each node has an "apply shared list" toggle (off by default) + its own custom peers;
// the platform pushes only the lists users entered, never auto-pushes our org's nodes, and does not
// touch static-nodes.json / bootnodes.

// RunPeerPush: in the background, resends the whole set with admin_addPeer every round (see
// pushPeers). No separate trigger events are needed: a node joining or being deleted, or a list or
// toggle change, just makes the next round compute a different result; convergence itself is
// idempotent.
func (s *Server) RunPeerPush(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.pushPeers(ctx); err != nil {
				log.Printf("peers: send admin_addPeer: %v", err)
			}
		}
	}
}

// pushPeers: runs pushNode once for each of our nodes. Besu keeps peers only in memory, so they are
// sent every round; offline nodes fail as a whole and are silently skipped until the next round.
func (s *Server) pushPeers(ctx context.Context) error {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return err
	}
	s.forgetGonePeers(nodes)
	for _, n := range nodes {
		if err := s.pushNode(ctx, n.ID); err != nil {
			return err
		}
	}
	return nil
}

// peerPushMu: push one node at a time and read the DB only inside the lock; pushing from a stale
// snapshot taken outside the lock would re-add peers removed after a toggle change and never remove
// them again. ponytail: global lock, each node rereads the whole list; at hundreds of nodes switch
// to per-node locks and one snapshot per round.
var peerPushMu sync.Mutex

// pushNode: resends the targets computed by pushTargets to this node with admin_addPeer, and
// removes with admin_removePeer those sent last round (nodes.pushed_peers) but not wanted this
// round. Returns an error only on DB read failure; an unreachable node is skipped until the next
// round.
func (s *Server) pushNode(ctx context.Context, id string) error {
	peerPushMu.Lock()
	defer peerPushMu.Unlock()
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(nodes, func(n store.Node) bool { return n.ID == id })
	if i < 0 { // just deleted
		return nil
	}
	n := nodes[i]
	manual, err := s.store.ListManualPeers(ctx)
	if err != nil {
		return err
	}
	custom, err := s.store.ListNodePeers(ctx, id)
	if err != nil {
		return err
	}
	want := pushTargets(nodes, manual, custom)[id]
	drop := rpcops.PeerDiff(n.PushedPeers, want)
	if len(want) == 0 && len(drop) == 0 { // nothing to push and nothing to remove: skip the RPC
		s.setMissing(id, "")
		return nil
	}
	res, err := s.ops.SyncPeers(ctx, id, want, drop)
	if err != nil {
		return nil
	}
	for _, e := range res.Failed {
		log.Printf("peers: node %s: %v", id, e)
	}
	sent := want
	if len(res.Unremoved) > 0 { // removals that were refused stay recorded and are retried next round
		sent = append(append([]string{}, want...), res.Unremoved...)
		sort.Strings(sent)
	}
	if !slices.Equal(sent, n.PushedPeers) {
		if err := s.store.SetPushedPeers(ctx, id, sent); err != nil {
			log.Printf("peers: node %s: save sent list to DB (retrying next round): %v", id, err)
		}
	}
	missing := strings.Join(res.Missing, ",")
	if s.setMissing(id, missing) && missing != "" {
		log.Printf("peers: node %s: sent %d peers, %d not connected yet: %v", id, len(want), len(res.Missing), res.Missing)
	}
	return nil
}

// setMissing: records which peers this node failed to connect this round; returns whether that
// differs from last round (only to log on change).
func (s *Server) setMissing(id, missing string) bool {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	changed := s.peersMissing[id] != missing
	s.peersMissing[id] = missing
	return changed
}

// pushTargets: pure function. The enodes to send each node this round (sorted): with the toggle on,
// the shared list first, then the custom ones; each public key is sent only once, the shared-list
// entry wins (same as the frontend).
func pushTargets(nodes []store.Node, manual []store.ManualPeer, custom []store.NodePeer) map[string][]string {
	own, quarantined := ownPubkeys(nodes), quarantinedPubkeys(nodes)
	lists := map[string][]peerEntry{}
	for _, p := range custom {
		lists[p.NodeID] = append(lists[p.NodeID], peerEntry{p.Pubkey, p.Enode})
	}
	out := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		var es []peerEntry
		if n.PeerShared {
			for _, m := range manual {
				es = append(es, peerEntry{m.Pubkey, m.Enode})
			}
		}
		out[n.ID] = filterPeers(n.ID, append(es, lists[n.ID]...), own, quarantined)
	}
	return out
}

// sharedListFor: the shared list as pushed to a given node (regardless of the toggle; used for the
// count shown in the UI).
func sharedListFor(id string, manual []store.ManualPeer, own map[string]string, quarantined map[string]bool) []string {
	es := make([]peerEntry, 0, len(manual))
	for _, m := range manual {
		es = append(es, peerEntry{m.Pubkey, m.Enode})
	}
	return filterPeers(id, es, own, quarantined)
}

type peerEntry struct{ pubkey, enode string }

// filterPeers: drops this node itself and entries whose public key belongs to a quarantined node of
// ours (same exclusion as SD); keeps only the first entry per public key; returns sorted enodes.
func filterPeers(id string, es []peerEntry, own map[string]string, quarantined map[string]bool) []string {
	want := []string{}
	seen := map[string]bool{}
	for _, e := range es {
		if !quarantined[e.pubkey] && own[e.pubkey] != id && !seen[e.pubkey] {
			seen[e.pubkey] = true
			want = append(want, e.enode)
		}
	}
	sort.Strings(want)
	return want
}

// forgetGonePeers: drops a node's records once it is deleted.
func (s *Server) forgetGonePeers(nodes []store.Node) {
	keep := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		keep[n.ID] = true
	}
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	for id := range s.peersMissing {
		if !keep[id] {
			delete(s.peersMissing, id)
		}
	}
}
