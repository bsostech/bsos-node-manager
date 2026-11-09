package server

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"besu-manager/backend/internal/alerting"
	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
)

// Validation and test-connection failures for notification channel settings return a reason. All of
// these return before settings are read, so no database is needed.
func TestAlertChannelErrorReasons(t *testing.T) {
	s := &Server{alertSvc: &alerting.Service{}} // the SMTP test reads its CA; no CA file here
	ctx := context.Background()
	str := func(v string) *string { return &v }
	expect := func(name string, e api.Error, want api.ErrorReason, k, v string) {
		t.Helper()
		if !hasReason(e, want) {
			t.Fatalf("%s 要 %s,得 %#v", name, want, e)
		}
		if k != "" && (e.Params == nil || (*e.Params)[k] != v) {
			t.Fatalf("%s params[%s] 要 %q,得 %#v", name, k, v, e.Params)
		}
	}
	errOf := func(r any, err error) api.Error {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		switch v := r.(type) {
		case api.PutAlertSettingsSmtp400JSONResponse:
			return api.Error(v)
		case api.PutAlertSettingsWebhooks400JSONResponse:
			return api.Error(v)
		case api.PutAlertSettingsTelegram400JSONResponse:
			return api.Error(v)
		case api.PutAlertSettingsSlack400JSONResponse:
			return api.Error(v)
		case api.TestAlertSettingsSmtp400JSONResponse:
			return api.Error(v)
		case api.TestAlertSettingsWebhook400JSONResponse:
			return api.Error(v)
		case api.TestAlertSettingsTelegram400JSONResponse:
			return api.Error(v)
		case api.TestAlertSettingsSlack400JSONResponse:
			return api.Error(v)
		}
		t.Fatalf("不是 400: %#v", r)
		return api.Error{}
	}

	expect("SMTP 有帳號沒密碼", errOf(s.PutAlertSettingsSmtp(ctx, api.PutAlertSettingsSmtpRequestObject{Body: &api.SmtpSettings{Host: "mail.example.com", Port: 25, Username: str("u")}})), api.ErrorReasonSmtpPasswordRequired, "", "")
	expect("SMTP 有帳號空密碼", errOf(s.PutAlertSettingsSmtp(ctx, api.PutAlertSettingsSmtpRequestObject{Body: &api.SmtpSettings{Host: "mail.example.com", Port: 25, Username: str("u"), Password: str("")}})), api.ErrorReasonSmtpPasswordRequired, "", "")
	hooks := api.PutAlertSettingsWebhooksJSONRequestBody{{Label: "a", Url: "https://a.example.com"}, {Label: " ", Url: "https://b.example.com"}}
	expect("webhook 缺名稱", errOf(s.PutAlertSettingsWebhooks(ctx, api.PutAlertSettingsWebhooksRequestObject{Body: &hooks})), api.ErrorReasonWebhookNameRequired, "index", "2")
	hooks = api.PutAlertSettingsWebhooksJSONRequestBody{{Label: "a", Url: "ftp://a.example.com"}}
	expect("webhook 網址", errOf(s.PutAlertSettingsWebhooks(ctx, api.PutAlertSettingsWebhooksRequestObject{Body: &hooks})), api.ErrorReasonWebhookUrlInvalid, "index", "1")
	expect("Telegram 缺 chat id", errOf(s.PutAlertSettingsTelegram(ctx, api.PutAlertSettingsTelegramRequestObject{Body: &api.TelegramSettings{BotToken: str("x")}})), api.ErrorReasonTelegramFieldsRequired, "", "")
	zero := int64(0)
	expect("Telegram 子話題", errOf(s.PutAlertSettingsTelegram(ctx, api.PutAlertSettingsTelegramRequestObject{Body: &api.TelegramSettings{BotToken: str("x"), ChatId: -1, MessageThreadId: &zero}})), api.ErrorReasonTelegramThreadIdInvalid, "", "")
	expect("Slack 沒網址", errOf(s.PutAlertSettingsSlack(ctx, api.PutAlertSettingsSlackRequestObject{Body: &api.SlackSettings{}})), api.ErrorReasonSlackUrlRequired, "", "")
	expect("Slack 網址", errOf(s.PutAlertSettingsSlack(ctx, api.PutAlertSettingsSlackRequestObject{Body: &api.SlackSettings{WebhookUrl: str("https://example.com/x")}})), api.ErrorReasonSlackUrlInvalid, "", "")

	expect("測試 Telegram 缺欄位", errOf(s.TestAlertSettingsTelegram(ctx, api.TestAlertSettingsTelegramRequestObject{Body: &api.TelegramSettings{}})), api.ErrorReasonTelegramFieldsRequired, "", "")
	// missing just one of the fields must also be caught here, not fall through to
	// alerting.TestTelegram and become telegram_test_failed
	expect("測試 Telegram 缺 token", errOf(s.TestAlertSettingsTelegram(ctx, api.TestAlertSettingsTelegramRequestObject{Body: &api.TelegramSettings{ChatId: -100}})), api.ErrorReasonTelegramFieldsRequired, "", "")
	expect("測試 Telegram 缺 chat id", errOf(s.TestAlertSettingsTelegram(ctx, api.TestAlertSettingsTelegramRequestObject{Body: &api.TelegramSettings{BotToken: str("123:x")}})), api.ErrorReasonTelegramFieldsRequired, "", "")
	expect("測試 Slack 網址", errOf(s.TestAlertSettingsSlack(ctx, api.TestAlertSettingsSlackRequestObject{Body: &api.SlackSettings{WebhookUrl: str("https://example.com/x")}})), api.ErrorReasonSlackUrlInvalid, "", "")
	e := errOf(s.TestAlertSettingsWebhook(ctx, api.TestAlertSettingsWebhookRequestObject{Body: &api.AlertWebhookUrl{Label: "a", Url: "ftp://x"}}))
	expect("測試 webhook", e, api.ErrorReasonWebhookTestUrlInvalid, "", "")
	// local addresses are blocked by SSRF protection, no real connection needed
	e = errOf(s.TestAlertSettingsSmtp(ctx, api.TestAlertSettingsSmtpRequestObject{Body: &api.SmtpSettings{Host: "127.0.0.1", Port: 25, From: "a@x", To: []string{"b@x"}}}))
	expect("測試 SMTP", e, api.ErrorReasonConnAddressBlocked, "address", "127.0.0.1")
	// unrecognized underlying causes (e.g. dial refused) stay xxx_test_failed + raw text; when
	// Slack is unreachable because the dial check blocked it, the outer Wrap does not mask the
	// inner one and the dial check's reason is returned
	expect("認不得的原因", *testFailed(api.ErrorReasonSlackTestFailed, errors.New("dial tcp 10.0.0.1:443: connect: connection refused")), api.ErrorReasonSlackTestFailed, "detail", "dial tcp 10.0.0.1:443: connect: connection refused")
	inner := apierr.Wrap(api.ErrorReasonConnResolveFailed, map[string]string{"host": "hooks.slack.com", "detail": "no such host"}, errors.New("解析主機 hooks.slack.com 失敗:no such host"))
	slack := apierr.Wrap(api.ErrorReasonSlackUnreachable, map[string]string{"detail": inner.Error()}, fmt.Errorf("連不到 Slack:%w", inner))
	expect("Slack 連不到", *testFailed(api.ErrorReasonSlackTestFailed, slack), api.ErrorReasonConnResolveFailed, "host", "hooks.slack.com")
}
