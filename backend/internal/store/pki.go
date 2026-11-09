package store

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"

	"besu-manager/backend/internal/ca"
	"besu-manager/backend/internal/reqinfo"
)

// GetCAMaterial loads the two-tier CA material; returns (nil, nil) if not bootstrapped yet.
func (s *Store) GetCAMaterial(ctx context.Context) (*ca.Material, error) {
	rows, err := s.pool.Query(ctx, `SELECT kind, cert_pem, key_enc FROM ca_certificates`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := &ca.Material{}
	n := 0
	for rows.Next() {
		var kind, certPEM string
		var keyEnc []byte
		if err := rows.Scan(&kind, &certPEM, &keyEnc); err != nil {
			return nil, err
		}
		switch kind {
		case "root":
			m.RootCertPEM, m.RootKeyEnc = []byte(certPEM), keyEnc
		case "intermediate":
			m.IntCertPEM, m.IntKeyEnc = []byte(certPEM), keyEnc
		}
		n++
	}
	if n == 0 {
		return nil, nil
	}
	return m, rows.Err()
}

func (s *Store) SaveCAMaterial(ctx context.Context, m *ca.Material, rootNotAfter, intNotAfter time.Time) error {
	now := time.Now().UTC()
	batch := &pgx.Batch{}
	batch.Queue(`INSERT INTO ca_certificates (kind, cert_pem, key_enc, created_at, not_after) VALUES ('root', $1, $2, $3, $4)`,
		string(m.RootCertPEM), m.RootKeyEnc, now, rootNotAfter)
	batch.Queue(`INSERT INTO ca_certificates (kind, cert_pem, key_enc, created_at, not_after) VALUES ('intermediate', $1, $2, $3, $4)`,
		string(m.IntCertPEM), m.IntKeyEnc, now, intNotAfter)
	return s.pool.SendBatch(ctx, batch).Close()
}

// UpdateIntermediate: replaces only the intermediate row when it is reissued (root unchanged). SaveCAMaterial
// is the INSERT used for initial creation.
func (s *Store) UpdateIntermediate(ctx context.Context, certPEM, keyEnc []byte, notAfter time.Time) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE ca_certificates SET cert_pem = $1, key_enc = $2, not_after = $3, created_at = now()
		WHERE kind = 'intermediate'`, string(certPEM), keyEnc, notAfter)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("ca_certificates has no intermediate row (CA not created yet?)")
	}
	return nil
}

type IssuedCert struct {
	Serial    string
	Subject   string
	NodeID    *string
	NotBefore time.Time
	NotAfter  time.Time
	CertPEM   string
}

func (s *Store) InsertIssuedCert(ctx context.Context, c IssuedCert) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO issued_certificates (serial, subject, node_id, not_before, not_after, cert_pem)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		c.Serial, c.Subject, c.NodeID, c.NotBefore, c.NotAfter, c.CertPEM)
	return err
}

// GetJoinTokenByHash looks up a pending token by sha256(full token); returns (nil, nil) if not found.
func (s *Store) GetJoinTokenByHash(ctx context.Context, hash string) (*JoinToken, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, node_name, allowed_sources, secret_hash, status, node_id, created_at, expires_at, rpc_port, metrics_port
		FROM join_tokens WHERE secret_hash = $1`, hash)
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

// MarkJoinTokenUsed: burn after use.
func (s *Store) MarkJoinTokenUsed(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE join_tokens SET status = 'used' WHERE id = $1`, id)
	return err
}

// UpsertOwnNode: on join, registers the node as owned by this org, with ports taken from the join token (left
// alone if it already exists; re-enroll does not come through here, and ports are not changed here; use
// SetNodePorts for that). The enode and validator address are not written here: the platform writes them
// later once read from the node's net_enode (see collector.recordIdentity). The join time is stamped
// with the platform clock, not the database's now(): the initial allowlist push compares it with
// collector snapshot times, and the two clocks can drift apart.
func (s *Store) UpsertOwnNode(ctx context.Context, id, org string, rpcPort, metricsPort int, name *string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO nodes (id, org, rpc_port, metrics_port, name, allowlist_initial_since) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO NOTHING`, id, org, rpcPort, metricsPort, name, s.now())
	return err
}

