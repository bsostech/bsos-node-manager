package server

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/store"

	"github.com/google/uuid"
)

// RPC user management plane. A user = a named set of method permissions; the platform stores no
// passwords, the operator "signs a token" for a user and hands it out, and nodes verify it with the
// platform public key. Users are per org, not bound to nodes.
//
// Zero interaction with nodes: no file rendering, no generation bump, no restart. The only thing a
// node needs is the public key placed at install time (GET /platform/rpc-public-key).
//
// Creation / issuance are also recorded in rpc_user_events while s.audit is called as usual: the
// duplication is intentional.

// rpcUsernameRe: RPC usernames allow only alphanumerics, - and _.
var rpcUsernameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// rpcTokenTTLs: contract enum -> duration. There is no "forever": Besu requires exp.
var rpcTokenTTLs = map[api.RpcTokenRequestTtl]time.Duration{
	api.RpcTokenTtl1h:   time.Hour,
	api.RpcTokenTtl24h:  24 * time.Hour,
	api.RpcTokenTtl7d:   7 * 24 * time.Hour,
	api.RpcTokenTtl30d:  30 * 24 * time.Hour,
	api.RpcTokenTtl365d: 365 * 24 * time.Hour,
}

// rpcTokenMaxTTL: upper bound for an explicit expiry, matching the longest enum.
const rpcTokenMaxTTL = 365 * 24 * time.Hour

var rpcUserNotFoundError = apierr.New(api.ErrorCodeNotFound, api.ErrorReasonRpcUserNotFound, nil)

func toAPIRpcUser(u store.RpcUser) api.RpcUser {
	return api.RpcUser{Id: u.ID, Username: u.Username, Permissions: u.Permissions, CreatedAt: u.CreatedAt.UTC()}
}

// rpcTokenTTL: exactly one of ttl and expiresAt is given; expiresAt must be in the future and at
// most 1 year out. Returns nil when valid.
func rpcTokenTTL(b api.RpcTokenRequest, now time.Time) (time.Duration, *api.Error) {
	switch {
	case (b.Ttl == nil) == (b.ExpiresAt == nil):
		return 0, invalidReason(api.ErrorReasonRpcTokenTtlXor, nil)
	case b.Ttl != nil:
		ttl, ok := rpcTokenTTLs[*b.Ttl]
		if !ok { // the contract's enum rejects this first; unreachable
			return 0, invalidMsg(fmt.Sprintf("invalid ttl %q (valid: 1h, 24h, 7d, 30d, 365d)", string(*b.Ttl)))
		}
		return ttl, nil
	}
	ttl := b.ExpiresAt.Sub(now)
	if ttl <= 0 {
		return 0, invalidReason(api.ErrorReasonRpcTokenExpiryPast, nil)
	}
	if ttl > rpcTokenMaxTTL {
		return 0, invalidReason(api.ErrorReasonRpcTokenExpiryTooFar, nil)
	}
	return ttl, nil
}

// ── GET /platform/rpc-public-key ────────────────────────────

func (s *Server) GetPlatformRpcPublicKey(ctx context.Context, _ api.GetPlatformRpcPublicKeyRequestObject) (api.GetPlatformRpcPublicKeyResponseObject, error) {
	return api.GetPlatformRpcPublicKey200JSONResponse{
		Algorithm:    s.rpcSigner.Algorithm(),
		PublicKeyPem: s.rpcSigner.PublicKeyPEM(),
		Sha256:       s.rpcSigner.Digest(),
	}, nil
}

// ── GET /rpc-users ──────────────────────────────────────────

func (s *Server) ListRpcUsers(ctx context.Context, _ api.ListRpcUsersRequestObject) (api.ListRpcUsersResponseObject, error) {
	users, err := s.store.ListRpcUsers(ctx)
	if err != nil {
		return nil, err
	}
	resp := make(api.ListRpcUsers200JSONResponse, 0, len(users))
	for _, u := range users {
		resp = append(resp, toAPIRpcUser(u))
	}
	return resp, nil
}

