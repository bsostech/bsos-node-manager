package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/auth"
	"besu-manager/backend/internal/reqinfo"
	"besu-manager/backend/internal/store/storetest"
)

// loginRows returns all current login records (newest first), with a nil username written as
// "<nil>" and a nil reason as "".
func loginRows(t *testing.T, s *Server) []string {
	t.Helper()
	r, err := s.ListLoginEvents(context.Background(), api.ListLoginEventsRequestObject{Params: api.ListLoginEventsParams{Since: time.Now().Add(-time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range r.(api.ListLoginEvents200JSONResponse).Events {
		u, why := "<nil>", ""
		if e.Username != nil {
			u = *e.Username
		}
		if e.Reason != nil {
			why = string(*e.Reason)
		}
		out = append(out, u+"/"+string(e.Result)+"/"+why+"@"+e.SourceIp)
	}
	return out
}

func login(t *testing.T, s *Server, ctx context.Context, user, pw string) {
	t.Helper()
	if _, err := s.Login(ctx, api.LoginRequestObject{Body: &api.LoginJSONRequestBody{Username: user, Password: pw}}); err != nil {
		t.Fatal(err)
	}
}

// A local account role this edition does not have (the open-source edition hitting an auditor left
// by the commercial edition): no session even with the right password; records no_role.
func TestLoginUnknownRole(t *testing.T) {
	s, pool := accessTestServer(t)
	recordAudit(t)
	ctx := reqinfo.With(context.Background(), reqinfo.Info{IP: "198.51.100.7"})
	s.auth = auth.New(s.store, "test", false)
	pw := "violet-tiger-lamp-42"
	if err := newViewer(asAdmin(context.Background()), s, "dave", pw); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE local_users SET role = 'auditor' WHERE username = 'dave'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.auth.Authenticate(ctx, "dave", pw); err == nil {
		t.Fatal("未知角色不該登入成功")
	}
	login(t, s, ctx, "dave", pw)
	login(t, s, ctx, "dave", "wrong-password-1") // wrong password is checked first: reveal nothing about roles
	want := []string{"dave/failure/bad_password@198.51.100.7", "dave/failure/no_role@198.51.100.7"}
	if got := loginRows(t, s); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v", got)
	}
}

// Local accounts: one entry each for success, wrong password, nonexistent account (what was typed
// is not recorded), disabled (correct password), disabled with a wrong password (password checked
// first, recorded as wrong password); source IP comes from reqinfo.
func TestLoginEventsLocal(t *testing.T) {
	s, pool := accessTestServer(t)
	recordAudit(t)
	ctx := reqinfo.With(context.Background(), reqinfo.Info{IP: "198.51.100.7"})
	s.auth = auth.New(s.store, "test", false)
	pw := "violet-tiger-lamp-42"
	for _, u := range []string{"carol", "erin"} {
		if err := newViewer(asAdmin(context.Background()), s, u, pw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE local_users SET disabled = true WHERE username = 'erin'`); err != nil {
		t.Fatal(err)
	}
	login(t, s, ctx, "Carol", pw)                 // different case: same person
	login(t, s, ctx, "carol", "wrong-password-1") // wrong password
	login(t, s, ctx, "hunter2-typed-as-name", pw) // nonexistent account: do not record what was typed
	login(t, s, ctx, "erin", pw)                  // disabled
	login(t, s, ctx, "erin", "wrong-password-1")  // disabled and wrong password
	want := []string{
		"erin/failure/bad_password@198.51.100.7",
		"erin/failure/disabled@198.51.100.7",
		"<nil>/failure/unknown_user@198.51.100.7",
		"carol/failure/bad_password@198.51.100.7",
		"carol/success/@198.51.100.7",
	}
	if got := loginRows(t, s); len(got) != len(want) {
		t.Fatalf("got %v", got)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("#%d got %s, want %s", i, got[i], want[i])
			}
		}
	}
}

type failingSSO struct{ err error }

func (f failingSSO) Authenticate(context.Context, string, string) (auth.Identity, error) {
	return auth.Identity{}, f.err
}
func (failingSSO) RoleFor(context.Context, []string) (string, error) { return "viewer", nil }
func (failingSSO) LoginURL() string                                  { return "" }

// SSO: a wrong LDAP password records the username (lowercased), an unknown account records null;
// ErrBadCredentials without a reason (connector is OIDC) is treated as a nonexistent account; no
// role, and a role changed mid-login, both record no_role.
func TestLoginEventsSSO(t *testing.T) {
	for _, tc := range []struct {
		name string
		sso  auth.SSO
		want string
	}{
		{"ldap bad bind", failingSSO{&auth.LoginFailure{Reason: auth.FailBadPassword, Username: "Dave "}}, "dave/failure/bad_password@"},
		{"ldap not found", failingSSO{&auth.LoginFailure{Reason: auth.FailUnknownUser}}, "<nil>/failure/unknown_user@"},
		{"oidc connector", failingSSO{auth.ErrBadCredentials}, "<nil>/failure/unknown_user@"},
		{"no role", &scriptedSSO{roles: []string{""}}, "dave/failure/no_role@"},
		{"role changed mid-login", &scriptedSSO{roles: []string{"admin", "viewer"}}, "dave/failure/no_role@"},
		{"success", fakeLDAP{}, "dave/success/@"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := accessTestServer(t)
			recordAudit(t)
			s.auth = auth.New(s.store, "test", false)
			s.auth.SetSSO(tc.sso)
			user := "Dave"
			if _, ok := tc.sso.(fakeLDAP); ok {
				user = "dave" // fakeLDAP accepts lowercase only
			}
			login(t, s, context.Background(), user, "ldap-pass")
			if got := loginRows(t, s); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("got %v, want [%s]", got, tc.want)
			}
		})
	}
}

// Audit cannot be written, login did not take effect: no success entry is kept. Login record cannot
// be written: login still succeeds.
func TestLoginEventsOrderingAndFailOpen(t *testing.T) {
	s, pool := accessTestServer(t)
	s.auth = auth.New(s.store, "test", false)
	pw := "violet-tiger-lamp-42"
	if err := newViewer(asAdmin(context.Background()), s, "carol", pw); err != nil {
		t.Fatal(err)
	}
	storetest.FailAuditForTest(t)
	if _, err := s.Login(context.Background(), api.LoginRequestObject{Body: &api.LoginJSONRequestBody{Username: "carol", Password: pw}}); !errors.Is(err, errAudit) {
		t.Fatalf("稽核寫不進去應登入失敗: %v", err)
	}
	if got := loginRows(t, s); len(got) != 0 {
		t.Fatalf("沒生效的登入不該留成功紀錄: %v", got)
	}
	recordAudit(t)
	if _, err := pool.Exec(context.Background(), `DROP TABLE login_events`); err != nil {
		t.Fatal(err)
	}
	r, err := s.Login(context.Background(), api.LoginRequestObject{Body: &api.LoginJSONRequestBody{Username: "carol", Password: pw}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(loginResponse); !ok {
		t.Fatalf("登入紀錄寫不進去也要能登入: %T", r)
	}
}

// Query: missing since gives 400; filters by username and result; exactly 500 rows is not
// truncated, over 500 rows is truncated and returns only the newest 500; rows before since are not
// returned; rows older than 90 days are purged on the next write; invalid result gives 400.
func TestListLoginEvents(t *testing.T) {
	s, pool := accessTestServer(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO login_events (at, username, result, reason)
		SELECT now() - make_interval(secs => g), 'bulk', 'failure', 'bad_password' FROM generate_series(1, 500) g`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO login_events (at, username, result) VALUES (now() - interval '91 days', 'old', 'success'), (now() - interval '2 hours', 'carol', 'success')`); err != nil {
		t.Fatal(err)
	}
	list := func(p api.ListLoginEventsParams) api.LoginEventList {
		t.Helper()
		r, err := s.ListLoginEvents(ctx, api.ListLoginEventsRequestObject{Params: p})
		if err != nil {
			t.Fatal(err)
		}
		return api.LoginEventList(r.(api.ListLoginEvents200JSONResponse))
	}
	hour := time.Now().Add(-time.Hour)
	if l := list(api.ListLoginEventsParams{Since: hour}); l.Truncated || len(l.Events) != 500 {
		t.Fatalf("剛好 500 筆不算截斷:truncated=%v len=%d", l.Truncated, len(l.Events))
	}
	if _, err := pool.Exec(ctx, `INSERT INTO login_events (at, username, result, reason) VALUES (now() - interval '501 seconds', 'bulk', 'failure', 'bad_password')`); err != nil {
		t.Fatal(err)
	}
	if l := list(api.ListLoginEventsParams{Since: hour}); !l.Truncated || len(l.Events) != 500 || l.Events[0].At.Before(l.Events[499].At) {
		t.Fatalf("超過 500 筆:truncated=%v len=%d", l.Truncated, len(l.Events))
	}
	carol, success := "carol", api.LoginResultSuccess
	if l := list(api.ListLoginEventsParams{Since: time.Now().Add(-3 * time.Hour), Username: &carol, Result: &success}); l.Truncated || len(l.Events) != 1 || *l.Events[0].Username != "carol" {
		t.Fatalf("篩選:%+v", l)
	}
	if l := list(api.ListLoginEventsParams{Since: hour, Username: &carol}); len(l.Events) != 0 {
		t.Fatalf("since 之前的不該回: %+v", l)
	}
	if r, _ := s.ListLoginEvents(ctx, api.ListLoginEventsRequestObject{}); r == nil {
		t.Fatal("沒帶 since 應 400")
	} else if _, ok := r.(api.ListLoginEvents400Response); !ok {
		t.Fatalf("沒帶 since 應 400: %T", r)
	}
	bad := api.LoginResult("maybe")
	if r, _ := s.ListLoginEvents(ctx, api.ListLoginEventsRequestObject{Params: api.ListLoginEventsParams{Since: hour, Result: &bad}}); r == nil {
		t.Fatal("result 不合法應 400")
	} else if _, ok := r.(api.ListLoginEvents400Response); !ok {
		t.Fatalf("result 不合法應 400: %T", r)
	}
	if err := s.store.RecordLoginEvent(ctx, &carol, ""); err != nil {
		t.Fatal(err)
	}
	var old int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM login_events WHERE username = 'old'`).Scan(&old); err != nil || old != 0 {
		t.Fatalf("90 天前的應在寫入時清掉: %d %v", old, err)
	}
}
