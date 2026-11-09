package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// NodePeer: one peer a node added itself (see node_peers in schema.sql).
type NodePeer struct {
	NodeID    string
	Pubkey    string
	Enode     string
	CreatedAt time.Time
}

const nodePeerCols = `node_id, pubkey, enode, created_at`

// ListNodePeers: peers every node added itself (for pushing); when nodeID is non-empty, only that node's.
func (s *Store) ListNodePeers(ctx context.Context, nodeID string) ([]NodePeer, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+nodePeerCols+` FROM node_peers
		WHERE $1 = '' OR node_id = $1 ORDER BY node_id, created_at, pubkey`, nodeID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[NodePeer])
}

// SetPeerShared: whether this node applies the shared list; the self-added list is left alone. Returns false
// if the node does not exist.
func (s *Store) SetPeerShared(ctx context.Context, nodeID string, on bool) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE nodes SET peer_use_shared = $2 WHERE id = $1`, nodeID, on)
	return tag.RowsAffected() > 0, err
}

// AddNodePeer: same as AddManualPeer, scoped to one node's list.
func (s *Store) AddNodePeer(ctx context.Context, p NodePeer, limit int) (added bool, full bool, err error) {
	return s.insertCappedPeer(ctx, p.NodeID,
		`INSERT INTO node_peers (`+nodePeerCols+`)
		SELECT $1, $2, $3, $4 WHERE (SELECT count(*) FROM node_peers WHERE node_id = $1) < $5
		ON CONFLICT (node_id, pubkey) DO NOTHING`, []any{p.NodeID, p.Pubkey, p.Enode, p.CreatedAt, limit},
		`SELECT EXISTS (SELECT 1 FROM node_peers WHERE node_id = $1 AND pubkey = $2)`, []any{p.NodeID, p.Pubkey})
}

// DeleteNodePeer: false = this entry is not in the list.
func (s *Store) DeleteNodePeer(ctx context.Context, nodeID, pubkey string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM node_peers WHERE node_id = $1 AND pubkey = $2`, nodeID, pubkey)
	return tag.RowsAffected() > 0, err
}
