// Package alerting manages alert notification settings: Email (SMTP), Telegram, Slack and custom webhooks.
// The platform never sends mail or calls webhooks itself. It renders the settings into alertmanager.yml on a shared volume
// and lets Alertmanager do the sending; the platform only owns "where the settings come from" and "making Alertmanager pick up changes".
package alerting

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // zone validation must not depend on the server image shipping zoneinfo

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/auth"
	"besu-manager/backend/internal/pki"
	"besu-manager/backend/internal/store"
)

// sdTokenPath: where the Alertmanager container reads SD_TOKEN. It reuses the same token as /sd/targets and /metrics
// (the server writes it into the sd-token volume at boot, and the alertmanager service mounts that same volume; see docker-compose.yml).
const sdTokenPath = "/sd-token/sd_token"

// smtpCAPath: where the Alertmanager container reads the SMTP_CA_FILE certificates. The server copies them into
// ALERTMANAGER_CONFIG_DIR next to alertmanager.yml, the volume Alertmanager already mounts at /alertmanager-config,
// so the CA needs no mount of its own on the Alertmanager side.
const (
	smtpCAFile = "smtp-ca.pem"
	smtpCAPath = "/alertmanager-config/" + smtpCAFile
)

// platformWebhookURL: the platform's own receiver for alerts sent by Alertmanager, persisted into the alerts table.
// Hard-coded (docker-compose internal service name + INTERNAL_ADDR port) and not configurable: this is not a user notification channel,
// it is how the platform collects its own data, so it must always be present.
const platformWebhookURL = "http://server:8080/alerts/webhook"

type SMTP struct {
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	Username string   `json:"username,omitempty"`
	Password string   `json:"-"` // only flows through memory / the encrypted column, never into the smtp_config JSONB
	Security Security `json:"security"`
	From     string   `json:"from"`
	To       []string `json:"to"`
}

type WebhookURL struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Telegram: one bot to one chat (usually the on-call group).
type Telegram struct {
	BotToken        string `json:"-"` // same as the SMTP password: only flows through memory / the encrypted column
	ChatID          int64  `json:"chatId"`
	MessageThreadID *int64 `json:"messageThreadId,omitempty"` // topic thread within the group (optional)
}

// Slack: one incoming webhook (the channel is fixed by the webhook itself). The URL is the credential, so it is stored encrypted in full.
type Slack struct {
	WebhookURL string
}

type Settings struct {
	SMTP        *SMTP
	Telegram    *Telegram
	Slack       *Slack
	WebhookURLs []WebhookURL
	// Language and Timezone apply to every channel. Timezone is what is stored: empty means the platform
	// host's zone, resolved by Zone at render time so a later host TZ change still takes effect.
	Language api.NotifyLanguage
	Timezone string
}

// Zone: the IANA name notification times are shown in.
func (s Settings) Zone() string {
	if s.Timezone != "" {
		return s.Timezone
	}
	return hostZone()
}

// hostZone: the server process's zone by name. time.Local only ever calls itself "Local", and Alertmanager
// needs a real name (its own Local is the Alertmanager container's zone), so read TZ, then the
// /etc/localtime link, and fall back to UTC.
func hostZone() string {
	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); ValidZone(tz) {
		return tz
	}
	if p, err := os.Readlink("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(p, "zoneinfo/"); ok && ValidZone(name) {
			return name
		}
	}
	return "UTC"
}

// zoneChars: the zone name is written into Alertmanager templates as is, so allow only what IANA names use.
var zoneChars = regexp.MustCompile(`^[A-Za-z0-9_+\-]+(/[A-Za-z0-9_+\-]+)*$`)

