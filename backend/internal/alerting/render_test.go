package alerting

import (
	"bytes"
	"fmt"
	htmltemplate "html/template"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	// nosemgrep: go.lang.security.audit.xss.import-text-template.import-text-template -- the test simulates Alertmanager expanding plain-text fields (which uses text/template); no HTML is output
	texttemplate "text/template"
	"time"

	"gopkg.in/yaml.v3"

	"besu-manager/backend/internal/api"
)

func TestRenderAlwaysIncludesPlatformWebhook(t *testing.T) {
	b, err := render(Settings{}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	var cfg amConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("rendered yaml doesn't parse: %v\n%s", err, b)
	}
	if len(cfg.Receivers) != 1 || cfg.Receivers[0].Name != "platform" {
		t.Fatalf("want single 'platform' receiver, got %+v", cfg.Receivers)
	}
	if len(cfg.Receivers[0].WebhookConfigs) != 1 || cfg.Receivers[0].WebhookConfigs[0].URL != platformWebhookURL {
		t.Fatalf("want platform webhook always present, got %+v", cfg.Receivers[0].WebhookConfigs)
	}
	if cfg.Global != nil {
		t.Fatalf("no SMTP configured, want no global smtp block, got %+v", cfg.Global)
	}
}

// The inhibit rule is always rendered (regardless of external receivers) and must not have equal: PlatformDown has no node label, so with equal it would inhibit nothing.
func TestRenderAlwaysInhibitsNodeOfflineOnPlatformDown(t *testing.T) {
	b, err := render(Settings{}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	var cfg amConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("rendered yaml doesn't parse: %v\n%s", err, b)
	}
	if len(cfg.InhibitRules) != 1 {
		t.Fatalf("want one inhibit rule, got %+v", cfg.InhibitRules)
	}
	r := cfg.InhibitRules[0]
	if len(r.SourceMatchers) != 1 || !strings.Contains(r.SourceMatchers[0], "PlatformDown") {
		t.Errorf("source should be PlatformDown, got %+v", r.SourceMatchers)
	}
	if len(r.TargetMatchers) != 1 || !strings.Contains(r.TargetMatchers[0], "NodeOffline") {
		t.Errorf("target should be NodeOffline, got %+v", r.TargetMatchers)
	}
	if strings.Contains(string(b), "equal:") {
		t.Errorf("inhibit rule must not carry `equal` (labels don't overlap):\n%s", b)
	}
}

// group_interval decides how fast resolved notifications reach the platform; going back to 5m would leave resolved alerts in "進行中" (active) for several extra minutes.
func TestRenderRouteTimings(t *testing.T) {
	b, err := render(Settings{}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	var cfg amConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("rendered yaml doesn't parse: %v\n%s", err, b)
	}
	r := cfg.Route
	if r.GroupWait != "30s" || r.GroupInterval != "30s" || r.RepeatInterval != "4h" {
		t.Fatalf("want group_wait 30s / group_interval 30s / repeat_interval 4h, got %s / %s / %s",
			r.GroupWait, r.GroupInterval, r.RepeatInterval)
	}
}

func TestRenderAddsCustomWebhooksAndSMTP(t *testing.T) {
	b, err := render(Settings{
		SMTP: &SMTP{Host: "smtp.example", Port: 587, Username: "u", Password: "p", From: "a@b.c", To: []string{"x@y.z"}},
		WebhookURLs: []WebhookURL{
			{Label: "slack", URL: "https://hooks.slack.com/x"},
		},
	}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	var cfg amConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("rendered yaml doesn't parse: %v\n%s", err, b)
	}
	rcv := cfg.Receivers[0]
	if len(rcv.WebhookConfigs) != 2 { // platform + custom
		t.Fatalf("want 2 webhook_configs (platform + custom), got %d", len(rcv.WebhookConfigs))
	}
	if len(rcv.EmailConfigs) != 1 || rcv.EmailConfigs[0].To != "x@y.z" {
		t.Fatalf("want one email_config to x@y.z, got %+v", rcv.EmailConfigs)
	}
	if cfg.Global == nil || cfg.Global.SMTPSmarthost != "smtp.example:587" {
		t.Fatalf("want global smtp_smarthost=smtp.example:587, got %+v", cfg.Global)
	}
	// The password really does end up in the rendered yaml (Alertmanager needs it in plaintext for SMTP auth).
	// That is a design limitation of Alertmanager itself, not a defect we introduced; the test only pins down that render does not drop the value.
	if !strings.Contains(string(b), "smtp_auth_password: p") {
		t.Fatalf("want smtp password in rendered yaml, got:\n%s", b)
	}
}

