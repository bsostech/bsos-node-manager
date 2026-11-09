package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/auth"
	"besu-manager/backend/internal/reqinfo"
	"besu-manager/backend/internal/store"
	"besu-manager/backend/internal/store/storetest"

	"github.com/go-chi/chi/v5"
)

func asAdmin(ctx context.Context) context.Context {
	root := "root"
	return auth.WithPrincipal(ctx, &auth.Principal{Username: &root, Role: "admin"})
}

// Audit cannot be written: the operation returns an error and leaves no effect in the database (one
// case each for accounts, RPC users, node settings, deleting nodes).
func TestAuditFailClosed(t *testing.T) {
	s, pool := accessTestServer(t)
	ctx := asAdmin(context.Background())
	if err := s.store.UpsertOwnNode(ctx, "n1", "org-A", 8545, 9545, nil); err != nil {
		t.Fatal(err)
	}
	storetest.FailAuditForTest(t)

	pw := "violet-tiger-lamp-42"
	if err := newViewer(ctx, s, "carol", pw); !errors.Is(err, errAudit) {
		t.Fatalf("新增使用者應因稽核失敗而失敗: %v", err)
	}
	if u, _ := s.store.GetLocalUser(ctx, "carol"); u != nil {
		t.Fatal("稽核失敗時不該建出使用者")
	}
	if _, err := s.CreateRpcUser(ctx, api.CreateRpcUserRequestObject{Body: &api.CreateRpcUserJSONRequestBody{Username: "dapp", Permissions: []string{"eth:*"}}}); !errors.Is(err, errAudit) {
		t.Fatalf("新增 RPC 使用者: %v", err)
	}
	if us, _ := s.store.ListRpcUsers(ctx); len(us) != 0 {
		t.Fatal("稽核失敗時不該建出 RPC 使用者")
	}
	if _, err := s.SetNodePorts(ctx, api.SetNodePortsRequestObject{Id: "n1", Body: &api.SetNodePortsJSONRequestBody{RpcPort: 18545, MetricsPort: 19545}}); !errors.Is(err, errAudit) {
		t.Fatalf("改埠: %v", err)
	}
	if n, _ := s.store.GetNode(ctx, "n1"); n == nil || n.RpcPort != 8545 {
		t.Fatalf("稽核失敗時埠不該變: %+v", n)
	}
	if _, err := s.DeleteNode(ctx, api.DeleteNodeRequestObject{Id: "n1"}); !errors.Is(err, errAudit) {
		t.Fatalf("刪節點: %v", err)
	}
	if n, _ := s.store.GetNode(ctx, "n1"); n == nil {
		t.Fatal("稽核失敗時節點不該被刪")
	}
	var sessions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("sessions = %d %v", sessions, err)
	}
}

