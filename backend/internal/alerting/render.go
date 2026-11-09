package alerting

import (
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"

	"besu-manager/backend/internal/api"
)

// amConfig and related types map a subset of the Alertmanager config file (only the fields actually produced here).
// Hand-written structs + yaml.Marshal rather than template string concatenation: the config has conditional blocks (whether SMTP is set,
// how many webhooks), and structs are less prone than string concatenation to missing keys or wrong indentation.
type amConfig struct {
	Global       *amGlobal       `yaml:"global,omitempty"`
	Route        amRoute         `yaml:"route"`
	InhibitRules []amInhibitRule `yaml:"inhibit_rules,omitempty"`
	Receivers    []amReceiver    `yaml:"receivers"`
}

type amInhibitRule struct {
	SourceMatchers []string `yaml:"source_matchers"`
	TargetMatchers []string `yaml:"target_matchers"`
	// Deliberately no equal: the two sides share no label (PlatformDown only has job/instance),
	// so listing one would make it inhibit nothing at all.
}

type amGlobal struct {
	SMTPFrom         string `yaml:"smtp_from,omitempty"`
	SMTPSmarthost    string `yaml:"smtp_smarthost,omitempty"`
	SMTPAuthUsername string `yaml:"smtp_auth_username,omitempty"`
	SMTPAuthPassword string `yaml:"smtp_auth_password,omitempty"`
}

type amRoute struct {
	Receiver       string   `yaml:"receiver"`
	GroupBy        []string `yaml:"group_by"`
	GroupWait      string   `yaml:"group_wait"`
	GroupInterval  string   `yaml:"group_interval"`
	RepeatInterval string   `yaml:"repeat_interval"`
}

type amReceiver struct {
	Name            string          `yaml:"name"`
	WebhookConfigs  []amWebhookCfg  `yaml:"webhook_configs,omitempty"`
	EmailConfigs    []amEmailCfg    `yaml:"email_configs,omitempty"`
	TelegramConfigs []amTelegramCfg `yaml:"telegram_configs,omitempty"`
	SlackConfigs    []amSlackCfg    `yaml:"slack_configs,omitempty"`
}

type amTelegramCfg struct {
	BotToken        string `yaml:"bot_token"`
	ChatID          int64  `yaml:"chat_id"`
	MessageThreadID *int64 `yaml:"message_thread_id,omitempty"`
	// HTML (summary in bold). All user-entered text goes through textEsc: unescaped, a < or & in the text makes Telegram
	// reject the whole message (400 can't parse entities), and the error only shows up in the Alertmanager log.
	ParseMode    string `yaml:"parse_mode"`
	Message      string `yaml:"message"`
	SendResolved bool   `yaml:"send_resolved"`
}

type amSlackCfg struct {
	APIURL string `yaml:"api_url"`
	Title  string `yaml:"title"`
	// Always set title_link and fallback explicitly: when omitted, Alertmanager's defaults include a link back to Alertmanager (9093, not exposed externally),
	// which recipients cannot open, i.e. a dead link. Verified against the loaded config.
	TitleLink    string `yaml:"title_link"`
	Fallback     string `yaml:"fallback"`
	Text         string `yaml:"text"`
	SendResolved bool   `yaml:"send_resolved"`
}

type amWebhookCfg struct {
	URL          string        `yaml:"url"`
	SendResolved bool          `yaml:"send_resolved,omitempty"`
	HTTPConfig   *amHTTPConfig `yaml:"http_config,omitempty"`
}

type amHTTPConfig struct {
	Authorization *amAuthorization `yaml:"authorization,omitempty"`
}

type amAuthorization struct {
	CredentialsFile string `yaml:"credentials_file,omitempty"`
}

