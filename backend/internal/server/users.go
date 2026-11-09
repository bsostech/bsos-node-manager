package server

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/auth"
	"besu-manager/backend/internal/store"
)

// Local user management (admin). SSO accounts are not managed here (they have no local row).

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func validRole(r api.Role) bool { return auth.ValidRole(string(r)) }

func toAPILocalUser(u store.LocalUser) api.LocalUser {
	out := api.LocalUser{Username: u.Username, Role: api.Role(u.Role), Disabled: u.Disabled,
		MustChangePassword: u.MustChangePassword, CreatedAt: u.CreatedAt.UTC(), Email: u.Email}
	if u.LastLoginAt != nil {
		t := u.LastLoginAt.UTC()
		out.LastLoginAt = &t
	}
	return out
}

func actorOf(ctx context.Context) string {
	if p := auth.PrincipalFrom(ctx); p != nil && p.Username != nil {
		return *p.Username
	}
	return ""
}

func (s *Server) ListLocalUsers(ctx context.Context, _ api.ListLocalUsersRequestObject) (api.ListLocalUsersResponseObject, error) {
	users, err := s.store.ListLocalUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make(api.ListLocalUsers200JSONResponse, 0, len(users))
	for _, u := range users {
		out = append(out, toAPILocalUser(u))
	}
	return out, nil
}

func (s *Server) CreateLocalUser(ctx context.Context, req api.CreateLocalUserRequestObject) (api.CreateLocalUserResponseObject, error) {
	bad := func(r api.ErrorReason, params map[string]string) (api.CreateLocalUserResponseObject, error) {
		return api.CreateLocalUser400JSONResponse(apierr.New(api.ErrorCodeInvalid, r, params)), nil
	}
	if req.Body == nil || !usernameRe.MatchString(req.Body.Username) {
		return bad(api.ErrorReasonFieldFormat, map[string]string{"field": "username"}) // the contract's pattern rejects this first; this is a backstop with the same reason as request validation
	}
	req.Body.Username = strings.ToLower(req.Body.Username) // always stored lowercase; Alice and alice are the same account
	if !validRole(req.Body.Role) {
		return bad(api.ErrorReasonFieldNotAllowed, map[string]string{"field": "role"})
	}
	email, ok := normalizeEmail(req.Body.Email)
	if !ok {
		return bad(api.ErrorReasonInvalidEmail, nil)
	}
	send := req.Body.SendEmail != nil && *req.Body.SendEmail
	if send && email == nil {
		return bad(api.ErrorReasonEmailRequiredToSend, nil)
	}
	if send && s.sendAccountMail == nil {
		return api.CreateLocalUser409JSONResponse(noAccountMailError), nil
	}
	pw := auth.GeneratePassword(req.Body.Username)
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return nil, err
	}
	d := map[string]any{"username": req.Body.Username, "role": string(req.Body.Role), "send_email": send}
	if err := s.audit(ctx, "user_created", d); err != nil {
		return nil, err
	}
	err = s.store.CreateLocalUser(ctx, store.LocalUser{Username: req.Body.Username, PasswordHash: hash,
		Role: string(req.Body.Role), MustChangePassword: true, Email: email})
	if errors.Is(err, store.ErrLocalUserExists) {
		s.auditFailed(ctx, "user_created", d, string(api.ErrorReasonUserExists))
		return api.CreateLocalUser409JSONResponse(apierr.New(api.ErrorCodeConflict, api.ErrorReasonUserExists, nil)), nil
	}
	if err != nil {
		s.auditFailedInternal(ctx, "user_created", d, err)
		return nil, err
	}
	u, err := s.store.GetLocalUser(ctx, req.Body.Username)
	if err != nil || u == nil {
		return nil, errors.Join(err, errors.New("user not found after create"))
	}
	dl := s.deliverPassword(ctx, "user_created", u, pw, send, mailNewUser)
	return api.CreateLocalUser201JSONResponse{User: toAPILocalUser(*u), Delivery: dl.Delivery, GeneratedPassword: dl.GeneratedPassword, MailError: dl.MailError, MailReason: dl.MailReason, MailParams: dl.MailParams}, nil
}

var (
	errLastAdmin = apierr.New(api.ErrorCodeConflict, api.ErrorReasonLastAdmin, nil)
	errNoUser    = apierr.New(api.ErrorCodeNotFound, api.ErrorReasonUserNotFound, nil)
)

// getLocalUser always lowercases the username from the path before lookup (it is stored lowercase
// on creation).
func (s *Server) getLocalUser(ctx context.Context, username string) (*store.LocalUser, error) {
	return s.store.GetLocalUser(ctx, strings.ToLower(username))
}

