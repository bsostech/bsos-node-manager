package rpcops

import (
	"context"
	"strings"
)

// PeerSync: the result of one SyncPeers. Missing = enodes in want that are not currently connected (excluding
// the node itself);
// Failed = individual addPeer / removePeer calls the node rejected (e.g. malformed enode); the rest were
// still sent;
// Unremoved = the entries in drop whose removePeer was rejected (the caller must keep them and remove them
// next round).
type PeerSync struct {
	Missing   []string
	Failed    []error
	Unremoved []string
}

// SyncPeers: resends all of want (admin_addPeer), removes drop (admin_removePeer), then reads admin_peers to
// compare. error only means the whole batch could not be reached or admin_peers could not be read back.
// admin_addPeer returns false for peers already in the list, so resending the full set every round is
// idempotent; admin_removePeer also disconnects a connected peer that is not in the list, so drop only holds
// what we sent last time, and is never used to clear connections we do not recognize.
func (c *Client) SyncPeers(ctx context.Context, nodeID string, want, drop []string) (PeerSync, error) {
	calls := make([]Call, 0, len(want)+len(drop)+2)
	for _, e := range want {
		calls = append(calls, Call{"admin_addPeer", []any{e}})
	}
	for _, e := range drop {
		calls = append(calls, Call{"admin_removePeer", []any{e}})
	}
	calls = append(calls, Call{"admin_nodeInfo", nil}, Call{"admin_peers", nil})
	res, err := c.Batch(ctx, nodeID, calls)
	if err != nil {
		return PeerSync{}, err
	}
	var out PeerSync
	for i, r := range res[:len(want)+len(drop)] {
		if r.Err != nil {
			out.Failed = append(out.Failed, r.Err)
			if i >= len(want) {
				out.Unremoved = append(out.Unremoved, drop[i-len(want)])
			}
		}
	}
	var self struct {
		Enode string `json:"enode"`
	}
	var peers []struct {
		Enode string `json:"enode"`
	}
	_ = res[len(res)-2].Decode(&self) // if unreadable we just fail to exclude ourselves; addPeer on self returns false anyway
	if err := res[len(res)-1].Decode(&peers); err != nil {
		return PeerSync{}, err
	}
	connected := map[string]bool{}
	for _, p := range peers {
		connected[enodeID(p.Enode)] = true
	}
	selfID := enodeID(self.Enode)
	for _, e := range want {
		if id := enodeID(e); id != selfID && !connected[id] {
			out.Missing = append(out.Missing, e)
		}
	}
	return out, nil
}

// enodeID: the public-key part of an enode (lowercase). Connections must be compared by this rather than the
// whole string, since an inbound connection's host:port is the peer's ephemeral port.
func enodeID(e string) string {
	rest := strings.TrimPrefix(e, "enode://")
	if at := strings.IndexByte(rest, '@'); at >= 0 {
		rest = rest[:at]
	}
	return strings.ToLower(rest)
}

// PeerDiff: enodes sent last time that are no longer wanted (compared by public key).
func PeerDiff(sent, want []string) (drop []string) {
	keep := map[string]bool{}
	for _, e := range want {
		keep[enodeID(e)] = true
	}
	for _, e := range sent {
		if !keep[enodeID(e)] {
			drop = append(drop, e)
		}
	}
	return drop
}