// ValidZone: an IANA name Go knows. "Local" is refused: it would mean whatever zone the Alertmanager
// container happens to run in.
func ValidZone(name string) bool {
	if name == "Local" || !zoneChars.MatchString(name) {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

// languageOf: the stored language; anything unknown (including the empty value of a fresh install) is English.
func languageOf(s string) api.NotifyLanguage {
	if api.NotifyLanguage(s) == api.NotifyLanguageZh {
		return api.NotifyLanguageZh
	}
	return api.NotifyLanguageEn
}

// Text: one piece of notification text in every supported language. A struct rather than a map so a
// missing translation is visible where the text is written.
type Text struct{ Zh, En string }

func (t Text) In(l api.NotifyLanguage) string {
	if l == api.NotifyLanguageZh {
		return t.Zh
	}
	return t.En
}

// Service: holds the CA master key (to decrypt the SMTP password), the shared volume path and the Alertmanager reload URL.
// An empty configDir/reloadURL disables that capability (e.g. deployments that manage Alertmanager themselves and do not wire this up).
type Service struct {
	// ShowOrg: include the organization in notification text. Only Enterprise sets it to true (Core has no notion of organizations). The org label is set in every edition,
	// so alert identity does not change and nothing re-fires when Core is upgraded to Enterprise.
	ShowOrg bool
	// saveMu: serializes the read-modify-write in SaveSMTP / SaveWebhooks. Each replaces only half of the settings,
	// but store.SaveAlertSettings overwrites the whole row with no version check. Without this lock, two channels saved at the same time
	// (easy once the UI is split into two sub-pages: two tabs each editing their own) let the later write silently revert the earlier half.
	// ponytail: in-process lock, protects a single instance only. The platform currently runs one server container; running multiple instances needs
	// optimistic locking in the database (a version column + WHERE version=$n), at which point this lock can go.
	saveMu sync.Mutex
	// Rules: rewritten in the new language when the language changes (default rule summaries are Prometheus
	// annotations, rendered into the rules file). nil skips that, e.g. in tests that only render alertmanager.yml.
	Rules *RulesService
	// SMTPCA: SMTP_CA_FILE, rendered as ca_file for STARTTLS/TLS mail and used by the connection test. nil = system store.
	SMTPCA    *SMTPCA
	store     *store.Store
	master    []byte
	configDir string
	configGID int
	reloadURL string
}

func New(st *store.Store, master []byte, configDir string, configGID int, reloadURL string) *Service {
	return &Service{store: st, master: master, configDir: configDir, configGID: configGID, reloadURL: reloadURL}
}

// LoadAndApply: runs once at boot. Even with no notification channel configured it must write the minimal alertmanager.yml
// containing only the platform receiver, otherwise Alertmanager cannot start (it needs a valid config file to boot).
func (s *Service) LoadAndApply(ctx context.Context) error {
	settings, err := s.load(ctx)
	if err != nil {
		return err
	}
	return s.apply(settings)
}

// CustomCA: whether SMTP_CA_FILE is set, so STARTTLS/TLS mail trusts only its CAs.
func (s *Service) CustomCA() bool { return s.SMTPCA != nil }

// WriteSMTPCA: copies SMTP_CA_FILE next to alertmanager.yml at boot. It runs before LoadAndApply and a failure must stop
// the server: the rendered config points at smtpCAPath, so a missing copy would break Alertmanager's config load.
func (s *Service) WriteSMTPCA() error {
	if !s.CustomCA() || s.configDir == "" {
		return nil
	}
	if err := pki.WriteAtomic(s.configDir, smtpCAFile, s.SMTPCA.PEM, 0o644, s.configGID); err != nil {
		return fmt.Errorf("write %s: %w", smtpCAFile, err)
	}
	return nil
}

// View: for GET /alert-settings, with the SMTP password cleared (same no-secrets-returned convention as auth.SettingsView).
func (s *Service) View(ctx context.Context) (Settings, error) {
	settings, err := s.load(ctx)
	if err != nil {
		return Settings{}, err
	}
	if settings.SMTP != nil {
		v := *settings.SMTP
		v.Password = ""
		settings.SMTP = &v
	}
	if settings.Telegram != nil {
		v := *settings.Telegram
		v.BotToken = ""
		settings.Telegram = &v
	}
	if settings.Slack != nil {
		settings.Slack = &Slack{}
	}
	return settings, nil
}

// Save: overwrites everything (no "leave blank to keep the current password" partial merge: settings change rarely,
// and always filling in the full form avoids confusion about which password is actually stored).
// Per-channel updates go through SaveSMTP / SaveTelegram / SaveSlack / SaveWebhooks, which do a
// read-modify-write on top of this function under saveMu.
//
// It applies the new settings first and persists them only once alertmanager.yml is written, so a failed
// save leaves the database, the file and Alertmanager all on the old settings. Same order and same one
// asymmetry as RulesService.SaveRule: if the database write fails after apply, Alertmanager runs settings
// the database does not hold until the next save or LoadAndApply at boot converges it back.
func (s *Service) Save(ctx context.Context, settings Settings) error {
	smtpConfigJSON, smtpSecret, err := s.encodeSMTP(settings.SMTP)
	if err != nil {
		return err
	}
	webhookJSON, err := json.Marshal(settings.WebhookURLs)
	if err != nil {
		return err
	}
	var tgConfig, tgSecret, slackSecret []byte
	if t := settings.Telegram; t != nil {
		if tgConfig, err = json.Marshal(t); err != nil {
			return err
		}
		if tgSecret, err = s.seal(t.BotToken); err != nil {
			return err
		}
	}
	if settings.Slack != nil {
		if slackSecret, err = s.seal(settings.Slack.WebhookURL); err != nil {
			return err
		}
	}
	if err := s.apply(settings); err != nil {
		return err
	}
	return s.store.SaveAlertSettings(ctx, store.AlertSettings{
		SmtpConfig: smtpConfigJSON, SmtpSecret: smtpSecret, WebhookUrls: webhookJSON,
		TelegramConfig: tgConfig, TelegramSecret: tgSecret, SlackSecret: slackSecret,
		NotifyLanguage: string(languageOf(string(settings.Language))), NotifyTimezone: settings.Timezone,
	})
}

// SaveSMTP: replaces only SMTP (nil = disable and clear); the webhook list is kept as is.
// Loads first, then writes everything back: load returns the full decrypted settings, so callers need not resend the other channel.
// This is what makes per-channel endpoints possible (a whole-object PUT would force re-entering the SMTP password just to change a webhook).
func (s *Service) SaveSMTP(ctx context.Context, smtp *SMTP) error {
	return s.saveOne(ctx, func(cur *Settings) { cur.SMTP = smtp })
}

// SaveTelegram: replaces only Telegram (nil = disable and clear); other channels are kept as is.
func (s *Service) SaveTelegram(ctx context.Context, t *Telegram) error {
	return s.saveOne(ctx, func(cur *Settings) { cur.Telegram = t })
}

// SaveSlack: replaces only Slack (nil = disable and clear); other channels are kept as is.
func (s *Service) SaveSlack(ctx context.Context, sl *Slack) error {
	return s.saveOne(ctx, func(cur *Settings) { cur.Slack = sl })
}

// saveOne: read-modify-write that replaces a single channel (same reasoning as SaveSMTP).
func (s *Service) saveOne(ctx context.Context, set func(*Settings)) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	cur, err := s.load(ctx)
	if err != nil {
		return err
	}
	set(&cur)
	return s.Save(ctx, cur)
}

// SaveLocale: sets the notification language and time zone (callers validate both). A language change
// rewrites the rules file first, since Prometheus is the side that can reject a file; if the
// alertmanager.yml write or the database write fails after that, the rules go back to the old language
// (best-effort), so nothing is left half in the new language.
func (s *Service) SaveLocale(ctx context.Context, lang api.NotifyLanguage, zone string) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	cur, err := s.load(ctx)
	if err != nil {
		return err
	}
	old := cur.Language
	cur.Language, cur.Timezone = lang, zone
	rewrite := s.Rules != nil && lang != old
	if rewrite {
		if err := s.Rules.ApplyLanguage(ctx, lang); err != nil {
			return err
		}
	}
	if err := s.Save(ctx, cur); err != nil {
		if rewrite {
			_ = s.Rules.ApplyLanguage(ctx, old)
		}
		return err
	}
	return nil
}