// A relay without login renders no smtp_auth_* keys, so Alertmanager never attempts AUTH.
func TestRenderSMTPWithoutLogin(t *testing.T) {
	b, err := render(Settings{SMTP: &SMTP{Host: "relay.internal", Port: 25, From: "a@b.c", To: []string{"x@y.z"}}}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "smtp_auth_") {
		t.Fatalf("want no smtp_auth_* keys, got:\n%s", b)
	}
}

// require_tls and force_implicit_tls are both always written, set from the chosen security. Omitting
// require_tls makes Alertmanager default to true, which cannot talk to plain-text relays, and omitting
// force_implicit_tls lets the port decide (465 = implicit TLS); either error only shows up in the
// Alertmanager log, so in the UI it looks like "configured but no mail arrives".
func TestRenderEmailSecurity(t *testing.T) {
	for _, tc := range []struct {
		security                     Security
		requireTLS, forceImplicitTLS bool
	}{
		{SecurityNone, false, false},
		{SecurityStartTLS, true, false},
		{SecurityTLS, false, true},
	} {
		for _, ca := range []bool{false, true} {
			b, err := render(Settings{SMTP: &SMTP{Host: "mail.internal", Port: 465, Security: tc.security, From: "a@b", To: []string{"c@d", "e@f"}}}, true, ca)
			if err != nil {
				t.Fatal(err)
			}
			var cfg struct {
				Receivers []struct {
					EmailConfigs []struct {
						To               string `yaml:"to"`
						RequireTLS       *bool  `yaml:"require_tls"`
						ForceImplicitTLS *bool  `yaml:"force_implicit_tls"`
						SendResolved     *bool  `yaml:"send_resolved"`
						TLSConfig        *struct {
							CAFile string `yaml:"ca_file"`
						} `yaml:"tls_config"`
					} `yaml:"email_configs"`
				} `yaml:"receivers"`
			}
			if err := yaml.Unmarshal(b, &cfg); err != nil {
				t.Fatal(err)
			}
			ec := cfg.Receivers[0].EmailConfigs
			if len(ec) != 2 {
				t.Fatalf("收件人數 = %d, want 2", len(ec))
			}
			for _, e := range ec {
				if e.RequireTLS == nil || *e.RequireTLS != tc.requireTLS || e.ForceImplicitTLS == nil || *e.ForceImplicitTLS != tc.forceImplicitTLS {
					t.Errorf("%s %s: require_tls = %v, force_implicit_tls = %v, want %v / %v", tc.security, e.To, e.RequireTLS, e.ForceImplicitTLS, tc.requireTLS, tc.forceImplicitTLS)
				}
				// Email's send_resolved defaults to false; omitting it means recovery mail is never sent
				if e.SendResolved == nil || !*e.SendResolved {
					t.Errorf("%s 的 send_resolved = %v, want true", e.To, e.SendResolved)
				}
				// ca_file replaces Alertmanager's trust store, so it appears only with a CA file and an encrypted connection
				wantCA := ca && tc.security != SecurityNone
				if gotCA := e.TLSConfig != nil && e.TLSConfig.CAFile == smtpCAPath; gotCA != wantCA || (!wantCA && e.TLSConfig != nil) {
					t.Errorf("%s ca=%v: tls_config = %+v, want ca_file %v", tc.security, ca, e.TLSConfig, wantCA)
				}
			}
		}
	}
}

