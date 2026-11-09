package alerting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
)

// Fake Telegram: getMe passes only for token "good"; chat 1 can send, 2 does not exist, 3 bot not in group, 4 thread does not exist, 5 rate-limited.
func TestTestTelegram(t *testing.T) {
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/botgood/") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			return
		}
		var m struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&m)
		sent = m.Text
		switch m.ChatID {
		case 2:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
		case 3:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"ok":false,"description":"Forbidden: bot is not a member of the group chat"}`))
		case 4:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: message thread not found"}`))
		case 5:
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"ok":false,"description":"Too Many Requests: retry after 5"}`))
		}
	}))
	defer srv.Close()
	saved := telegramAPI
	t.Cleanup(func() { telegramAPI = saved })
	telegramAPI = srv.URL

	ctx := context.Background()
	// the test message goes out in the notification language
	for lang, want := range map[api.NotifyLanguage]string{api.NotifyLanguageEn: "BSOS Node Manager test message", api.NotifyLanguageZh: "BSOS 節點治理工具測試訊息"} {
		if err := TestTelegram(ctx, Telegram{BotToken: "good", ChatID: 1}, lang); err != nil || !strings.HasPrefix(sent, want) {
			t.Errorf("ok case %s: err %v, sent %q", lang, err, sent)
		}
	}
	// The Chinese sentences stay as is (for the server log) and the reason is for the UI to translate; required fields are checked by the handler first, so there is no reason here
	for _, c := range []struct {
		tg     Telegram
		want   string
		reason api.ErrorReason
	}{
		{Telegram{BotToken: "bad", ChatID: 1}, "bot token is invalid", api.ErrorReasonTelegramTokenInvalid},
		{Telegram{BotToken: "good", ChatID: 2}, "chat id not found", api.ErrorReasonTelegramChatNotFound},
		{Telegram{BotToken: "good", ChatID: 3}, "added to the group", api.ErrorReasonTelegramForbidden},
		{Telegram{BotToken: "good", ChatID: 4}, "topic id not found", api.ErrorReasonTelegramThreadNotFound},
		{Telegram{BotToken: "good", ChatID: 5}, "Telegram responded 429", api.ErrorReasonTelegramHttpStatus},
		{Telegram{BotToken: "good"}, "required", ""},
	} {
		err := TestTelegram(ctx, c.tg, api.NotifyLanguageEn)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: got %v, want %q", c.tg, err, c.want)
		}
		if r, _, _ := apierr.ReasonOf(err); r != c.reason {
			t.Errorf("%+v: reason %q, want %q", c.tg, r, c.reason)
		}
	}

	// when unreachable, the error message must not leak the bot token (it is in the URL)
	telegramAPI = "http://127.0.0.1:1"
	err := TestTelegram(ctx, Telegram{BotToken: "123:SECRET", ChatID: 1}, api.NotifyLanguageEn)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("unreachable: %v", err)
	}
	if r, p, _ := apierr.ReasonOf(err); r != api.ErrorReasonTelegramUnreachable || strings.Contains(p["detail"], "SECRET") || p["url"] != telegramAPI {
		t.Errorf("unreachable: reason %q params %v", r, p)
	}
}

func TestTestSlack(t *testing.T) {
	ctx := context.Background()
	if err := TestSlack(ctx, "http://hooks.slack.com/services/x", api.NotifyLanguageEn); err == nil || !strings.Contains(err.Error(), "https") {
		t.Errorf("http must be rejected: %v", err)
	}
	// only Slack webhook hosts are accepted: internal, loopback, credentials or custom ports are all rejected before sending
	for _, u := range []string{
		"https://127.0.0.1/services/SECRET",
		"https://10.0.0.5/services/x",
		"https://hooks.slack.com.evil.example/services/x",
		"https://user:pw@hooks.slack.com/services/x",
		"https://hooks.slack.com:8443/services/x",
	} {
		if err := TestSlack(ctx, u, api.NotifyLanguageEn); err == nil || !strings.Contains(err.Error(), "hooks.slack.com") || strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%s: %v", u, err)
		}
	}
	if err := ValidateSlackURL("https://HOOKS.slack.com/services/T0/B0/x"); err != nil {
		t.Errorf("valid url rejected: %v", err)
	}
}
