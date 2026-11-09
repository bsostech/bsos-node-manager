package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/auth"
	"besu-manager/backend/internal/store"
)

func asUser(name string) context.Context {
	return auth.WithPrincipal(context.Background(), &auth.Principal{Username: &name, Role: "admin"})
}

func sessionCount(t *testing.T, pool *pgxpool.Pool, user string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE username=$1 AND local`, user).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// recordAudit swaps the audit writer for an in-memory recorder during tests (Core does not write
// audits otherwise). It changes a package variable, so tests that use it cannot call t.Parallel.
func recordAudit(t *testing.T) func(event string) []map[string]any {
	var mu sync.Mutex
	got := map[string][]map[string]any{}
	store.AuditSink = func(_ context.Context, _ store.AuditDB, event string, detail map[string]any) error {
		mu.Lock()
		defer mu.Unlock()
		got[event] = append(got[event], detail)
		return nil
	}
	t.Cleanup(func() { store.AuditSink = nil })
	return func(event string) []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return got[event]
	}
}

// Create (system-generated password), role change syncs sessions, disabling logs out, password
// reset logs out, safeguards for the last admin and for acting on yourself, delete.
func TestLocalUserManagement(t *testing.T) {
	s, pool := accessTestServer(t)
	audited := recordAudit(t)
	s.auth = auth.New(s.store, "test", false)
	root := asUser("root")
	hash, _ := auth.HashPassword("root-password-123")
	if err := s.store.CreateLocalUser(context.Background(), store.LocalUser{Username: "root", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatal(err)
	}

	res, err := s.CreateLocalUser(root, api.CreateLocalUserRequestObject{Body: &api.LocalUserCreateRequest{Username: "alice", Role: api.Viewer}})
	if err != nil {
		t.Fatal(err)
	}
	created, ok := res.(api.CreateLocalUser201JSONResponse)
	if !ok || created.GeneratedPassword == nil || !created.User.MustChangePassword {
		t.Fatalf("create %+v", res)
	}
	if r, _ := s.CreateLocalUser(root, api.CreateLocalUserRequestObject{Body: &api.LocalUserCreateRequest{Username: "alice", Role: api.Viewer}}); r == nil {
		t.Fatal("nil")
	} else if e, ok := r.(api.CreateLocalUser409JSONResponse); !ok || e.Reason == nil || *e.Reason != api.ErrorReasonUserExists {
		t.Fatalf("重複應 409、reason user_exists: %+v", r)
	}
	if r, _ := s.CreateLocalUser(root, api.CreateLocalUserRequestObject{Body: &api.LocalUserCreateRequest{Username: "ALICE", Role: api.Viewer}}); r == nil {
		t.Fatal("nil")
	} else if _, ok := r.(api.CreateLocalUser409JSONResponse); !ok {
		t.Fatalf("大小寫不同也算重複,應 409: %+v", r)
	}

	// alice logs in: the session carries "must change password"
	sess, err := s.auth.Authenticate(context.Background(), "alice", *created.GeneratedPassword)
	if err != nil || !sess.MustChangePassword {
		t.Fatalf("login %+v %v", sess, err)
	}
	// An IdP session with the same name (alice logged in via LDAP) is unaffected by local account
	// management
	ssoName := "alice"
	sso := store.Session{Token: "sso-alice", Username: &ssoName, Name: "alice", Role: "operator", Org: "test", ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.store.CreateSession(context.Background(), sso); err != nil {
		t.Fatal(err)
	}
	// Acting on yourself, last admin
	if r, _ := s.UpdateLocalUser(root, api.UpdateLocalUserRequestObject{Username: "root", Body: &api.LocalUserUpdateRequest{Disabled: true}}); r == nil {
		t.Fatal("nil")
	} else if e, ok := r.(api.UpdateLocalUser409JSONResponse); !ok || e.Reason == nil || *e.Reason != api.ErrorReasonCannotDisableSelf {
		t.Fatalf("停用自己應 409、reason cannot_disable_self: %+v", r)
	}
	if r, _ := s.UpdateLocalUser(root, api.UpdateLocalUserRequestObject{Username: "Root", Body: &api.LocalUserUpdateRequest{Disabled: true}}); r == nil {
		t.Fatal("nil")
	} else if _, ok := r.(api.UpdateLocalUser409JSONResponse); !ok {
		t.Fatalf("停用自己(大小寫不同)應 409: %+v", r)
	}
	if r, _ := s.UpdateLocalUser(asUser("alice"), api.UpdateLocalUserRequestObject{Username: "root", Body: &api.LocalUserUpdateRequest{Disabled: true}}); r == nil {
		t.Fatal("nil")
	} else if e, ok := r.(api.UpdateLocalUser409JSONResponse); !ok || e.Reason == nil || *e.Reason != api.ErrorReasonLastAdmin {
		t.Fatalf("停用最後一個 admin 應 409、reason last_admin: %+v", r)
	}
	if r, _ := s.DeleteLocalUser(asUser("alice"), api.DeleteLocalUserRequestObject{Username: "root"}); r == nil {
		t.Fatal("nil")
	} else if _, ok := r.(api.DeleteLocalUser409JSONResponse); !ok {
		t.Fatalf("刪最後一個 admin 應 409: %+v", r)
	}
	// Disable: all sessions logged out
	if r, _ := s.UpdateLocalUser(root, api.UpdateLocalUserRequestObject{Username: "alice", Body: &api.LocalUserUpdateRequest{Disabled: true}}); r == nil {
		t.Fatal("nil")
	}
	if n := sessionCount(t, pool, "alice"); n != 0 {
		t.Fatalf("停用後還有 %d 個 session", n)
	}
	if got, _ := s.store.GetSession(context.Background(), "sso-alice"); got == nil {
		t.Fatal("IdP session 不該被登出")
	}
	// Password reset: logged out + forced change next time
	_, _ = s.UpdateLocalUser(root, api.UpdateLocalUserRequestObject{Username: "alice", Body: &api.LocalUserUpdateRequest{Disabled: false}})
	r2, _ := s.ResetLocalUserPassword(root, api.ResetLocalUserPasswordRequestObject{Username: "alice", Body: &api.PasswordResetRequest{}})
	reset, ok := r2.(api.ResetLocalUserPassword200JSONResponse)
	if !ok || reset.GeneratedPassword == nil {
		t.Fatalf("reset %+v", r2)
	}
	if u, _ := s.store.GetLocalUser(context.Background(), "alice"); !u.MustChangePassword {
		t.Fatal("重設後應強制改密碼")
	}
	// Delete
	if r, _ := s.DeleteLocalUser(root, api.DeleteLocalUserRequestObject{Username: "alice"}); r == nil {
		t.Fatal("nil")
	} else if _, ok := r.(api.DeleteLocalUser204Response); !ok {
		t.Fatalf("delete %+v", r)
	}
	// The audit carries the actor; record first, then act, so the blocked attempt (alice deleting
	// root: last admin) adds a failed entry
	d := audited("user_deleted")
	if len(d) != 3 || d[1]["result"] != "failed" || d[1]["reason"] != "last_admin" || d[2]["actor"] != "root" || d[2]["username"] != "alice" || d[2]["result"] != nil {
		t.Fatalf("user_deleted 稽核 %v", d)
	}
}

// Changing your own password: rules apply, other sessions are logged out, the current one is kept
// and its "can only change password" restriction lifted.
func TestChangePasswordSessions(t *testing.T) {
	s, _ := accessTestServer(t)
	s.auth = auth.New(s.store, "test", false)
	ctx := context.Background()
	hash, _ := auth.HashPassword("initial-password-1")
	_ = s.store.CreateLocalUser(ctx, store.LocalUser{Username: "carol", PasswordHash: hash, Role: "viewer", MustChangePassword: true})
	a, _ := s.auth.Authenticate(ctx, "carol", "initial-password-1")
	b, _ := s.auth.Authenticate(ctx, "carol", "initial-password-1")
	if err := s.auth.ChangePassword(ctx, "carol", a.Token, "initial-password-1", "carol-is-weak-1", nil); err == nil {
		t.Fatal("含帳號的密碼應被擋")
	}
	var pwErr *auth.PasswordError
	if err := s.auth.ChangePassword(ctx, "carol", a.Token, "initial-password-1", "initial-password-1", nil); !errors.As(err, &pwErr) || pwErr.Reason != api.ErrorReasonPasswordUnchanged {
		t.Fatalf("沿用目前密碼應被擋: %v", err)
	}
	if got, _ := s.store.GetSession(ctx, a.Token); got == nil || !got.MustChangePassword {
		t.Fatal("沿用被擋後仍須改密碼")
	}
	if err := s.auth.ChangePassword(ctx, "carol", a.Token, "initial-password-1", "a much better passphrase", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.store.GetSession(ctx, b.Token); got != nil {
		t.Fatal("其他 session 應登出")
	}
	if got, _ := s.store.GetSession(ctx, a.Token); got == nil || got.MustChangePassword {
		t.Fatalf("目前 session 應保留並解除: %+v", got)
	}
}

// Middleware: a session that has not changed its password is only allowed to change password / read
// itself / log out; a session idle past the timeout is treated as not logged in.
func TestSessionGateAndIdle(t *testing.T) {
	s, pool := accessTestServer(t)
	s.auth = auth.New(s.store, "test", false)
	ctx := context.Background()
	hash, _ := auth.HashPassword("initial-password-1")
	_ = s.store.CreateLocalUser(ctx, store.LocalUser{Username: "dave", PasswordHash: hash, Role: "admin", MustChangePassword: true})
	sess, err := s.auth.Authenticate(ctx, "dave", "initial-password-1")
	if err != nil {
		t.Fatal(err)
	}
	h := s.auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	call := func(method, path string) int {
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(s.auth.SessionCookie(sess.Token))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if c := call("GET", "/api/v1/nodes"); c != http.StatusForbidden {
		t.Fatalf("未改密碼應 403,得 %d", c)
	}
	if c := call("GET", "/api/v1/me"); c != http.StatusOK {
		t.Fatalf("/me 應放行,得 %d", c)
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET must_change_password=false, last_seen_at=now()-interval '31 minutes' WHERE token=$1`, sess.Token); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/nodes", nil)
	r.AddCookie(s.auth.SessionCookie(sess.Token))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("閒置逾時應 401,得 %d", w.Code)
	}
	if cs := w.Result().Cookies(); len(cs) != 1 || cs[0].MaxAge >= 0 {
		t.Fatalf("閒置逾時應清掉 cookie: %+v", cs)
	}
}

