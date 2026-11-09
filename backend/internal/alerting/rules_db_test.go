package alerting

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/store"
)

// Disabled rules can still be edited and re-enabled; the rules file contains only enabled rules. Only an id that truly does not exist counts as not found.
func TestSaveRuleDisabledRule(t *testing.T) {
	st, _ := testStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	svc := NewRulesService(st, dir, "")

	r := store.AlertRule{ID: "r1", Name: "cpu", Metric: "cpu", Operator: "gt", Threshold: 95, ForDuration: "5m",
		Severity: "warning", Enabled: true, CreatedBy: "ops", UpdatedBy: "ops", KeepFiringFor: "0s"}
	if _, err := svc.SaveRule(ctx, r, true); err != nil {
		t.Fatal(err)
	}

	r.Enabled = false
	if ok, err := svc.SaveRule(ctx, r, false); err != nil || !ok {
		t.Fatalf("停用: ok=%v err=%v", ok, err)
	}
	if strings.Contains(readRulesFile(t, dir), "custom_rule_r1") {
		t.Fatal("停用後規則檔不該有這條")
	}

	r.Threshold = 90
	if ok, err := svc.SaveRule(ctx, r, false); err != nil || !ok {
		t.Fatalf("停用中改門檻: ok=%v err=%v", ok, err)
	}
	if strings.Contains(readRulesFile(t, dir), "custom_rule_r1") {
		t.Fatal("停用中改欄位後規則檔不該有這條")
	}

	r.Enabled = true
	if ok, err := svc.SaveRule(ctx, r, false); err != nil || !ok {
		t.Fatalf("再啟用: ok=%v err=%v", ok, err)
	}
	if got := readRulesFile(t, dir); !strings.Contains(got, "custom_rule_r1") || !strings.Contains(got, "> 90") {
		t.Fatalf("再啟用後規則檔要有這條且用新門檻: %s", got)
	}
	if saved, _ := st.GetAlertRule(ctx, "r1"); saved == nil || !saved.Enabled || saved.Threshold != 90 {
		t.Fatalf("DB 沒更新: %+v", saved)
	}

	if ok, err := svc.SaveRule(ctx, store.AlertRule{ID: "nope", ForDuration: "5m"}, false); err != nil || ok {
		t.Fatalf("不存在的 id 要回 ok=false: ok=%v err=%v", ok, err)
	}
}