type amEmailCfg struct {
	To string `yaml:"to"`
	// Override Alertmanager's built-in templates: our own HTML + plain text, neither containing links.
	// The built-in templates insert two links back to Alertmanager itself, but 9093 is not exposed and the hostname is the container ID
	// (which changes on every restart), so recipients can never open them, and they leak internal naming into the mail.
	// Both fields must be set explicitly: if either is missing, Alertmanager uses its own and the dead links come back.
	HTML    string            `yaml:"html"`
	Text    string            `yaml:"text"`
	Headers map[string]string `yaml:"headers"`
	// Both always rendered, set from SMTP.Security. Omitting either hands the choice to Alertmanager's defaults:
	// require_tls defaults to true, which blocks plain-text relays with the error only in the Alertmanager log, and
	// without force_implicit_tls the port decides (465 = implicit TLS), which would override an explicit starttls on 465.
	// Plain bools rather than omitempty pointers: false must appear in the file too.
	RequireTLS       bool `yaml:"require_tls"`
	ForceImplicitTLS bool `yaml:"force_implicit_tls"`
	// Explicitly true: send_resolved defaults to false for Email (webhook and Telegram default to true);
	// omitting it means recovery mail is never sent, and the "已恢復" (resolved) part of the template is dead weight.
	SendResolved bool `yaml:"send_resolved"`
	// Only with SMTP_CA_FILE and an encrypted connection: ca_file replaces Alertmanager's system trust store.
	TLSConfig *amTLSConfig `yaml:"tls_config,omitempty"`
}

type amTLSConfig struct {
	CAFile string `yaml:"ca_file"`
}

// -- Notification content (Email / Telegram / Slack share the same text) --------------
// The first line is the status: [Critical] Firing, [Attention] Firing: 3 alerts, [Resolved] after 1m 0s
// ([嚴重] 觸發中, [警告] 3 則觸發中, [已恢復] 持續 1 分 0 秒 in Chinese); then one block per alert:
// summary (bold), description (firing only), alert rule, node (organization), start/resolve time. Times are shown in the
// configured zone followed by its name (Alertmanager's tz takes an IANA name; the image ships timezone data). No links.
// Alertmanager grouping merges alerts with the same rule and node into one, so the headline only states the count when it is greater than 1.

// noticeText: the fixed words of a notification in one language, plus the zone times are shown in (set per render).
// Every template below is built from one noticeText, so a receiver could later be given its own language without changing this data.
type noticeText struct {
	zone     string // IANA name, already validated; not part of the per-language data
	htmlLang string
	// sev: severity of the alert in scope. Only critical / warning exist (the contract enum); other values are printed as is.
	sev string
	// firing / resolved: the headline after the severity chip, and the headline when everything has resolved.
	firing, resolved string
	// field labels; orgIn prefixes the organization when it follows the node inline
	rule, node, org, orgIn, started, ended string
	colon                                  string
	open, shut                             string // brackets around the inline organization and the zone name
}

var noticeTexts = map[api.NotifyLanguage]noticeText{
	api.NotifyLanguageZh: {
		htmlLang: "zh-Hant",
		sev:      `{{ if eq .Labels.severity "critical" }}嚴重{{ else if eq .Labels.severity "warning" }}警告{{ else }}{{ .Labels.severity }}{{ end }}`,
		firing:   `{{ if gt (len .Alerts.Firing) 1 }}{{ len .Alerts.Firing }} 則{{ end }}觸發中`,
		resolved: `[已恢復]{{ if eq (len .Alerts.Resolved) 1 }}{{ with index .Alerts.Resolved 0 }} 持續 {{ (.EndsAt.Sub .StartsAt).Seconds | ` + zhDuration + ` }}{{ end }}{{ else }} {{ len .Alerts.Resolved }} 則{{ end }}`,
		rule:     "告警規則", node: "發生節點", org: "單位", orgIn: "單位 ", started: "開始時間", ended: "恢復時間",
		colon: "：", open: "（", shut: "）",
	},
	api.NotifyLanguageEn: {
		htmlLang: "en",
		sev:      `{{ if eq .Labels.severity "critical" }}Critical{{ else if eq .Labels.severity "warning" }}Attention{{ else }}{{ .Labels.severity }}{{ end }}`,
		firing:   `Firing{{ if gt (len .Alerts.Firing) 1 }}: {{ len .Alerts.Firing }} alerts{{ end }}`,
		resolved: `[Resolved]{{ if eq (len .Alerts.Resolved) 1 }}{{ with index .Alerts.Resolved 0 }} after {{ (.EndsAt.Sub .StartsAt).Seconds | humanizeDuration }}{{ end }}{{ else }} {{ len .Alerts.Resolved }} alerts{{ end }}`,
		rule:     "Alert rule", node: "Node", org: "Organization", orgIn: "organization ", started: "Started", ended: "Resolved",
		colon: ": ", open: " (", shut: ")",
	},
}