// Alert mail must contain no links, but must have an HTML version.
// Alertmanager's built-in templates insert two links back to itself, but 9093 is not exposed and the hostname is the container ID,
// so those links are always dead for recipients; if either the html or text field is missing, the built-in template is used.
func TestRenderEmailHasNoLinks(t *testing.T) {
	b, err := render(Settings{SMTP: &SMTP{Host: "mail.internal", Port: 25, From: "a@b", To: []string{"c@d"}}}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Receivers []struct {
			EmailConfigs []struct {
				HTML *string `yaml:"html"`
				Text string  `yaml:"text"`
			} `yaml:"email_configs"`
		} `yaml:"receivers"`
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	ec := cfg.Receivers[0].EmailConfigs[0]
	if !strings.Contains(string(b), "Subject: ") {
		t.Error("主旨要明確給值,否則 Alertmanager 用預設的 [FIRING:1] 主旨")
	}

	// Both fields must be set explicitly: if either is missing, Alertmanager uses its own (with the dead links).
	if ec.HTML == nil || *ec.HTML == "" {
		t.Fatal("html 模板是空的,Alertmanager 會退回內建模板")
	}
	if ec.Text == "" {
		t.Fatal("text 模板是空的")
	}

	for name, tmpl := range map[string]string{"html": *ec.HTML, "text": ec.Text} {
		for _, bad := range []string{"http://", "https://", "<a ", "href=", ".ExternalURL", ".GeneratorURL"} {
			if strings.Contains(tmpl, bad) {
				t.Errorf("%s 模板含 %q,告警信不該有連結", name, bad)
			}
		}
	}

	// Hard limits for mail HTML: no external resources (images/fonts/stylesheets), or they get blocked or act as tracking pixels;
	// and no <style> blocks either: clients such as Gmail web strip them entirely, so styles must be inline.
	for _, bad := range []string{"<img", "<link", "@import", "src=", "<style"} {
		if strings.Contains(*ec.HTML, bad) {
			t.Errorf("html 模板含 %q,郵件不該引用任何外部資源", bad)
		}
	}
}

// -- Does the template actually run? -------------------------------------
// The tests above only do string matching: they catch "wrote something wrong" but not "the template itself is broken".
// This one actually executes it with html/template, as Alertmanager does, checking three things string matching cannot see:
//   (1) the template parses and executes without error (conditionals, range and variable reassignment syntax are all valid)
//   (2) the severity -> color mapping really takes effect (the scope of `{{ $fg = }}` must reach outside the if block;
//      if it did not, every alert would get the default gray and string matching would still pass)
//   (3) alert content is auto-escaped (custom alert rule text is user-entered)

type tmplAlert struct {
	Status      string
	Labels      map[string]string
	Annotations map[string]string
	StartsAt    time.Time
	EndsAt      time.Time
}

// tmplAlerts: mirrors Alertmanager's template.Alerts: rangeable itself, plus Firing/Resolved methods.
type tmplAlerts []tmplAlert

func (a tmplAlerts) Firing() []tmplAlert   { return a.with("firing") }
func (a tmplAlerts) Resolved() []tmplAlert { return a.with("resolved") }

func (a tmplAlerts) with(status string) []tmplAlert {
	var out []tmplAlert
	for _, x := range a {
		if x.Status == status {
			out = append(out, x)
		}
	}
	return out
}

// humanizeDuration: whole-second version of Alertmanager's, which drops leading zero units: 70 -> "1m 10s".
func humanizeDuration(v float64) string {
	n := int(v)
	d, h, m, sec := n/86400, n/3600%24, n/60%60, n%60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh %dm %ds", d, h, m, sec)
	case h > 0:
		return fmt.Sprintf("%dh %dm %ds", h, m, sec)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}

func withZone(lang api.NotifyLanguage, zone string) noticeText {
	nt := noticeTexts[lang]
	nt.zone = zone
	return nt
}

// amFuncs: the functions the templates use, filled in to match Alertmanager's behavior (tz, humanizeDuration, reReplaceAll).
var amFuncs = map[string]any{
	"tz": func(name string, t time.Time) (time.Time, error) {
		loc, err := time.LoadLocation(name)
		return t.In(loc), err
	},
	"humanizeDuration": humanizeDuration,
	"reReplaceAll": func(pattern, repl, text string) string {
		return regexp.MustCompile(pattern).ReplaceAllString(text, repl)
	},
}

func TestEmailHTMLTemplateExecutes(t *testing.T) {
	data := struct {
		Status string
		Alerts tmplAlerts
	}{
		Status: "firing",
		Alerts: tmplAlerts{
			{
				Status:      "firing",
				Labels:      map[string]string{"alertname": "custom_rule_abc-123", "severity": "critical", "node": "node-a1", "org": "org-a"},
				Annotations: map[string]string{"summary": "peer 數 < 3 且 A&B 都掉線", "rule_name": "夜測 peer 偏低"},
				StartsAt:    time.Date(2026, 8, 26, 7, 43, 54, 0, time.UTC),
			},
			{
				Status:      "firing",
				Labels:      map[string]string{"alertname": "NodePeerCountLow", "severity": "warning", "node": "node-a2"},
				Annotations: map[string]string{"summary": "peer 偏低"},
				StartsAt:    time.Date(2026, 8, 26, 7, 44, 0, 0, time.UTC),
			},
		},
	}

	exec := func(org bool) string {
		tmpl, err := htmltemplate.New("email").Funcs(amFuncs).Parse(emailHTML(withZone(api.NotifyLanguageZh, "Asia/Taipei"), org))
		if err != nil {
			t.Fatalf("模板 parse 失敗: %v", err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			t.Fatalf("模板 execute 失敗: %v", err)
		}
		return buf.String()
	}
	out := exec(true)

	// (5) The organization row is printed only in Enterprise (org=true); Core does not print it for the same data.
	if !strings.Contains(out, ">單位<") || !strings.Contains(out, "org-a") {
		t.Errorf("Enterprise 應印出單位那一列:\n%s", out)
	}
	if core := exec(false); strings.Contains(core, ">單位<") || strings.Contains(core, "org-a") {
		t.Errorf("Core 不該印出單位:\n%s", core)
	}

	// (2) Each severity's chip must get its own foreground + background colors.
	//
	// The check must be tied to the chip's own inline style, not just the color code: the firing dot in the header hard-codes
	// #B3322F, so searching for the color alone would be satisfied by it and pass falsely (this actually happened: mutation-testing `=` to `:=` still passed with the old check).
	for _, c := range []struct{ sev, fg, bg string }{
		{"嚴重", "#B3322F", "#FBE9E9"},
		{"警告", "#8F5F00", "#FBF3DB"},
	} {
		chip := fmt.Sprintf("color:%s;background:%s;\">%s</span>", c.fg, c.bg, c.sev)
		if !strings.Contains(out, chip) {
			t.Errorf("severity=%s 的 chip 沒拿到 %s/%s —— 檢查 {{ $fg = }} 用的是 `=` 不是 `:=`(用 `:=` 會在 if 內另建變數,出了區塊就沒了)\n輸出:\n%s",
				c.sev, c.fg, c.bg, out)
		}
	}

	// (4) The "告警規則" (alert rule) field: shows rule_name when present, without exposing custom_rule_<uuid>; falls back to alertname otherwise.
	if strings.Contains(out, "custom_rule_abc-123") || !strings.Contains(out, "夜測 peer 偏低") {
		t.Errorf("告警規則欄應顯示 rule_name 而非 alertname:\n%s", out)
	}
	if !strings.Contains(out, "NodePeerCountLow") {
		t.Errorf("沒有 rule_name 時應退回 alertname:\n%s", out)
	}

	// (3) User-controlled text must be escaped.
	if strings.Contains(out, "< 3 且 A&B") {
		t.Error("summary 沒有被跳脫;html/template 應該把 < 與 & 轉成實體")
	}
	if !strings.Contains(out, "&lt; 3") || !strings.Contains(out, "&amp;B") {
		t.Errorf("預期看到跳脫後的 &lt; 與 &amp;,實際輸出:\n%s", out)
	}
}

// Telegram uses HTML (summary in bold) and Slack uses mrkdwn, both with user text escaped first; both send resolved. Absent when not configured.
func TestRenderTelegramAndSlack(t *testing.T) {
	thread := int64(42)
	b, err := render(Settings{
		Telegram: &Telegram{BotToken: "123:abc", ChatID: -1001234567890, MessageThreadID: &thread},
		Slack:    &Slack{WebhookURL: "https://hooks.slack.com/services/T0/B0/xyz"},
	}, true, false)
	if err != nil {
		t.Fatal(err)
	}

	var cfg amConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("rendered yaml doesn't parse: %v\n%s", err, b)
	}
	r := cfg.Receivers[0]
	if len(r.TelegramConfigs) != 1 {
		t.Fatalf("telegram_configs = %+v", r.TelegramConfigs)
	}
	tg := r.TelegramConfigs[0]
	if tg.BotToken != "123:abc" || tg.ChatID != -1001234567890 || tg.MessageThreadID == nil || *tg.MessageThreadID != 42 || !tg.SendResolved ||
		tg.ParseMode != "HTML" || tg.Message != telegramMessage(withZone(api.NotifyLanguageEn, hostZone()), true) || !strings.Contains(tg.Message, `reReplaceAll "<" "&lt;"`) {
		t.Errorf("telegram = %+v", tg)
	}
	if len(r.SlackConfigs) != 1 {
		t.Fatalf("slack_configs = %+v", r.SlackConfigs)
	}
	sl := r.SlackConfigs[0]
	if sl.APIURL != "https://hooks.slack.com/services/T0/B0/xyz" || !sl.SendResolved || !strings.Contains(sl.Text, `reReplaceAll "<" "&lt;"`) || sl.Fallback != headline(noticeTexts[api.NotifyLanguageEn]) {
		t.Errorf("slack = %+v", sl)
	}
	if !strings.Contains(string(b), `title_link: ""`) {
		t.Errorf("title_link must render as an explicit empty string (default links back to Alertmanager):\n%s", b)
	}

	b, err = render(Settings{}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "telegram_configs") || strings.Contains(string(b), "slack_configs") {
		t.Errorf("unconfigured channels must not render:\n%s", b)
	}
}

// In plain text / Telegram / Slack, only Enterprise appends the organization after "發生節點" (node).
func TestNoticeTextOrgOnlyInEnterprise(t *testing.T) {
	for _, c := range []struct {
		name string
		tmpl func(noticeText, bool) string
	}{{"email", emailText}, {"telegram", telegramMessage}, {"slack", slackText}, {"html", emailHTML}} {
		for lang := range noticeTexts {
			nt := withZone(lang, "UTC")
			if !strings.Contains(c.tmpl(nt, true), "Labels.org") {
				t.Errorf("%s %s: Enterprise 應帶單位", c.name, lang)
			}
			if core := c.tmpl(nt, false); strings.Contains(core, "Labels.org") || strings.Contains(core, nt.org) {
				t.Errorf("%s %s: Core 不該帶單位", c.name, lang)
			}
		}
	}
}

// Every notification template is written in the configured language and shows times in the configured zone, followed by
// the zone name. Executed on a firing and a resolved alert, for each channel's templates as rendered into alertmanager.yml.
func TestNoticeLanguageAndZone(t *testing.T) {
	han := regexp.MustCompile(`\p{Han}`)
	start := time.Date(2026, 8, 26, 7, 43, 54, 0, time.UTC)
	alert := func(status string) tmplAlert {
		a := tmplAlert{
			Status:      status,
			Labels:      map[string]string{"alertname": "NodeCpuHigh", "severity": "critical", "node": "node-a1", "org": "org-a"},
			Annotations: map[string]string{"summary": "CPU high", "description": "Condition: CPU > 90%", "rule_name": "NodeCpuHigh"},
			StartsAt:    start,
		}
		if status == "resolved" {
			a.EndsAt = start.Add(70 * time.Second)
		}
		return a
	}
	for _, c := range []struct {
		lang             api.NotifyLanguage
		zone             string
		firing, resolved []string // in every channel's output
		node             string   // the node line, with the organization (Enterprise)
		chinese          bool
	}{
		{
			api.NotifyLanguageZh, "Asia/Taipei",
			[]string{"[嚴重] 觸發中", "2026-08-26 15:43:54（Asia/Taipei）"},
			[]string{"[已恢復] 持續 1 分 10 秒", "2026-08-26 15:43:54（Asia/Taipei）", "2026-08-26 15:45:04（Asia/Taipei）"},
			"發生節點：node-a1（單位 org-a）", true,
		},
		{
			api.NotifyLanguageEn, "America/New_York",
			[]string{"[Critical] Firing", "2026-08-26 03:43:54 (America/New_York)"},
			[]string{"[Resolved] after 1m 10s", "2026-08-26 03:43:54 (America/New_York)", "2026-08-26 03:45:04 (America/New_York)"},
			"Node: node-a1 (organization org-a)", false,
		},
	} {
		b, err := render(Settings{
			SMTP:     &SMTP{Host: "mail.internal", Port: 25, From: "a@b", To: []string{"c@d"}},
			Telegram: &Telegram{BotToken: "1:x", ChatID: -1},
			Slack:    &Slack{WebhookURL: "https://hooks.slack.com/services/T0/B0/x"},
			Language: c.lang, Timezone: c.zone,
		}, true, false)
		if err != nil {
			t.Fatal(err)
		}
		var cfg amConfig
		if err := yaml.Unmarshal(b, &cfg); err != nil {
			t.Fatal(err)
		}
		r := cfg.Receivers[0]
		texts := map[string]string{
			"subject": r.EmailConfigs[0].Headers["Subject"], "text": r.EmailConfigs[0].Text, "telegram": r.TelegramConfigs[0].Message,
			"slack title": r.SlackConfigs[0].Title, "slack text": r.SlackConfigs[0].Text,
		}
		for _, status := range []string{"firing", "resolved"} {
			data := struct {
				Status string
				Alerts tmplAlerts
			}{status, tmplAlerts{alert(status)}}
			outs := map[string]string{}
			for name, src := range texts {
				tmpl, err := texttemplate.New(name).Funcs(amFuncs).Parse(src)
				if err != nil {
					t.Fatalf("%s %s: parse: %v", c.lang, name, err)
				}
				var buf bytes.Buffer
				if err := tmpl.Execute(&buf, data); err != nil {
					t.Fatalf("%s %s: execute: %v", c.lang, name, err)
				}
				outs[name] = buf.String()
			}
			tmpl, err := htmltemplate.New("html").Funcs(amFuncs).Parse(r.EmailConfigs[0].HTML)
			if err != nil {
				t.Fatalf("%s html: parse: %v", c.lang, err)
			}
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, data); err != nil {
				t.Fatalf("%s html: execute: %v", c.lang, err)
			}
			outs["html"] = buf.String()

			want := c.firing
			if status == "resolved" {
				want = c.resolved
			}
			for name, out := range outs {
				for i, w := range want {
					// the headline is in every output except the Slack body; the times are in every output except the headline-only ones
					headlineOnly := name == "subject" || name == "slack title"
					if (i == 0 && name == "slack text") || (i > 0 && headlineOnly) {
						continue
					}
					if !strings.Contains(out, w) {
						t.Errorf("%s %s %s: missing %q in:\n%s", c.lang, status, name, w, out)
					}
				}
				if han.MatchString(out) != c.chinese {
					t.Errorf("%s %s %s: Chinese present = %v:\n%s", c.lang, status, name, !c.chinese, out)
				}
				if strings.Contains(out, "台北時間") {
					t.Errorf("%s %s %s: hard-coded Taipei suffix:\n%s", c.lang, status, name, out)
				}
			}
			if !strings.Contains(outs["text"], c.node) {
				t.Errorf("%s %s: node line %q missing:\n%s", c.lang, status, c.node, outs["text"])
			}
		}
	}
}

