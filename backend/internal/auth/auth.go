// Package auth implements platform AuthN + RBAC:
// server-side sessions (the cookie holds an opaque token), the bootstrap admin, local accounts,
// a chi middleware that "resolves identity first, then requires login", and RequireScopes, which enforces the scopes declared in the contract.
// SSO and group -> role resolution live in Enterprise and plug in through the SSO interface, SetExtraPublic and SetRolePerms.
// Frontend gating is only UX; the real enforcement is here.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/store"
)

const cookieName = "bsos_session"

// ── Permission table (kept in sync with the frontend's auth/permissions.ts) ──────────────

type Permission string

const (
	PermMonitorView    Permission = "monitor:view"
	PermValidatorsView Permission = "validators:view"
	PermNodesOnboard   Permission = "nodes:onboard"
	PermMembersManage  Permission = "members:manage"

	// PermLoggedIn: the operation explicitly requires only login (the contract scope says logged-in). Operations with no declared scope are always rejected,
	// so a forgotten entry never lets a request through.
	PermLoggedIn Permission = "logged-in"

	// permUndeclared is the permission field in the audit entry when an operation with no declared permission is rejected. No role ever has it.
	permUndeclared Permission = "undeclared"
)

// rolePerms maps role -> permissions. Core has three roles; Enterprise replaces the whole table with SetRolePerms at startup.
// admin manages accounts only; it neither sees monitoring nor touches nodes.
var rolePerms = map[string][]Permission{
	"viewer":   {PermMonitorView, PermValidatorsView},
	"operator": {PermMonitorView, PermValidatorsView, PermNodesOnboard},
	"admin":    {PermMembersManage},
}

// SetRolePerms replaces the whole role table. Call it only once during startup wiring: roleCan reads the table without locking.
func SetRolePerms(m map[string][]Permission) { rolePerms = m }

// ValidRole reports whether this build has the role (checked when creating / editing a local account).
func ValidRole(role string) bool {
	_, ok := rolePerms[role]
	return ok
}

func roleCan(role string, p Permission) bool {
	for _, x := range rolePerms[role] {
		if x == p {
			return true
		}
	}
	return false
}

// ── principal (the identity of this request) + context ──────────────────

type Principal struct {
	Token    string // this request's session token (so logout can delete the row)
	Username *string
	Account  *string // SSO display account (preferred_username / email / LDAP login name); Username is the identifier and may not be human-readable
	Name     string
	Role     string
	Org      string
}

type ctxKey struct{}

func principalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}

// PrincipalFrom returns the current logged-in principal for handlers (already set by the middleware).
func PrincipalFrom(ctx context.Context) *Principal { return principalFrom(ctx) }

// WithPrincipal puts the principal into the context (used by the middleware; handler tests also use it to simulate different roles).
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// Can reports whether the current principal has a permission. Used when one route decides how many fields to return by role (the real route enforcement is in RequireScopes).
func (p *Principal) Can(perm Permission) bool { return p != nil && roleCan(p.Role, perm) }

// ── Service ──────────────────────────────────────────────────

// SSO is Enterprise's single sign-on (LDAP / OIDC). Core has no implementation, so sso is always nil.
// Credential checks and role lookup are separate: after login writes the session the role is looked up again, and that lookup must not re-verify credentials.
type SSO interface {
	// Authenticate verifies credentials against a password-based SSO (LDAP) and returns the identity from the IdP. Wrong credentials return ErrBadCredentials; redirect-based SSO (OIDC) always returns ErrBadCredentials.
	Authenticate(ctx context.Context, username, password string) (Identity, error)
	// RoleFor maps groups -> platform role; "" = no role matched. A failed lookup (database error) returns an error and must not be treated as no match.
	RoleFor(ctx context.Context, groups []string) (string, error)
	// LoginURL is the login entry point for redirect-based SSO; password-based SSO returns "".
	LoginURL() string
}

// Identity is an identity verified by the IdP. Account is the human-readable account (LDAP login name; OIDC preferred_username / email).
type Identity struct {
	Account string
	Name    string
	Groups  []string
}