// zhDuration: pipeline from seconds to a Chinese duration. humanizeDuration outputs "1m 0s"; this swaps in Chinese units.
// Both Alertmanager notifications and Prometheus rule summaries (defaults.go) have these two functions.
const zhDuration = `humanizeDuration | reReplaceAll "([0-9.]+)d" "${1} 天" | reReplaceAll "([0-9.]+)h" "${1} 小時" | reReplaceAll "([0-9.]+)m" "${1} 分" | reReplaceAll "([0-9.]+)s" "${1} 秒"`

// stamp: a time field in the configured zone, followed by the zone name. The zone has passed ValidZone, so it is safe inside the template.
func stamp(t noticeText, field string) string {
	return `{{ (` + field + ` | tz ` + strconv.Quote(t.zone) + `).Format "2006-01-02 15:04:05" }}` + t.open + t.zone + t.shut
}

// headline: first line of a notification (first line in Telegram, Slack title, start of the Email subject). Contains no user-entered text.
func headline(t noticeText) string {
	return `{{ if eq .Status "firing" }}{{ with index .Alerts.Firing 0 }}[` + t.sev + `]{{ end }} ` + t.firing + `{{ else }}` + t.resolved + `{{ end }}`
}

// alertBlocks: one block per alert. esc is the escaping pipeline applied to user text (empty string for plain text); b0/b1 wrap the summary in bold;
// sep goes between blocks (a divider for Telegram / plain text, a blank line for Slack). org = append the organization after the node (Enterprise only).
func alertBlocks(t noticeText, esc, b0, b1, sep string, org bool) string {
	e := escaper(esc)
	orgPart := ""
	if org {
		orgPart = "{{ with $a.Labels.org }}" + t.open + t.orgIn + e(".") + t.shut + "{{ end }}"
	}
	return "{{ range $i, $a := .Alerts }}{{ if $i }}" + sep + "{{ end }}" +
		b0 + "{{ with .Annotations.summary }}" + e(".") + "{{ else }}" + e(".Labels.alertname") + "{{ end }}" + b1 + "\n" +
		`{{ if eq .Status "firing" }}{{ with .Annotations.description }}` + e(".") + "\n\n{{ end }}{{ end }}" +
		t.rule + t.colon + ruleName(e) + "\n" +
		"{{ with .Labels.node }}" + t.node + t.colon + e(".") + orgPart + "\n{{ end }}" +
		t.started + t.colon + stamp(t, ".StartsAt") + "\n" +
		`{{ if eq .Status "resolved" }}` + t.ended + t.colon + stamp(t, ".EndsAt") + "\n{{ end }}{{ end }}"
}

// escaper: wraps template field x as {{ x }}, adding the escaping pipeline when esc is non-empty.
func escaper(esc string) func(string) string {
	return func(x string) string {
		if esc == "" {
			return "{{ " + x + " }}"
		}
		return "{{ " + x + " | " + esc + " }}"
	}
}

// ruleName: the alert rule field. Shows the user-visible rule name (annotation rule_name, see rules.go);
// falls back to alertname when absent. alertname is a stable identifier (custom rules use custom_rule_<uuid>) and is only a fallback.
func ruleName(e func(string) string) string {
	return "{{ with .Annotations.rule_name }}" + e(".") + "{{ else }}" + e(".Labels.alertname") + "{{ end }}"
}

// rule: divider for Telegram and plain-text mail (below the status line and between alerts).
const rule = "══════════════\n"

// textEsc: both Telegram HTML and Slack mrkdwn need & < > escaped (Slack treats <...> as links; Telegram HTML rejects the whole message on an unbalanced tag).
const textEsc = `reReplaceAll "&" "&amp;" | reReplaceAll "<" "&lt;" | reReplaceAll ">" "&gt;"`

// emailText / emailSubject: plain-text version and subject of the Email. With a single alert the subject includes the summary.
func emailText(t noticeText, org bool) string {
	return headline(t) + "\n" + rule + alertBlocks(t, "", "", "", rule, org)
}

func emailSubject(t noticeText) string {
	return headline(t) + `{{ if eq (len .Alerts) 1 }}{{ with index .Alerts 0 }}` + t.colon + `{{ with .Annotations.summary }}{{ . }}{{ else }}{{ .Labels.alertname }}{{ end }}{{ end }}{{ end }}`
}