// Default admin: uses BOOTSTRAP_ADMIN_PASSWORD if set, otherwise a random one (never a fixed
// admin/admin); both must change the password on first login.
func TestBootstrapAdminPassword(t *testing.T) {
	s, _ := accessTestServer(t)
	s.auth = auth.New(s.store, "test", false)
	ctx := context.Background()
	t.Setenv("BOOTSTRAP_ADMIN_PASSWORD", "env-initial-password")
	if err := s.auth.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	sess, err := s.auth.Authenticate(ctx, "admin", "env-initial-password")
	if err != nil || !sess.MustChangePassword {
		t.Fatalf("env 密碼登入 %+v %v", sess, err)
	}
	if _, err := s.auth.Authenticate(ctx, "admin", "admin"); err == nil {
		t.Fatal("admin/admin 不該能登入")
	}
}

// Two admins disabling each other at the same time: the check and the change are under the same
// lock, so only one side can succeed.
func TestLastAdminConcurrent(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	for _, u := range []string{"x", "y"} {
		if err := s.store.CreateLocalUser(ctx, store.LocalUser{Username: u, PasswordHash: "h", Role: "admin"}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, u := range []string{"x", "y"} {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = s.store.SetLocalUserDisabled(ctx, u, true) }()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, store.ErrLastAdmin) {
			t.Fatal(err)
		}
	}
	if ok != 1 {
		t.Fatalf("應只有一邊成功,得 %d", ok)
	}
}

