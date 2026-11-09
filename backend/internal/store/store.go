// Package store provides PostgreSQL access to the node inventory.
package store

import (
	"context"
	_ "embed"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Unavailable reports whether err means the database could not be reached at all (connection refused,
// the host does not resolve, the connection timed out), as opposed to a query that reached it and failed.
func Unavailable(err error) bool {
	var ce *pgconn.ConnectError
	return errors.As(err, &ce)
}

//go:embed schema.sql
var schemaSQL string

type Node struct {
	ID                    string
	Org                   string
	ValidatorAddress      *string    // validator address derived from the node key (the platform derives it from the enode in net_enode)
	QuarantinedAt         *time.Time // non-nil = a revoked cert keeps dialing in (not a network problem); quarantined
	RpcPort               int        // node-local RPC port (for X-Target; default 8545)
	MetricsPort           int        // node-local metrics port (default 9545)
	ReportedEnode         *string    // enode reported by net_enode (the address is Besu's p2p-host)
	Name                  *string    // user-assigned display name; nil = the UI shows the id
	PeerShared            bool       // whether extra peers apply the shared list
	PushedPeers           []string   // enodes the platform sent via admin_addPeer last round
	AllowlistShared       bool       // whether the account allowlist applies the shared list
	AllowlistInitialSince *time.Time // non-nil = the first allowlist pass after enrollment is not done yet; the value is the enrollment time
	AgentRuntime          *string    // how the agent last said it runs (systemd / docker); nil = never reported
	// Note: this struct uses pgx.RowToStructByPos (positional mapping); new fields must be added at the **end, in
	// the same position** of both SELECTs in ListNodes / GetNode, or they will silently map to the wrong columns.
}

type JoinToken struct {
	ID             string
	NodeName       *string  // display name for the node after enrollment (optional)
	AllowedSources []string // source-IP allowlist (CIDR); empty = unrestricted
	SecretHash     string
	Status         string // pending | used | revoked (expired is computed from ExpiresAt)
	NodeID         string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	RpcPort        int // written to nodes.rpc_port after enrollment
	MetricsPort    int // written to nodes.metrics_port after enrollment
}

// EffectiveStatus: pending but past expiry -> expired.
func (t *JoinToken) EffectiveStatus(now time.Time) string {
	if t.Status == "pending" && !t.ExpiresAt.After(now) {
		return "expired"
	}
	return t.Status
}

type Store struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, now: time.Now}
}

// Migrate applies schema.sql (idempotent).
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, schemaSQL)
	return err
}

// ListNodes: all nodes, not filtered by org; nodes holds only this org's nodes (see the comment on the nodes
// table in schema.sql for why).
func (s *Store) ListNodes(ctx context.Context) ([]Node, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT n.id, n.org, n.validator_address,
		       n.quarantined_at, n.rpc_port, n.metrics_port, n.reported_enode, n.name, n.peer_use_shared, n.pushed_peers, n.allowlist_use_shared,
		       n.allowlist_initial_since, n.agent_runtime
		FROM nodes n
		ORDER BY n.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Node])
}

// GetNode returns the node, or (nil, nil) if it does not exist.
func (s *Store) GetNode(ctx context.Context, id string) (*Node, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT n.id, n.org, n.validator_address,
		       n.quarantined_at, n.rpc_port, n.metrics_port, n.reported_enode, n.name, n.peer_use_shared, n.pushed_peers, n.allowlist_use_shared,
		       n.allowlist_initial_since, n.agent_runtime
		FROM nodes n WHERE n.id = $1`, id)
	if err != nil {
		return nil, err
	}
	n, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[Node])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// MarkNodeQuarantined: called when the node's latest cert is revoked (see pki.syncQuarantine); if already
// quarantined the time is not overwritten (the same quarantine period is not reset).
func (s *Store) MarkNodeQuarantined(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE nodes SET quarantined_at = now() WHERE id = $1 AND quarantined_at IS NULL`, id)
	return err
}