// SaveWebhooks: replaces only the webhook list (empty slice = clear); SMTP settings (including the stored password) are kept as is.
func (s *Service) SaveWebhooks(ctx context.Context, urls []WebhookURL) error {
	return s.saveOne(ctx, func(cur *Settings) { cur.WebhookURLs = urls })
}

func (s *Service) load(ctx context.Context) (Settings, error) {
	a, err := s.store.GetAlertSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	out := Settings{Language: languageOf(a.NotifyLanguage), Timezone: a.NotifyTimezone}
	if len(a.SmtpConfig) > 0 {
		var smtp SMTP
		if err := json.Unmarshal(a.SmtpConfig, &smtp); err != nil {
			return Settings{}, fmt.Errorf("parse smtp_config: %w", err)
		}
		if smtp.Password, err = s.open(a.SmtpSecret); err != nil {
			return Settings{}, fmt.Errorf("decrypt SMTP password: %w", err)
		}
		out.SMTP = &smtp
	}
	if len(a.WebhookUrls) > 0 {
		if err := json.Unmarshal(a.WebhookUrls, &out.WebhookURLs); err != nil {
			return Settings{}, fmt.Errorf("parse webhook_urls: %w", err)
		}
	}
	if len(a.TelegramConfig) > 0 {
		var t Telegram
		if err := json.Unmarshal(a.TelegramConfig, &t); err != nil {
			return Settings{}, fmt.Errorf("parse telegram_config: %w", err)
		}
		if t.BotToken, err = s.open(a.TelegramSecret); err != nil {
			return Settings{}, fmt.Errorf("decrypt Telegram bot token: %w", err)
		}
		out.Telegram = &t
	}
	if len(a.SlackSecret) > 0 {
		u, err := s.open(a.SlackSecret)
		if err != nil {
			return Settings{}, fmt.Errorf("decrypt Slack webhook: %w", err)
		}
		out.Slack = &Slack{WebhookURL: u}
	}
	return out, nil
}

