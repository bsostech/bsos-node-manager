package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"besu-manager/backend/internal/alerting"
	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/store"
)

// postAlertWebhook sends body to the Alertmanager webhook endpoint the way Alertmanager does
// (bearer = SD_TOKEN) and returns the status code.
func postAlertWebhook(t *testing.T, h http.HandlerFunc, auth, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/alerts/webhook", strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec.Code
}

func alertsByFingerprint(t *testing.T, st *store.Store) map[string]store.Alert {
	t.Helper()
	list, err := st.ListAlerts(context.Background(), store.AlertFilter{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.Alert{}
	for _, a := range list {
		out[a.Fingerprint] = a
	}
	return out
}

// Ingest: node comes from the node label of measured rules, falling back to node_id for platform
// event rules; an empty label counts as missing; org/scope/severity come from labels; labels and
// annotations are kept whole. A later "resolved" for the same firing updates that row with
// endsAt, and resolved rows past retention are pruned on the next ingest.
func TestAlertWebhookIngest(t *testing.T) {
	s, _ := accessTestServer(t)
	h := AlertWebhookHandler(s.store, "sd-secret")
	start := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	old := time.Now().Add(-alertRetention - 24*time.Hour).UTC().Truncate(time.Second)
	if err := s.store.UpsertAlert(context.Background(), store.Alert{Fingerprint: "fp-old", Alertname: "NodeOffline", Scope: "hw", Severity: "critical",
		Status: "resolved", Labels: map[string]string{}, Annotations: map[string]string{}, StartsAt: old.Add(-time.Hour), EndsAt: &old}); err != nil {
		t.Fatal(err)
	}

	body := `{"version":"4","status":"firing","receiver":"platform","alerts":[
		{"status":"firing","fingerprint":"fp-node","startsAt":"` + start.Format(time.RFC3339) + `","endsAt":"0001-01-01T00:00:00Z",
		 "labels":{"alertname":"NodeCpuHigh","node":"node-a1","node_id":"ignored","org":"org-a","scope":"hw","severity":"warning","monitor":"x"},
		 "annotations":{"summary":"CPU 91.5%"}},
		{"status":"firing","fingerprint":"fp-cert","startsAt":"` + start.Format(time.RFC3339) + `",
		 "labels":{"alertname":"CertExpiring","node_id":"node-a2","scope":"security","severity":"critical"},"annotations":{}},
		{"status":"firing","fingerprint":"fp-vote","startsAt":"` + start.Format(time.RFC3339) + `",
		 "labels":{"alertname":"VotePending","node":"","org":"","scope":"consensus","severity":"warning"},"annotations":{}}]}`
	if code := postAlertWebhook(t, h, "Bearer sd-secret", body); code != http.StatusNoContent {
		t.Fatalf("攝入應 204,得 %d", code)
	}
	got := alertsByFingerprint(t, s.store)
	n := got["fp-node"]
	if n.NodeID == nil || *n.NodeID != "node-a1" || n.Org == nil || *n.Org != "org-a" || n.Alertname != "NodeCpuHigh" ||
		n.Scope != "hw" || n.Severity != "warning" || n.Status != "firing" || n.EndsAt != nil || !n.StartsAt.Equal(start) {
		t.Fatalf("量測型告警欄位不對: %+v", n)
	}
	if n.Labels["monitor"] != "x" || n.Annotations["summary"] != "CPU 91.5%" {
		t.Fatalf("labels/annotations 要整份留著: %+v %+v", n.Labels, n.Annotations)
	}
	if c := got["fp-cert"]; c.NodeID == nil || *c.NodeID != "node-a2" || c.Org != nil {
		t.Fatalf("沒有 node label 應退而讀 node_id: %+v", c)
	}
	if v := got["fp-vote"]; v.NodeID != nil || v.Org != nil {
		t.Fatalf("空字串 label 應當沒有: node=%v org=%v", v.NodeID, v.Org)
	}
	if _, ok := got["fp-old"]; ok {
		t.Fatal("超過保留期的已恢復告警應在攝入時清掉")
	}

	end := start.Add(30 * time.Minute)
	resolved := `{"alerts":[{"status":"resolved","fingerprint":"fp-node","startsAt":"` + start.Format(time.RFC3339) + `","endsAt":"` + end.Format(time.RFC3339) + `",
		"labels":{"alertname":"NodeCpuHigh","node":"node-a1","org":"org-a","scope":"hw","severity":"warning"},"annotations":{}}]}`
	if code := postAlertWebhook(t, h, "Bearer sd-secret", resolved); code != http.StatusNoContent {
		t.Fatalf("恢復應 204,得 %d", code)
	}
	got = alertsByFingerprint(t, s.store)
	if n := got["fp-node"]; n.Status != "resolved" || n.EndsAt == nil || !n.EndsAt.Equal(end) || len(got) != 3 {
		t.Fatalf("恢復要更新同一筆並帶 endsAt: %+v (共 %d 筆)", n, len(got))
	}
}

// The endpoint is only for our own Alertmanager: disabled (404) without SD_TOKEN, 401 for a missing
// or wrong bearer, 400 for a body that is not JSON; none of these write anything.
func TestAlertWebhookRejects(t *testing.T) {
	s, _ := accessTestServer(t)
	body := `{"alerts":[{"status":"firing","fingerprint":"fp-x","startsAt":"2026-11-01T00:00:00Z","labels":{"alertname":"NodeOffline"},"annotations":{}}]}`
	for _, c := range []struct {
		name, token, auth, body string
		want                    int
	}{
		{"沒設 token 停用", "", "Bearer ", body, http.StatusNotFound},
		{"沒帶 bearer", "sd-secret", "", body, http.StatusUnauthorized},
		{"錯 bearer", "sd-secret", "Bearer sd-secreT", body, http.StatusUnauthorized},
		{"不是 JSON", "sd-secret", "Bearer sd-secret", `{"alerts":[`, http.StatusBadRequest},
	} {
		if code := postAlertWebhook(t, AlertWebhookHandler(s.store, c.token), c.auth, c.body); code != c.want {
			t.Errorf("%s: 應 %d,得 %d", c.name, c.want, code)
		}
	}
	if got := alertsByFingerprint(t, s.store); len(got) != 0 {
		t.Fatalf("被擋的請求不該寫入: %v", got)
	}
}

// Saving notification settings fails when alertmanager.yml cannot be written: the endpoint answers
// 500 alert_settings_save_failed with the raw cause in params.detail, and the audit gets a
// result=failed entry with the same reason.
func TestAlertSettingsSaveFailed(t *testing.T) {
	s, _ := accessTestServer(t)
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s.alertSvc = alerting.New(s.store, []byte("0123456789abcdef0123456789abcdef"), blocker, -1, "")
	audited := recordAudit(t)
	ctx := asAdmin(context.Background())
	hooks := api.PutAlertSettingsWebhooksJSONRequestBody{{Label: "ops", Url: "https://hooks.example/x"}}
	r, err := s.PutAlertSettingsWebhooks(ctx, api.PutAlertSettingsWebhooksRequestObject{Body: &hooks})
	if err != nil {
		t.Fatal(err)
	}
	e, ok := r.(api.PutAlertSettingsWebhooks500JSONResponse)
	if !ok || e.Code != api.ErrorCodeInternal || !hasReason(api.Error(e), api.ErrorReasonAlertSettingsSaveFailed) ||
		e.Params == nil || !strings.Contains((*e.Params)["detail"], "alertmanager.yml") {
		t.Fatalf("寫不了 alertmanager.yml 應 500 alert_settings_save_failed 並附原文: %#v", r)
	}
	entries := audited("alert_webhooks_set")
	if len(entries) != 2 || entries[1]["result"] != "failed" || entries[1]["reason"] != string(api.ErrorReasonAlertSettingsSaveFailed) {
		t.Fatalf("稽核要先記再補一筆 failed: %v", entries)
	}
	if v, err := s.alertSvc.View(ctx); err != nil || len(v.WebhookURLs) != 0 {
		t.Fatalf("存檔失敗後資料庫應維持舊值(沒有 webhook): %+v %v", v, err)
	}

	// the other channel endpoints, deletes included, share the same save path and answer the same 500
	tok, hook := "123:abc", "https://hooks.slack.com/services/T0/B0/x"
	for name, r := range map[string]any{
		"smtp":            must(s.PutAlertSettingsSmtp(ctx, api.PutAlertSettingsSmtpRequestObject{Body: &api.SmtpSettings{Host: "mail.example.com", Port: 25, From: "a@example.com", To: []string{"b@example.com"}}})),
		"telegram":        must(s.PutAlertSettingsTelegram(ctx, api.PutAlertSettingsTelegramRequestObject{Body: &api.TelegramSettings{BotToken: &tok, ChatId: -100}})),
		"slack":           must(s.PutAlertSettingsSlack(ctx, api.PutAlertSettingsSlackRequestObject{Body: &api.SlackSettings{WebhookUrl: &hook}})),
		"locale":          must(s.PutAlertSettingsLocale(ctx, api.PutAlertSettingsLocaleRequestObject{Body: &api.AlertLocale{Language: api.NotifyLanguageEn}})),
		"delete smtp":     must(s.DeleteAlertSettingsSmtp(ctx, api.DeleteAlertSettingsSmtpRequestObject{})),
		"delete telegram": must(s.DeleteAlertSettingsTelegram(ctx, api.DeleteAlertSettingsTelegramRequestObject{})),
		"delete slack":    must(s.DeleteAlertSettingsSlack(ctx, api.DeleteAlertSettingsSlackRequestObject{})),
	} {
		// every *500JSONResponse is an api.Error under its own generated name
		v := reflect.ValueOf(r)
		if !strings.HasSuffix(v.Type().Name(), "500JSONResponse") || !hasReason(v.Convert(reflect.TypeOf(api.Error{})).Interface().(api.Error), api.ErrorReasonAlertSettingsSaveFailed) {
			t.Errorf("%s: 寫不了 alertmanager.yml 應 500: %#v", name, r)
		}
	}
	for _, ev := range []string{"alert_smtp_deleted", "alert_telegram_deleted", "alert_slack_deleted"} {
		if d := audited(ev); len(d) != 2 || d[1]["result"] != "failed" || d[1]["reason"] != string(api.ErrorReasonAlertSettingsSaveFailed) {
			t.Errorf("%s 稽核要先記再補一筆 failed 並帶 alert_settings_save_failed: %v", ev, d)
		}
	}
}
