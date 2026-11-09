package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"besu-manager/backend/internal/alerting"
	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/auth"
	"besu-manager/backend/internal/store"
)

// Allowlist diff: record which addresses were added and removed, not just the counts.
func TestDiffAddresses(t *testing.T) {
	added, removed := diffAddresses([]string{"0xa", "0xb"}, []string{"0xb", "0xc"})
	if fmt.Sprint(added) != "[0xc]" || fmt.Sprint(removed) != "[0xa]" {
		t.Fatalf("added=%v removed=%v", added, removed)
	}
	if a, r := diffAddresses([]string{"0xABC"}, []string{"0xabc"}); len(a) != 0 || len(r) != 0 {
		t.Fatalf("只差大小寫不算改: added=%v removed=%v", a, r)
	}
	added, removed = diffAddresses(nil, nil)
	if added == nil || removed == nil {
		t.Fatal("沒有差異也要是空陣列,稽核裡才是 [] 不是 null")
	}
}

// Webhooks record only name and host: path and query often carry a token and must not go into the
// audit. SMTP does not record the password.
func TestAlertSettingsAuditViewsHideSecrets(t *testing.T) {
	v := webhooksAuditView([]alerting.WebhookURL{{Label: "slack", URL: "https://user:pw@hooks.slack.com/services/T000/B000/SECRET?token=xyz"}})
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "xyz") || strings.Contains(string(b), "pw") {
		t.Fatalf("webhook 稽核漏出 token: %s", b)
	}
	if v[0]["host"] != "https://hooks.slack.com" || v[0]["label"] != "slack" {
		t.Fatalf("webhook 稽核內容不對: %v", v)
	}
	b, _ = json.Marshal(smtpAuditView(&alerting.SMTP{Host: "smtp", Port: 587, Username: "u", Password: "hunter2", From: "a@x", To: []string{"b@x"}}))
	if strings.Contains(string(b), "hunter2") {
		t.Fatalf("SMTP 稽核漏出密碼: %s", b)
	}
	if smtpAuditView(nil) != nil {
		t.Fatal("沒設定 SMTP 應記 nil")
	}
}

// Editing an alert rule records the previous content (adding is an Enterprise feature, tested there).
func TestAlertRuleAudit(t *testing.T) {
	s, _ := accessTestServer(t)
	s.rulesSvc = alerting.NewRulesService(s.store, t.TempDir(), "")
	audited := recordAudit(t)
	ctx := asAdmin(context.Background())

	rule := customRule(t, s, "cpu", 80)
	m, op, th := api.Cpu, api.Gt, float32(90)
	if _, err := s.UpdateAlertRule(ctx, api.UpdateAlertRuleRequestObject{Id: rule.ID, Body: &api.UpdateAlertRuleJSONRequestBody{
		Name: "cpu", Metric: &m, Operator: &op, Threshold: &th, ForDuration: api.AlertRuleDurationN5m, Severity: api.AlertRuleUpdateRequestSeverityCritical,
	}}); err != nil {
		t.Fatal(err)
	}
	u := audited("alert_rule_updated")
	if len(u) != 1 {
		t.Fatalf("修改稽核筆數不對: %v", u)
	}
	before, after := u[0]["before"].(map[string]any), u[0]["after"].(map[string]any)
	if before["threshold"] != float64(80) || after["threshold"] != float64(90) || before["severity"] != "warning" || after["severity"] != "critical" {
		t.Fatalf("修改前後不對: before=%v after=%v", before, after)
	}
}

