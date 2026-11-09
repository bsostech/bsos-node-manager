package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ── account allowlist: one row for the shared list (SharedPolicy), plus one row per node with self-added addresses ──

// SharedPolicy: the key of the shared-list row. It is not a valid node id format, so it cannot collide with a node.
const SharedPolicy = "*"

type PermissioningPolicy struct {
	Revision          int64
	AccountsAllowlist []string
}

// ListPermissioningPolicies: all lists, keyed by node id (SharedPolicy = shared). The shared row is created
// at schema init, so it is always present.
func (s *Store) ListPermissioningPolicies(ctx context.Context) (map[string]PermissioningPolicy, error) {
	rows, err := s.pool.Query(ctx, `SELECT node_id, accounts_allowlist, revision FROM permissioning_policy`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PermissioningPolicy{}
	for rows.Next() {
		var id string
		var p PermissioningPolicy
		if err := rows.Scan(&id, &p.AccountsAllowlist, &p.Revision); err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rows.Err()
}

// ErrPolicyStale: the revision passed on save is not the current one (someone else saved, created, or deleted
// it first).
var ErrPolicyStale = errors.New("permissioning policy revision is stale")

// SavePermissioningPolicy: saves and increments revision only if the revision equals expected, returning the new
// revision; otherwise ErrPolicyStale. The comparison and write are a single statement, so of two concurrent saves
// only one succeeds. When the node row does not exist, expected 0 = create; for two concurrent creates, ON CONFLICT
// blocks the later one. Applying is done by the platform sending perm_* RPC directly to nodes.
func (s *Store) SavePermissioningPolicy(ctx context.Context, nodeID string, list []string, expected int64) (int64, error) {
	var revision int64
	err := s.pool.QueryRow(ctx,
		`UPDATE permissioning_policy SET accounts_allowlist=$2, revision=revision+1, updated_at=now()
		 WHERE node_id=$1 AND revision=$3 RETURNING revision`,
		nodeID, list, expected).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) && expected == 0 {
		err = s.pool.QueryRow(ctx,
			`INSERT INTO permissioning_policy(node_id, accounts_allowlist, revision) VALUES($1, $2, 1)
			 ON CONFLICT DO NOTHING RETURNING revision`,
			nodeID, list).Scan(&revision)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrPolicyStale
	}
	return revision, err
}

// DeletePermissioningPolicy: deletes a node's self-added addresses; returns false if there were none. The
// shared row is never deleted.
func (s *Store) DeletePermissioningPolicy(ctx context.Context, nodeID string) (bool, error) {
	if nodeID == SharedPolicy {
		return false, errors.New("shared allowlist cannot be deleted")
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM permissioning_policy WHERE node_id=$1`, nodeID)
	return tag.RowsAffected() > 0, err
}

// SetNodeAllowlistShared: toggles whether this node applies the shared list, and in the same transaction
// replaces its self-added addresses with extras (when turning on, those already in the shared list are
// removed; if empty, the row is deleted). Returns ErrPolicyStale if the revision is not expected, and the
// switch stays unchanged. Returns the revision of this node's list, 0 if it has no row; not incremented if
// the addresses did not change.
func (s *Store) SetNodeAllowlistShared(ctx context.Context, nodeID string, useShared bool, extras []string, expected int64) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE nodes SET allowlist_use_shared = $2 WHERE id = $1`, nodeID, useShared); err != nil {
		return 0, err
	}
	var revision int64
	if len(extras) == 0 {
		_, err = tx.Exec(ctx, `DELETE FROM permissioning_policy WHERE node_id = $1`, nodeID)
	} else {
		err = tx.QueryRow(ctx,
			`UPDATE permissioning_policy SET accounts_allowlist = $2, updated_at = now(),
			        revision = CASE WHEN accounts_allowlist = $2 THEN revision ELSE revision + 1 END
			 WHERE node_id = $1 AND revision = $3 RETURNING revision`,
			nodeID, extras, expected).Scan(&revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrPolicyStale
		}
	}
	if err != nil {
		return 0, err
	}
	return revision, tx.Commit(ctx)
}

// ClearAllowlistInitial: the first allowlist pass after this node enrolled is done (pushed, or nothing to push).
func (s *Store) ClearAllowlistInitial(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE nodes SET allowlist_initial_since = NULL WHERE id = $1`, id)
	return err
}