// SetNodeName: changes the display name of this org's node; nil = clear. Returns false = no such node.
func (s *Store) SetNodeName(ctx context.Context, id string, name *string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE nodes SET name = $2 WHERE id = $1`, id, name)
	return tag.RowsAffected() > 0, err
}

// SetNodePorts: changes the node-local RPC / metrics ports (the targets the platform relays to through the
// agent tunnel). Returns false = no such node of this org.
func (s *Store) SetNodePorts(ctx context.Context, id string, rpcPort, metricsPort int) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE nodes SET rpc_port = $2, metrics_port = $3 WHERE id = $1`,
		id, rpcPort, metricsPort)
	return tag.RowsAffected() > 0, err
}

// SetNodeIdentity: the on-chain identity the platform read from the node's net_enode.
func (s *Store) SetNodeIdentity(ctx context.Context, id, validatorAddress, reported string) error {
	_, err := s.pool.Exec(ctx, `UPDATE nodes SET validator_address = $2, reported_enode = $3 WHERE id = $1`,
		id, validatorAddress, reported)
	return err
}

// SetNodeAgentRuntime: records how the node's agent says it runs, so it is still known while the agent is offline.
func (s *Store) SetNodeAgentRuntime(ctx context.Context, id, runtime string) error {
	_, err := s.pool.Exec(ctx, `UPDATE nodes SET agent_runtime = $2 WHERE id = $1`, id, runtime)
	return err
}

// SetPushedPeers: records the enodes the platform sent to a node this round (the next round uses it to decide
// what to remove).
func (s *Store) SetPushedPeers(ctx context.Context, id string, enodes []string) error {
	if enodes == nil {
		enodes = []string{}
	}
	_, err := s.pool.Exec(ctx, `UPDATE nodes SET pushed_peers = $2 WHERE id = $1`, id, enodes)
	return err
}

// ErrAudit: the audit entry could not be written, so the operation was not performed (fail-closed). Callers
// wrap it with %w and return it, and it is uniformly mapped to 503 with a fixed message (the raw database
// error is never returned to the caller).
var ErrAudit = errors.New("audit write failed; operation not performed")

// AuditSink: the audit writer. In Core it is nil = writes nothing; in Enterprise it is installed at the
// very start of startup (so the first start's ca_bootstrap is recorded too). Set
// once at startup and read-only afterwards. db is normally the connection pool; AuditIn passes the caller's
// transaction (Begin opens a savepoint, so the audit commits or rolls back with the outer transaction).
var AuditSink func(ctx context.Context, db AuditDB, event string, detail map[string]any) error

// AuditDB: the database interface the audit writer needs; both *pgxpool.Pool and pgx.Tx satisfy it.
type AuditDB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Audit: writes one audit entry. Callers call it as usual; Core does nothing. A failed write does not fail
// the operation (callers mostly ignore the return value), so it is logged to the platform log here, so at
// least the missing entry can be traced.
func (s *Store) Audit(ctx context.Context, event string, detail map[string]any) error {
	if AuditSink == nil {
		return nil
	}
	err := AuditSink(ctx, s.pool, event, withRequestInfo(ctx, detail))
	if err != nil {
		log.Printf("audit write (event=%s): %v", event, err)
	}
	return err
}

// AuditIn: writes one audit entry within the caller's transaction, committing together with the operation; returns
// ErrAudit on failure, and the caller returns it so the whole transaction rolls back (only for pure database
// operations done in one transaction; operations with external side effects still record first and act second).
func (s *Store) AuditIn(ctx context.Context, tx pgx.Tx, event string, detail map[string]any) error {
	if AuditSink == nil {
		return nil
	}
	if err := AuditSink(ctx, tx, event, withRequestInfo(ctx, detail)); err != nil {
		log.Printf("audit write (event=%s): %v", event, err)
		return fmt.Errorf("%w: %v", ErrAudit, err)
	}
	return nil
}

// withRequestInfo: audit entries from requests carry the source IP and user agent (in detail, so they go into
// the hash chain and exports without changing the table or the hash formula). Background jobs and the startup
// flow have no request and carry neither. Works on a copy and does not modify the caller's map.
func withRequestInfo(ctx context.Context, detail map[string]any) map[string]any {
	ri, ok := reqinfo.From(ctx)
	if !ok {
		return detail
	}
	d := make(map[string]any, len(detail)+2)
	for k, v := range detail {
		d[k] = v
	}
	if ri.IP != "" {
		d["ip"] = ri.IP
	}
	if ri.UA != "" {
		d["ua"] = ri.UA
	}
	return d
}