func (s *Server) UpdateLocalUser(ctx context.Context, req api.UpdateLocalUserRequestObject) (api.UpdateLocalUserResponseObject, error) {
	u, err := s.getLocalUser(ctx, req.Username)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return api.UpdateLocalUser404JSONResponse(errNoUser), nil
	}
	if req.Body == nil {
		return api.UpdateLocalUser400JSONResponse{Code: api.ErrorCodeInvalid, Message: "missing body"}, nil // the contract's required check rejects this first; unreachable
	}
	disabled := req.Body.Disabled
	if disabled == u.Disabled {
		return api.UpdateLocalUser200JSONResponse(toAPILocalUser(*u)), nil
	}
	if u.Username == actorOf(ctx) {
		return api.UpdateLocalUser409JSONResponse(apierr.New(api.ErrorCodeConflict, api.ErrorReasonCannotDisableSelf, nil)), nil
	}
	event, d := "user_enabled", map[string]any{"username": u.Username}
	if disabled {
		event = "user_disabled"
	}
	if err := s.audit(ctx, event, d); err != nil {
		return nil, err
	}
	switch err := s.store.SetLocalUserDisabled(ctx, u.Username, disabled); {
	case errors.Is(err, store.ErrLastAdmin):
		s.auditFailed(ctx, event, d, string(api.ErrorReasonLastAdmin))
		return api.UpdateLocalUser409JSONResponse(errLastAdmin), nil
	case errors.Is(err, store.ErrLocalUserNotFound):
		s.auditFailed(ctx, event, d, string(api.ErrorReasonUserNotFound))
		return api.UpdateLocalUser404JSONResponse(errNoUser), nil
	case err != nil:
		s.auditFailedInternal(ctx, event, d, err)
		return nil, err
	}
	u.Disabled = disabled
	return api.UpdateLocalUser200JSONResponse(toAPILocalUser(*u)), nil
}

func (s *Server) ResetLocalUserPassword(ctx context.Context, req api.ResetLocalUserPasswordRequestObject) (api.ResetLocalUserPasswordResponseObject, error) {
	u, err := s.getLocalUser(ctx, req.Username)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return api.ResetLocalUserPassword404JSONResponse(errNoUser), nil
	}
	if u.Username == actorOf(ctx) {
		return api.ResetLocalUserPassword409JSONResponse(apierr.New(api.ErrorCodeConflict, api.ErrorReasonCannotResetSelf, nil)), nil
	}
	if req.Body == nil {
		return api.ResetLocalUserPassword400JSONResponse{Code: api.ErrorCodeInvalid, Message: "missing body"}, nil // the contract's required check rejects this first; unreachable
	}
	send := req.Body.SendEmail != nil && *req.Body.SendEmail
	if send && u.Email == nil {
		return api.ResetLocalUserPassword400JSONResponse(apierr.New(api.ErrorCodeInvalid, api.ErrorReasonAccountHasNoEmail, nil)), nil
	}
	if send && s.sendAccountMail == nil {
		return api.ResetLocalUserPassword409JSONResponse(noAccountMailError), nil
	}
	pw := auth.GeneratePassword(u.Username)
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return nil, err
	}
	d := map[string]any{"username": u.Username, "send_email": send}
	if err := s.audit(ctx, "user_password_reset", d); err != nil {
		return nil, err
	}
	if err := s.store.ResetLocalPassword(ctx, u.Username, hash); errors.Is(err, store.ErrLocalUserNotFound) {
		s.auditFailed(ctx, "user_password_reset", d, string(api.ErrorReasonUserNotFound))
		return api.ResetLocalUserPassword404JSONResponse(errNoUser), nil
	} else if err != nil {
		s.auditFailedInternal(ctx, "user_password_reset", d, err)
		return nil, err
	}
	dl := s.deliverPassword(ctx, "user_password_reset", u, pw, send, mailReset)
	return api.ResetLocalUserPassword200JSONResponse{Delivery: dl.Delivery, Email: u.Email, GeneratedPassword: dl.GeneratedPassword, MailError: dl.MailError, MailReason: dl.MailReason, MailParams: dl.MailParams}, nil
}

// GetAccountMailStatus: GET /account-mail. Returns only whether it is configured, not the settings.
func (s *Server) GetAccountMailStatus(context.Context, api.GetAccountMailStatusRequestObject) (api.GetAccountMailStatusResponseObject, error) {
	return api.GetAccountMailStatus200JSONResponse{Configured: s.sendAccountMail != nil}, nil
}

var noAccountMailError = apierr.New(api.ErrorCodeConflict, api.ErrorReasonAccountMailNotConfigured, nil)

// delivery is how the temporary password was delivered this time. When mailed, the password is not
// included; if mailing fails it falls back to showing it, with the reason.
type delivery struct {
	Delivery          api.PasswordDelivery
	GeneratedPassword *string
	MailError         *string
	MailReason        *api.ErrorReason
	MailParams        *map[string]string
}