// Allowlist changes record which addresses were added and removed.
func TestPolicyAuditDiff(t *testing.T) {
	s, _ := accessTestServer(t)
	audited := recordAudit(t)
	ctx := asAdmin(context.Background())
	if _, err := s.store.SavePermissioningPolicy(ctx, store.SharedPolicy, []string{"0x" + strings.Repeat("a", 40)}, 0); err != nil {
		t.Fatal(err)
	}
	b := "0x" + strings.Repeat("b", 40)
	if _, err := s.SetPermissioningPolicy(ctx, api.SetPermissioningPolicyRequestObject{Body: &api.SetPermissioningPolicyJSONRequestBody{AccountsAllowlist: []string{b}, Revision: 1}}); err != nil {
		t.Fatal(err)
	}
	p := audited("permissioning_policy_set")
	if len(p) == 0 || fmt.Sprint(p[0]["added"]) != "["+b+"]" || fmt.Sprint(p[0]["removed"]) != "[0x"+strings.Repeat("a", 40)+"]" {
		t.Fatalf("白名單稽核不對: %v", p)
	}
}

// Node ports record the previous value.
func TestNodeSettingsAuditBefore(t *testing.T) {
	s, pool := accessTestServer(t)
	s.org = "test"
	ctx := asAdmin(context.Background())
	if _, err := pool.Exec(ctx, `INSERT INTO nodes (id, org) VALUES ('node-x', 'test')`); err != nil {
		t.Fatal(err)
	}
	audited := recordAudit(t)
	if _, err := s.SetNodePorts(ctx, api.SetNodePortsRequestObject{Id: "node-x", Body: &api.SetNodePortsJSONRequestBody{RpcPort: 8546, MetricsPort: 9546}}); err != nil {
		t.Fatal(err)
	}
	p := audited("node_ports_set")
	if len(p) != 1 || fmt.Sprint(p[0]["before"]) != "map[metrics_port:9545 rpc_port:8545]" || fmt.Sprint(p[0]["after"]) != "map[metrics_port:9546 rpc_port:8546]" {
		t.Fatalf("節點埠稽核不對: %v", p)
	}
}