// ── POST /rpc-users ─────────────────────────────────────────

func (s *Server) CreateRpcUser(ctx context.Context, req api.CreateRpcUserRequestObject) (api.CreateRpcUserResponseObject, error) {
	if req.Body == nil {
		return api.CreateRpcUser400JSONResponse{Code: api.ErrorCodeInvalid, Message: "empty body"}, nil // the contract's required rejects this first; unreachable
	}
	name := req.Body.Username
	if name == "" || strings.HasPrefix(name, "bsos-") {
		return api.CreateRpcUser400JSONResponse(*invalidReason(api.ErrorReasonRpcUsernameReserved, nil)), nil
	}
	if !rpcUsernameRe.MatchString(name) {
		return api.CreateRpcUser400JSONResponse(*invalidReason(api.ErrorReasonRpcUsernameFormat, nil)), nil
	}
	if e := validatePermissions(req.Body.Permissions); e != nil {
		return api.CreateRpcUser400JSONResponse(*e), nil
	}
	id := uuid.NewString()
	d := map[string]any{"rpc_user_id": id, "username": name}
	if err := s.audit(ctx, "rpc_user_create", d); err != nil {
		return nil, err
	}
	if err := s.store.CreateRpcUser(ctx, store.RpcUser{ID: id, Username: name, Permissions: req.Body.Permissions}, actorOf(ctx)); err != nil {
		if errors.Is(err, store.ErrRpcUserExists) {
			e := apierr.New(api.ErrorCodeConflict, api.ErrorReasonRpcUserExists, nil)
			s.auditFailedError(ctx, "rpc_user_create", d, e)
			return api.CreateRpcUser409JSONResponse(e), nil
		}
		s.auditFailedInternal(ctx, "rpc_user_create", d, err)
		return nil, err
	}
	u, err := s.store.GetRpcUser(ctx, id)
	if err != nil || u == nil {
		return nil, fmt.Errorf("createRpcUser: reread: %v", err)
	}
	return api.CreateRpcUser201JSONResponse(toAPIRpcUser(*u)), nil
}

// rpcTokenLabelMax: maximum length of the token purpose (in characters).
const rpcTokenLabelMax = 64

// ── POST /rpc-users/{id}/token ──────────────────────────────

func (s *Server) IssueRpcUserToken(ctx context.Context, req api.IssueRpcUserTokenRequestObject) (api.IssueRpcUserTokenResponseObject, error) {
	if req.Body == nil {
		return api.IssueRpcUserToken400JSONResponse{Code: api.ErrorCodeInvalid, Message: "empty body"}, nil // the contract's required rejects this first; unreachable
	}
	ttl, e := rpcTokenTTL(*req.Body, time.Now())
	if e != nil {
		return api.IssueRpcUserToken400JSONResponse(*e), nil
	}
	var label *string // leading/trailing whitespace trimmed, empty means not provided
	if req.Body.Label != nil {
		if l := strings.TrimSpace(*req.Body.Label); l != "" {
			if utf8.RuneCountInString(l) > rpcTokenLabelMax {
				return api.IssueRpcUserToken400JSONResponse{Code: api.ErrorCodeInvalid, Message: fmt.Sprintf("purpose must be at most %d characters", rpcTokenLabelMax)}, nil // the contract's maxLength rejects this first; unreachable
			}
			label = &l
		}
	}
	u, err := s.store.GetRpcUser(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return api.IssueRpcUserToken404JSONResponse(rpcUserNotFoundError), nil
	}
	token, tokenID, exp, err := s.rpcSigner.Sign(u.Username, u.Permissions, ttl)
	if err != nil {
		return nil, err
	}
	// Hand out the token only after both audit and record are written: the signature itself leaves
	// no trace, so if it cannot be recorded this token is dropped and no invisible token exists
	// outside. The audit records "signed for whom, valid until when", never the token itself.
	d := map[string]any{"rpc_user_id": u.ID, "username": u.Username, "expires_at": exp, "jti": tokenID, "purpose": label}
	if err := s.audit(ctx, "rpc_token_issue", d); err != nil {
		return nil, err
	}
	if err := s.store.RecordRpcToken(ctx, u.ID, u.Permissions, exp, actorOf(ctx), tokenID, label); err != nil {
		s.auditFailedInternal(ctx, "rpc_token_issue", d, err)
		return nil, err
	}
	return api.IssueRpcUserToken200JSONResponse{Token: token, TokenId: tokenID, Label: label, ExpiresAt: exp, Permissions: u.Permissions}, nil
}

