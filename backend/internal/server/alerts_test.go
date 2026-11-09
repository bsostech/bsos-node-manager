package server

import (
	"context"
	"testing"

	"besu-manager/backend/internal/alerting"
	"besu-manager/backend/internal/api"
)

// Every notification-settings write answers with the full settings after the change, which the
// page uses to redraw the form; the SMTP username comes back and the Slack audit records that the
// channel was on before and after.
func TestAlertSettingsWriteResponses(t *testing.T) {
	s, _ := accessTestServer(t)
	s.alertSvc = alerting.New(s.store, []byte("0123456789abcdef0123456789abcdef"), "", -1, "")
	audited := recordAudit(t)
	ctx := asAdmin(context.Background())
	user, pw := "mailer", "hunter2-smtp"
	r, err := s.PutAlertSettingsSmtp(ctx, api.PutAlertSettingsSmtpRequestObject{Body: &api.PutAlertSettingsSmtpJSONRequestBody{
		Host: "smtp.x", Port: 587, From: "a@x", To: []string{"b@x"}, Username: &user, Password: &pw,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := r.(api.PutAlertSettingsSmtp200JSONResponse); !ok || v.Smtp == nil || v.Smtp.Host != "smtp.x" || v.Smtp.Username == nil || *v.Smtp.Username != user {
		t.Fatalf("smtp put: %#v", r)
	}
	wr, err := s.PutAlertSettingsWebhooks(ctx, api.PutAlertSettingsWebhooksRequestObject{Body: &api.PutAlertSettingsWebhooksJSONRequestBody{
		{Label: "ops", Url: "https://hooks.example/x"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := wr.(api.PutAlertSettingsWebhooks200JSONResponse); !ok || len(v.WebhookUrls) != 1 || v.Smtp == nil {
		t.Fatalf("webhooks put: %#v", wr)
	}
	tok := "123:TGSECRET"
	if _, err := s.PutAlertSettingsTelegram(ctx, api.PutAlertSettingsTelegramRequestObject{Body: &api.TelegramSettings{BotToken: &tok, ChatId: -100}}); err != nil {
		t.Fatal(err)
	}
	hook := "https://hooks.slack.com/services/T0/B0/SLACKSECRET"
	if _, err := s.PutAlertSettingsSlack(ctx, api.PutAlertSettingsSlackRequestObject{Body: &api.SlackSettings{WebhookUrl: &hook}}); err != nil {
		t.Fatal(err)
	}

	dr, err := s.DeleteAlertSettingsSmtp(ctx, api.DeleteAlertSettingsSmtpRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := dr.(api.DeleteAlertSettingsSmtp200JSONResponse); !ok || v.Smtp != nil || len(v.WebhookUrls) != 1 {
		t.Fatalf("smtp delete: %#v", dr)
	}
	tr, err := s.DeleteAlertSettingsTelegram(ctx, api.DeleteAlertSettingsTelegramRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := tr.(api.DeleteAlertSettingsTelegram200JSONResponse); !ok || v.Telegram != nil || v.Slack == nil {
		t.Fatalf("telegram delete: %#v", tr)
	}
	sr, err := s.DeleteAlertSettingsSlack(ctx, api.DeleteAlertSettingsSlackRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := sr.(api.DeleteAlertSettingsSlack200JSONResponse); !ok || v.Slack != nil || len(v.WebhookUrls) != 1 {
		t.Fatalf("slack delete: %#v", sr)
	}
	got, err := s.GetAlertSettings(ctx, api.GetAlertSettingsRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if v := got.(api.GetAlertSettings200JSONResponse); v.Smtp != nil {
		t.Fatalf("smtp still stored after delete: %+v", v)
	}

	set, del := audited("alert_slack_set"), audited("alert_slack_deleted")
	if len(set) != 1 || set[0]["after"].(map[string]any)["enabled"] != true {
		t.Fatalf("slack set audit: %v", set)
	}
	if len(del) != 1 || del[0]["before"].(map[string]any)["enabled"] != true {
		t.Fatalf("slack delete audit: %v", del)
	}
}

// An intranet relay takes no login: with no username the settings save without a password, and a
// password sent anyway is dropped rather than stored.
func TestAlertSettingsSmtpWithoutLogin(t *testing.T) {
	s, _ := accessTestServer(t)
	s.alertSvc = alerting.New(s.store, []byte("0123456789abcdef0123456789abcdef"), "", -1, "")
	ctx := asAdmin(context.Background())
	empty, stray := "", "stray"
	for _, body := range []api.SmtpSettings{
		{Host: "relay.x", Port: 25, From: "a@x", To: []string{"b@x"}},
		{Host: "relay.x", Port: 25, From: "a@x", To: []string{"b@x"}, Username: &empty, Password: &empty},
		{Host: "relay.x", Port: 25, From: "a@x", To: []string{"b@x"}, Password: &stray},
	} {
		r, err := s.PutAlertSettingsSmtp(ctx, api.PutAlertSettingsSmtpRequestObject{Body: &body})
		if err != nil {
			t.Fatal(err)
		}
		if v, ok := r.(api.PutAlertSettingsSmtp200JSONResponse); !ok || v.Smtp == nil || v.Smtp.Host != "relay.x" || v.Smtp.Username != nil {
			t.Fatalf("smtp put without username: %#v", r)
		}
		if c := smtpFromAPI(body); c.Password != "" {
			t.Fatalf("smtp to store = %+v, want no password", c)
		}
	}
}

// Notification language and zone: GET shows the fresh-install defaults (English, following the
// host's zone), PUT stores and returns them, an empty zone goes back to following the host, bad
// values are refused with the contract's field reasons, and every change is audited with before and
// after.
func TestAlertSettingsLocale(t *testing.T) {
	s, _ := accessTestServer(t)
	s.alertSvc = alerting.New(s.store, []byte("0123456789abcdef0123456789abcdef"), "", -1, "")
	audited := recordAudit(t)
	ctx := asAdmin(context.Background())

	got, err := s.GetAlertSettings(ctx, api.GetAlertSettingsRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if v := got.(api.GetAlertSettings200JSONResponse); v.Locale.Language != api.NotifyLanguageEn || v.Locale.Timezone != "" ||
		v.Locale.EffectiveTimezone == "" || v.Locale.EffectiveTimezone == "Local" {
		t.Fatalf("fresh install locale: %+v", v.Locale)
	}
	hostZone := got.(api.GetAlertSettings200JSONResponse).Locale.EffectiveTimezone

	put := func(lang api.NotifyLanguage, zone string) api.PutAlertSettingsLocaleResponseObject {
		t.Helper()
		r, err := s.PutAlertSettingsLocale(ctx, api.PutAlertSettingsLocaleRequestObject{Body: &api.AlertLocale{Language: lang, Timezone: zone}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if v, ok := put(api.NotifyLanguageZh, " Asia/Taipei ").(api.PutAlertSettingsLocale200JSONResponse); !ok || v.Locale.Language != api.NotifyLanguageZh || v.Locale.Timezone != "Asia/Taipei" || v.Locale.EffectiveTimezone != "Asia/Taipei" {
		t.Fatalf("put: %#v", v)
	}
	for _, c := range []struct {
		lang         api.NotifyLanguage
		zone, reason string
		field        string
	}{
		{"fr", "UTC", string(api.ErrorReasonFieldNotAllowed), "language"},
		{api.NotifyLanguageEn, "Mars/Olympus", string(api.ErrorReasonFieldFormat), "timezone"},
		{api.NotifyLanguageEn, "Local", string(api.ErrorReasonFieldFormat), "timezone"},
		{api.NotifyLanguageEn, "../../etc/passwd", string(api.ErrorReasonFieldFormat), "timezone"},
	} {
		e, ok := put(c.lang, c.zone).(api.PutAlertSettingsLocale400JSONResponse)
		if !ok || e.Reason == nil || string(*e.Reason) != c.reason || e.Params == nil || (*e.Params)["field"] != c.field {
			t.Errorf("%q %q: %#v", c.lang, c.zone, e)
		}
	}
	if v := must(s.GetAlertSettings(ctx, api.GetAlertSettingsRequestObject{})).(api.GetAlertSettings200JSONResponse); v.Locale.Language != api.NotifyLanguageZh || v.Locale.Timezone != "Asia/Taipei" {
		t.Fatalf("rejected puts must not change the stored locale: %+v", v.Locale)
	}
	// back to following the host: an empty zone is stored as such, and the effective zone is the host's again
	if v, ok := put(api.NotifyLanguageZh, "").(api.PutAlertSettingsLocale200JSONResponse); !ok || v.Locale.Timezone != "" || v.Locale.EffectiveTimezone != hostZone {
		t.Fatalf("put empty zone: %#v", v)
	}

	entries := audited("alert_locale_set")
	if len(entries) != 2 {
		t.Fatalf("want two audit entries (rejected values are not audited), got %v", entries)
	}
	before, after := entries[0]["before"].(map[string]any), entries[0]["after"].(map[string]any)
	if before["language"] != "en" || before["timezone"] != "" || after["language"] != "zh" || after["timezone"] != "Asia/Taipei" {
		t.Fatalf("audit: %v", entries[0])
	}
	if back := entries[1]["after"].(map[string]any); back["timezone"] != "" {
		t.Fatalf("audit of follow-host: %v", entries[1])
	}
}

// The password sent with the SMTP settings is what gets stored (reads never return it, so this is
// checked on the conversion itself).
func TestSmtpFromAPI(t *testing.T) {
	user, pw := "mailer", "hunter2-smtp"
	got := smtpFromAPI(api.SmtpSettings{Host: "smtp.x", Port: 587, Username: &user, Password: &pw})
	if got.Password != pw || got.Username != user || got.Host != "smtp.x" {
		t.Fatalf("%+v", got)
	}
	// security left out: port 465 means implicit TLS, any other port plain text; an explicit value is kept
	starttls := api.SmtpSecurityStarttls
	for _, tc := range []struct {
		port int
		in   *api.SmtpSecurity
		want alerting.Security
	}{
		{465, nil, alerting.SecurityTLS},
		{587, nil, alerting.SecurityNone},
		{25, &starttls, alerting.SecurityStartTLS},
	} {
		if got := smtpFromAPI(api.SmtpSettings{Host: "smtp.x", Port: tc.port, Security: tc.in}); got.Security != tc.want {
			t.Errorf("port %d security %v: got %q, want %q", tc.port, tc.in, got.Security, tc.want)
		}
	}
}

// From and every to must be a bare address: the connection test cannot catch a bad one, Alertmanager
// only fails when it sends.
func TestSmtpAddressError(t *testing.T) {
	for _, tc := range []struct {
		from  string
		to    []string
		field string
	}{
		{"alerts@org-a.example", []string{"ops@org-a.example", "b@x"}, ""},
		{"not-an-email", []string{"ops@org-a.example"}, "from"},
		{"", []string{"ops@org-a.example"}, "from"},
		{"Alerts <alerts@org-a.example>", []string{"ops@org-a.example"}, "from"},
		{" alerts@org-a.example", []string{"ops@org-a.example"}, "from"},
		{"alerts@org-a.example", []string{"ops@org-a.example", "bad"}, "to.1"},
	} {
		e := smtpAddressError(api.SmtpSettings{Host: "smtp.x", Port: 25, From: tc.from, To: tc.to})
		if tc.field == "" {
			if e != nil {
				t.Errorf("%q %v: unexpected %+v", tc.from, tc.to, e)
			}
			continue
		}
		if e == nil || e.Reason == nil || *e.Reason != api.ErrorReasonInvalidEmail {
			t.Errorf("%q %v: got %+v, want invalid_email", tc.from, tc.to, e)
			continue
		}
		if e.Params == nil || (*e.Params)["field"] != tc.field {
			t.Errorf("%q %v: params %v, want field %s", tc.from, tc.to, e.Params, tc.field)
		}
	}
}