// Login successes and failures both go into the audit; there is no lockout, so the correct password
// still works after several wrong ones.
func TestLoginAudit(t *testing.T) {
	s, _ := accessTestServer(t)
	audited := recordAudit(t)
	s.auth = auth.New(s.store, "test", false)
	ctx := context.Background()
	hash, _ := auth.HashPassword("frank-initial-pw")
	_ = s.store.CreateLocalUser(ctx, store.LocalUser{Username: "frank", PasswordHash: hash, Role: "viewer"})
	login := func(pw string) api.LoginResponseObject {
		r, err := s.Login(ctx, api.LoginRequestObject{Body: &api.LoginJSONRequestBody{Username: "frank", Password: pw}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for i := 0; i < 6; i++ {
		if _, ok := login("wrong-password-x").(api.Login401JSONResponse); !ok {
			t.Fatal("錯密碼應 401")
		}
	}
	if _, ok := login("frank-initial-pw").(loginResponse); !ok {
		t.Fatal("正確密碼應登入")
	}
	for event, want := range map[string]int{"login": 1, "login_failed": 6} {
		n := 0
		for _, d := range audited(event) {
			if d["username"] == "frank" {
				n++
			}
		}
		if n != want {
			t.Fatalf("%s 稽核 %d 筆,應 %d", event, n, want)
		}
	}

	// Nonexistent account (e.g. password typed into the username field): what was typed is not
	// recorded; an existing account with different case is recorded as usual
	for _, typed := range []string{"frank-initial-pw", "not a username!", "FRANK"} {
		_, _ = s.Login(ctx, api.LoginRequestObject{Body: &api.LoginJSONRequestBody{Username: typed, Password: "x"}})
	}
	got := audited("login_failed")
	tail := got[len(got)-3:]
	if tail[0]["username"] != unknownUsername || tail[1]["username"] != unknownUsername || tail[2]["username"] != "frank" {
		t.Fatalf("帳號不存在時記了 %v", tail)
	}
}

// BOOTSTRAP_ADMIN_PASSWORD unset: random password, admin/admin does not get in.
func TestBootstrapRandomPassword(t *testing.T) {
	s, _ := accessTestServer(t)
	s.auth = auth.New(s.store, "test", false)
	t.Setenv("BOOTSTRAP_ADMIN_PASSWORD", "")
	if err := s.auth.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.store.GetLocalUser(context.Background(), "admin"); u == nil || !u.MustChangePassword {
		t.Fatalf("bootstrap %+v", u)
	}
	if _, err := s.auth.Authenticate(context.Background(), "admin", "admin"); err == nil {
		t.Fatal("admin/admin 不該能登入")
	}
}

// Account changes and session handling are in the same transaction: if the session step fails, the
// account must not change either.
func TestUserMutationAtomic(t *testing.T) {
	s, pool := accessTestServer(t)
	s.auth = auth.New(s.store, "test", false)
	ctx := context.Background()
	for _, u := range []store.LocalUser{{Username: "root", PasswordHash: "h", Role: "admin"}, {Username: "gina", PasswordHash: "old-hash", Role: "operator"}} {
		if err := s.store.CreateLocalUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION fail_sessions() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'boom'; END $$;
		CREATE TRIGGER fail_sessions BEFORE UPDATE OR DELETE ON sessions FOR EACH STATEMENT EXECUTE FUNCTION fail_sessions();`); err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetLocalUserDisabled(ctx, "gina", true); err == nil {
		t.Fatal("停用應失敗")
	}
	if err := s.store.ResetLocalPassword(ctx, "gina", "new-hash"); err == nil {
		t.Fatal("重設應失敗")
	}
	if err := s.store.DeleteLocalUser(ctx, "gina"); err == nil {
		t.Fatal("刪除應失敗")
	}
	u, _ := s.store.GetLocalUser(ctx, "gina")
	if u == nil || u.Disabled || u.Role != "operator" || u.PasswordHash != "old-hash" || u.MustChangePassword {
		t.Fatalf("帳號不該被改: %+v", u)
	}
	if err := s.store.SetLocalUserDisabled(ctx, "nobody", true); !errors.Is(err, store.ErrLocalUserNotFound) {
		t.Fatalf("不存在應回 ErrLocalUserNotFound: %v", err)
	}
	// PATCH and reset without a body: 400
	if r, _ := s.UpdateLocalUser(asUser("root"), api.UpdateLocalUserRequestObject{Username: "gina"}); r == nil {
		t.Fatal("nil")
	} else if _, ok := r.(api.UpdateLocalUser400JSONResponse); !ok {
		t.Fatalf("空 PATCH 應 400: %+v", r)
	}
	if r, _ := s.ResetLocalUserPassword(asUser("root"), api.ResetLocalUserPasswordRequestObject{Username: "gina"}); r == nil {
		t.Fatal("nil")
	} else if _, ok := r.(api.ResetLocalUserPassword400JSONResponse); !ok {
		t.Fatalf("重設不帶 body 應 400: %+v", r)
	}
}

// BOOTSTRAP_ADMIN_PASSWORD must also pass the password rules; otherwise startup fails, no account
// is created, and the error message does not include the password.
func TestBootstrapRejectsWeakEnvPassword(t *testing.T) {
	s, _ := accessTestServer(t)
	s.auth = auth.New(s.store, "test", false)
	ctx := context.Background()
	for _, pw := range []string{"admin", "short", "my-admin-password", "qwerty123456"} {
		t.Setenv("BOOTSTRAP_ADMIN_PASSWORD", pw)
		err := s.auth.Bootstrap(ctx)
		if err == nil {
			t.Fatalf("%q 應被拒", pw)
		}
		if strings.Contains(err.Error(), pw) {
			t.Fatalf("錯誤訊息帶了密碼: %v", err)
		}
	}
	if n, _ := s.store.CountLocalUsers(ctx); n != 0 {
		t.Fatalf("不該建帳號,有 %d 個", n)
	}
}

// fakeSSO records whether it was asked; it always returns viewer.
type fakeSSO struct{ calls int }

func (f *fakeSSO) Authenticate(_ context.Context, u, _ string) (auth.Identity, error) {
	f.calls++
	return auth.Identity{Account: u, Name: "SSO User"}, nil
}
func (f *fakeSSO) RoleFor(context.Context, []string) (string, error) { return "viewer", nil }
func (f *fakeSSO) LoginURL() string                                  { return "" }

// If the account exists locally only local is consulted: being disabled or a wrong password never
// falls through to an SSO user with the same name; SSO is used only when there is no local account.
func TestLocalAccountShadowsSSO(t *testing.T) {
	s, _ := accessTestServer(t)
	s.auth = auth.New(s.store, "test", false)
	ctx := context.Background()
	hash, _ := auth.HashPassword("carl-password-1")
	if err := s.store.CreateLocalUser(ctx, store.LocalUser{Username: "carl", PasswordHash: hash, Role: "viewer", Disabled: true}); err != nil {
		t.Fatal(err)
	}
	sso := &fakeSSO{}
	s.auth.SetSSO(sso)
	if _, err := s.auth.Authenticate(ctx, "carl", "carl-password-1"); err == nil {
		t.Fatal("停用的本地帳號不該登入")
	}
	if _, err := s.auth.Authenticate(ctx, "carl", "anything"); err == nil {
		t.Fatal("本地帳號密碼錯不該登入")
	}
	if sso.calls != 0 {
		t.Fatalf("本地有這個帳號時不該問 SSO,問了 %d 次", sso.calls)
	}
	sess, err := s.auth.Authenticate(ctx, "sam", "whatever")
	if err != nil || sso.calls != 1 || sess.Local {
		t.Fatalf("本地查無應走 SSO:sess=%+v err=%v calls=%d", sess, err, sso.calls)
	}
}

// newViewer creates a viewer account and sets its password to a known value (temporary passwords
// are always system-generated, and tests need a fixed password to log in).
func newViewer(ctx context.Context, s *Server, username, pw string) error {
	if _, err := s.CreateLocalUser(ctx, api.CreateLocalUserRequestObject{Body: &api.LocalUserCreateRequest{Username: username, Role: api.Viewer}}); err != nil {
		return err
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	return s.store.ResetLocalPassword(context.Background(), username, hash)
}

// Temporary password delivery: shown by default; when mailing, success does not return the password
// and failure falls back to showing it with the reason; no email gives 400, no mail server
// configured gives 409. Reset follows the same rules, and accounts without an email cannot be
// mailed. email accepts only the bare address.
func TestLocalUserPasswordDelivery(t *testing.T) {
	s, _ := accessTestServer(t)
	audited := recordAudit(t)
	root := asUser("root")
	create := func(username string, email *string, send bool) api.CreateLocalUserResponseObject {
		t.Helper()
		r, err := s.CreateLocalUser(root, api.CreateLocalUserRequestObject{Body: &api.LocalUserCreateRequest{Username: username, Role: api.Viewer, Email: email, SendEmail: &send}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	addr := func(v string) *string { return &v }

	// No mail server configured
	if r, ok := create("amy", addr("amy@org-a.example"), false).(api.CreateLocalUser201JSONResponse); !ok || r.Delivery != api.PasswordDeliveryShown || r.GeneratedPassword == nil || r.User.Email == nil || *r.User.Email != "amy@org-a.example" {
		t.Fatalf("預設顯示 %+v", r)
	}
	if e, ok := create("ben", addr("ben@org-a.example"), true).(api.CreateLocalUser409JSONResponse); !ok || e.Reason == nil || *e.Reason != api.ErrorReasonAccountMailNotConfigured {
		t.Fatal("沒設郵件伺服器卻要寄應 409、reason account_mail_not_configured")
	}
	if e, ok := create("bob", addr("Bob <bob@org-a.example>"), false).(api.CreateLocalUser400JSONResponse); !ok || e.Reason == nil || *e.Reason != api.ErrorReasonInvalidEmail {
		t.Fatal("email 帶顯示名稱應 400、reason invalid_email")
	}
	if r, _ := s.GetAccountMailStatus(root, api.GetAccountMailStatusRequestObject{}); r.(api.GetAccountMailStatus200JSONResponse).Configured {
		t.Fatal("沒設應為 false")
	}

	var sent []string
	var fail error
	s.sendAccountMail = func(_ context.Context, to, subject, body string) error {
		if fail != nil {
			return fail
		}
		sent = append(sent, to+"|"+body)
		return nil
	}
	if r, _ := s.GetAccountMailStatus(root, api.GetAccountMailStatusRequestObject{}); !r.(api.GetAccountMailStatus200JSONResponse).Configured {
		t.Fatal("有設應為 true")
	}
	if _, ok := create("cat", nil, true).(api.CreateLocalUser400JSONResponse); !ok {
		t.Fatal("要寄卻沒填 email 應 400")
	}
	for _, bad := range []string{"Cat <cat@org-a.example>", "cat@org-a.example (x)", "not-an-email"} {
		if _, ok := create("cat", addr(bad), false).(api.CreateLocalUser400JSONResponse); !ok {
			t.Fatalf("%q 應 400", bad)
		}
	}
	r := create("cat", addr(" cat@org-a.example "), true).(api.CreateLocalUser201JSONResponse)
	if r.Delivery != api.PasswordDeliveryEmail || r.GeneratedPassword != nil || len(sent) != 1 || !strings.HasPrefix(sent[0], "cat@org-a.example|") || !strings.Contains(sent[0], "cat") {
		t.Fatalf("寄成功 %+v %v", r, sent)
	}
	fail = errors.New("連不上")
	r = create("dan", addr("dan@org-a.example"), true).(api.CreateLocalUser201JSONResponse)
	// Mail failure: the reason carries a code (operation_failed + raw text when unrecognized), and
	// error is an English description
	if r.Delivery != api.PasswordDeliveryShown || r.GeneratedPassword == nil || r.MailReason == nil || *r.MailReason != api.ErrorReasonOperationFailed ||
		r.MailParams == nil || (*r.MailParams)["detail"] != "連不上" || r.MailError == nil || *r.MailError != "Internal error (連不上)" {
		t.Fatalf("寄失敗退回顯示 %+v", r)
	}
	// The audit stores the code and params, not the raw sentence
	var mailAudit map[string]any
	for _, d := range audited("user_created") {
		if d["username"] == "dan" && d["mail_error"] != nil {
			mailAudit = d
		}
	}
	if p, _ := mailAudit["mail_error_params"].(map[string]string); mailAudit == nil || mailAudit["mail_error"] != string(api.ErrorReasonOperationFailed) || p["detail"] != "連不上" {
		t.Fatalf("寄失敗的稽核 %v", mailAudit)
	}
	if u, _ := s.store.GetLocalUser(context.Background(), "dan"); u == nil {
		t.Fatal("寄失敗帳號照樣建立")
	}

	// Reset
	fail = nil
	send := true
	rr, _ := s.ResetLocalUserPassword(root, api.ResetLocalUserPasswordRequestObject{Username: "cat", Body: &api.PasswordResetRequest{SendEmail: &send}})
	if res, ok := rr.(api.ResetLocalUserPassword200JSONResponse); !ok || res.Delivery != api.PasswordDeliveryEmail || res.GeneratedPassword != nil || res.Email == nil || len(sent) != 2 {
		t.Fatalf("重設寄信 %+v", rr)
	}
	create("eve", nil, false)
	if rr, _ := s.ResetLocalUserPassword(root, api.ResetLocalUserPasswordRequestObject{Username: "eve", Body: &api.PasswordResetRequest{SendEmail: &send}}); rr == nil {
		t.Fatal("nil")
	} else if _, ok := rr.(api.ResetLocalUserPassword400JSONResponse); !ok {
		t.Fatalf("沒有 email 不能寄 %+v", rr)
	}
	rr, _ = s.ResetLocalUserPassword(root, api.ResetLocalUserPasswordRequestObject{Username: "eve", Body: &api.PasswordResetRequest{}})
	if res, ok := rr.(api.ResetLocalUserPassword200JSONResponse); !ok || res.Delivery != api.PasswordDeliveryShown || res.GeneratedPassword == nil || res.Email != nil {
		t.Fatalf("重設預設顯示 %+v", rr)
	}
	s.sendAccountMail = nil
	if rr, _ := s.ResetLocalUserPassword(root, api.ResetLocalUserPasswordRequestObject{Username: "cat", Body: &api.PasswordResetRequest{SendEmail: &send}}); rr == nil {
		t.Fatal("nil")
	} else if _, ok := rr.(api.ResetLocalUserPassword409JSONResponse); !ok {
		t.Fatalf("沒設郵件伺服器重設要寄應 409 %+v", rr)
	}
}

// Account and login errors all carry a reason with an English message; rejected attempts store the
// code in the audit.
func TestAccountErrorReasons(t *testing.T) {
	s, _ := accessTestServer(t)
	audited := recordAudit(t)
	s.auth = auth.New(s.store, "test", false)
	ctx := context.Background()
	root := asUser("root")
	hash, _ := auth.HashPassword("root-password-123")
	for _, u := range []string{"root", "dan"} {
		if err := s.store.CreateLocalUser(ctx, store.LocalUser{Username: u, PasswordHash: hash, Role: "admin"}); err != nil {
			t.Fatal(err)
		}
	}
	sess, err := s.auth.Authenticate(ctx, "root", "root-password-123")
	if err != nil {
		t.Fatal(err)
	}
	name := "root"
	rootSession := auth.WithPrincipal(ctx, &auth.Principal{Token: sess.Token, Username: &name, Role: "admin"})
	ssoName := "ldap-alice"
	ssoSession := auth.WithPrincipal(ctx, &auth.Principal{Token: "sso-token", Username: &ssoName, Role: "viewer"})
	reasonOf := func(r any) api.ErrorReason {
		t.Helper()
		v := reflect.ValueOf(r)
		e, ok := v.Convert(reflect.TypeOf(api.Error{})).Interface().(api.Error)
		if !ok || e.Reason == nil || e.Message == "" || !isEnglishText(e.Message) {
			t.Fatalf("要帶 reason、英文 message: %+v", r)
		}
		return *e.Reason
	}
	for _, c := range []struct {
		got  any
		want api.ErrorReason
	}{
		{must(s.Login(ctx, api.LoginRequestObject{Body: &api.LoginJSONRequestBody{Username: "root", Password: "wrong-password-0"}})), api.ErrorReasonBadCredentials},
		{must(s.CreateLocalUser(root, api.CreateLocalUserRequestObject{Body: &api.LocalUserCreateRequest{Username: "zed", Role: api.Viewer, SendEmail: ptr(true)}})), api.ErrorReasonEmailRequiredToSend},
		{must(s.CreateLocalUser(root, api.CreateLocalUserRequestObject{Body: &api.LocalUserCreateRequest{Username: "root", Role: api.Viewer}})), api.ErrorReasonUserExists},
		{must(s.ResetLocalUserPassword(root, api.ResetLocalUserPasswordRequestObject{Username: "dan", Body: &api.ResetLocalUserPasswordJSONRequestBody{SendEmail: ptr(true)}})), api.ErrorReasonAccountHasNoEmail},
		{must(s.ResetLocalUserPassword(root, api.ResetLocalUserPasswordRequestObject{Username: "root", Body: &api.ResetLocalUserPasswordJSONRequestBody{}})), api.ErrorReasonCannotResetSelf},
		{must(s.DeleteLocalUser(root, api.DeleteLocalUserRequestObject{Username: "root"})), api.ErrorReasonCannotDeleteSelf},
		{must(s.ChangePassword(rootSession, api.ChangePasswordRequestObject{Body: &api.ChangePasswordJSONRequestBody{CurrentPassword: "wrong-password-0", NewPassword: "a much better passphrase"}})), api.ErrorReasonWrongCurrentPassword},
		// An SSO account has no local row: not_local_account, and no failed attempt is recorded (checked below)
		{must(s.ChangePassword(ssoSession, api.ChangePasswordRequestObject{Body: &api.ChangePasswordJSONRequestBody{CurrentPassword: "whatever-pass-0", NewPassword: "a much better passphrase"}})), api.ErrorReasonNotLocalAccount},
	} {
		if got := reasonOf(c.got); got != c.want {
			t.Errorf("reason %s, want %s", got, c.want)
		}
	}
	if d := audited("user_created"); len(d) != 2 || d[1]["result"] != "failed" || d[1]["reason"] != "user_exists" { // record the intent first, then add a failed entry when blocked
		t.Errorf("user_created 稽核 %v", d)
	}
	if d := audited("password_changed"); len(d) != 1 || d[0]["reason"] != "wrong_current_password" {
		t.Errorf("password_changed 稽核 %v", d)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// isEnglishText: ASCII only, plus the en dash and typographic apostrophe English messages use (same
// as the UI).
func isEnglishText(s string) bool {
	for _, r := range s {
		if r > 127 && r != '–' && r != '’' {
			return false
		}
	}
	return true
}
