package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/auth"
	"besu-manager/backend/internal/store"
)

// Custom response objects: the strict server's Visit(w) lets us set/clear the session cookie here.
type loginResponse struct {
	me     api.Me
	cookie *http.Cookie
}

func (r loginResponse) VisitLoginResponse(w http.ResponseWriter) error {
	http.SetCookie(w, r.cookie)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(r.me)
}

type logoutResponse struct{ cookie *http.Cookie }

func (r logoutResponse) VisitLogoutResponse(w http.ResponseWriter) error {
	http.SetCookie(w, r.cookie)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func meFromPrincipal(p *auth.Principal) api.Me {
	id := "user"
	if p.Username != nil {
		id = *p.Username
	}
	return api.Me{
		User: api.User{Id: id, Name: p.Name, Account: p.Account},
		Role: api.Role(p.Role),
		Org:  p.Org,
	}
}

// meWithFlags adds mustChangePassword for local accounts (SSO/LDAP accounts have no local row ->
// not set).
func (s *Server) meWithFlags(ctx context.Context, username *string, me api.Me) api.Me {
	if username != nil {
		if u, _ := s.store.GetLocalUser(ctx, *username); u != nil && u.MustChangePassword {
			t := true
			me.MustChangePassword = &t
		}
	}
	// Commands and URLs meant for nodes (install, upgrade, rejoin, log settings, RPC public key)
	// use these two, not the browser URL: when an admin connects through 127.0.0.1, a VPN or a jump
	// host, the nodes cannot reach the browser URL. Unset means empty; the frontend falls back to
	// the browser URL and shows a hint.
	if s.platformAddr != "" && s.platformAddr != defaultPlatformAddr {
		me.PlatformAddr = &s.platformAddr
	}
	if s.publicAPIBase != "" {
		me.PublicApiBase = &s.publicAPIBase
	}
	return me
}

// defaultPlatformAddr is the default when main.go has no PLATFORM_ADDR; nodes cannot reach it, so
// it is treated as unset.
const defaultPlatformAddr = "localhost:8443"

// GET /auth/config (public): the login page uses it to decide whether to show the password form and
// SSO button. Core always shows only the password form.
func (s *Server) GetAuthConfig(_ context.Context, _ api.GetAuthConfigRequestObject) (api.GetAuthConfigResponseObject, error) {
	resp := api.GetAuthConfig200JSONResponse{PasswordLogin: s.auth.PasswordLogin(), LdapLogin: s.auth.LDAPLogin()}
	if url := s.auth.SSOLoginURL(); url != "" {
		resp.SsoLoginUrl = &url
	}
	return resp, nil
}

// POST /auth/login (public): username/password -> session cookie.
func (s *Server) Login(ctx context.Context, req api.LoginRequestObject) (api.LoginResponseObject, error) {
	if req.Body == nil {
		return api.Login401JSONResponse{Code: api.ErrorCodeUnauthorized, Message: "missing username or password"}, nil // the contract's required check rejects this first; unreachable
	}
	sess, err := s.auth.Authenticate(ctx, req.Body.Username, req.Body.Password)
	// Wrong credentials are recorded as (unknown); if the IdP verified the user but there is no
	// role, record the username
	var noRole *auth.NoRoleError
	if errors.As(err, &noRole) {
		name := auth.LoginName(noRole.Account)
		s.recordLogin(ctx, &name, auth.FailNoRole)
		if err := s.audit(ctx, "login_failed", map[string]any{"username": noRole.Account, "reason": string(api.ErrorReasonNoRole)}); err != nil {
			return nil, err
		}
		return api.Login401JSONResponse(apierr.New(api.ErrorCodeUnauthorized, api.ErrorReasonBadCredentials, nil)), nil
	}
	if err != nil && !errors.Is(err, auth.ErrBadCredentials) {
		return nil, err // database error: not a login failure, do not record login_failed
	}
	if err != nil {
		s.recordLoginFailure(ctx, err)
		if err := s.audit(ctx, "login_failed", map[string]any{"username": s.failedLoginName(ctx, req.Body.Username)}); err != nil {
			return nil, err
		}
		return api.Login401JSONResponse(apierr.New(api.ErrorCodeUnauthorized, api.ErrorReasonBadCredentials, nil)), nil
	}
	// Who the user is is only known after the credentials check: the session already exists, so if
	// the audit cannot be written, delete it and do not let this login take effect
	method := "ldap" // password login is either a local account or LDAP; OIDC uses its own callback
	if sess.Local {
		method = "local"
	}
	detail := map[string]any{"username": *sess.Username, "method": method}
	if sess.Account != nil {
		detail["account"] = *sess.Account
	}
	if err := s.audit(ctx, "login", detail); err != nil {
		_ = s.store.DeleteSession(ctx, sess.Token)
		return nil, err
	}
	// The login takes effect only after the audit is written; the success record comes after it
	name := auth.LoginName(*sess.Username)
	s.recordLogin(ctx, &name, "")
	me := meFromPrincipal(&auth.Principal{Username: sess.Username, Name: sess.Name, Role: sess.Role, Org: sess.Org})
	me = s.meWithFlags(ctx, sess.Username, me)
	return loginResponse{me: me, cookie: s.auth.SessionCookie(sess.Token)}, nil
}

// GET /me: the current user (the middleware has filled in principal).
func (s *Server) GetMe(ctx context.Context, _ api.GetMeRequestObject) (api.GetMeResponseObject, error) {
	p := auth.PrincipalFrom(ctx)
	if p == nil {
		return api.GetMe401JSONResponse(apierr.New(api.ErrorCodeUnauthorized, api.ErrorReasonNotLoggedIn, nil)), nil
	}
	return api.GetMe200JSONResponse(s.meWithFlags(ctx, p.Username, meFromPrincipal(p))), nil
}

// POST /auth/logout: delete the session row + clear the cookie.
func (s *Server) Logout(ctx context.Context, _ api.LogoutRequestObject) (api.LogoutResponseObject, error) {
	if p := auth.PrincipalFrom(ctx); p != nil {
		// Best effort: logging out only lowers privileges, so an audit write failure must not stop
		// someone from logging out
		if p.Username != nil {
			_ = s.store.Audit(ctx, "logout", withActor(ctx, map[string]any{"username": *p.Username}))
		}
		_ = s.store.DeleteSession(ctx, p.Token)
	}
	return logoutResponse{cookie: s.auth.ClearCookie()}, nil
}

// POST /auth/password: change your own local password (the forced first-time change also goes
// through here).
func (s *Server) ChangePassword(ctx context.Context, req api.ChangePasswordRequestObject) (api.ChangePasswordResponseObject, error) {
	p := auth.PrincipalFrom(ctx)
	if p == nil || p.Username == nil {
		return api.ChangePassword401JSONResponse(apierr.New(api.ErrorCodeUnauthorized, api.ErrorReasonNotLocalAccount, nil)), nil
	}
	if req.Body == nil {
		return nil, fmt.Errorf("changePassword: missing body")
	}
	// Reject a new password that breaks the rules up front, rather than recording an entry and then
	// a failure (whether the current password is correct is only known inside ChangePassword)
	if err := auth.ValidatePassword(*p.Username, req.Body.NewPassword); err != nil {
		var pwErr *auth.PasswordError
		if errors.As(err, &pwErr) {
			return api.ChangePassword400JSONResponse(apierr.New(api.ErrorCodeInvalid, pwErr.Reason, nil)), nil
		}
		return nil, err
	}
	// Write only one entry, with the result: the success entry is in the same transaction as the
	// password change (if it cannot be written the password is not changed); if the rejected entry
	// cannot be written an error is returned too, same as login_failed: password-guessing attempts
	// must not go unrecorded
	d := map[string]any{"username": *p.Username}
	err := s.auth.ChangePassword(ctx, *p.Username, p.Token, req.Body.CurrentPassword, req.Body.NewPassword, withActor(ctx, d, "result", "done"))
	var pwErr *auth.PasswordError
	switch {
	case errors.Is(err, auth.ErrNotLocalUser): // an SSO account: nothing to guess, so not recorded as a failed attempt
		return api.ChangePassword401JSONResponse(apierr.New(api.ErrorCodeUnauthorized, api.ErrorReasonNotLocalAccount, nil)), nil
	case errors.Is(err, auth.ErrWrongPassword):
		if err := s.audit(ctx, "password_changed", withActor(ctx, d, "result", "failed", "reason", string(api.ErrorReasonWrongCurrentPassword))); err != nil {
			return nil, err
		}
		return api.ChangePassword401JSONResponse(apierr.New(api.ErrorCodeUnauthorized, api.ErrorReasonWrongCurrentPassword, nil)), nil
	case errors.As(err, &pwErr): // the rules were checked above, so this is a check only the service makes (such as password_unchanged); rule violations are not recorded
		return api.ChangePassword400JSONResponse(apierr.New(api.ErrorCodeInvalid, pwErr.Reason, nil)), nil
	case errors.Is(err, errAudit):
		return nil, err
	case err != nil:
		s.auditFailedInternal(ctx, "password_changed", d, err)
		return nil, err
	}
	return api.ChangePassword204Response{}, nil
}

// errAudit means the audit could not be written and the operation was not performed (see
// store.ErrAudit).
var errAudit = store.ErrAudit

// withActor copies detail and adds the actor (the currently logged-in user; unauthenticated paths
// such as login itself have no actor, and the event carries its own username).
func withActor(ctx context.Context, detail map[string]any, extra ...any) map[string]any {
	d := make(map[string]any, len(detail)+3)
	for k, v := range detail {
		d[k] = v
	}
	for i := 0; i+1 < len(extra); i += 2 {
		d[extra[i].(string)] = extra[i+1]
	}
	if a := actorOf(ctx); a != "" {
		d["actor"] = a
	}
	return d
}

// audit records first, then acts. Call it after validating parameters and before doing anything; if
// it cannot write it returns errAudit and the caller returns immediately without performing the
// operation. Core does not write audits and always returns nil.
func (s *Server) audit(ctx context.Context, event string, detail map[string]any) error {
	if err := s.store.Audit(ctx, event, withActor(ctx, detail)); err != nil {
		return fmt.Errorf("%w: %v", errAudit, err)
	}
	return nil
}

// auditFailed: the operation did not succeed after being recorded (blocked by a business rule or an
// error), so add an entry for the same event with result=failed and the reason. Best effort: the
// operation did not happen, so if this entry cannot be written it only goes to the server log.
func (s *Server) auditFailed(ctx context.Context, event string, detail map[string]any, reason string) {
	_ = s.store.Audit(ctx, event, withActor(ctx, detail, "result", "failed", "reason", reason))
}

// auditFailedInternal: an internal error (database, encryption, file write) made the operation
// fail; the reason is stored as operation_failed and the raw text goes in reason_params.detail.
func (s *Server) auditFailedInternal(ctx context.Context, event string, detail map[string]any, err error) {
	s.auditFailedReason(ctx, event, detail, api.ErrorReasonOperationFailed, map[string]string{"detail": err.Error()})
}

// auditFailedReason is like auditFailed but stores the reason as an error code; values in the
// sentence go in a separate reason_params sub-object instead of being flattened into detail.
func (s *Server) auditFailedReason(ctx context.Context, event string, detail map[string]any, reason api.ErrorReason, params map[string]string) {
	extra := []any{"result", "failed", "reason", string(reason)}
	if len(params) > 0 {
		extra = append(extra, "reason_params", params)
	}
	_ = s.store.Audit(ctx, event, withActor(ctx, detail, extra...))
}

// auditFailedError is for when the rejection response itself is the audit reason: reason and params
// come from the response's Error, so the two cannot diverge. e must carry a reason (produced by
// apierr.New); errors without a reason use auditFailed.
func (s *Server) auditFailedError(ctx context.Context, event string, detail map[string]any, e api.Error) {
	var params map[string]string
	if e.Params != nil {
		params = *e.Params
	}
	s.auditFailedReason(ctx, event, detail, *e.Reason, params)
}

// auditDone adds an entry for the same event with result=done for information only known afterwards
// (new serial, revocation count, per-node results). Best effort: the operation already happened and
// the initial entry is already there.
func (s *Server) auditDone(ctx context.Context, event string, detail map[string]any) {
	_ = s.store.Audit(ctx, event, withActor(ctx, detail, "result", "done"))
}

// unknownUsername: the username entered on a failed login does not exist; the audit records only
// this.
const unknownUsername = auth.UnknownAccount

// failedLoginName returns the username to record for a failed login. The name is recorded only if
// it is an existing local account; otherwise what was typed is not recorded: people often type
// their password into the username field, so it is hidden as AWS CloudTrail does. SSO users have no
// local account and are also recorded as nonexistent on failure.
func (s *Server) failedLoginName(ctx context.Context, typed string) string {
	name := auth.LoginName(typed)
	if !usernameRe.MatchString(name) {
		return unknownUsername
	}
	if u, err := s.store.GetLocalUser(ctx, name); err != nil || u == nil {
		return unknownUsername
	}
	return name
}

// recordLogin writes a login record (empty reason = success). Login records are not audits; a write
// failure does not block the login.
func (s *Server) recordLogin(ctx context.Context, username *string, reason string) {
	if err := s.store.RecordLoginEvent(ctx, username, reason); err != nil {
		log.Printf("write login event: %v", err)
	}
}

// recordLoginFailure: the credentials did not pass. ErrBadCredentials without a reason (someone
// used the password form while the connector is OIDC) is treated as a nonexistent account.
func (s *Server) recordLoginFailure(ctx context.Context, err error) {
	var f *auth.LoginFailure
	if !errors.As(err, &f) {
		s.recordLogin(ctx, nil, auth.FailUnknownUser)
		return
	}
	var name *string // account does not exist: do not record what was typed
	if f.Username != "" {
		n := auth.LoginName(f.Username)
		name = &n
	}
	s.recordLogin(ctx, name, f.Reason)
}
