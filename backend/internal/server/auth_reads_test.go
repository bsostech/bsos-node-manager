package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/auth"
	"besu-manager/backend/internal/store"
	"besu-manager/backend/internal/version"
)

// oidcSSO is a redirect-based SSO: it never takes passwords and has a login URL.
type oidcSSO struct{}

func (oidcSSO) Authenticate(context.Context, string, string) (auth.Identity, error) {
	return auth.Identity{}, auth.ErrBadCredentials
}
func (oidcSSO) RoleFor(context.Context, []string) (string, error) { return "", nil }
func (oidcSSO) LoginURL() string                                  { return "https://idp.example/authorize" }

// /auth/config decides what the login page shows: Core is password only; with OIDC on and local
// login off there is only the SSO button; password-based SSO (LDAP) keeps the password form even
// with local login off, and only then is ldapLogin set.
func TestGetAuthConfig(t *testing.T) {
	s := New(Deps{})
	s.auth = auth.New(nil, "org-test", false)
	get := func() api.GetAuthConfig200JSONResponse {
		t.Helper()
		r, err := s.GetAuthConfig(context.Background(), api.GetAuthConfigRequestObject{})
		if err != nil {
			t.Fatal(err)
		}
		return r.(api.GetAuthConfig200JSONResponse)
	}
	if c := get(); !c.PasswordLogin || c.LdapLogin || c.SsoLoginUrl != nil {
		t.Fatalf("Core 只有帳密: %+v", c)
	}
	s.auth.SetSSO(oidcSSO{})
	s.auth.SetLocalEnabled(false)
	if c := get(); c.PasswordLogin || c.LdapLogin || c.SsoLoginUrl == nil || *c.SsoLoginUrl != "https://idp.example/authorize" {
		t.Fatalf("OIDC 且關本地登入應只有 SSO 按鈕: %+v", c)
	}
	s.auth.SetSSO(fakeLDAP{})
	if c := get(); !c.PasswordLogin || !c.LdapLogin || c.SsoLoginUrl != nil {
		t.Fatalf("LDAP 走帳密表單: %+v", c)
	}
}

// /me through the real session middleware: identity and role come from the session, a local
// account that has not changed its initial password gets mustChangePassword, and no session gives
// 401 not_logged_in. A session last seen more than a minute ago has its activity time moved
// forward (idle timeout restarts); one seen within the minute is not written again.
func TestGetMeThroughSession(t *testing.T) {
	s, pool := accessTestServer(t)
	s.auth = auth.New(s.store, "org-test", false)
	ctx := context.Background()
	hash, _ := auth.HashPassword("initial-password-1")
	if err := s.store.CreateLocalUser(ctx, store.LocalUser{Username: "dave", PasswordHash: hash, Role: "operator", MustChangePassword: true}); err != nil {
		t.Fatal(err)
	}
	sess, err := s.auth.Authenticate(ctx, "dave", "initial-password-1")
	if err != nil {
		t.Fatal(err)
	}
	h := s.auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res, err := s.GetMe(r.Context(), api.GetMeRequestObject{})
		if err != nil {
			t.Error(err)
			return
		}
		switch v := res.(type) {
		case api.GetMe200JSONResponse:
			_ = json.NewEncoder(w).Encode(v)
		case api.GetMe401JSONResponse:
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(v)
		}
	}))
	lastSeen := func() time.Time {
		t.Helper()
		var at time.Time
		if err := pool.QueryRow(ctx, `SELECT last_seen_at FROM sessions WHERE token=$1`, sess.Token).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	callMe := func() api.Me {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
		r.AddCookie(s.auth.SessionCookie(sess.Token))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("/me 應 200,得 %d %s", w.Code, w.Body)
		}
		var me api.Me
		if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
			t.Fatal(err)
		}
		return me
	}

	if _, err := pool.Exec(ctx, `UPDATE sessions SET last_seen_at = now() - interval '5 minutes' WHERE token=$1`, sess.Token); err != nil {
		t.Fatal(err)
	}
	before := lastSeen()
	me := callMe()
	if me.User.Id != "dave" || me.Role != api.Operator || me.Org != "org-test" || me.MustChangePassword == nil || !*me.MustChangePassword {
		t.Fatalf("/me 內容不對: %+v", me)
	}
	touched := lastSeen()
	if time.Since(touched) > time.Minute || !touched.After(before) {
		t.Fatalf("超過一分鐘沒動的 session 應更新活動時間: before=%v after=%v", before, touched)
	}
	callMe()
	if again := lastSeen(); !again.Equal(touched) {
		t.Fatalf("一分鐘內不該再寫: %v -> %v", touched, again)
	}

	// After the password is changed the flag is gone
	if _, err := pool.Exec(ctx, `UPDATE local_users SET must_change_password=false WHERE username='dave'`); err != nil {
		t.Fatal(err)
	}
	if me := callMe(); me.MustChangePassword != nil {
		t.Fatalf("改過密碼不該帶 mustChangePassword: %+v", me)
	}

	r, err := s.GetMe(ctx, api.GetMeRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := r.(api.GetMe401JSONResponse); !ok || e.Code != api.ErrorCodeUnauthorized || !hasReason(api.Error(e), api.ErrorReasonNotLoggedIn) {
		t.Fatalf("沒登入應 401 not_logged_in: %#v", r)
	}
}

// /me carries the build version for the About dialog; a release date only for an exact release.
func TestMeCarriesBuildVersion(t *testing.T) {
	defer func(v, d string) { version.Version, version.ReleaseDate = v, d }(version.Version, version.ReleaseDate)
	s := New(Deps{})

	version.Version, version.ReleaseDate = "v1.0.0-3-gabc1234", ""
	if me := s.meWithFlags(context.Background(), nil, api.Me{}); me.Version != "v1.0.0-3-gabc1234" || me.ReleaseDate != nil {
		t.Fatalf("untagged build: want version and no date, got %q %v", me.Version, me.ReleaseDate)
	}

	version.Version, version.ReleaseDate = "v1.0.0", "2026-11-10"
	me := s.meWithFlags(context.Background(), nil, api.Me{})
	if me.Version != "v1.0.0" || me.ReleaseDate == nil || *me.ReleaseDate != "2026-11-10" {
		t.Fatalf("release build: want v1.0.0 2026-11-10, got %q %v", me.Version, me.ReleaseDate)
	}
}