// ClearNodeQuarantine: called when the node switches to an unrevoked cert (deny watcher convergence, or a
// successful re-enroll), lifting the quarantine automatically.
func (s *Store) ClearNodeQuarantine(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE nodes SET quarantined_at = NULL WHERE id = $1`, id)
	return err
}

// DeleteNode: removes the platform-side data of this org's node (self-added peers, self-added account
// allowlist + node inventory) in one transaction, and revokes its unused join codes (codeDetail goes into
// each revocation entry): once the id is free, such a code would join it again as a new node. Certs are
// revoked separately by RevokeNodeCerts (issued/revoked records are kept for audit, not deleted). RPC users
// are shared across the org and not bound to nodes, so they are not deleted with it.
func (s *Store) DeleteNode(ctx context.Context, id string, codeDetail map[string]any) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.revokeNodeJoinTokens(ctx, tx, id, codeDetail); err != nil {
		return err
	}
	// Self-added peers: its own, plus entries in other lists whose public key belongs to it (the part of the
	// enode before @ is the public key). The shared list is user-entered and left alone.
	if _, err := tx.Exec(ctx, `DELETE FROM node_peers WHERE node_id = $1 OR pubkey = (
		SELECT lower(substring(reported_enode FROM '^enode://([0-9a-fA-F]{128})@')) FROM nodes WHERE id = $1)`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM permissioning_policy WHERE node_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM nodes WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const insertJoinToken = `
	INSERT INTO join_tokens (id, node_name, allowed_sources, secret_hash, status, node_id, created_at, expires_at, rpc_port, metrics_port)
	VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

func joinTokenArgs(t JoinToken) []any {
	if t.AllowedSources == nil {
		t.AllowedSources = []string{} // nil would be written as NULL; no list = unrestricted
	}
	return []any{t.ID, t.NodeName, t.AllowedSources, t.SecretHash, t.Status, t.NodeID, t.CreatedAt, t.ExpiresAt, t.RpcPort, t.MetricsPort}
}

// CreateJoinToken inserts t as is, without touching the node's other codes (tests seed codes in any
// state with it; the API goes through CreateJoinTokenSuperseding).
func (s *Store) CreateJoinToken(ctx context.Context, t JoinToken) error {
	_, err := s.pool.Exec(ctx, insertJoinToken, joinTokenArgs(t)...)
	return err
}

// joinTokenNodeLock: the first key of the advisory lock taken while changing a node's unused join
// codes (two-argument form, separate from localUserAdminLock and peerListLock; the second key is
// hashtext(node_id)). Row locks alone are not enough: two concurrent creates for the same node each
// miss the other's uncommitted code and both insert.
const joinTokenNodeLock = 62_000_003

// revokeNodeJoinTokens revokes, within tx, every unused join code bound to nodeID (pending in the
// table, including ones already past expiry) and writes one join_token_revoked entry per code with
// detail merged in. A node id only ever consumes one code, so older unused ones have no legitimate
// use and would otherwise stay valid until they expire.
func (s *Store) revokeNodeJoinTokens(ctx context.Context, tx pgx.Tx, nodeID string, detail map[string]any) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, joinTokenNodeLock, nodeID); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `UPDATE join_tokens SET status = 'revoked' WHERE node_id = $1 AND status = 'pending' RETURNING id, secret_hash`, nodeID)
	if err != nil {
		return err
	}
	revoked, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ ID, SecretHash string }])
	if err != nil {
		return err
	}
	for _, r := range revoked {
		d := map[string]any{"code_id": r.ID, "token_hash": r.SecretHash, "node_id": nodeID}
		for k, v := range detail {
			d[k] = v
		}
		if err := s.AuditIn(ctx, tx, "join_token_revoked", d); err != nil {
			return err
		}
	}
	return nil
}

// CreateJoinTokenSuperseding makes t the only unused join code of its node: in one transaction it
// revokes the node's other unused codes (supersededDetail goes into each revocation entry), writes
// createdDetail as join_token_created and inserts t. If any audit entry cannot be written nothing
// changes.
func (s *Store) CreateJoinTokenSuperseding(ctx context.Context, t JoinToken, createdDetail, supersededDetail map[string]any) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.revokeNodeJoinTokens(ctx, tx, t.NodeID, supersededDetail); err != nil {
		return err
	}
	if err := s.AuditIn(ctx, tx, "join_token_created", createdDetail); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, insertJoinToken, joinTokenArgs(t)...); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) ListJoinTokens(ctx context.Context) ([]JoinToken, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, node_name, allowed_sources, secret_hash, status, node_id, created_at, expires_at, rpc_port, metrics_port
		FROM join_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[JoinToken])
}

// GetJoinToken returns a single token, or (nil, nil) if it does not exist.
func (s *Store) GetJoinToken(ctx context.Context, id string) (*JoinToken, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, node_name, allowed_sources, secret_hash, status, node_id, created_at, expires_at, rpc_port, metrics_port
		FROM join_tokens WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	t, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[JoinToken])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// MarkJoinTokenRevoked revokes a code that is still pending. Returns false when it is no longer
// pending, e.g. a join used it after the caller read it: the node did join, so it must not read as
// revoked.
func (s *Store) MarkJoinTokenRevoked(ctx context.Context, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE join_tokens SET status = 'revoked' WHERE id = $1 AND status = 'pending'`, id)
	return tag.RowsAffected() > 0, err
}
