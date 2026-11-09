package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// AlertSettings: alert notification settings. Single-row (id=1) layout: non-secret fields are stored as
// JSONB, and secrets (SMTP password) are encrypted with the CA master key and stored as BYTEA.
type AlertSettings struct {
	SmtpConfig     []byte // JSON: host/port/username/security/from/to (non-secret)
	SmtpSecret     []byte // AES-256-GCM (SMTP password)
	WebhookUrls    []byte // JSON:[{label,url}, ...]
	TelegramConfig []byte // JSON: chatId/messageThreadId (non-secret)
	TelegramSecret []byte // AES-256-GCM(bot token)
	SlackSecret    []byte // AES-256-GCM (Slack webhook URL)
	NotifyLanguage string // zh | en
	NotifyTimezone string // IANA name; empty = the platform host's zone
}

// GetAlertSettings: returns all-empty values when there is no row (not configured yet).
func (s *Store) GetAlertSettings(ctx context.Context) (*AlertSettings, error) {
	var a AlertSettings
	err := s.pool.QueryRow(ctx,
		`SELECT smtp_config, smtp_secret, webhook_urls, telegram_config, telegram_secret, slack_secret, notify_language, notify_timezone FROM alert_settings WHERE id=1`,
	).Scan(&a.SmtpConfig, &a.SmtpSecret, &a.WebhookUrls, &a.TelegramConfig, &a.TelegramSecret, &a.SlackSecret, &a.NotifyLanguage, &a.NotifyTimezone)
	if errors.Is(err, pgx.ErrNoRows) {
		return &AlertSettings{}, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// SaveAlertSettings: upserts the single row (id=1), overwriting everything.
func (s *Store) SaveAlertSettings(ctx context.Context, a AlertSettings) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO alert_settings (id, smtp_config, smtp_secret, webhook_urls, telegram_config, telegram_secret, slack_secret, notify_language, notify_timezone, updated_at)
		 VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, now())
		 ON CONFLICT (id) DO UPDATE SET
		   smtp_config=EXCLUDED.smtp_config, smtp_secret=EXCLUDED.smtp_secret,
		   webhook_urls=EXCLUDED.webhook_urls, telegram_config=EXCLUDED.telegram_config,
		   telegram_secret=EXCLUDED.telegram_secret, slack_secret=EXCLUDED.slack_secret,
		   notify_language=EXCLUDED.notify_language, notify_timezone=EXCLUDED.notify_timezone, updated_at=now()`,
		a.SmtpConfig, a.SmtpSecret, a.WebhookUrls, a.TelegramConfig, a.TelegramSecret, a.SlackSecret, a.NotifyLanguage, a.NotifyTimezone)
	return err
}