// seal / open: encrypt/decrypt secret columns (same as the SMTP password, with the CA master key). An empty string is stored as nil.
func (s *Service) seal(v string) ([]byte, error) {
	if v == "" {
		return nil, nil
	}
	return auth.EncryptSecret(s.master, []byte(v))
}

func (s *Service) open(b []byte) (string, error) {
	if len(b) == 0 {
		return "", nil
	}
	v, err := auth.DecryptSecret(s.master, b)
	return string(v), err
}

func (s *Service) encodeSMTP(smtp *SMTP) (cfgJSON, secret []byte, err error) {
	if smtp == nil {
		return nil, nil, nil
	}
	if cfgJSON, err = json.Marshal(smtp); err != nil { // Password is tagged `json:"-"`, so it never goes into the JSON
		return nil, nil, err
	}
	if secret, err = s.seal(smtp.Password); err != nil {
		return nil, nil, err
	}
	return cfgJSON, secret, nil
}

// apply: render alertmanager.yml, write it to the shared volume, trigger a hot reload. Any step that is not enabled (empty path) is skipped.
func (s *Service) apply(settings Settings) error {
	yamlBytes, err := render(settings, s.ShowOrg, s.CustomCA())
	if err != nil {
		return err
	}
	if s.configDir != "" {
		if err := pki.WriteAtomic(s.configDir, "alertmanager.yml", yamlBytes, 0o644, s.configGID); err != nil {
			return fmt.Errorf("write alertmanager.yml: %w", err)
		}
	}
	if s.reloadURL != "" {
		go s.reload() // best-effort: if Alertmanager happens to be restarting during the reload, the next settings change triggers another one; does not block Save
	}
	return nil
}

func (s *Service) reload() {
	req, err := http.NewRequest(http.MethodPost, s.reloadURL, nil)
	if err != nil {
		return
	}
	hc := &http.Client{Timeout: 5 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}
