package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// RpcUser: an RPC user = a named set of method permissions, one set per org. The platform stores no passwords: to
// use one, sign a JWT for that user (IssueRpcUserToken in server/rpcusers.go), which nodes verify with the platform
// public key. How many were issued and until when is recorded in rpc_user_events; JWTs are stateless, so deleting a
// user = no more issuance, and tokens already issued remain valid on nodes until exp.
type RpcUser struct {
	ID          string
	Username    string
	Permissions []string // besu permission strings (namespace:* or namespace:method); stored as JSONB
	CreatedAt   time.Time
}

// RpcUserEvent: one row of rpc_user_events. Kind is created / token_issued.
type RpcUserEvent struct {
	ID          int64
	Kind        string
	At          time.Time
	Actor       string
	Permissions []string
	ExpiresAt   *time.Time // token_issued only
	TokenID     *string    // token_issued only: the JWT jti
	Label       *string    // token_issued only: the purpose entered at issuance
}

const (
	RpcEventCreated     = "created"
	RpcEventTokenIssued = "token_issued"
)

// ErrRpcUserExists: an RPC user with the same name already exists (unique violation); the handler maps it to 409.
var ErrRpcUserExists = errors.New("rpc user already exists")

const rpcUserCols = `id, username, permissions, created_at`

// CreateRpcUser: INSERTs the user and records a created event (with the current permissions) in the same
// transaction. Returns ErrRpcUserExists for a duplicate name.
func (s *Store) CreateRpcUser(ctx context.Context, u RpcUser, actor string) error {
	perms, err := json.Marshal(u.Permissions)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO rpc_users (id, username, permissions) VALUES ($1, $2, $3)`, u.ID, u.Username, perms)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrRpcUserExists
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO rpc_user_events (rpc_user_id, kind, actor, permissions) VALUES ($1, $2, $3, $4)`,
		u.ID, RpcEventCreated, actor, perms); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListRpcUsers: all users, oldest first.
func (s *Store) ListRpcUsers(ctx context.Context) ([]RpcUser, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+rpcUserCols+` FROM rpc_users ORDER BY created_at, username`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanRpcUser)
}

// GetRpcUser: (nil, nil) if not found.
func (s *Store) GetRpcUser(ctx context.Context, id string) (*RpcUser, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+rpcUserCols+` FROM rpc_users WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	u, err := pgx.CollectOneRow(rows, scanRpcUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// DeleteRpcUser: returns the deleted user's name; "" means the row does not exist. Its records are deleted by
// the ON DELETE CASCADE foreign key, with no extra record.
func (s *Store) DeleteRpcUser(ctx context.Context, id string) (string, error) {
	var name string
	err := s.pool.QueryRow(ctx, `DELETE FROM rpc_users WHERE id = $1 RETURNING username`, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// RecordRpcToken: records a token_issued event for each token signed (id, purpose, expiry and the permissions at
// the time; the token itself is not stored). If the user was already deleted (issuance racing with deletion) the
// foreign key blocks it and returns an error; a duplicate id is blocked by the unique index and returns an error.
func (s *Store) RecordRpcToken(ctx context.Context, id string, permissions []string, exp time.Time, actor, tokenID string, label *string) error {
	perms, err := json.Marshal(permissions)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO rpc_user_events (rpc_user_id, kind, actor, permissions, expires_at, token_id, label)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, RpcEventTokenIssued, actor, perms, exp, tokenID, label)
	return err
}

// ListRpcUserEvents: the user's records, newest first.
func (s *Store) ListRpcUserEvents(ctx context.Context, id string) ([]RpcUserEvent, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, kind, at, actor, permissions, expires_at, token_id, label
		FROM rpc_user_events WHERE rpc_user_id = $1 ORDER BY id DESC`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (RpcUserEvent, error) {
		var e RpcUserEvent
		var perms []byte
		if err := row.Scan(&e.ID, &e.Kind, &e.At, &e.Actor, &perms, &e.ExpiresAt, &e.TokenID, &e.Label); err != nil {
			return e, err
		}
		if err := json.Unmarshal(perms, &e.Permissions); err != nil {
			return e, err
		}
		return e, nil
	})
}

// scanRpcUser: permissions is JSONB, so it is read as []byte and then unmarshaled (RowToStructByPos cannot be
// used).
func scanRpcUser(row pgx.CollectableRow) (RpcUser, error) {
	var u RpcUser
	var perms []byte
	if err := row.Scan(&u.ID, &u.Username, &perms, &u.CreatedAt); err != nil {
		return u, err
	}
	if len(perms) > 0 {
		if err := json.Unmarshal(perms, &u.Permissions); err != nil {
			return u, err
		}
	}
	return u, nil
}