// The notification language and zone: a fresh install sends English in the host's zone; changing the language rewrites
// both alertmanager.yml and the rules file. If Prometheus rejects the rules, alertmanager.yml cannot be written or
// the database refuses the row, the rules go back to the old language and the database keeps it.
func TestSaveLocale(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	amDir, rulesDir := t.TempDir(), t.TempDir()
	svc := New(st, []byte("0123456789abcdef0123456789abcdef"), amDir, -1, "")
	svc.Rules = NewRulesService(st, rulesDir, "")
	if err := svc.Rules.SeedDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.LoadAndApply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.Rules.LoadAndApply(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := svc.View(ctx)
	if err != nil || v.Language != api.NotifyLanguageEn || v.Timezone != "" || v.Zone() != hostZone() {
		t.Fatalf("fresh install: %+v %v", v, err)
	}
	if got := readRulesFile(t, rulesDir); !strings.Contains(got, "Node {{ $labels.node }} disconnected") {
		t.Fatalf("fresh install rules should be English:\n%s", got)
	}

	if err := svc.SaveLocale(ctx, api.NotifyLanguageZh, "Asia/Taipei"); err != nil {
		t.Fatal(err)
	}
	if v, _ := svc.View(ctx); v.Language != api.NotifyLanguageZh || v.Zone() != "Asia/Taipei" {
		t.Fatalf("after save: %+v", v)
	}
	if got := readRulesFile(t, rulesDir); !strings.Contains(got, "節點 {{ $labels.node }} 連線中斷") {
		t.Fatalf("rules should be rewritten in Chinese:\n%s", got)
	}
	// alertmanager.yml only carries templates for configured channels; add one to see them
	if err := svc.SaveSlack(ctx, &Slack{WebhookURL: "https://hooks.slack.com/services/T0/B0/x"}); err != nil {
		t.Fatal(err)
	}
	am, err := os.ReadFile(filepath.Join(amDir, "alertmanager.yml"))
	if err != nil || !strings.Contains(string(am), "告警規則") || !strings.Contains(string(am), `tz "Asia/Taipei"`) || !strings.Contains(string(am), "（Asia/Taipei）") {
		t.Fatalf("alertmanager.yml should be Chinese, in Asia/Taipei: %v\n%s", err, am)
	}
	// a channel save keeps the language
	if got := readRulesFile(t, rulesDir); !strings.Contains(got, "連線中斷") {
		t.Fatal("channel save must not change the rules language")
	}

	// Prometheus rejects the rewritten rules: the database, the rules file and alertmanager.yml all stay as they were.
	reject := newFakeReloadServer(t, func() int { return 400 })
	defer reject.Close()
	svc.Rules.reloadURL = reject.URL
	if err := svc.SaveLocale(ctx, api.NotifyLanguageEn, "UTC"); err == nil {
		t.Fatal("want the Prometheus rejection")
	}
	if v, _ := svc.View(ctx); v.Language != api.NotifyLanguageZh || v.Zone() != "Asia/Taipei" {
		t.Fatalf("rejected save must not be stored: %+v", v)
	}
	if got := readRulesFile(t, rulesDir); !strings.Contains(got, "連線中斷") {
		t.Fatal("rules file must be rolled back to Chinese")
	}
	if am2, _ := os.ReadFile(filepath.Join(amDir, "alertmanager.yml")); string(am2) != string(am) {
		t.Fatal("alertmanager.yml must not change when the rules are rejected")
	}

	// Prometheus accepts the English rules but alertmanager.yml cannot be written (its directory is a regular file):
	// the rules go back to Chinese and the database keeps the old language.
	svc.Rules.reloadURL = ""
	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	svc.configDir = notDir
	if err := svc.SaveLocale(ctx, api.NotifyLanguageEn, "UTC"); err == nil {
		t.Fatal("want the alertmanager.yml write error")
	}
	if got := readRulesFile(t, rulesDir); !strings.Contains(got, "連線中斷") || strings.Contains(got, "disconnected") {
		t.Fatalf("rules must be rewritten back to Chinese after the alertmanager.yml write failed:\n%s", got)
	}
	if v, _ := svc.View(ctx); v.Language != api.NotifyLanguageZh || v.Timezone != "Asia/Taipei" {
		t.Fatalf("failed save must not be stored: %+v", v)
	}
	svc.configDir = amDir

	// The rules and alertmanager.yml are written but the database refuses the row: the rules go back to Chinese and
	// the database keeps the old language.
	if _, err := pool.Exec(ctx, `ALTER TABLE alert_settings ADD CONSTRAINT refuse_en CHECK (notify_language <> 'en') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	var pgErr *pgconn.PgError
	if err := svc.SaveLocale(ctx, api.NotifyLanguageEn, "UTC"); !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("want the check violation from the database, got %v", err)
	}
	if got := readRulesFile(t, rulesDir); !strings.Contains(got, "連線中斷") || strings.Contains(got, "disconnected") {
		t.Fatalf("rules must be rewritten back to Chinese after the database write failed:\n%s", got)
	}
	if v, _ := svc.View(ctx); v.Language != api.NotifyLanguageZh || v.Timezone != "Asia/Taipei" {
		t.Fatalf("failed save must not be stored: %+v", v)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE alert_settings DROP CONSTRAINT refuse_en`); err != nil {
		t.Fatal(err)
	}

	// Saving only the language with an empty zone goes back to following the host and does not pin a zone.
	if err := svc.SaveLocale(ctx, api.NotifyLanguageEn, ""); err != nil {
		t.Fatal(err)
	}
	if v, _ := svc.View(ctx); v.Language != api.NotifyLanguageEn || v.Timezone != "" || v.Zone() != hostZone() {
		t.Fatalf("empty zone must mean follow the host: %+v", v)
	}
}

func testStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL absent")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || !strings.Contains(u.Path, "test") {
		t.Fatal("expected local test database")
	}
	ctx := context.Background()
	cfg, _ := pgxpool.ParseConfig(raw)
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("alerting_test_%s", strings.ToLower(rand.Text()))
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`); admin.Close() })
	st := store.New(pool)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st, pool
}