// RevokeCert writes to the deny list (serial is the lookup key).
func (s *Store) RevokeCert(ctx context.Context, serial, reason, revokedBy string, certNotAfter time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO revoked_certificates (serial, reason, revoked_by, revoked_at, expires_at)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (serial) DO NOTHING`,
		serial, reason, revokedBy, time.Now().UTC(), certNotAfter)
	return err
}

// RevokeNodeCerts: revokes all unexpired leaf certs of the node (for node retirement; revoking only the
// latest one is not enough, since rotation may leave unexpired old certs, and the agent could still reconnect
// with them). Returns the number of newly revoked certs.
func (s *Store) RevokeNodeCerts(ctx context.Context, nodeID, reason, revokedBy string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO revoked_certificates (serial, reason, revoked_by, revoked_at, expires_at)
		SELECT ic.serial, $2, $3, now(), ic.not_after
		FROM issued_certificates ic
		WHERE ic.node_id = $1 AND ic.not_after > now()
		ON CONFLICT (serial) DO NOTHING`,
		nodeID, reason, revokedBy)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// RevokedSerials returns the unexpired deny list (expired certs are invalid anyway, so their entries can be
// ignored).
func (s *Store) RevokedSerials(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT serial FROM revoked_certificates WHERE expires_at > now()`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// LatestCertForNode: the most recently issued cert of a single node (for cert status on the node settings
// page). nil = no record (e.g. after a CA reset).
func (s *Store) LatestCertForNode(ctx context.Context, nodeID string) (*IssuedCert, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT serial, subject, node_id, not_before, not_after, cert_pem
		FROM issued_certificates WHERE node_id=$1 ORDER BY not_before DESC LIMIT 1`, nodeID)
	if err != nil {
		return nil, err
	}
	c, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[IssuedCert])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// "Has this node joined?": it holds at least one issuance record that is **not revoked**. Both join and
// re-enroll call InsertIssuedCert before UpsertOwnNode, so an inventory row always has an issuance record.
//
// **The "not revoked" condition cannot be dropped.** DeleteNode first revokes all unexpired certs of the node and
// then deletes the inventory row (DeleteNode -> RevokeNodeCerts in server.go), while issued_certificates is an
// audit record and is deliberately not deleted. Without this condition, a node "deleted and recreated with the same
// id" would be judged as joined, even though that machine has not been installed yet; it would be fed to Prometheus
// (up == 0 -> a false NodeOffline two minutes later) and also be allowed to receive config.
//
// Expiry is not compared: a node whose cert expired was still installed; it should stay in the list showing
// offline, not disappear.
//
// Two known edge cases:
// (1) after manually resetting the CA and clearing issued_certificates, those nodes are judged as never
// joined (they could not connect anyway; the only difference is going from "in the list but unscrapable" to
// "not in the list");
// (2) if the deletion happens after the last cert already expired, that cert is not revoked by
// RevokeNodeCerts (it only revokes unexpired ones), so recreating with the same id is still judged as joined.
// Such a node was dead long before deletion, and the impact is limited to one false offline alert.
// The two functions below answer the same question, one for the list and one for a single node.
//
// EnrolledNodeIDs: ids of all nodes that have joined.
func (s *Store) EnrolledNodeIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ic.node_id FROM issued_certificates ic
		WHERE ic.node_id IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM revoked_certificates rc WHERE rc.serial = ic.serial)`)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// LatestCertPerNode: the "most recently issued" cert of each node (for the rotation scheduler). Picks the
// most recent issuance by not_before DESC; not_after must not be used, or an older long-lived cert would win
// over a recent short-lived one.
func (s *Store) LatestCertPerNode(ctx context.Context) ([]IssuedCert, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (node_id) serial, subject, node_id, not_before, not_after, cert_pem
		FROM issued_certificates WHERE node_id IS NOT NULL
		ORDER BY node_id, not_before DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[IssuedCert])
}