// The default zone is the server process's, by IANA name: Alertmanager cannot use "Local" (that would be its own container's zone).
func TestHostZoneAndValidZone(t *testing.T) {
	t.Setenv("TZ", "America/New_York")
	if z := hostZone(); z != "America/New_York" {
		t.Errorf("TZ set: hostZone = %q", z)
	}
	t.Setenv("TZ", "Local")
	if z := hostZone(); !ValidZone(z) {
		t.Errorf("TZ=Local must fall through to a real name, got %q", z)
	}
	for zone, ok := range map[string]bool{"UTC": true, "Asia/Taipei": true, "Etc/GMT+8": true, "America/Argentina/Buenos_Aires": true,
		"": false, "Local": false, "Mars/Olympus": false, "../etc/passwd": false, `Asia/Taipei" }}{{ "x`: false} {
		if ValidZone(zone) != ok {
			t.Errorf("ValidZone(%q) = %v, want %v", zone, !ok, ok)
		}
	}
}

// The rendered ca_file points at smtpCAPath, so the boot copy must land under that name in the config dir.
func TestWriteSMTPCA(t *testing.T) {
	dir := t.TempDir()
	s := New(nil, nil, dir, -1, "")
	if err := s.WriteSMTPCA(); err != nil {
		t.Fatalf("no CA: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, smtpCAFile)); !os.IsNotExist(err) {
		t.Fatalf("no CA should write nothing, stat err = %v", err)
	}
	s.SMTPCA = &SMTPCA{PEM: []byte("pem")}
	if err := s.WriteSMTPCA(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, smtpCAFile))
	if err != nil || string(got) != "pem" {
		t.Fatalf("copy = %q, %v", got, err)
	}
	if filepath.Base(smtpCAPath) != smtpCAFile {
		t.Fatalf("smtpCAPath %q does not name %q", smtpCAPath, smtpCAFile)
	}
}
