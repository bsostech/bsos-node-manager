package store

import (
	"context"
	"log"
	"time"

	"besu-manager/backend/internal/reqinfo"

	"github.com/jackc/pgx/v5"
)

// LoginEvent: one login attempt (see login_events in schema.sql).
type LoginEvent struct {
	ID       int64
	At       time.Time
	Username *string // nil when the account does not exist
	Result   string  // success | failure
	Reason   *string // only for failure
	SourceIP string
}

const loginEventRetention = 90 * 24 * time.Hour

// RecordLoginEvent: records one login attempt; empty reason = success. The source IP comes from reqinfo, same
// as audit. Afterwards it also purges entries older than 90 days; a purge failure is only logged and does not
// drop this entry.
func (s *Store) RecordLoginEvent(ctx context.Context, username *string, reason string) error {
	result, why := "success", (*string)(nil)
	if reason != "" {
		result, why = "failure", &reason
	}
	ip := ""
	if ri, ok := reqinfo.From(ctx); ok {
		ip = ri.IP
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO login_events (username, result, reason, source_ip) VALUES ($1, $2, $3, $4)`,
		username, result, why, ip); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM login_events WHERE at < $1`, time.Now().Add(-loginEventRetention)); err != nil {
		log.Printf("purge expired login events: %v", err)
	}
	return nil
}

// ListLoginEvents: login records after since, newest first, at most limit entries; empty username or result =
// no filter.
func (s *Store) ListLoginEvents(ctx context.Context, since time.Time, username, result string, limit int) ([]LoginEvent, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, at, username, result, reason, source_ip FROM login_events
		WHERE at >= $1 AND ($2 = '' OR username = $2) AND ($3 = '' OR result = $3)
		ORDER BY at DESC, id DESC LIMIT $4`, since, username, result, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[LoginEvent])
}
