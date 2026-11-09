package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ── local accounts ─────────────────────────────────────────────

type LocalUser struct {
	Username           string
	PasswordHash       string
	Role               string
	IsBootstrap        bool
	Disabled           bool
	MustChangePassword bool
	CreatedAt          time.Time
	LastLoginAt        *time.Time
	Email              *string // optional; temporary password emails are sent here
}

const localUserCols = `username, password_hash, role, is_bootstrap, disabled, must_change_password, created_at, last_login_at, email`

func (s *Store) GetLocalUser(ctx context.Context, username string) (*LocalUser, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+localUserCols+` FROM local_users WHERE username=$1`, username)
	if err != nil {
		return nil, err
	}
	u, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[LocalUser])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) ListLocalUsers(ctx context.Context) ([]LocalUser, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+localUserCols+` FROM local_users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[LocalUser])
}

// ErrLastAdmin: this change would leave the org with no enabled local admin.
var ErrLastAdmin = errors.New("last active admin")

// ErrLocalUserNotFound: this local account was not found within the transaction (someone may have just deleted it).
var ErrLocalUserNotFound = errors.New("local user not found")

// localUserAdminLock: advisory lock key for disabling / deleting local accounts. When two admins disable each
// other at the same time, the "how many admins are left" check and the change must happen under the same
// lock; otherwise both see 2 and none are left.
const localUserAdminLock = 62_000_001

// mutateLocalUser: within one transaction, confirms the account exists, checks that "at least one enabled
// admin remains after the change" (when staysActiveAdmin=false), then runs fn. The account change and the
// session logout both happen in fn, so they take effect together or not at all; otherwise the account could
// be disabled while deleting sessions failed, leaving old logins alive.
func (s *Store) mutateLocalUser(ctx context.Context, username string, staysActiveAdmin bool, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, localUserAdminLock); err != nil {
		return err
	}
	var activeAdmin bool
	err = tx.QueryRow(ctx, `SELECT role='admin' AND NOT disabled FROM local_users WHERE username=$1`, username).Scan(&activeAdmin)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLocalUserNotFound
	}
	if err != nil {
		return err
	}
	if activeAdmin && !staysActiveAdmin {
		var others int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM local_users WHERE role='admin' AND NOT disabled AND username<>$1`, username).Scan(&others); err != nil {
			return err
		}
		if others == 0 {
			return ErrLastAdmin
		}
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Sessions of local accounts only touch local ones (see sessions.local in schema.sql).
const deleteLocalSessions = `DELETE FROM sessions WHERE username=$1 AND local`

// DropSSOSessions / DropLocalSessionsExcept: log everyone out at once when permissions are revoked externally. Runs
// in the caller's transaction, taking effect together with the write that revokes the permission.
func DropSSOSessions(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `DELETE FROM sessions WHERE NOT local`)
	return err
}

// DropLocalSessionsExcept: all local-account sessions except keepToken (the operator turning off local login).
func DropLocalSessionsExcept(ctx context.Context, tx pgx.Tx, keepToken string) error {
	_, err := tx.Exec(ctx, `DELETE FROM sessions WHERE local AND token <> $1`, keepToken)
	return err
}

// SetLocalUserDisabled: disabling = logging out all sessions; enabling only changes the account. The role is
// fixed at creation and cannot be changed. Returns ErrLastAdmin when disabling the org's last enabled admin.
func (s *Store) SetLocalUserDisabled(ctx context.Context, username string, disabled bool) error {
	return s.mutateLocalUser(ctx, username, !disabled, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE local_users SET disabled=$2 WHERE username=$1`, username, disabled); err != nil {
			return err
		}
		if !disabled {
			return nil
		}
		_, err := tx.Exec(ctx, deleteLocalSessions, username)
		return err
	})
}

// ResetLocalPassword: admin resets the password; logs out all sessions and forces a change on next login.
func (s *Store) ResetLocalPassword(ctx context.Context, username, passwordHash string) error {
	return s.mutateLocalUser(ctx, username, true, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE local_users SET password_hash=$2, must_change_password=true WHERE username=$1`, username, passwordHash); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, deleteLocalSessions, username)
		return err
	})
}