// deliverPassword, after the account is created / password reset, mails it or hands it to the admin
// to pass on; the result adds a done audit entry (the password is not recorded). A mail failure
// does not fail the whole operation: the account and password are already in effect, so it switches
// to showing it for the admin to pass on.
func (s *Server) deliverPassword(ctx context.Context, event string, u *store.LocalUser, pw string, send bool, compose func(username, pw string) (string, string)) delivery {
	// The account and password are already in effect: finish mailing and recording even if the
	// admin disconnects midway
	ctx = context.WithoutCancel(ctx)
	out := delivery{Delivery: api.PasswordDeliveryShown, GeneratedPassword: &pw}
	if send {
		subject, body := compose(u.Username, pw)
		if err := s.sendAccountMail(ctx, *u.Email, subject, body); err != nil { // use the reason if the underlying error carries one; put unrecognized raw text in detail
			e := causeError(api.ErrorCodeInternal, api.ErrorReasonOperationFailed, err)
			out.MailError, out.MailReason, out.MailParams = &e.Message, e.Reason, e.Params
		} else {
			out = delivery{Delivery: api.PasswordDeliveryEmail}
		}
	}
	done := map[string]any{"username": u.Username, "delivery": string(out.Delivery)}
	if out.MailReason != nil { // the audit stores the code with parameters separate (same as reason / reason_params)
		done["mail_error"] = string(*out.MailReason)
		if out.MailParams != nil {
			done["mail_error_params"] = *out.MailParams
		}
	}
	s.auditDone(ctx, event, done)
	return out
}

// normalizeEmail: optional; if filled in, accepts only a single address without display name or
// comments and stores the parsed address; returns false on a bad format.
func normalizeEmail(raw *string) (*string, bool) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, true
	}
	in := strings.TrimSpace(*raw)
	if !validEmail(in) {
		return nil, false
	}
	return &in, true
}

// validEmail: s is exactly one bare address, with no display name and no surrounding spaces.
func validEmail(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s
}

// mailNewUser / mailReset: the mail body, one section each in Chinese and English; no login URL.
func mailNewUser(username, pw string) (string, string) {
	return "BSOS 節點治理工具帳號已建立 / Your BSOS Node Manager account", fmt.Sprintf(
		"管理員替你建立了 BSOS 節點治理工具帳號。\n帳號：%[1]s\n臨時密碼：%[2]s\n首次登入須變更密碼。\n\n"+
			"An administrator created a BSOS Node Manager account for you.\nUsername: %[1]s\nTemporary password: %[2]s\nPassword change required at first sign-in.\n",
		username, pw)
}

func mailReset(username, pw string) (string, string) {
	return "BSOS 節點治理工具密碼已重設 / Your BSOS Node Manager password was reset", fmt.Sprintf(
		"管理員已重設你的 BSOS 節點治理工具密碼，所有已登入的工作階段均已登出。\n帳號：%[1]s\n臨時密碼：%[2]s\n下次登入須變更密碼。\n\n"+
			"An administrator reset your BSOS Node Manager password; all sessions were signed out.\nUsername: %[1]s\nTemporary password: %[2]s\nPassword change required at next sign-in.\n",
		username, pw)
}

func (s *Server) DeleteLocalUser(ctx context.Context, req api.DeleteLocalUserRequestObject) (api.DeleteLocalUserResponseObject, error) {
	u, err := s.getLocalUser(ctx, req.Username)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return api.DeleteLocalUser404JSONResponse(errNoUser), nil
	}
	if u.Username == actorOf(ctx) {
		return api.DeleteLocalUser409JSONResponse(apierr.New(api.ErrorCodeConflict, api.ErrorReasonCannotDeleteSelf, nil)), nil
	}
	d := map[string]any{"username": u.Username, "role": u.Role}
	if err := s.audit(ctx, "user_deleted", d); err != nil {
		return nil, err
	}
	switch err := s.store.DeleteLocalUser(ctx, u.Username); {
	case errors.Is(err, store.ErrLastAdmin):
		s.auditFailed(ctx, "user_deleted", d, string(api.ErrorReasonLastAdmin))
		return api.DeleteLocalUser409JSONResponse(errLastAdmin), nil
	case errors.Is(err, store.ErrLocalUserNotFound):
		s.auditFailed(ctx, "user_deleted", d, string(api.ErrorReasonUserNotFound))
		return api.DeleteLocalUser404JSONResponse(errNoUser), nil
	case err != nil:
		s.auditFailedInternal(ctx, "user_deleted", d, err)
		return nil, err
	}
	return api.DeleteLocalUser204Response{}, nil
}