// ── DELETE /rpc-users/{id} ──────────────────────────────────

func (s *Server) DeleteRpcUser(ctx context.Context, req api.DeleteRpcUserRequestObject) (api.DeleteRpcUserResponseObject, error) {
	u, err := s.store.GetRpcUser(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return api.DeleteRpcUser404JSONResponse(rpcUserNotFoundError), nil
	}
	d := map[string]any{"rpc_user_id": req.Id, "username": u.Username}
	if err := s.audit(ctx, "rpc_user_delete", d); err != nil {
		return nil, err
	}
	name, err := s.store.DeleteRpcUser(ctx, req.Id)
	if err != nil {
		s.auditFailedInternal(ctx, "rpc_user_delete", d, err)
		return nil, err
	}
	if name == "" { // deleted by someone else between the read and the delete
		s.auditFailedError(ctx, "rpc_user_delete", d, rpcUserNotFoundError)
		return api.DeleteRpcUser404JSONResponse(rpcUserNotFoundError), nil
	}
	return api.DeleteRpcUser204Response{}, nil
}

// ── GET /rpc-users/{id}/events ──────────────────────────────

func (s *Server) ListRpcUserEvents(ctx context.Context, req api.ListRpcUserEventsRequestObject) (api.ListRpcUserEventsResponseObject, error) {
	u, err := s.store.GetRpcUser(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return api.ListRpcUserEvents404JSONResponse(rpcUserNotFoundError), nil
	}
	events, err := s.store.ListRpcUserEvents(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	resp := make(api.ListRpcUserEvents200JSONResponse, 0, len(events))
	for _, e := range events {
		out := api.RpcUserEvent{
			Id: e.ID, Kind: api.RpcUserEventKind(e.Kind), At: e.At.UTC(), Actor: e.Actor,
			Permissions: e.Permissions, TokenId: e.TokenID, Label: e.Label,
		}
		if e.ExpiresAt != nil {
			t := e.ExpiresAt.UTC()
			out.ExpiresAt = &t
		}
		resp = append(resp, out)
	}
	return resp, nil
}

// ── GET /rpc-method-catalog ─────────────────────────────────

func (s *Server) GetRpcMethodCatalog(ctx context.Context, _ api.GetRpcMethodCatalogRequestObject) (api.GetRpcMethodCatalogResponseObject, error) {
	return api.GetRpcMethodCatalog200JSONResponse(rpcMethodCatalog), nil
}

// validatePermissions: each permission string must be "<ns>:*" (whole namespace) or "<ns>:<method>"
// (single method), with <ns> in the method catalog and, for the method form, <method> one of that
// namespace's catalog methods. Rejects typos / bare * / *:* (*:* is reserved for the platform
// itself and is not signed for operators).
func validatePermissions(perms []string) *api.Error {
	if len(perms) == 0 { // the contract's minItems rejects this first; unreachable
		return invalidMsg("at least one permission is required")
	}
	for _, p := range perms {
		value := map[string]string{"value": strconv.Quote(p)}
		ns, method, ok := strings.Cut(p, ":")
		if !ok || ns == "" || method == "" {
			return invalidReason(api.ErrorReasonRpcPermissionFormat, value)
		}
		methods, known := catalogNamespaces[ns]
		if !known {
			return invalidReason(api.ErrorReasonRpcPermissionUnknownNamespace, value)
		}
		if method == "*" {
			continue
		}
		if !methods[method] {
			return invalidReason(api.ErrorReasonRpcPermissionUnknownMethod, value)
		}
	}
	return nil
}
