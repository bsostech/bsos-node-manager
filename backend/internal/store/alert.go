package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Alert: one firing landed from Alertmanager webhook ingestion. (fingerprint, starts_at) is one firing:
// repeated pushes between firing->resolved upsert the same row; when the same alert fires again later,
// starts_at differs and a new row is started.
type Alert struct {
	Fingerprint string
	Alertname   string
	NodeID      *string
	Org         *string
	Scope       string
	Severity    string
	Status      string // firing | resolved
	Labels      map[string]string
	Annotations map[string]string
	StartsAt    time.Time
	EndsAt      *time.Time
	UpdatedAt   time.Time
	ReadFire    bool // whether ListAlerts' Reader has read the "firing" item (not used when writing)
	ReadResolve bool // same, for the "resolved" item
}

// UpsertAlert: writes/overwrites the whole record (same fingerprint + starts_at = a status update of the same
// firing). When the same alert has a new firing, close any earlier row still open: a Prometheus/Alertmanager
// restart gives a still-firing alert a new starts_at, so the old row never gets resolved, and if not closed
// it would show as firing forever and could not be cleared.
func (s *Store) UpsertAlert(ctx context.Context, a Alert) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE alerts SET status = 'resolved', ends_at = $2, updated_at = now()
		WHERE fingerprint = $1 AND starts_at < $2 AND ends_at IS NULL`, a.Fingerprint, a.StartsAt); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO alerts (fingerprint, alertname, node_id, org, scope, severity, status, labels, annotations, starts_at, ends_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, now())
		ON CONFLICT (fingerprint, starts_at) DO UPDATE SET
			alertname   = EXCLUDED.alertname,
			node_id     = EXCLUDED.node_id,
			org         = EXCLUDED.org,
			scope       = EXCLUDED.scope,
			severity    = EXCLUDED.severity,
			status      = EXCLUDED.status,
			labels      = EXCLUDED.labels,
			annotations = EXCLUDED.annotations,
			ends_at     = EXCLUDED.ends_at,
			updated_at  = now()`,
		a.Fingerprint, a.Alertname, a.NodeID, a.Org, a.Scope, a.Severity, a.Status,
		a.Labels, a.Annotations, a.StartsAt, a.EndsAt)
	return err
}

// PruneAlerts: deletes firing records that ended before before.
func (s *Store) PruneAlerts(ctx context.Context, before time.Time) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM alerts WHERE ends_at < $1`, before)
	return err
}

// AlertFilter: filter for ListAlerts; nil fields do not filter. Since = only those still firing or ended
// after this. Reader = include this user's read state.
type AlertFilter struct {
	Status    *string
	NodeID    *string
	Alertname *string
	Since     *time.Time
	Reader    string
}

// ListAlerts: this org's alerts, newest first.
func (s *Store) ListAlerts(ctx context.Context, f AlertFilter) ([]Alert, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.fingerprint, a.alertname, a.node_id, a.org, a.scope, a.severity, a.status, a.labels, a.annotations, a.starts_at, a.ends_at, a.updated_at,
		       EXISTS (SELECT 1 FROM alert_reads r WHERE r.username = $5 AND r.fingerprint = a.fingerprint AND r.starts_at = a.starts_at AND r.phase = 'fire'),
		       EXISTS (SELECT 1 FROM alert_reads r WHERE r.username = $5 AND r.fingerprint = a.fingerprint AND r.starts_at = a.starts_at AND r.phase = 'resolve')
		FROM alerts a
		WHERE ($1::text IS NULL OR a.status = $1)
		  AND ($2::text IS NULL OR a.node_id = $2)
		  AND ($3::text IS NULL OR a.alertname = $3)
		  AND ($4::timestamptz IS NULL OR a.ends_at IS NULL OR a.ends_at >= $4)
		ORDER BY a.starts_at DESC`, f.Status, f.NodeID, f.Alertname, f.Since, f.Reader)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Alert])
}

// AlertKey: one firing (the primary key of alerts).
type AlertKey struct {
	Fingerprint string
	StartsAt    time.Time
}

// MarkAlertsRead: marks the current item of these alerts as read by username: firing marks fire, resolved
// marks both fire and resolve. Missing ones are ignored.
func (s *Store) MarkAlertsRead(ctx context.Context, username string, keys []AlertKey) error {
	return s.markAlertsRead(ctx, username, false, keys)
}

// MarkAllAlertsRead: same as MarkAlertsRead, but for all alerts.
func (s *Store) MarkAllAlertsRead(ctx context.Context, username string) error {
	return s.markAlertsRead(ctx, username, true, nil)
}

func (s *Store) markAlertsRead(ctx context.Context, username string, all bool, keys []AlertKey) error {
	fps, starts := make([]string, len(keys)), make([]time.Time, len(keys))
	for i, k := range keys {
		fps[i], starts[i] = k.Fingerprint, k.StartsAt
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO alert_reads (username, fingerprint, starts_at, phase)
		SELECT $1, a.fingerprint, a.starts_at, p.phase
		FROM alerts a CROSS JOIN (VALUES ('fire'), ('resolve')) AS p(phase)
		WHERE (p.phase = 'fire' OR a.status = 'resolved')
		  AND ($2 OR (a.fingerprint, a.starts_at) IN (SELECT * FROM unnest($3::text[], $4::timestamptz[])))
		ON CONFLICT DO NOTHING`, username, all, fps, starts)
	return err
}
