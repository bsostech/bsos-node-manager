// Alerts: ingesting and storing the Alertmanager webhook + reading via GET /alerts + notification
// channel settings (email, Telegram, Slack, webhooks, language and time zone). The rules that
// produce alerts are in alertrules.go (rendered in internal/alerting/rules.go); this file only
// receives, presents and configures "where to send".
package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"besu-manager/backend/internal/alerting"
	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/auth"
	"besu-manager/backend/internal/store"
)

// ListAlerts: GET /alerts. Our organization's alerts, filterable by status/nodeId/alertname/since.
// When the monitoring source is down it still returns the alerts in the database, with
// monitoringAvailable=false.
func (s *Server) ListAlerts(ctx context.Context, req api.ListAlertsRequestObject) (api.ListAlertsResponseObject, error) {
	var status *string
	if req.Params.Status != nil {
		v := string(*req.Params.Status)
		status = &v
	}
	alerts, err := s.store.ListAlerts(ctx, store.AlertFilter{
		Status: status, NodeID: req.Params.NodeId, Alertname: req.Params.Alertname, Since: req.Params.Since, Reader: alertReader(ctx),
	})
	if err != nil {
		return nil, err
	}
	return api.ListAlerts200JSONResponse(s.alertList(alerts)), nil
}

// GetNodeAlerts: GET /nodes/{id}/alerts. Only our own nodes have alerts; external nodes return 404.
func (s *Server) GetNodeAlerts(ctx context.Context, req api.GetNodeAlertsRequestObject) (api.GetNodeAlertsResponseObject, error) {
	n, err := s.store.GetNode(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return api.GetNodeAlerts404JSONResponse(nodeNotFoundError), nil
	}
	alerts, err := s.store.ListAlerts(ctx, store.AlertFilter{NodeID: &n.ID, Reader: alertReader(ctx)})
	if err != nil {
		return nil, err
	}
	return api.GetNodeAlerts200JSONResponse(s.alertList(alerts)), nil
}

func (s *Server) alertList(alerts []store.Alert) api.AlertList {
	out := api.AlertList{Alerts: make([]api.Alert, 0, len(alerts)), MonitoringAvailable: s.prom.AlertsLive()}
	for _, a := range alerts {
		out.Alerts = append(out.Alerts, toAPIAlert(a))
	}
	return out
}

// promReachable: with PROMETHEUS_URL unset (dev) it is always treated as available; when set, it
// checks whether the background refresh is still alive. The alerts table only records what
// Alertmanager sends in; if Prometheus is down the table freezes at its last state, which must not
// be read as "all clear".
func (s *Server) promReachable() bool {
	return s.prom == nil || s.prom.Reachable()
}

// ── Read state (per user) ────────────────

// alertReader is who the read state is recorded under: the session's username (local account; for
// SSO it is the subject). The middleware guarantees a logged-in user.
func alertReader(ctx context.Context) string {
	if p := auth.PrincipalFrom(ctx); p != nil && p.Username != nil {
		return *p.Username
	}
	return ""
}

// alertID is fingerprint|starts_at (RFC3339Nano, UTC). Externally it is an opaque string used only
// to mark alerts read.
func alertID(a store.Alert) string {
	return a.Fingerprint + "|" + a.StartsAt.UTC().Format(time.RFC3339Nano)
}

// parseAlertIDs ignores IDs it cannot parse.
func parseAlertIDs(ids []string) []store.AlertKey {
	out := make([]store.AlertKey, 0, len(ids))
	for _, id := range ids {
		fp, ts, ok := strings.Cut(id, "|")
		t, err := time.Parse(time.RFC3339Nano, ts)
		if ok && fp != "" && err == nil {
			out = append(out, store.AlertKey{Fingerprint: fp, StartsAt: t})
		}
	}
	return out
}

// MarkAlertsRead: POST /alerts/read.
func (s *Server) MarkAlertsRead(ctx context.Context, req api.MarkAlertsReadRequestObject) (api.MarkAlertsReadResponseObject, error) {
	if req.Body == nil {
		return api.MarkAlertsRead400Response{}, nil
	}
	if err := s.store.MarkAlertsRead(ctx, alertReader(ctx), parseAlertIDs(req.Body.Ids)); err != nil {
		return nil, err
	}
	return api.MarkAlertsRead204Response{}, nil
}