// telegramMessage: HTML format (summary in bold), all user text escaped.
func telegramMessage(t noticeText, org bool) string {
	return headline(t) + "\n" + rule + alertBlocks(t, textEsc, "<b>", "</b>", rule, org)
}

// slackText: the Slack body (the title is the headline, which has no user text); user text is all escaped, and the summary is mrkdwn bold.
func slackText(t noticeText, org bool) string {
	return alertBlocks(t, textEsc, "*", "*", "\n", org)
}

// emailHTML: HTML template for alert mail. Same content as emailText; the client picks which to show.
//
// Mail HTML follows different rules than the web, so this is deliberately constrained:
//   - table layout, no flex/grid: Outlook's Word rendering engine does not support them.
//   - all styles inline: many clients (including Gmail web) strip <style> blocks.
//   - no web fonts: they cannot load in mail, so always use a system font stack. Brand fonts are UI-only.
//   - no images at all (including the logo): external images are blocked by default and also act as tracking pixels;
//     embedded CID attachments would require changing Alertmanager's send path, which is not worth it.
//   - no links at all: this layer does not know the platform's external address, so any link would be dead.
//
// Inserting alert content (summary/description/labels) directly into HTML is safe: Alertmanager renders this field with html/template,
// which escapes automatically (verified by testing). Worth remembering: custom alert rule text is user-entered.
//
// Colors are taken from frontend/src/theme/brand.ts and synced by hand: that file is TS, which Go cannot read.
// Change both sides when the brand colors change.
func emailHTML(t noticeText, org bool) string {
	row := func(label, value string) string {
		return `<tr><td style="padding:2px 14px 2px 0;color:#6C727C;">` + label + `</td><td style="padding:2px 0;color:#14161A;font-family:'SFMono-Regular',Consolas,monospace;">` + value + `</td></tr>`
	}
	// The organization row is printed only in Enterprise; Core prints nothing there.
	orgRow := ""
	if org {
		orgRow = `{{ with .Labels.org }}` + row(t.org, "{{ . }}") + `{{ end }}`
	}
	return `<!DOCTYPE html>
<html lang="` + t.htmlLang + `">
<head><meta charset="utf-8"/></head>
<body style="margin:0;padding:0;background:#FAFBFC;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#FAFBFC;padding:24px 12px;">
<tr><td align="center">
<table role="presentation" cellpadding="0" cellspacing="0" border="0" width="600" style="width:100%;max-width:600px;background:#FFFFFF;border:1px solid #E6E8EB;border-radius:8px;">
<tr><td style="padding:14px 20px;border-bottom:1px solid #E6E8EB;font-family:-apple-system,'Segoe UI','Noto Sans TC','Microsoft JhengHei',sans-serif;font-size:14px;font-weight:600;color:#14161A;">
{{ if eq .Status "firing" }}<span style="color:#B3322F;">●</span>{{ else }}<span style="color:#2E7D43;">●</span>{{ end }} ` + headline(t) + `

</td></tr>
{{ range .Alerts }}
{{ $fg := "#5B6068" }}{{ $bg := "#EFF1F3" }}
{{ if eq .Labels.severity "critical" }}{{ $fg = "#B3322F" }}{{ $bg = "#FBE9E9" }}{{ else if eq .Labels.severity "warning" }}{{ $fg = "#8F5F00" }}{{ $bg = "#FBF3DB" }}{{ end }}
<tr><td style="padding:18px 20px;border-bottom:1px solid #E6E8EB;font-family:-apple-system,'Segoe UI','Noto Sans TC','Microsoft JhengHei',sans-serif;">
<div style="font-size:15px;font-weight:600;color:#14161A;padding-bottom:6px;">
{{ with .Annotations.summary }}{{ . }}{{ else }}{{ .Labels.alertname }}{{ end }}
<span style="display:inline-block;margin-left:6px;padding:1px 7px;border-radius:999px;font-size:11px;font-weight:600;color:{{ $fg }};background:{{ $bg }};">` + t.sev + `</span>
</div>
{{ if eq .Status "firing" }}{{ with .Annotations.description }}<div style="font-size:13px;color:#6C727C;line-height:1.5;">{{ . }}</div>{{ end }}{{ end }}
<table role="presentation" cellpadding="0" cellspacing="0" border="0" style="padding-top:10px;font-size:12px;">
` + row(t.rule, ruleName(escaper(""))) + `
{{ with .Labels.node }}` + row(t.node, "{{ . }}") + `{{ end }}
` + orgRow + `
` + row(t.started, stamp(t, ".StartsAt")) + `{{ if eq .Status "resolved" }}` + row(t.ended, stamp(t, ".EndsAt")) + `{{ end }}
</table>
</td></tr>
{{ end }}
</table>
</td></tr>
</table>
</body>
</html>
`
}