// UnknownAccount is the account recorded when login fails before the user is known (account rules forbid parentheses, so it cannot collide with a real account).
const UnknownAccount = "(unknown)"

// NoRoleError: the IdP verified the credentials, but the groups matched no role.
type NoRoleError struct{ Account string }

func (e *NoRoleError) Error() string { return "no role mapping for " + e.Account }

type Service struct {
	store  *store.Store
	org    string
	ttl    time.Duration // absolute session lifetime
	idle   time.Duration // idle timeout
	secure bool

	// routeMatch reports whether a method+path hits any registered route (it wraps chi Router.Match,
	// injected by main.go as a closure so this package does not depend on chi directly). Only GET/HEAD with no matching route counts as public,
	// because those are guaranteed to land in backend/internal/webapp: the frontend SPA fallback (embedded files, fixed at compile time, no secrets),
	// or the fixed 404 JSON under /api/.
	// Any real route added later (e.g. an accidental /debug/pprof) still requires login and is not let through by this rule.
	// nil = this pass-through is disabled (the conservative default; it only takes effect once main.go wires it).
	routeMatch func(method, path string) bool

	// Enterprise's public routes (SetExtraPublic): consulted only when Core's isPublic does not match. nil = none.
	extraPublic func(method, path string) bool

	// The fields below are changed at runtime by Enterprise's SSO settings and guarded by mu; in Core they are always sso=nil, localEnabled=true.
	mu           sync.RWMutex
	sso          SSO
	localEnabled bool

	denied deniedLog // throttle for permission-denied audits
}

// New creates the service (local login enabled, no SSO).
func New(st *store.Store, org string, secure bool) *Service {
	return &Service{store: st, org: org, ttl: 12 * time.Hour, idle: 30 * time.Minute,
		secure: secure, localEnabled: true}
}

// SetRouteMatch wires the route lookup (see the Service.routeMatch comment). main.go calls it once after all routes are registered.
func (s *Service) SetRouteMatch(f func(method, path string) bool) {
	s.routeMatch = f
}

// SetExtraPublic wires Enterprise's public-route check (called once during startup wiring).
func (s *Service) SetExtraPublic(public func(method, path string) bool) {
	s.extraPublic = public
}

// SetSSO installs (or, with nil, removes) the SSO; Enterprise's settings page changes it live.
func (s *Service) SetSSO(sso SSO) {
	s.mu.Lock()
	s.sso = sso
	s.mu.Unlock()
}

// SetLocalEnabled turns local account login on or off. Persistence and the "cannot disable without SSO" guard live in Enterprise; Core never calls it.
func (s *Service) SetLocalEnabled(enabled bool) {
	s.mu.Lock()
	s.localEnabled = enabled
	s.mu.Unlock()
}

// PasswordLogin reports whether the login page shows the username/password form (local accounts enabled, or a password-based SSO is configured).
func (s *Service) PasswordLogin() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.localEnabled || (s.sso != nil && s.sso.LoginURL() == "")
}

// LDAPLogin reports whether the password form also goes through a password-based SSO (LDAP), so a
// rejected sign-in may be a group that maps to no role rather than a wrong password.
func (s *Service) LDAPLogin() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sso != nil && s.sso.LoginURL() == ""
}

// SSOLoginURL is where the login page's SSO button redirects; "" without a redirect-based SSO.
func (s *Service) SSOLoginURL() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.sso == nil {
		return ""
	}
	return s.sso.LoginURL()
}

// IssueSession opens a session for redirect-based SSO (the OIDC callback) after the identity is verified.
func (s *Service) IssueSession(ctx context.Context, sub string, id Identity, role string) (*store.Session, error) {
	return s.makeSession(ctx, sub, id, role, nil)
}

// CookieSecure: the SSO's own short-lived cookies (OIDC flow) must use the same Secure setting as the session cookie.
func (s *Service) CookieSecure() bool { return s.secure }