// MarkAllAlertsRead: POST /alerts/read-all.
func (s *Server) MarkAllAlertsRead(ctx context.Context, _ api.MarkAllAlertsReadRequestObject) (api.MarkAllAlertsReadResponseObject, error) {
	if err := s.store.MarkAllAlertsRead(ctx, alertReader(ctx)); err != nil {
		return nil, err
	}
	return api.MarkAllAlertsRead204Response{}, nil
}

func toAPIAlert(a store.Alert) api.Alert {
	out := api.Alert{
		Id:          alertID(a),
		ReadFire:    a.ReadFire,
		ReadResolve: a.ReadResolve,
		Fingerprint: a.Fingerprint,
		Alertname:   a.Alertname,
		NodeId:      a.NodeID,
		Severity:    api.AlertSeverity(a.Severity),
		Status:      api.AlertStatus(a.Status),
		Labels:      a.Labels,
		Annotations: a.Annotations,
		StartsAt:    a.StartsAt.UTC(),
	}
	if a.EndsAt != nil {
		t := a.EndsAt.UTC()
		out.EndsAt = &t
	}
	return out
}

// ── Alertmanager webhook ingestion (internal endpoint, see the platform receiver in alertmanager.yml) ──

// amWebhookPayload is the standard payload of an Alertmanager webhook receiver (only the fields we
// use).
// https://prometheus.io/docs/alerting/latest/configuration/#webhook_config
type amWebhookPayload struct {
	Alerts []amAlert `json:"alerts"`
}

type amAlert struct {
	Status      string            `json:"status"` // firing | resolved
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      time.Time         `json:"endsAt"` // only meaningful when resolved; zero value while firing
	Fingerprint string            `json:"fingerprint"`
}

const alertRetention = 30 * 24 * time.Hour