// Notification settings: SMTP settings and disabling, and the webhook list, all record before and
// after; passwords and webhook paths do not go into the audit.
func TestAlertSettingsAudit(t *testing.T) {
	s, _ := accessTestServer(t)
	s.alertSvc = alerting.New(s.store, []byte("0123456789abcdef0123456789abcdef"), "", -1, "")
	audited := recordAudit(t)
	ctx := asAdmin(context.Background())
	user, pw := "mailer", "hunter2-smtp"
	if r, err := s.PutAlertSettingsSmtp(ctx, api.PutAlertSettingsSmtpRequestObject{Body: &api.PutAlertSettingsSmtpJSONRequestBody{
		Host: "smtp.x", Port: 587, From: "a@x", To: []string{"b@x"}, Username: &user, Password: &pw,
	}}); err != nil {
		t.Fatal(err)
	} else if _, ok := r.(api.PutAlertSettingsSmtp200JSONResponse); !ok {
		t.Fatalf("帶密碼應存成功:%#v", r)
	}
	// Clearing the username drops the stored password, so that is still a password change
	if _, err := s.PutAlertSettingsSmtp(ctx, api.PutAlertSettingsSmtpRequestObject{Body: &api.PutAlertSettingsSmtpJSONRequestBody{
		Host: "smtp.x", Port: 587, From: "a@x", To: []string{"b@x"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutAlertSettingsWebhooks(ctx, api.PutAlertSettingsWebhooksRequestObject{Body: &api.PutAlertSettingsWebhooksJSONRequestBody{
		{Label: "ops", Url: "https://hooks.example/services/T0/B0/TOKEN123"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteAlertSettingsSmtp(ctx, api.DeleteAlertSettingsSmtpRequestObject{}); err != nil {
		t.Fatal(err)
	}
	set, wh, del := audited("alert_smtp_set"), audited("alert_webhooks_set"), audited("alert_smtp_deleted")
	if b, _ := json.Marshal(set[0]["before"]); len(set) != 2 || string(b) != "null" || set[0]["after"].(map[string]any)["host"] != "smtp.x" || set[0]["after"].(map[string]any)["security"] != "none" || set[0]["password_changed"] != true || set[1]["password_changed"] != true {
		t.Fatalf("SMTP 設定稽核不對: %v", set)
	}
	if len(wh) != 1 || fmt.Sprint(wh[0]["after"]) != "[map[host:https://hooks.example label:ops]]" {
		t.Fatalf("webhook 稽核不對: %v", wh)
	}
	if len(del) != 1 || del[0]["before"].(map[string]any)["host"] != "smtp.x" || del[0]["before"].(map[string]any)["security"] != "none" {
		t.Fatalf("SMTP 停用稽核不對: %v", del)
	}
	all, _ := json.Marshal([]any{set, wh, del})
	if strings.Contains(string(all), pw) || strings.Contains(string(all), "TOKEN123") {
		t.Fatalf("稽核漏出秘密: %s", all)
	}

	// Reading settings: those who can edit see the URLs, everyone else only sees how many there are
	for role, want := range map[string]string{"operator": "https://hooks.example/services/T0/B0/TOKEN123", "viewer": ""} {
		u := role
		got, err := s.GetAlertSettings(auth.WithPrincipal(context.Background(), &auth.Principal{Username: &u, Role: role}), api.GetAlertSettingsRequestObject{})
		if err != nil {
			t.Fatal(err)
		}
		if w := got.(api.GetAlertSettings200JSONResponse).WebhookUrls; len(w) != 1 || w[0].Label != "ops" || w[0].Url != want {
			t.Errorf("%s 看到的 webhook = %+v, want url %q", role, w, want)
		}
	}
}

// Telegram and Slack: stored independently; GET does not return the token or webhook URL and the
// audit does not record them; setting one channel does not touch the others.
func TestAlertSettingsTelegramSlack(t *testing.T) {
	s, _ := accessTestServer(t)
	s.alertSvc = alerting.New(s.store, []byte("0123456789abcdef0123456789abcdef"), "", -1, "")
	audited := recordAudit(t)
	ctx := asAdmin(context.Background())
	pw := "hunter2-smtp"
	if _, err := s.PutAlertSettingsSmtp(ctx, api.PutAlertSettingsSmtpRequestObject{Body: &api.PutAlertSettingsSmtpJSONRequestBody{
		Host: "smtp.x", Port: 587, From: "a@x", To: []string{"b@x"}, Password: &pw,
	}}); err != nil {
		t.Fatal(err)
	}
	tok, thread := "123:TGSECRET", int64(7)
	if r, err := s.PutAlertSettingsTelegram(ctx, api.PutAlertSettingsTelegramRequestObject{Body: &api.TelegramSettings{BotToken: &tok, ChatId: -100, MessageThreadId: &thread}}); err != nil {
		t.Fatal(err)
	} else if _, ok := r.(api.PutAlertSettingsTelegram200JSONResponse); !ok {
		t.Fatalf("telegram put: %#v", r)
	}
	hook := "https://hooks.slack.com/services/T0/B0/SLACKSECRET"
	if r, _ := s.PutAlertSettingsSlack(ctx, api.PutAlertSettingsSlackRequestObject{Body: &api.SlackSettings{WebhookUrl: &hook}}); r == nil {
		t.Fatal("slack put")
	}
	plain := "http://hooks.slack.com/x"
	if r, _ := s.PutAlertSettingsSlack(ctx, api.PutAlertSettingsSlackRequestObject{Body: &api.SlackSettings{WebhookUrl: &plain}}); func() bool { _, ok := r.(api.PutAlertSettingsSlack400JSONResponse); return !ok }() {
		t.Fatalf("http slack url must be rejected: %#v", r)
	}

	got, err := s.GetAlertSettings(ctx, api.GetAlertSettingsRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	v := got.(api.GetAlertSettings200JSONResponse)
	if v.Smtp == nil || v.Telegram == nil || v.Telegram.ChatId != -100 || *v.Telegram.MessageThreadId != 7 || v.Telegram.BotToken != nil || v.Slack == nil || v.Slack.WebhookUrl != nil {
		t.Fatalf("view = %+v", v)
	}
	// Stored encrypted in the database, not in plain text
	raw, err := s.store.GetAlertSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw.TelegramSecret) == 0 || len(raw.SlackSecret) == 0 || strings.Contains(string(raw.TelegramSecret)+string(raw.SlackSecret)+string(raw.TelegramConfig), "SECRET") {
		t.Fatalf("stored secrets not encrypted: %+v", raw)
	}
	bad := int64(0)
	if r, _ := s.PutAlertSettingsTelegram(ctx, api.PutAlertSettingsTelegramRequestObject{Body: &api.TelegramSettings{BotToken: &tok, ChatId: -100, MessageThreadId: &bad}}); func() bool { _, ok := r.(api.PutAlertSettingsTelegram400JSONResponse); return !ok }() {
		t.Fatalf("topic id 0 must be rejected: %#v", r)
	}

	if _, err := s.DeleteAlertSettingsTelegram(ctx, api.DeleteAlertSettingsTelegramRequestObject{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteAlertSettingsSlack(ctx, api.DeleteAlertSettingsSlackRequestObject{}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetAlertSettings(ctx, api.GetAlertSettingsRequestObject{})
	if v := got.(api.GetAlertSettings200JSONResponse); v.Telegram != nil || v.Slack != nil || v.Smtp == nil {
		t.Fatalf("after delete = %+v", v)
	}

	tg := audited("alert_telegram_set")
	if len(tg) != 1 || tg[0]["after"].(map[string]any)["chat_id"] != int64(-100) {
		t.Fatalf("telegram 稽核不對: %v", tg)
	}
	all, _ := json.Marshal([]any{tg, audited("alert_slack_set"), audited("alert_telegram_deleted"), audited("alert_slack_deleted")})
	if strings.Contains(string(all), "TGSECRET") || strings.Contains(string(all), "SLACKSECRET") {
		t.Fatalf("稽核漏出秘密: %s", all)
	}
}

// Restore defaults records the whole rule as it was before (custom thresholds get overwritten).
func TestRestoreDefaultsAuditBefore(t *testing.T) {
	s, _ := accessTestServer(t)
	s.rulesSvc = alerting.NewRulesService(s.store, t.TempDir(), "")
	ctx := asAdmin(context.Background())
	customRule(t, s, "mine", 70)
	audited := recordAudit(t)
	if _, err := s.RestoreDefaultAlertRules(ctx, api.RestoreDefaultAlertRulesRequestObject{}); err != nil {
		t.Fatal(err)
	}
	r := audited("alert_rules_restored")
	if len(r) != 1 || !strings.Contains(fmt.Sprint(r[0]["before"]), "name:mine") {
		t.Fatalf("還原稽核應帶還原前的規則: %v", r)
	}
}

// Creating and revoking join tokens are both recorded; the record does not contain the token
// itself, and token_hash is the same value as in the join / re_enroll records. Renaming a node
// records before and after.
func TestJoinTokenAndRenameAudit(t *testing.T) {
	s, pool := accessTestServer(t)
	s.org = "test"
	audited := recordAudit(t)
	ctx := asAdmin(context.Background())

	r, err := s.CreateJoinToken(ctx, api.CreateJoinTokenRequestObject{Body: &api.CreateJoinTokenJSONRequestBody{NodeId: "node-j"}})
	if err != nil {
		t.Fatal(err)
	}
	created, ok := r.(api.CreateJoinToken201JSONResponse)
	if !ok {
		t.Fatalf("建立失敗: %#v", r)
	}
	c := audited("join_token_created")
	if len(c) != 1 || c[0]["code_id"] != created.Id || c[0]["node_id"] != "node-j" || c[0]["token_hash"] != tokenHashOf(created.Token) {
		t.Fatalf("建立稽核不對: %v", c)
	}
	if b, _ := json.Marshal(c[0]); strings.Contains(string(b), created.Token) {
		t.Fatalf("稽核漏出 token: %s", b)
	}
	if _, err := s.RevokeJoinToken(ctx, api.RevokeJoinTokenRequestObject{Id: created.Id}); err != nil {
		t.Fatal(err)
	}
	if v := audited("join_token_revoked"); len(v) != 1 || v[0]["code_id"] != created.Id || v[0]["token_hash"] != c[0]["token_hash"] || v[0]["node_id"] != "node-j" {
		t.Fatalf("撤銷稽核不對: %v", v)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO nodes (id, org, name) VALUES ('node-x', 'test', '舊名')`); err != nil {
		t.Fatal(err)
	}
	name := "新名"
	if _, err := s.SetNodeName(ctx, api.SetNodeNameRequestObject{Id: "node-x", Body: &api.SetNodeNameJSONRequestBody{Name: &name}}); err != nil {
		t.Fatal(err)
	}
	n := audited("node_name_set")
	if len(n) != 1 || *(n[0]["before"].(map[string]any)["name"].(*string)) != "舊名" || *(n[0]["after"].(map[string]any)["name"].(*string)) != "新名" {
		t.Fatalf("改名稽核不對: %v", n)
	}
}

// Pending and expired tokens can be revoked (expired ones are still audited); already joined or
// already revoked gives 409 with a failed audit entry; nonexistent gives 404.
func TestRevokeJoinTokenStates(t *testing.T) {
	s, pool := accessTestServer(t)
	s.org = "test"
	audited := recordAudit(t)
	ctx := asAdmin(context.Background())
	create := func(node string) string {
		t.Helper()
		r, err := s.CreateJoinToken(ctx, api.CreateJoinTokenRequestObject{Body: &api.CreateJoinTokenJSONRequestBody{NodeId: node}})
		if err != nil {
			t.Fatal(err)
		}
		return r.(api.CreateJoinToken201JSONResponse).Id
	}
	revoke := func(id string) api.RevokeJoinTokenResponseObject {
		t.Helper()
		r, err := s.RevokeJoinToken(ctx, api.RevokeJoinTokenRequestObject{Id: id})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	pending, expired, used := create("node-p"), create("node-e"), create("node-u")
	if _, err := pool.Exec(ctx, `UPDATE join_tokens SET expires_at = now() - interval '1 minute' WHERE id = $1`, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE join_tokens SET status = 'used' WHERE id = $1`, used); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{pending, expired} {
		if r, ok := revoke(id).(api.RevokeJoinToken200JSONResponse); !ok || r.Status != api.JoinTokenStatusRevoked {
			t.Fatalf("%s 應能撤銷: %#v", id, r)
		}
	}
	if v := audited("join_token_revoked"); len(v) != 2 || v[1]["code_id"] != expired {
		t.Fatalf("過期的撤銷也要記稽核: %v", v)
	}
	for _, id := range []string{used, pending} { // pending already revoked
		if r, ok := revoke(id).(api.RevokeJoinToken409JSONResponse); !ok {
			t.Fatalf("%s 應 409: %#v", id, r)
		}
	}
	// The refusals are recorded too, so trying to revoke a code a node already joined with is visible
	v := audited("join_token_revoked")
	if len(v) != 4 || v[2]["code_id"] != used || v[2]["result"] != "failed" || v[2]["reason"] != string(api.ErrorReasonJoinTokenNotRevocable) ||
		v[3]["code_id"] != pending || v[3]["result"] != "failed" {
		t.Fatalf("409 也要記失敗稽核: %v", v)
	}
	if _, ok := revoke("jt-nope").(api.RevokeJoinToken404JSONResponse); !ok {
		t.Fatal("不存在應 404")
	}
}

// A node keeps one usable join code: creating another revokes the earlier unused one (recorded as
// superseded by the new code), and unjoining revokes the code made for rejoining it, so the freed
// id can't be joined again with it.
func TestJoinTokenSupersedeAndUnjoin(t *testing.T) {
	s, pool := accessTestServer(t)
	s.org = "test"
	audited := recordAudit(t)
	ctx := asAdmin(context.Background())
	create := func(node string) string {
		t.Helper()
		r, err := s.CreateJoinToken(ctx, api.CreateJoinTokenRequestObject{Body: &api.CreateJoinTokenJSONRequestBody{NodeId: node}})
		if err != nil {
			t.Fatal(err)
		}
		return r.(api.CreateJoinToken201JSONResponse).Id
	}
	status := func(id string) string {
		t.Helper()
		tk, err := s.store.GetJoinToken(ctx, id)
		if err != nil || tk == nil {
			t.Fatal(id, err)
		}
		return tk.Status
	}

	first := create("node-s")
	second := create("node-s")
	if status(first) != "revoked" || status(second) != "pending" {
		t.Fatalf("first=%s second=%s", status(first), status(second))
	}
	if v := audited("join_token_revoked"); len(v) != 1 || v[0]["code_id"] != first || v[0]["reason"] != "superseded" || v[0]["superseded_by"] != second || v[0]["actor"] != "root" {
		t.Fatalf("superseded entry: %v", v)
	}
	if c := audited("join_token_created"); len(c) != 2 || c[1]["code_id"] != second || c[1]["actor"] != "root" {
		t.Fatalf("created entries: %v", c)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO nodes (id, org) VALUES ('node-s', 'test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteNode(ctx, api.DeleteNodeRequestObject{Id: "node-s"}); err != nil {
		t.Fatal(err)
	}
	if status(second) != "revoked" {
		t.Fatalf("unjoin left the rejoin code %s", status(second))
	}
	if v := audited("join_token_revoked"); len(v) != 2 || v[1]["code_id"] != second || v[1]["reason"] != "node_deleted" || v[1]["actor"] != "root" {
		t.Fatalf("unjoin entry: %v", v)
	}
}

// Revoke races a join: the handler read the code as pending, but a join marks it used before the
// revoke's update lands. The revoke must answer 409 with a failed entry and leave the code used,
// not report a node that did join as revoked.
func TestRevokeJoinTokenLosesToJoin(t *testing.T) {
	s, pool := accessTestServer(t)
	s.org = "test"
	audited := recordAudit(t)
	ctx := asAdmin(context.Background())
	r, err := s.CreateJoinToken(ctx, api.CreateJoinTokenRequestObject{Body: &api.CreateJoinTokenJSONRequestBody{NodeId: "node-r"}})
	if err != nil {
		t.Fatal(err)
	}
	id := r.(api.CreateJoinToken201JSONResponse).Id

	// The join holds the row: the revoke still reads it as pending, then blocks on its update.
	join, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer join.Rollback(ctx)
	if _, err := join.Exec(ctx, `UPDATE join_tokens SET status = 'used' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	type result struct {
		r   api.RevokeJoinTokenResponseObject
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := s.RevokeJoinToken(ctx, api.RevokeJoinTokenRequestObject{Id: id})
		done <- result{r, err}
	}()
	for deadline := time.Now().Add(5 * time.Second); ; {
		var waiting int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query LIKE 'UPDATE join_tokens SET status = ''revoked''%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("revoke never reached its update")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := join.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if _, ok := got.r.(api.RevokeJoinToken409JSONResponse); !ok {
		t.Fatalf("want 409, got %#v", got.r)
	}
	if tk, _ := s.store.GetJoinToken(ctx, id); tk == nil || tk.Status != "used" {
		t.Fatalf("code must stay used: %+v", tk)
	}
	if v := audited("join_token_revoked"); len(v) != 2 || v[1]["result"] != "failed" || v[1]["reason"] != string(api.ErrorReasonJoinTokenNotRevocable) {
		t.Fatalf("revoke entries: %v", v)
	}
}

func tokenHashOf(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Saving the allowlist with a stale version: returns 409, writes no audit, and leaves the list
// unchanged.
func TestSetPolicyStaleRevision(t *testing.T) {
	s, _ := accessTestServer(t)
	audited := recordAudit(t)
	ctx := asAdmin(context.Background())
	a := "0x" + strings.Repeat("a", 40)
	if _, err := s.store.SavePermissioningPolicy(ctx, store.SharedPolicy, []string{a}, 0); err != nil {
		t.Fatal(err)
	}
	r, err := s.SetPermissioningPolicy(ctx, api.SetPermissioningPolicyRequestObject{Body: &api.SetPermissioningPolicyJSONRequestBody{AccountsAllowlist: []string{"0x" + strings.Repeat("b", 40)}, Revision: 0}})
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := r.(api.SetPermissioningPolicy409JSONResponse); !ok || c.Reason == nil || *c.Reason != api.ErrorReasonPolicyStale {
		t.Fatalf("stale revision: %#v", r)
	}
	if len(audited("permissioning_policy_set")) != 0 {
		t.Fatal("stale save was audited")
	}
	if all, _ := s.store.ListPermissioningPolicies(ctx); all[store.SharedPolicy].Revision != 1 || all[store.SharedPolicy].AccountsAllowlist[0] != a {
		p := all[store.SharedPolicy]
		t.Fatalf("policy changed: %+v", p)
	}
}

// A fixed-condition rule given a recovery threshold gets 400 (the 400 for a recovery threshold in
// the wrong direction on create is tested with the Enterprise create handler); slow block production accepts
// only a multiplier (at least 1); omitted recovery wait and recovery threshold stay as they were.
func TestAlertRuleClearAndKeepFiringValidation(t *testing.T) {
	s, _ := accessTestServer(t)
	s.rulesSvc = alerting.NewRulesService(s.store, t.TempDir(), "")
	ctx := asAdmin(context.Background())
	if err := s.rulesSvc.SeedDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	bad := float32(95)
	byPreset := func(p string) store.AlertRule {
		r, err := s.store.GetAlertRuleByPreset(ctx, p)
		if err != nil || r == nil {
			t.Fatalf("%s: %v", p, err)
		}
		return *r
	}
	off := byPreset("NodeOffline")
	if resp, _ := s.UpdateAlertRule(ctx, api.UpdateAlertRuleRequestObject{Id: off.ID, Body: &api.UpdateAlertRuleJSONRequestBody{
		Name: off.Name, ClearThreshold: &bad, ForDuration: api.AlertRuleDurationN2m, Severity: api.AlertRuleUpdateRequestSeverityCritical,
	}}); func() bool {
		r, ok := resp.(api.UpdateAlertRule400JSONResponse)
		return !ok || !hasReason(api.Error(r), api.ErrorReasonRuleConditionFixed)
	}() {
		t.Fatalf("條件固定的規則帶恢復門檻應 400: %#v", resp)
	}
	slow := byPreset("ChainSlow")
	slowUpdate := func(th *float32, metric *api.MetricId) api.UpdateAlertRuleResponseObject {
		resp, _ := s.UpdateAlertRule(ctx, api.UpdateAlertRuleRequestObject{Id: slow.ID, Body: &api.UpdateAlertRuleJSONRequestBody{
			Name: slow.Name, Threshold: th, Metric: metric, ForDuration: api.AlertRuleDurationN1m, Severity: api.AlertRuleUpdateRequestSeverityWarning,
		}})
		return resp
	}
	half, eight, cpuMetric := float32(0.5), float32(8), api.Cpu
	for name, c := range map[string]struct {
		resp api.UpdateAlertRuleResponseObject
		want api.ErrorReason
	}{
		"沒帶倍數":     {slowUpdate(nil, nil), api.ErrorReasonChainSlowFieldsLocked},
		"倍數 0.5":   {slowUpdate(&half, nil), api.ErrorReasonChainSlowThresholdMin},
		"帶 metric": {slowUpdate(&eight, &cpuMetric), api.ErrorReasonChainSlowFieldsLocked},
	} {
		if r, ok := c.resp.(api.UpdateAlertRule400JSONResponse); !ok || !hasReason(api.Error(r), c.want) {
			t.Fatalf("ChainSlow %s 應 400 %s: %#v", name, c.want, c.resp)
		}
	}
	if r := slowUpdate(&half, nil).(api.UpdateAlertRule400JSONResponse); (*r.Params)["min"] != "1" {
		t.Fatalf("倍數下限 params: %#v", r.Params)
	}
	if got, ok := slowUpdate(&eight, nil).(api.UpdateAlertRule200JSONResponse); !ok || got.Kind != api.AlertRuleKindChainSlow || got.Threshold == nil || *got.Threshold != 8 {
		t.Fatalf("ChainSlow 改倍數 8")
	}
	// Changing only the trigger threshold (the current rule editor): the recovery wait stays; the
	// new threshold 80 crosses the old recovery threshold 85, so the recovery threshold resets to
	// the trigger threshold
	cpu := byPreset("NodeCpuHigh")
	m, op, th := api.Cpu, api.Gt, float32(80)
	resp2, _ := s.UpdateAlertRule(ctx, api.UpdateAlertRuleRequestObject{Id: cpu.ID, Body: &api.UpdateAlertRuleJSONRequestBody{
		Name: cpu.Name, Metric: &m, Operator: &op, Threshold: &th, ForDuration: api.AlertRuleDurationN5m, Severity: api.AlertRuleUpdateRequestSeverityWarning,
	}})
	got, ok := resp2.(api.UpdateAlertRule200JSONResponse)
	if !ok || got.KeepFiringFor != api.AlertRuleDurationN2m || got.ClearThreshold != nil {
		t.Fatalf("只改門檻: %#v", resp2)
	}
	// Threshold out of range, changing the metric of a default rule, missing threshold field, rule
	// does not exist
	update := func(id string, metric *api.MetricId, th *float32) api.UpdateAlertRuleResponseObject {
		r, _ := s.UpdateAlertRule(ctx, api.UpdateAlertRuleRequestObject{Id: id, Body: &api.UpdateAlertRuleJSONRequestBody{
			Name: cpu.Name, Metric: metric, Operator: &op, Threshold: th, ForDuration: api.AlertRuleDurationN5m, Severity: api.AlertRuleUpdateRequestSeverityWarning,
		}})
		return r
	}
	over, disk := float32(150), api.Disk
	for name, c := range map[string]struct {
		resp api.UpdateAlertRuleResponseObject
		want api.ErrorReason
	}{
		"門檻 150":  {update(cpu.ID, &m, &over), api.ErrorReasonThresholdOutOfRange},
		"預設規則改指標": {update(cpu.ID, &disk, &th), api.ErrorReasonPresetRuleFieldsLocked},
		"沒帶門檻":    {update(cpu.ID, &m, nil), api.ErrorReasonThresholdRuleFieldsRequired},
	} {
		if r, ok := c.resp.(api.UpdateAlertRule400JSONResponse); !ok || !hasReason(api.Error(r), c.want) {
			t.Fatalf("%s 應 400 %s: %#v", name, c.want, c.resp)
		}
	}
	if r, ok := update("nope", &m, &th).(api.UpdateAlertRule404JSONResponse); !ok || !hasReason(api.Error(r), api.ErrorReasonRuleNotFound) {
		t.Fatalf("不存在的規則應 404 rule_not_found")
	}
}

// customRule stores a user-added threshold rule directly (Core has no create API).
func customRule(t *testing.T, s *Server, name string, th float64) store.AlertRule {
	t.Helper()
	r := store.AlertRule{ID: name, Name: name, Metric: string(api.Cpu), Operator: string(api.Gt), Threshold: th,
		ForDuration: "5m", KeepFiringFor: "0s", Severity: "warning", Enabled: true, CreatedBy: "root", UpdatedBy: "root"}
	if _, err := s.rulesSvc.SaveRule(context.Background(), r, true); err != nil {
		t.Fatal(err)
	}
	return r
}