// Bootstrap creates the default administrator admin when the DB has no local accounts (password change forced on first login). The password comes from BOOTSTRAP_ADMIN_PASSWORD;
// if unset, one is generated randomly and printed to the log, so there is never a fixed default credential.
func (s *Service) Bootstrap(ctx context.Context) error {
	n, err := s.store.CountLocalUsers(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	const defaultUser = "admin"
	defaultPass, generated := os.Getenv("BOOTSTRAP_ADMIN_PASSWORD"), false
	if defaultPass == "" {
		defaultPass, generated = GeneratePassword(defaultUser), true
	} else if err := ValidatePassword(defaultUser, defaultPass); err != nil {
		return fmt.Errorf("BOOTSTRAP_ADMIN_PASSWORD does not meet the password rules: %w", err) // the error message states only the reason, never the password
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(defaultPass), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := s.store.CreateLocalUser(ctx, store.LocalUser{
		Username: defaultUser, PasswordHash: string(hash), Role: "admin", IsBootstrap: true,
		MustChangePassword: true, // force a password change on first login (Grafana style)
	}); err != nil {
		return err
	}
	log.Printf("════════ default admin ════════")
	log.Printf("  username: %s", defaultUser)
	if generated {
		log.Printf("  password: %s", defaultPass)
	} else {
		log.Printf("  password: (from BOOTSTRAP_ADMIN_PASSWORD)") // a password supplied by the operator is never printed to the log
	}
	log.Printf("  ⚠ the first sign-in forces a password change; do it before the platform is exposed, so nobody else changes it first")
	log.Printf("════════════════════════════")
	return nil
}

// ErrBadCredentials: wrong credentials, a disabled account, or groups that match no role (handlers always return 401 without revealing which).
var ErrBadCredentials = errors.New("bad credentials")

// Reasons a login failed (for the login log; values match the contract's LoginFailReason).
const (
	FailBadPassword = "bad_password"
	FailUnknownUser = "unknown_user"
	FailDisabled    = "disabled"
	FailNoRole      = "no_role"
)

// LoginFailure holds the reason and account of a failed password login (account empty if it does not exist), for the login log. errors.Is(err, ErrBadCredentials) still holds,
// and the external response and response time do not vary with the reason.
type LoginFailure struct {
	Reason   string
	Username string
}

func (e *LoginFailure) Error() string        { return "bad credentials: " + e.Reason }
func (e *LoginFailure) Is(target error) bool { return target == ErrBadCredentials }

// LoginName normalizes an account name: trims surrounding whitespace and lowercases it. Local accounts are stored this way already; SSO (AD) is case-insensitive, and login log filtering is an exact match.
func LoginName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// dummyHash: run bcrypt even when the account does not exist, so the response time does not reveal whether it exists.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-password-for-timing"), bcrypt.DefaultCost)

// Authenticate checks local accounts first when local login is enabled; if a local account exists, only the local one counts (a wrong password or disabled account is rejected outright,
// with no fallback to SSO). Only when no such account exists, or local login is off, does it go to SSO (Enterprise only).
// This way a disabled local account cannot be bypassed by an SSO account with the same name, and identical names on both sides never confuse who is logging in.
// No lockout on failed logins: bcrypt itself rate-limits, and passwords are 12+ characters with common passwords blocked.
func (s *Service) Authenticate(ctx context.Context, username, password string) (*store.Session, error) {
	now := time.Now()
	sess, err := s.authenticate(ctx, username, password)
	if err != nil {
		return nil, err
	}
	_ = s.store.PruneSessions(ctx, now.Add(-s.idle))
	return sess, nil
}

// authenticate returns ErrBadCredentials for wrong credentials; database errors are returned as is (the caller returns 500 and does not log a failed login).
func (s *Service) authenticate(ctx context.Context, username, password string) (*store.Session, error) {
	s.mu.RLock()
	localOn, sso := s.localEnabled, s.sso
	s.mu.RUnlock()

	if localOn {
		// local accounts are always lowercase (lowercased at creation); login also trims and lowercases before lookup, so Alice and alice are the same person
		u, err := s.store.GetLocalUser(ctx, LoginName(username))
		if err != nil {
			return nil, err
		}
		if u != nil {
			username = u.Username
			// compare the password before checking disabled, so a disabled account takes as long to respond as a wrong password
			if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
				return nil, &LoginFailure{Reason: FailBadPassword, Username: username}
			}
			if u.Disabled {
				return nil, &LoginFailure{Reason: FailDisabled, Username: username}
			}
			// no session for a role this build does not have
			if !ValidRole(u.Role) {
				return nil, &LoginFailure{Reason: FailNoRole, Username: username}
			}
			sess, err := s.makeSession(ctx, username, Identity{Name: displayName(username)}, u.Role, u)
			if err == nil {
				_ = s.store.SetLastLogin(ctx, username, time.Now().UTC())
			}
			return sess, err
		}
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
	}
	if sso != nil {
		id, err := sso.Authenticate(ctx, username, password)
		if err != nil {
			return nil, err
		}
		role, err := sso.RoleFor(ctx, id.Groups)
		if err != nil {
			return nil, err
		}
		if role == "" {
			return nil, &NoRoleError{Account: id.Account}
		}
		sess, err := s.makeSession(ctx, username, id, role, nil)
		if err != nil {
			return nil, err
		}
		// Between the role lookup and session creation, an admin may have just changed the role mapping: its session purge ran before this write, so this session would survive with the old role.
		// Look the role up again after the session is written (no credential re-check): if the change has taken effect we see the new role here; if not, its purge will delete this session.
		if again, err := sso.RoleFor(ctx, id.Groups); err != nil || again != role {
			_ = s.store.DeleteSession(ctx, sess.Token)
			if err != nil {
				return nil, err
			}
			return nil, &LoginFailure{Reason: FailNoRole, Username: id.Account}
		}
		return sess, nil
	}
	return nil, &LoginFailure{Reason: FailUnknownUser}
}

// makeSession: a non-nil local means a local account login, which carries "must change password" and marks the session as local; IdP logins pass nil.
// id.Account is the SSO display account; if the IdP does not provide one it falls back to the identifier (username = sub) so the UI has something to show. Not stored for local accounts.
func (s *Service) makeSession(ctx context.Context, username string, id Identity, role string, local *store.LocalUser) (*store.Session, error) {
	var acct *string
	if local == nil {
		a := id.Account
		if a == "" {
			a = username
		}
		acct = &a
	}
	sess := store.Session{
		Token: RandomToken(), Username: &username, Account: acct, Name: id.Name,
		Role: role, Org: s.org, ExpiresAt: time.Now().UTC().Add(s.ttl),
		MustChangePassword: local != nil && local.MustChangePassword, Local: local != nil,
	}
	if err := s.store.CreateSession(ctx, sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

// SessionCookie / ClearCookie are set by handlers through custom response objects.
func (s *Service) SessionCookie(token string) *http.Cookie {
	// nosemgrep: go.lang.security.audit.net.cookie-missing-secure.cookie-missing-secure -- Secure is derived from the TLS mode (see cmd/server/main.go); hardcoding true would make browsers drop the cookie outright in plain HTTP mode
	return &http.Cookie{
		Name: cookieName, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.secure, Expires: time.Now().Add(s.ttl),
	}
}

func (s *Service) ClearCookie() *http.Cookie {
	// Attributes must match SessionCookie (including Secure/SameSite): if the clearing cookie lacked Secure,
	// it would itself be sent over plain HTTP connections.
	// nosemgrep: go.lang.security.audit.net.cookie-missing-secure.cookie-missing-secure -- Secure is derived from the TLS mode (see cmd/server/main.go); hardcoding true would make browsers drop the cookie outright in plain HTTP mode
	return &http.Cookie{
		Name: cookieName, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.secure, MaxAge: -1,
	}
}

// ── Middleware: resolve identity first, then enforce permissions by path ────────────────────

func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 0) Reject any encoded slash in the path: chi routes on RawPath while the checks here use the decoded Path,
		// so "/nodes/a%2Fb" is one {id} segment to chi but two segments here, and the two would see different routes. No valid id contains a slash.
		if strings.Contains(strings.ToLower(r.URL.RawPath), "%2f") {
			apierr.Write(w, http.StatusBadRequest, apierr.New(api.ErrorCodeInvalid, api.ErrorReasonEncodedSlash, nil))
			return
		}
		// 1) Resolve identity (only with a valid session; an idle-expired session counts as logged out and is deleted)
		var mustChange bool
		var sessErr error // database error while looking up the session: not "logged out"; return 503 at the step that requires login
		if c, err := r.Cookie(cookieName); err == nil {
			sess, err := s.store.GetSession(r.Context(), c.Value)
			sessErr = err
			if sess != nil {
				if now := time.Now().UTC(); now.Sub(sess.LastSeenAt) > s.idle {
					_ = s.store.DeleteSession(r.Context(), sess.Token)
					http.SetCookie(w, s.ClearCookie()) // stop the browser from carrying an invalid cookie
				} else {
					// write at most once every 60 seconds: the frontend polls, so writing on every request would mean writing to the database constantly
					if now.Sub(sess.LastSeenAt) > time.Minute {
						_ = s.store.TouchSession(r.Context(), sess.Token, now)
					}
					mustChange = sess.MustChangePassword
					p := &Principal{Token: sess.Token, Username: sess.Username, Account: sess.Account, Name: sess.Name, Role: sess.Role, Org: sess.Org}
					r = r.WithContext(WithPrincipal(r.Context(), p))
				}
			}
		}
		// 2) Let public routes through
		if s.public(r.Method, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// 2b) Let GET/HEAD with no matching route through (guaranteed to land in the frontend SPA fallback, see the routeMatch comment).
		// The check is precisely "is a route registered", not "what the path looks like": any real route added later (such as /debug/pprof)
		// is reported as registered by routeMatch, so it is not let through here and still needs an isPublic entry or a declared permission.
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && s.unregistered(r.Method, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// 3) Everything else requires login
		p := principalFrom(r.Context())
		if p == nil && sessErr != nil {
			apierr.Write(w, http.StatusServiceUnavailable, apierr.New(api.ErrorCodeInternal, api.ErrorReasonDbUnavailable, nil))
			return
		}
		if p == nil {
			apierr.Write(w, http.StatusUnauthorized, apierr.New(api.ErrorCodeUnauthorized, api.ErrorReasonLoginRequired, nil))
			return
		}
		// 3b) A session that has not yet changed its initial / reset password may only change the password, view itself and log out (the frontend alone cannot enforce this)
		if mustChange && !allowedBeforePasswordChange(r.Method, r.URL.Path) {
			apierr.Write(w, http.StatusForbidden, apierr.New(api.ErrorCodePasswordChangeRequired, api.ErrorReasonPasswordChangeRequired, nil))
			return
		}
		// 4) Role permissions are checked only after a route matches (RequireScopes / Require): unrouted requests are left to the router to return 404 / 405,
		// rather than returning 403 first.
		next.ServeHTTP(w, r)
	})
}

func allowedBeforePasswordChange(method, path string) bool {
	return (method == http.MethodPost && path == "/api/v1/auth/password") ||
		(method == http.MethodGet && path == "/api/v1/me") ||
		(method == http.MethodPost && path == "/api/v1/auth/logout")
}

func isPublic(method, path string) bool {
	return (method == http.MethodGet && path == "/api/v1/auth/config") ||
		(method == http.MethodPost && path == "/api/v1/auth/login") ||
		// public key the platform signs node RPC tokens with: the node side curls it at install time and places it next to Besu (a public key is public anyway)
		(method == http.MethodGet && path == "/api/v1/platform/rpc-public-key") ||
		(method == http.MethodGet && path == "/api/v1/platform/rpc-public-key.pem") ||
		// Besu logging config (log4j2.xml; no secrets, the node side saves it with curl -o)
		(method == http.MethodGet && path == "/api/v1/platform/besu-log4j2.xml") ||
		// public install/upgrade scripts + agent binary (the operator must curl them before having credentials; the scripts hold no secrets)
		(method == http.MethodGet && path == "/install.sh") ||
		(method == http.MethodGet && path == "/upgrade-agent.sh") ||
		// HEAD too: the manual checks the download with curl -sI
		((method == http.MethodGet || method == http.MethodHead) && strings.HasPrefix(path, "/besu-agent-linux-")) ||
		// Prometheus HTTP SD: our own Prometheus fetches the target list with the SD_TOKEN bearer, not a human session (the handler verifies the token itself)
		(method == http.MethodGet && path == "/sd/targets") ||
		// node metrics reverse proxy: likewise, our own Prometheus scrapes /agent-proxy/{nodeID}/metrics with the SD_TOKEN bearer
		// (the handler verifies the token itself and strips this header before forwarding to the agent)
		(method == http.MethodGet && strings.HasPrefix(path, "/agent-proxy/")) ||
		// the platform's own metrics: scraped by our own Prometheus with the SD_TOKEN bearer, not a human session (the handler verifies the token itself, as /sd/targets does)
		(method == http.MethodGet && path == "/metrics") ||
		// Alertmanager webhook ingest: pushed by our own Alertmanager with the SD_TOKEN bearer, not a human session (the handler verifies the token itself)
		(method == http.MethodPost && path == "/alerts/webhook") ||
		// for the docker-compose healthcheck: a pure liveness check with no identity involved (the container has just started and has no sessions yet)
		(method == http.MethodGet && path == "/healthz")
}

func (s *Service) public(method, path string) bool {
	return isPublic(method, path) || (s.extraPublic != nil && s.extraPublic(method, path))
}

// unregistered reports that the route is definitely not registered. When routeMatch is not wired it always assumes registered (conservative).
func (s *Service) unregistered(method, path string) bool {
	return s.routeMatch != nil && !s.routeMatch(method, path)
}

// RequireScopes enforces permissions on contract routes and is mounted on the generated ChiServerOptions.Middlewares; scopes extracts the operation's declared permissions from ctx
// (each contract has a different key type, so the caller supplies it). Anything undeclared is rejected.
func (s *Service) RequireScopes(scopes func(context.Context) ([]string, bool)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sc, declared := scopes(r.Context())
			switch {
			case !declared && s.public(r.Method, r.URL.Path):
				next.ServeHTTP(w, r)
			case !declared || len(sc) != 1:
				log.Printf("auth: %s %s declares no permission (contract security scope), rejected", r.Method, r.URL.Path)
				if p := principalFrom(r.Context()); p != nil {
					s.auditDenied(r.Context(), p, permUndeclared, r.Method, r.URL.Path)
				}
				apierr.Write(w, http.StatusForbidden, apierr.New(api.ErrorCodeForbidden, api.ErrorReasonPermissionDenied, nil))
			default:
				s.Require(Permission(sc[0]))(next).ServeHTTP(w, r)
			}
		})
	}
}