// AlertWebhookHandler receives alerts from Alertmanager and upserts them into the alerts table.
// Protected by the SD_TOKEN bearer (same as /sd/targets and /metrics, same token, disabled when
// unset). This is an internal endpoint for Alertmanager only, not an API for people, so its trust
// model differs from the public API.
func AlertWebhookHandler(st *store.Store, token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sdBearerOK(w, r, token) {
			return
		}
		var payload amWebhookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		for _, a := range payload.Alerts {
			if err := st.UpsertAlert(ctx, amAlertToStore(a)); err != nil {
				http.Error(w, "internal", http.StatusInternalServerError)
				return
			}
		}
		// Prune expired records along with ingestion; a prune failure does not affect this
		// ingestion.
		if err := st.PruneAlerts(ctx, time.Now().Add(-alertRetention)); err != nil {
			log.Printf("purge expired alert records: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// amAlertToStore: node_id is read first from the node label of measurement rules (the bsos_node_*
// series, provided by SD), falling back to the node_id label only when that is missing (platform
// event metrics bsos_platform_* use node_id, e.g. the certificate expiry alert on
// bsos_platform_cert_expiry_seconds; if a registered platform-event default rule's metric also
// carries node, it takes the path above).
func amAlertToStore(a amAlert) store.Alert {
	nodeID := optionalLabel(a.Labels, "node")
	if nodeID == nil {
		nodeID = optionalLabel(a.Labels, "node_id")
	}
	out := store.Alert{
		Fingerprint: a.Fingerprint,
		Alertname:   a.Labels["alertname"],
		NodeID:      nodeID,
		Org:         optionalLabel(a.Labels, "org"),
		Scope:       a.Labels["scope"],
		Severity:    a.Labels["severity"],
		Status:      a.Status,
		Labels:      a.Labels,
		Annotations: a.Annotations,
		StartsAt:    a.StartsAt,
	}
	if !a.EndsAt.IsZero() {
		t := a.EndsAt
		out.EndsAt = &t
	}
	return out
}

func optionalLabel(labels map[string]string, key string) *string {
	if v, ok := labels[key]; ok && v != "" {
		return &v
	}
	return nil
}

// ── Alert notification settings (writes need nodes:onboard, reads need monitor:view) ──

func alertSettingsToAPI(v alerting.Settings, customCA bool) api.AlertSettings {
	out := api.AlertSettings{WebhookUrls: []api.AlertWebhookUrl{}, Locale: api.AlertLocaleView{Language: v.Language, Timezone: v.Timezone, EffectiveTimezone: v.Zone()}, CustomCa: &customCA}
	for _, w := range v.WebhookURLs {
		out.WebhookUrls = append(out.WebhookUrls, api.AlertWebhookUrl{Label: w.Label, Url: w.URL})
	}
	if v.SMTP != nil {
		sec := api.SmtpSecurity(v.SMTP.Security)
		s := api.SmtpSettings{Host: v.SMTP.Host, Port: v.SMTP.Port, Security: &sec, From: v.SMTP.From, To: v.SMTP.To}
		if v.SMTP.Username != "" {
			s.Username = &v.SMTP.Username
		}
		out.Smtp = &s
	}
	if t := v.Telegram; t != nil {
		out.Telegram = &api.TelegramSettings{ChatId: t.ChatID, MessageThreadId: t.MessageThreadID}
	}
	if v.Slack != nil {
		out.Slack = &api.SlackSettings{}
	}
	return out
}

func smtpFromAPI(in api.SmtpSettings) *alerting.SMTP {
	out := &alerting.SMTP{Host: in.Host, Port: in.Port, From: in.From, To: in.To}
	if in.Username != nil {
		out.Username = *in.Username
	}
	// Without a username there is no login, so a password would only end up in the rendered config.
	if in.Password != nil && out.Username != "" {
		out.Password = *in.Password
	}
	out.Security = alerting.DefaultAlertSecurity(in.Port)
	if in.Security != nil {
		out.Security = alerting.Security(*in.Security)
	}
	return out
}

// smtpAddressError checks from and every to address. The connection test never sends MAIL FROM or
// RCPT TO, so a malformed address would pass it and only fail later when Alertmanager sends.
func smtpAddressError(in api.SmtpSettings) *api.Error {
	for i, a := range append([]string{in.From}, in.To...) {
		field := "from"
		if i > 0 {
			field = "to." + strconv.Itoa(i-1)
		}
		if !validEmail(a) {
			return invalidReason(api.ErrorReasonInvalidEmail, map[string]string{"field": field})
		}
	}
	return nil
}

func webhooksFromAPI(in []api.AlertWebhookUrl) []alerting.WebhookURL {
	out := make([]alerting.WebhookURL, len(in))
	for i, w := range in {
		out[i] = alerting.WebhookURL{Label: w.Label, URL: w.Url}
	}
	return out
}

// alertSettingsSaveError: saving notification settings failed (database, encryption, writing
// alertmanager.yml); the input was already accepted, so this is a server fault (500). The raw text is
// for operators.
func alertSettingsSaveError(err error) api.Error {
	return apierr.New(api.ErrorCodeInternal, api.ErrorReasonAlertSettingsSaveFailed, map[string]string{"detail": err.Error()})
}

// testFailed: the connection test failed. If internal/alerting carries a reason it is returned (the
// root cause); unrecognized raw text goes in params.detail.
func testFailed(reason api.ErrorReason, err error) *api.Error {
	e := causeError(api.ErrorCodeInvalid, reason, err)
	return &e
}

// validateWebhooks: label / url are non-empty and url is an absolute http(s) URL. Rejected here
// rather than waiting for Alertmanager to error out: a single bad URL keeps the whole
// alertmanager.yml from loading.
func validateWebhooks(in []api.AlertWebhookUrl) *api.Error {
	for i, w := range in {
		index := map[string]string{"index": strconv.Itoa(i + 1)}
		if strings.TrimSpace(w.Label) == "" {
			return invalidReason(api.ErrorReasonWebhookNameRequired, index)
		}
		u, err := url.Parse(strings.TrimSpace(w.Url))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return invalidReason(api.ErrorReasonWebhookUrlInvalid, index)
		}
	}
	return nil
}

// GetAlertSettings: GET /alert-settings.
func (s *Server) GetAlertSettings(ctx context.Context, _ api.GetAlertSettingsRequestObject) (api.GetAlertSettingsResponseObject, error) {
	v, err := s.alertSvc.View(ctx)
	if err != nil {
		return nil, err
	}
	out := alertSettingsToAPI(v, s.alertSvc.CustomCA())
	// URLs often carry secrets (Slack and Teams webhooks); people who cannot change settings only
	// see how many there are
	if !auth.PrincipalFrom(ctx).Can(auth.PermNodesOnboard) {
		for i := range out.WebhookUrls {
			out.WebhookUrls[i].Url = ""
		}
	}
	return api.GetAlertSettings200JSONResponse(out), nil
}

// alertSettingsView is the shared "return the full updated settings" tail of the write endpoints.
func (s *Server) alertSettingsView(ctx context.Context) (api.AlertSettings, error) {
	v, err := s.alertSvc.View(ctx)
	if err != nil {
		return api.AlertSettings{}, err
	}
	return alertSettingsToAPI(v, s.alertSvc.CustomCA()), nil
}

// PutAlertSettingsSmtp: PUT /alert-settings/smtp. Changes only SMTP; the webhook list is
// unaffected. With a username the password is required: "leave empty to keep the current password"
// is not supported, so operators never have to guess which one is stored. Without a username
// (an intranet relay that takes no login) no password is needed.
func (s *Server) PutAlertSettingsSmtp(ctx context.Context, req api.PutAlertSettingsSmtpRequestObject) (api.PutAlertSettingsSmtpResponseObject, error) {
	if req.Body == nil {
		return api.PutAlertSettingsSmtp400JSONResponse{Code: api.ErrorCodeInvalid, Message: "missing settings"}, nil // the contract's requestBody required rejects this first; unreachable
	}
	if u, p := req.Body.Username, req.Body.Password; u != nil && *u != "" && (p == nil || *p == "") {
		return api.PutAlertSettingsSmtp400JSONResponse(*invalidReason(api.ErrorReasonSmtpPasswordRequired, nil)), nil
	}
	if e := smtpAddressError(*req.Body); e != nil {
		return api.PutAlertSettingsSmtp400JSONResponse(*e), nil
	}
	before, err := s.alertSvc.View(ctx)
	if err != nil {
		return nil, err
	}
	next := smtpFromAPI(*req.Body)
	// A password is sent in full every time it is used, so it counts as "changed" whenever there is
	// one; its value is not recorded. Clearing the username also drops the stored password, which is a
	// change too (View blanks the password, but a stored username always came with one).
	pwChanged := next.Password != "" || (before.SMTP != nil && before.SMTP.Username != "")
	d := map[string]any{"before": smtpAuditView(before.SMTP), "after": smtpAuditView(next), "password_changed": pwChanged}
	if err := s.audit(ctx, "alert_smtp_set", d); err != nil {
		return nil, err
	}
	if err := s.alertSvc.SaveSMTP(ctx, next); err != nil {
		e := alertSettingsSaveError(err)
		s.auditFailedError(ctx, "alert_smtp_set", d, e)
		return api.PutAlertSettingsSmtp500JSONResponse(e), nil
	}
	out, err := s.alertSettingsView(ctx)
	if err != nil {
		return nil, err
	}
	return api.PutAlertSettingsSmtp200JSONResponse(out), nil
}

// DeleteAlertSettingsSmtp: DELETE /alert-settings/smtp. Disables email notifications and clears the
// settings; webhooks are unaffected.
func (s *Server) DeleteAlertSettingsSmtp(ctx context.Context, _ api.DeleteAlertSettingsSmtpRequestObject) (api.DeleteAlertSettingsSmtpResponseObject, error) {
	before, err := s.alertSvc.View(ctx)
	if err != nil {
		return nil, err
	}
	d := map[string]any{"before": smtpAuditView(before.SMTP)}
	if err := s.audit(ctx, "alert_smtp_deleted", d); err != nil {
		return nil, err
	}
	if err := s.alertSvc.SaveSMTP(ctx, nil); err != nil {
		e := alertSettingsSaveError(err)
		s.auditFailedError(ctx, "alert_smtp_deleted", d, e)
		return api.DeleteAlertSettingsSmtp500JSONResponse(e), nil
	}
	out, err := s.alertSettingsView(ctx)
	if err != nil {
		return nil, err
	}
	return api.DeleteAlertSettingsSmtp200JSONResponse(out), nil
}

// PutAlertSettingsWebhooks: PUT /alert-settings/webhooks. Replaces the whole webhook list (empty
// array = clear); SMTP is unaffected.
func (s *Server) PutAlertSettingsWebhooks(ctx context.Context, req api.PutAlertSettingsWebhooksRequestObject) (api.PutAlertSettingsWebhooksResponseObject, error) {
	if req.Body == nil {
		return api.PutAlertSettingsWebhooks400JSONResponse{Code: api.ErrorCodeInvalid, Message: "missing settings"}, nil // the contract's requestBody required rejects this first; unreachable
	}
	if e := validateWebhooks(*req.Body); e != nil {
		return api.PutAlertSettingsWebhooks400JSONResponse(*e), nil
	}
	before, err := s.alertSvc.View(ctx)
	if err != nil {
		return nil, err
	}
	next := webhooksFromAPI(*req.Body)
	d := map[string]any{"before": webhooksAuditView(before.WebhookURLs), "after": webhooksAuditView(next)}
	if err := s.audit(ctx, "alert_webhooks_set", d); err != nil {
		return nil, err
	}
	if err := s.alertSvc.SaveWebhooks(ctx, next); err != nil {
		e := alertSettingsSaveError(err)
		s.auditFailedError(ctx, "alert_webhooks_set", d, e)
		return api.PutAlertSettingsWebhooks500JSONResponse(e), nil
	}
	out, err := s.alertSettingsView(ctx)
	if err != nil {
		return nil, err
	}
	return api.PutAlertSettingsWebhooks200JSONResponse(out), nil
}

func telegramFromAPI(in api.TelegramSettings) alerting.Telegram {
	t := alerting.Telegram{ChatID: in.ChatId, MessageThreadID: in.MessageThreadId}
	if in.BotToken != nil {
		t.BotToken = strings.TrimSpace(*in.BotToken)
	}
	return t
}

// telegramAuditView / slackAuditView: the settings recorded in the audit, without the token or
// webhook URL (both are credentials).
func telegramAuditView(t *alerting.Telegram) map[string]any {
	if t == nil {
		return nil
	}
	return map[string]any{"chat_id": t.ChatID, "message_thread_id": t.MessageThreadID}
}

func slackAuditView(sl *alerting.Slack) map[string]any {
	if sl == nil {
		return nil
	}
	return map[string]any{"enabled": true}
}

// PutAlertSettingsTelegram: PUT /alert-settings/telegram. botToken is required (same reason as the
// SMTP password).
func (s *Server) PutAlertSettingsTelegram(ctx context.Context, req api.PutAlertSettingsTelegramRequestObject) (api.PutAlertSettingsTelegramResponseObject, error) {
	if req.Body == nil {
		return api.PutAlertSettingsTelegram400JSONResponse{Code: api.ErrorCodeInvalid, Message: "missing settings"}, nil // the contract's requestBody required rejects this first; unreachable
	}
	next := telegramFromAPI(*req.Body)
	if next.BotToken == "" || next.ChatID == 0 {
		return api.PutAlertSettingsTelegram400JSONResponse(*invalidReason(api.ErrorReasonTelegramFieldsRequired, nil)), nil
	}
	if next.MessageThreadID != nil && *next.MessageThreadID <= 0 {
		return api.PutAlertSettingsTelegram400JSONResponse(*invalidReason(api.ErrorReasonTelegramThreadIdInvalid, nil)), nil
	}
	before, err := s.alertSvc.View(ctx)
	if err != nil {
		return nil, err
	}
	d := map[string]any{"before": telegramAuditView(before.Telegram), "after": telegramAuditView(&next), "secret_changed": true}
	if err := s.audit(ctx, "alert_telegram_set", d); err != nil {
		return nil, err
	}
	if err := s.alertSvc.SaveTelegram(ctx, &next); err != nil {
		e := alertSettingsSaveError(err)
		s.auditFailedError(ctx, "alert_telegram_set", d, e)
		return api.PutAlertSettingsTelegram500JSONResponse(e), nil
	}
	out, err := s.alertSettingsView(ctx)
	if err != nil {
		return nil, err
	}
	return api.PutAlertSettingsTelegram200JSONResponse(out), nil
}

// DeleteAlertSettingsTelegram: DELETE /alert-settings/telegram.
func (s *Server) DeleteAlertSettingsTelegram(ctx context.Context, _ api.DeleteAlertSettingsTelegramRequestObject) (api.DeleteAlertSettingsTelegramResponseObject, error) {
	before, err := s.alertSvc.View(ctx)
	if err != nil {
		return nil, err
	}
	d := map[string]any{"before": telegramAuditView(before.Telegram)}
	if err := s.audit(ctx, "alert_telegram_deleted", d); err != nil {
		return nil, err
	}
	if err := s.alertSvc.SaveTelegram(ctx, nil); err != nil {
		e := alertSettingsSaveError(err)
		s.auditFailedError(ctx, "alert_telegram_deleted", d, e)
		return api.DeleteAlertSettingsTelegram500JSONResponse(e), nil
	}
	out, err := s.alertSettingsView(ctx)
	if err != nil {
		return nil, err
	}
	return api.DeleteAlertSettingsTelegram200JSONResponse(out), nil
}

// PutAlertSettingsSlack: PUT /alert-settings/slack. webhookUrl is required and must be https.
func (s *Server) PutAlertSettingsSlack(ctx context.Context, req api.PutAlertSettingsSlackRequestObject) (api.PutAlertSettingsSlackResponseObject, error) {
	if req.Body == nil || req.Body.WebhookUrl == nil {
		return api.PutAlertSettingsSlack400JSONResponse(*invalidReason(api.ErrorReasonSlackUrlRequired, nil)), nil
	}
	u := strings.TrimSpace(*req.Body.WebhookUrl)
	if err := alerting.ValidateSlackURL(u); err != nil {
		return api.PutAlertSettingsSlack400JSONResponse(*invalidReason(api.ErrorReasonSlackUrlInvalid, nil)), nil
	}
	before, err := s.alertSvc.View(ctx)
	if err != nil {
		return nil, err
	}
	next := &alerting.Slack{WebhookURL: u}
	d := map[string]any{"before": slackAuditView(before.Slack), "after": slackAuditView(next), "secret_changed": true}
	if err := s.audit(ctx, "alert_slack_set", d); err != nil {
		return nil, err
	}
	if err := s.alertSvc.SaveSlack(ctx, next); err != nil {
		e := alertSettingsSaveError(err)
		s.auditFailedError(ctx, "alert_slack_set", d, e)
		return api.PutAlertSettingsSlack500JSONResponse(e), nil
	}
	out, err := s.alertSettingsView(ctx)
	if err != nil {
		return nil, err
	}
	return api.PutAlertSettingsSlack200JSONResponse(out), nil
}

// DeleteAlertSettingsSlack: DELETE /alert-settings/slack.
func (s *Server) DeleteAlertSettingsSlack(ctx context.Context, _ api.DeleteAlertSettingsSlackRequestObject) (api.DeleteAlertSettingsSlackResponseObject, error) {
	before, err := s.alertSvc.View(ctx)
	if err != nil {
		return nil, err
	}
	d := map[string]any{"before": slackAuditView(before.Slack)}
	if err := s.audit(ctx, "alert_slack_deleted", d); err != nil {
		return nil, err
	}
	if err := s.alertSvc.SaveSlack(ctx, nil); err != nil {
		e := alertSettingsSaveError(err)
		s.auditFailedError(ctx, "alert_slack_deleted", d, e)
		return api.DeleteAlertSettingsSlack500JSONResponse(e), nil
	}
	out, err := s.alertSettingsView(ctx)
	if err != nil {
		return nil, err
	}
	return api.DeleteAlertSettingsSlack200JSONResponse(out), nil
}

// PutAlertSettingsLocale: PUT /alert-settings/locale. Language and time zone for every channel; see
// alerting.Service.SaveLocale for what gets rewritten.
func (s *Server) PutAlertSettingsLocale(ctx context.Context, req api.PutAlertSettingsLocaleRequestObject) (api.PutAlertSettingsLocaleResponseObject, error) {
	if req.Body == nil {
		return api.PutAlertSettingsLocale400JSONResponse{Code: api.ErrorCodeInvalid, Message: "missing settings"}, nil // the contract's requestBody required rejects this first; unreachable
	}
	if !req.Body.Language.Valid() { // the contract's enum rejects this first; same reason as request validation
		return api.PutAlertSettingsLocale400JSONResponse(*invalidReason(api.ErrorReasonFieldNotAllowed, map[string]string{"field": "language"})), nil
	}
	zone := strings.TrimSpace(req.Body.Timezone) // empty = follow the platform host
	if zone != "" && !alerting.ValidZone(zone) {
		return api.PutAlertSettingsLocale400JSONResponse(*invalidReason(api.ErrorReasonFieldFormat, map[string]string{"field": "timezone"})), nil
	}
	before, err := s.alertSvc.View(ctx)
	if err != nil {
		return nil, err
	}
	d := map[string]any{
		"before": map[string]any{"language": string(before.Language), "timezone": before.Timezone},
		"after":  map[string]any{"language": string(req.Body.Language), "timezone": zone},
	}
	if err := s.audit(ctx, "alert_locale_set", d); err != nil {
		return nil, err
	}
	if err := s.alertSvc.SaveLocale(ctx, req.Body.Language, zone); err != nil {
		e := alertSettingsSaveError(err)
		s.auditFailedError(ctx, "alert_locale_set", d, e)
		return api.PutAlertSettingsLocale500JSONResponse(e), nil
	}
	out, err := s.alertSettingsView(ctx)
	if err != nil {
		return nil, err
	}
	return api.PutAlertSettingsLocale200JSONResponse(out), nil
}

// notifyLanguage: the language test messages are sent in, so a test looks like the real notifications.
// Falls back to English when the settings cannot be read: the test is about the connection, not the wording.
func (s *Server) notifyLanguage(ctx context.Context) api.NotifyLanguage {
	v, err := s.alertSvc.View(ctx)
	if err != nil {
		return api.NotifyLanguageEn
	}
	return v.Language
}

// TestAlertSettingsTelegram: POST /alert-settings/test-telegram. Sends a test message using the
// form contents; see alerting.TestTelegram.
func (s *Server) TestAlertSettingsTelegram(ctx context.Context, req api.TestAlertSettingsTelegramRequestObject) (api.TestAlertSettingsTelegramResponseObject, error) {
	if req.Body == nil {
		return api.TestAlertSettingsTelegram400JSONResponse{Code: api.ErrorCodeInvalid, Message: "missing settings"}, nil // the contract's requestBody required rejects this first; unreachable
	}
	t := telegramFromAPI(*req.Body)
	if t.BotToken == "" || t.ChatID == 0 { // same as the first check in alerting.TestTelegram; rejecting here first lets us attach the reason
		return api.TestAlertSettingsTelegram400JSONResponse(*invalidReason(api.ErrorReasonTelegramFieldsRequired, nil)), nil
	}
	if err := alerting.TestTelegram(ctx, t, s.notifyLanguage(ctx)); err != nil {
		return api.TestAlertSettingsTelegram400JSONResponse(*testFailed(api.ErrorReasonTelegramTestFailed, err)), nil
	}
	return api.TestAlertSettingsTelegram204Response{}, nil
}

// TestAlertSettingsSlack: POST /alert-settings/test-slack. Sends a test message using the form
// contents; see alerting.TestSlack.
func (s *Server) TestAlertSettingsSlack(ctx context.Context, req api.TestAlertSettingsSlackRequestObject) (api.TestAlertSettingsSlackResponseObject, error) {
	if req.Body == nil || req.Body.WebhookUrl == nil {
		return api.TestAlertSettingsSlack400JSONResponse(*invalidReason(api.ErrorReasonSlackUrlRequired, nil)), nil
	}
	u := strings.TrimSpace(*req.Body.WebhookUrl)
	if err := alerting.ValidateSlackURL(u); err != nil { // alerting.TestSlack checks this too; rejecting here first gives the same reason as when saving
		return api.TestAlertSettingsSlack400JSONResponse(*invalidReason(api.ErrorReasonSlackUrlInvalid, nil)), nil
	}
	if err := alerting.TestSlack(ctx, u, s.notifyLanguage(ctx)); err != nil {
		return api.TestAlertSettingsSlack400JSONResponse(*testFailed(api.ErrorReasonSlackTestFailed, err)), nil
	}
	return api.TestAlertSettingsSlack204Response{}, nil
}

// TestAlertSettingsSmtp: POST /alert-settings/test-smtp. Tests the connection on the spot with the
// form's current contents (not the saved settings), without sending mail or storing anything: it
// only answers "can these settings connect", see alerting.TestSMTP.
func (s *Server) TestAlertSettingsSmtp(ctx context.Context, req api.TestAlertSettingsSmtpRequestObject) (api.TestAlertSettingsSmtpResponseObject, error) {
	if req.Body == nil {
		return api.TestAlertSettingsSmtp400JSONResponse{Code: api.ErrorCodeInvalid, Message: "missing settings"}, nil // the contract's requestBody required rejects this first; unreachable
	}
	if e := smtpAddressError(*req.Body); e != nil {
		return api.TestAlertSettingsSmtp400JSONResponse(*e), nil
	}
	if err := alerting.TestSMTP(ctx, *smtpFromAPI(*req.Body), s.alertSvc.SMTPCA); err != nil {
		return api.TestAlertSettingsSmtp400JSONResponse(*testFailed(api.ErrorReasonSmtpTestFailed, err)), nil
	}
	return api.TestAlertSettingsSmtp204Response{}, nil
}

// TestAlertSettingsWebhook: POST /alert-settings/test-webhook. Tests only the TCP+TLS connection
// layer and sends no HTTP request: we do not control the payload Alertmanager actually sends, see
// alerting.TestWebhookConnectivity.
func (s *Server) TestAlertSettingsWebhook(ctx context.Context, req api.TestAlertSettingsWebhookRequestObject) (api.TestAlertSettingsWebhookResponseObject, error) {
	if req.Body == nil {
		return api.TestAlertSettingsWebhook400JSONResponse{Code: api.ErrorCodeInvalid, Message: "missing settings"}, nil // the contract's requestBody required rejects this first; unreachable
	}
	if err := alerting.TestWebhookConnectivity(ctx, req.Body.Url); err != nil {
		return api.TestAlertSettingsWebhook400JSONResponse(*testFailed(api.ErrorReasonWebhookTestFailed, err)), nil
	}
	return api.TestAlertSettingsWebhook204Response{}, nil
}

// smtpAuditView: the SMTP settings recorded in the audit (without the password); nil when not
// configured.
func smtpAuditView(c *alerting.SMTP) map[string]any {
	if c == nil {
		return nil
	}
	return map[string]any{"host": c.Host, "port": c.Port, "security": string(c.Security), "username": c.Username, "from": c.From, "to": c.To}
}

// webhooksAuditView: the audit records each webhook's name and destination host. Path and query are
// not recorded: they often carry a token.
func webhooksAuditView(ws []alerting.WebhookURL) []map[string]any {
	out := make([]map[string]any, 0, len(ws))
	for _, w := range ws {
		host := ""
		if u, err := url.Parse(w.URL); err == nil {
			host = u.Scheme + "://" + u.Host
		}
		out = append(out, map[string]any{"label": w.Label, "host": host})
	}
	return out
}
