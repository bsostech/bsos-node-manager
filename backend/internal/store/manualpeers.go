package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// ManualPeer: a node the user wants connected (shared within this org).
type ManualPeer struct {
	Pubkey    string
	Enode     string
	CreatedAt time.Time
}

func (s *Store) ListManualPeers(ctx context.Context) ([]ManualPeer, error) {
	rows, err := s.pool.Query(ctx, `SELECT pubkey, enode, created_at FROM manual_peers ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[ManualPeer])
}

// AddManualPeer: false = the same public key is already in the list; writes only within limit.
func (s *Store) AddManualPeer(ctx context.Context, p ManualPeer, limit int) (added bool, full bool, err error) {
	return s.insertCappedPeer(ctx, sharedPeerList,
		`INSERT INTO manual_peers (pubkey, enode, created_at)
		SELECT $1, $2, $3 WHERE (SELECT count(*) FROM manual_peers) < $4
		ON CONFLICT (pubkey) DO NOTHING`, []any{p.Pubkey, p.Enode, p.CreatedAt, limit},
		`SELECT EXISTS (SELECT 1 FROM manual_peers WHERE pubkey = $1)`, []any{p.Pubkey})
}

// peerListLock: the first key of the advisory lock taken when adding a peer (two-argument form, separate from
// localUserAdminLock); the second key is which list: the empty string sharedPeerList for the shared list,
// node_id for a single node's; both go through hashtext to become keys.
const peerListLock = 62_000_002

// sharedPeerList: the shared list's lock key in insertCappedPeer (a node id is never empty, so it cannot
// collide with a single node).
const sharedPeerList = ""

// insertCappedPeer: locks this list, then counts, then writes. Without the lock, under READ COMMITTED two
// concurrent adds both see limit-1 and one extra row gets written. When nothing is written, exists tells
// "already in the list" from "list full". The lock is released when the transaction ends; deletes only shrink
// the list, so they need no lock.
func (s *Store) insertCappedPeer(ctx context.Context, lockKey, insert string, insertArgs []any, exists string, existsArgs []any) (added bool, full bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, peerListLock, lockKey); err != nil {
		return false, false, err
	}
	tag, err := tx.Exec(ctx, insert, insertArgs...)
	if err != nil {
		return false, false, err
	}
	if tag.RowsAffected() > 0 {
		return true, false, tx.Commit(ctx)
	}
	var found bool
	if err := tx.QueryRow(ctx, exists, existsArgs...).Scan(&found); err != nil {
		return false, false, err
	}
	return false, !found, nil
}

// DeleteManualPeer: false = this public key is not in the list.
func (s *Store) DeleteManualPeer(ctx context.Context, pubkey string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM manual_peers WHERE pubkey = $1`, pubkey)
	return tag.RowsAffected() > 0, err
}