// Permits reports whether this role can pass a route that requires perm (the check Require uses; tests also use it to run the role matrix against the contract).
func Permits(role string, perm Permission) bool {
	return perm == PermLoggedIn || roleCan(role, perm)
}

// Require makes the route require perm. Contract routes reach it through RequireScopes; login-protected routes outside the contract (such as the offline install bundle) mount it directly.
func (s *Service) Require(perm Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := principalFrom(r.Context())
			if p == nil { // Middleware already blocks unauthenticated requests; paths without Middleware are rejected all the same
				apierr.Write(w, http.StatusUnauthorized, apierr.New(api.ErrorCodeUnauthorized, api.ErrorReasonLoginRequired, nil))
				return
			}
			if !Permits(p.Role, perm) {
				s.auditDenied(r.Context(), p, perm, r.Method, r.URL.Path)
				apierr.Write(w, http.StatusForbidden, apierr.New(api.ErrorCodeForbidden, api.ErrorReasonPermissionDenied, nil))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ── Helpers ──────────────────────────────────────────────────

// RandomToken returns the hex of 32 random bytes (session tokens; Enterprise's OIDC state uses it too).
func RandomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func bcryptHash(pw string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(h), err
}

func bcryptCompare(hash, pw string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw))
}

func displayName(username string) string {
	if username == "admin" {
		return "Admin"
	}
	return username
}