// inhibitRules: suppresses the flood of false NodeOffline alerts when the platform is down (all scraping goes through the platform, so every node's up is 0 when it is down).
// It must be rendered here: on the compose path alertmanager.yml is produced by this function, so putting it in a static file would do nothing.
var inhibitRules = []amInhibitRule{{
	SourceMatchers: []string{`alertname="PlatformDown"`},
	TargetMatchers: []string{`alertname="NodeOffline"`},
}}

// render: a single receiver "platform" takes all rules (rules are not split by route):
// the platform webhook is always present (ingests into the alerts table); SMTP / Telegram / Slack / custom webhooks are layered on per settings.
// customCA: SMTP_CA_FILE is set and copied to smtpCAPath.
func render(s Settings, org, customCA bool) ([]byte, error) {
	rcv := amReceiver{
		Name: "platform",
		WebhookConfigs: []amWebhookCfg{{
			URL:          platformWebhookURL,
			SendResolved: true,
			HTTPConfig:   &amHTTPConfig{Authorization: &amAuthorization{CredentialsFile: sdTokenPath}},
		}},
	}
	for _, w := range s.WebhookURLs {
		rcv.WebhookConfigs = append(rcv.WebhookConfigs, amWebhookCfg{URL: w.URL, SendResolved: true})
	}

	// group_interval also decides how fast resolved notifications reach the platform; the platform's "進行中" (active) list depends on this webhook landing,
	// so lengthening it leaves resolved alerts on screen for several extra minutes.
	cfg := amConfig{
		InhibitRules: inhibitRules,
		Route: amRoute{
			Receiver: "platform", GroupBy: []string{"alertname", "node"},
			GroupWait: "30s", GroupInterval: "30s", RepeatInterval: "4h",
		},
	}
	// One language and zone for every receiver for now; each config below takes a noticeText, so a per-channel
	// language only needs to pick a different one here.
	nt := noticeTexts[languageOf(string(s.Language))]
	nt.zone = s.Zone()
	if s.SMTP != nil {
		cfg.Global = &amGlobal{
			SMTPFrom:         s.SMTP.From,
			SMTPSmarthost:    fmt.Sprintf("%s:%d", s.SMTP.Host, s.SMTP.Port),
			SMTPAuthUsername: s.SMTP.Username,
			SMTPAuthPassword: s.SMTP.Password,
		}
		starttls, implicit := s.SMTP.Security == SecurityStartTLS, s.SMTP.Security == SecurityTLS
		var tlsCfg *amTLSConfig
		if customCA && (starttls || implicit) {
			tlsCfg = &amTLSConfig{CAFile: smtpCAPath}
		}
		for _, to := range s.SMTP.To {
			rcv.EmailConfigs = append(rcv.EmailConfigs, amEmailCfg{To: to, RequireTLS: starttls, ForceImplicitTLS: implicit, SendResolved: true, TLSConfig: tlsCfg, HTML: emailHTML(nt, org), Text: emailText(nt, org), Headers: map[string]string{"Subject": emailSubject(nt)}})
		}
	}
	if t := s.Telegram; t != nil {
		rcv.TelegramConfigs = []amTelegramCfg{{
			BotToken: t.BotToken, ChatID: t.ChatID, MessageThreadID: t.MessageThreadID,
			ParseMode: "HTML", Message: telegramMessage(nt, org), SendResolved: true,
		}}
	}
	if s.Slack != nil {
		rcv.SlackConfigs = []amSlackCfg{{APIURL: s.Slack.WebhookURL, Title: headline(nt), TitleLink: "", Fallback: headline(nt), Text: slackText(nt, org), SendResolved: true}}
	}
	cfg.Receivers = []amReceiver{rcv}

	return yaml.Marshal(cfg)
}