// DeleteLocalUser: deletes the account and logs out all sessions; returns ErrLastAdmin if the org would be
// left with no enabled admin.
func (s *Store) DeleteLocalUser(ctx context.Context, username string) error {
	return s.mutateLocalUser(ctx, username, false, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM local_users WHERE username=$1`, username); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, deleteLocalSessions, username)
		return err
	})
}

// ChangeOwnPassword: the user changes their own password. Changing the password, clearing
// must_change_password, logging out other sessions, lifting the "password change only" restriction on the
// current session (keepToken), and finally writing the audit entry (auditDetail) all happen in one
// transaction: if the audit write fails, the password is not changed.
func (s *Store) ChangeOwnPassword(ctx context.Context, username, passwordHash, keepToken string, auditDetail map[string]any) error {
	return s.mutateLocalUser(ctx, username, true, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE local_users SET password_hash=$2, must_change_password=false WHERE username=$1`, username, passwordHash); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, deleteLocalSessions+` AND token<>$2`, username, keepToken); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE sessions SET must_change_password=false WHERE token=$1`, keepToken); err != nil {
			return err
		}
		return s.AuditIn(ctx, tx, "password_changed", auditDetail)
	})
}

func (s *Store) SetLastLogin(ctx context.Context, username string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE local_users SET last_login_at=$2 WHERE username=$1`, username, at)
	return err
}

func (s *Store) CountLocalUsers(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM local_users`).Scan(&n)
	return n, err
}

// ErrLocalUserExists: an account with the same name already exists.
var ErrLocalUserExists = errors.New("local user exists")

func (s *Store) CreateLocalUser(ctx context.Context, u LocalUser) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO local_users (username, password_hash, role, is_bootstrap, disabled, must_change_password, created_at, email)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		u.Username, u.PasswordHash, u.Role, u.IsBootstrap, u.Disabled, u.MustChangePassword, time.Now().UTC(), u.Email)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrLocalUserExists
	}
	return err
}

// ── sessions (server-side; delete = forced logout) ────────────────────

type Session struct {
	Token              string
	Username           *string
	Account            *string // account shown for SSO; nil for local accounts
	Name               string
	Role               string
	Org                string
	ExpiresAt          time.Time
	LastSeenAt         time.Time
	MustChangePassword bool
	Local              bool // local-account login (see sessions.local in schema.sql)
}

func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	now := time.Now().UTC()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO sessions (token, username, account, name, role, org, created_at, expires_at, last_seen_at, must_change_password, local)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$7,$9,$10)`,
		sess.Token, sess.Username, sess.Account, sess.Name, sess.Role, sess.Org, now, sess.ExpiresAt, sess.MustChangePassword, sess.Local)
	return err
}

// GetSession returns a valid session; expired/missing returns (nil,nil), and expired ones are deleted on the
// way. Idle timeout is decided by the caller (auth.Middleware).
func (s *Store) GetSession(ctx context.Context, token string) (*Session, error) {
	var sess Session
	err := s.pool.QueryRow(ctx,
		`SELECT token, username, account, name, role, org, expires_at, last_seen_at, must_change_password, local FROM sessions WHERE token=$1`, token,
	).Scan(&sess.Token, &sess.Username, &sess.Account, &sess.Name, &sess.Role, &sess.Org, &sess.ExpiresAt, &sess.LastSeenAt, &sess.MustChangePassword, &sess.Local)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if sess.ExpiresAt.Before(time.Now().UTC()) {
		_ = s.DeleteSession(ctx, token)
		return nil, nil
	}
	return &sess, nil
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token=$1`, token)
	return err
}

// TouchSession: updates the last activity time (for idle timeout).
func (s *Store) TouchSession(ctx context.Context, token string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE sessions SET last_seen_at=$2 WHERE token=$1`, token, at)
	return err
}

// PruneSessions: removes sessions that are expired or idle-timed-out (run on login).
func (s *Store) PruneSessions(ctx context.Context, idleBefore time.Time) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now() OR last_seen_at < $1`, idleBefore)
	return err
}