// Record first, then act: add a failed entry when a business rule blocks it; add a done entry for
// information only known afterwards.
func TestAuditFirstRecordsOutcome(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := asAdmin(context.Background())
	if err := s.store.UpsertOwnNode(ctx, "n1", "org-A", 8545, 9545, nil); err != nil {
		t.Fatal(err)
	}
	events := storetest.SinkForTest(t, nil)
	body := &api.LocalUserCreateRequest{Username: "carol", Role: "viewer"}
	for range 2 {
		if _, err := s.CreateLocalUser(ctx, api.CreateLocalUserRequestObject{Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DeleteNode(ctx, api.DeleteNodeRequestObject{Id: "n1"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"user_created", "user_created:done", "user_created", "user_created:failed", "node_delete", "node_delete:done"}
	if !slices.Equal(*events, want) {
		t.Fatalf("events = %v, want %v", *events, want)
	}
}

// Login: the credentials are correct but the audit cannot be written, so the newly created session
// must be deleted.
func TestLoginFailClosed(t *testing.T) {
	s, pool := accessTestServer(t)
	ctx := context.Background()
	s.auth = auth.New(s.store, "test", false)
	pw := "violet-tiger-lamp-42"
	if err := newViewer(asAdmin(ctx), s, "carol", pw); err != nil {
		t.Fatal(err)
	}
	storetest.FailAuditForTest(t)
	if _, err := s.Login(ctx, api.LoginRequestObject{Body: &api.LoginJSONRequestBody{Username: "carol", Password: pw}}); !errors.Is(err, errAudit) {
		t.Fatalf("登入應因稽核失敗而失敗: %v", err)
	}
	var sessions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("稽核失敗時不該留下 session: %d %v", sessions, err)
	}
}

// Login records the login method and source IP; logout records an entry, and still logs out if the
// audit cannot be written (logout only lowers privileges).
func TestLoginMethodAndLogout(t *testing.T) {
	s, pool := accessTestServer(t)
	ctx := reqinfo.With(context.Background(), reqinfo.Info{IP: "198.51.100.7", UA: "test-agent"})
	s.auth = auth.New(s.store, "test", false)
	pw := "violet-tiger-lamp-42"
	if err := newViewer(asAdmin(context.Background()), s, "carol", pw); err != nil {
		t.Fatal(err)
	}
	audited := recordAudit(t)
	if _, err := s.Login(ctx, api.LoginRequestObject{Body: &api.LoginJSONRequestBody{Username: "carol", Password: pw}}); err != nil {
		t.Fatal(err)
	}
	l := audited("login")
	if len(l) != 1 || l[0]["method"] != "local" || l[0]["ip"] != "198.51.100.7" || l[0]["ua"] != "test-agent" {
		t.Fatalf("login 稽核不對: %v", l)
	}

	var token string
	if err := pool.QueryRow(ctx, `SELECT token FROM sessions WHERE username='carol'`).Scan(&token); err != nil {
		t.Fatal(err)
	}
	name := "carol"
	who := auth.WithPrincipal(ctx, &auth.Principal{Token: token, Username: &name, Role: "viewer"})
	if _, err := s.Logout(who, api.LogoutRequestObject{}); err != nil {
		t.Fatal(err)
	}
	if o := audited("logout"); len(o) != 1 || o[0]["actor"] != "carol" || o[0]["ip"] != "198.51.100.7" {
		t.Fatalf("logout 稽核不對: %v", o)
	}

	// Log in again, this time with the audit broken: logout must still delete the session
	store.AuditSink = nil
	if _, err := s.Login(ctx, api.LoginRequestObject{Body: &api.LoginJSONRequestBody{Username: "carol", Password: pw}}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT token FROM sessions WHERE username='carol'`).Scan(&token); err != nil {
		t.Fatal(err)
	}
	storetest.FailAuditForTest(t)
	who = auth.WithPrincipal(ctx, &auth.Principal{Token: token, Username: &name, Role: "viewer"})
	if _, err := s.Logout(who, api.LogoutRequestObject{}); err != nil {
		t.Fatal(err)
	}
	if n := sessionCount(t, pool, "carol"); n != 0 {
		t.Fatalf("稽核失敗時登出仍應刪 session: 剩 %d", n)
	}
}

type fakeLDAP struct{}

func (fakeLDAP) Authenticate(_ context.Context, u, p string) (auth.Identity, error) {
	if u != "dave" || p != "ldap-pass" {
		return auth.Identity{}, auth.ErrBadCredentials
	}
	return auth.Identity{Account: u, Name: u, Groups: []string{"g"}}, nil
}
func (fakeLDAP) RoleFor(context.Context, []string) (string, error) { return "viewer", nil }
func (fakeLDAP) LoginURL() string                                  { return "" }

// Password login that goes to LDAP records method=ldap.
func TestLoginMethodLDAP(t *testing.T) {
	s, _ := accessTestServer(t)
	s.auth = auth.New(s.store, "test", false)
	s.auth.SetSSO(fakeLDAP{})
	audited := recordAudit(t)
	if _, err := s.Login(context.Background(), api.LoginRequestObject{Body: &api.LoginJSONRequestBody{Username: "dave", Password: "ldap-pass"}}); err != nil {
		t.Fatal(err)
	}
	if l := audited("login"); len(l) != 1 || l[0]["method"] != "ldap" {
		t.Fatalf("LDAP 登入應記 method=ldap: %v", l)
	}
}

// Through the middleware: insufficient permission writes access_denied (with IP); being blocked for
// not having changed the initial password writes nothing.
func TestMiddlewareAccessDenied(t *testing.T) {
	s, pool := accessTestServer(t)
	ctx := context.Background()
	authSvc := auth.New(s.store, "test", false)
	s.auth = authSvc
	pw := "violet-tiger-lamp-42"
	for _, u := range []string{"vera", "newbie"} {
		if err := newViewer(asAdmin(ctx), s, u, pw); err != nil {
			t.Fatal(err)
		}
	}
	// vera has changed her password; newbie has not (accounts created by an admin must change it by
	// default)
	if _, err := pool.Exec(ctx, `UPDATE local_users SET must_change_password=false WHERE username='vera'`); err != nil {
		t.Fatal(err)
	}
	audited := recordAudit(t)
	// Wired the same way as main.go: global middleware blocks login and password change, and
	// contract route permissions are enforced by RequireScopes
	h := chi.NewRouter()
	h.Use(reqinfo.Middleware(nil), authSvc.Middleware)
	api.HandlerWithOptions(api.NewStrictHandler(s, nil), api.ChiServerOptions{BaseURL: "/api/v1", BaseRouter: h,
		Middlewares: []api.MiddlewareFunc{authSvc.RequireScopes(Scopes)}})
	call := func(user string) int {
		sess, err := authSvc.Authenticate(ctx, user, pw)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/join-tokens", nil) // viewer lacks nodes:onboard
		r.RemoteAddr = "198.51.100.7:4000"
		r.AddCookie(authSvc.SessionCookie(sess.Token))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if c := call("vera"); c != http.StatusForbidden {
		t.Fatalf("vera 應 403: %d", c)
	}
	d := audited("access_denied")
	if len(d) != 1 || d[0]["actor"] != "vera" || d[0]["ip"] != "198.51.100.7" || d[0]["path"] != "/api/v1/join-tokens" {
		t.Fatalf("access_denied 不對: %v", d)
	}
	if c := call("newbie"); c != http.StatusForbidden {
		t.Fatalf("newbie 應 403: %d", c)
	}
	if n := len(audited("access_denied")); n != 1 {
		t.Fatalf("還沒改密碼被擋不該寫 access_denied: %d 筆", n)
	}
}

// Password change writes only one entry, with the result: failure records failed, success records
// done (the success entry is in the same transaction as the password change). Audit cannot be
// written: the success path rolls back entirely and the password is unchanged; the rejected path
// also returns an error, so password guessing is never unrecorded.
func TestPasswordChangeRecordsOutcome(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	s.auth = auth.New(s.store, "test", false)
	pw := "violet-tiger-lamp-42"
	if err := newViewer(asAdmin(ctx), s, "carol", pw); err != nil {
		t.Fatal(err)
	}
	sess, err := s.auth.Authenticate(ctx, "carol", pw)
	if err != nil {
		t.Fatal(err)
	}
	name := "carol"
	who := auth.WithPrincipal(ctx, &auth.Principal{Token: sess.Token, Username: &name, Role: "viewer"})
	change := func(current, next string) error {
		_, err := s.ChangePassword(who, api.ChangePasswordRequestObject{Body: &api.ChangePasswordJSONRequestBody{CurrentPassword: current, NewPassword: next}})
		return err
	}

	storetest.FailAuditForTest(t)
	if err := change("wrong-password-0", "a much better passphrase"); !errors.Is(err, errAudit) {
		t.Fatalf("被拒那筆寫不進去應回 errAudit: %v", err)
	}
	if err := change(pw, "a much better passphrase"); !errors.Is(err, errAudit) {
		t.Fatalf("成功那筆寫不進去應回 errAudit: %v", err)
	}
	if _, err := s.auth.Authenticate(ctx, "carol", pw); err != nil {
		t.Fatalf("稽核寫不進去,密碼不該被改: %v", err)
	}

	events := storetest.SinkForTest(t, nil)
	if err := change("wrong-password-0", "a much better passphrase"); err != nil {
		t.Fatal(err)
	}
	if err := change(pw, "a much better passphrase"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"password_changed:failed", "password_changed:done"}; !slices.Equal(*events, want) {
		t.Fatalf("稽核不對: %v", *events)
	}
}

// scriptedSSO accepts any credentials (account dave); each RoleFor call returns the next entry in
// roles, and always returns err when err is non-nil. auths counts how many credential checks ran.
type scriptedSSO struct {
	roles []string
	err   error
	n     int
	auths int
}

func (f *scriptedSSO) Authenticate(context.Context, string, string) (auth.Identity, error) {
	f.auths++
	return auth.Identity{Account: "dave", Name: "Dave", Groups: []string{"g"}}, nil
}

func (f *scriptedSSO) RoleFor(context.Context, []string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	r := f.roles[min(f.n, len(f.roles)-1)]
	f.n++
	return r, nil
}
func (*scriptedSSO) LoginURL() string { return "" }

// Database error while looking up the role mapping: login returns an error (500) and is not
// recorded as a login failure.
func TestLoginSSODBErrorIsNotLoginFailed(t *testing.T) {
	s, _ := accessTestServer(t)
	s.auth = auth.New(s.store, "test", false)
	s.auth.SetSSO(&scriptedSSO{err: errors.New("role_mappings: connection refused")})
	audited := recordAudit(t)
	if _, err := s.Login(context.Background(), api.LoginRequestObject{Body: &api.LoginJSONRequestBody{Username: "dave", Password: "ldap-pass"}}); err == nil {
		t.Fatal("資料庫錯誤應回錯誤(500),不是 401")
	}
	if f := audited("login_failed"); len(f) != 0 {
		t.Fatalf("資料庫錯誤不該記 login_failed: %v", f)
	}
}

// The role mapping changes between the role lookup and session creation: the second lookup after
// the session is written sees a different role, so the session is deleted on the spot and the login
// fails.
func TestLoginSSORoleChangedMidLogin(t *testing.T) {
	s, pool := accessTestServer(t)
	s.auth = auth.New(s.store, "test", false)
	sso := &scriptedSSO{roles: []string{"admin", "viewer"}}
	s.auth.SetSSO(sso)
	recordAudit(t)
	r, err := s.Login(context.Background(), api.LoginRequestObject{Body: &api.LoginJSONRequestBody{Username: "dave", Password: "ldap-pass"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(api.Login401JSONResponse); !ok {
		t.Fatalf("角色在登入途中變了應拒絕: %T", r)
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE NOT local`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("不該留下 SSO session: n=%d err=%v", n, err)
	}
	if sso.auths != 1 {
		t.Fatalf("複查只該重查角色,不該再驗一次帳密(LDAP bind):驗了 %d 次", sso.auths)
	}
}

// The IdP verified the credentials but no group mapped to a role: login_failed records the verified
// username and the reason, not (unknown).
func TestLoginSSONoRoleRecordsAccount(t *testing.T) {
	s, _ := accessTestServer(t)
	s.auth = auth.New(s.store, "test", false)
	s.auth.SetSSO(&scriptedSSO{roles: []string{""}})
	audited := recordAudit(t)
	r, err := s.Login(context.Background(), api.LoginRequestObject{Body: &api.LoginJSONRequestBody{Username: "dave", Password: "ldap-pass"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(api.Login401JSONResponse); !ok {
		t.Fatalf("沒有角色應 401: %T", r)
	}
	if f := audited("login_failed"); len(f) != 1 || f[0]["username"] != "dave" || f[0]["reason"] != "no_role" {
		t.Fatalf("沒有角色的登入失敗應記下帳號與原因: %v", f)
	}
}

// Wrong credentials (not verified by the IdP): still recorded as (unknown), without what was typed.
func TestLoginSSOBadPasswordStaysUnknown(t *testing.T) {
	s, _ := accessTestServer(t)
	s.auth = auth.New(s.store, "test", false)
	s.auth.SetSSO(fakeLDAP{})
	audited := recordAudit(t)
	if _, err := s.Login(context.Background(), api.LoginRequestObject{Body: &api.LoginJSONRequestBody{Username: "dave", Password: "wrong"}}); err != nil {
		t.Fatal(err)
	}
	if f := audited("login_failed"); len(f) != 1 || f[0]["username"] != "(unknown)" {
		t.Fatalf("帳密錯應記 (unknown): %v", f)
	}
}
