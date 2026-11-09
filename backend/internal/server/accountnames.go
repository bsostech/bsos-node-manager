package server

import (
	"context"
	"strings"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
)

// Names for transaction-permissioned addresses: the platform's own address book, not written to
// nodes or on chain; changes are saved immediately and not tied to applying the list.

func (s *Server) ListAccountNames(ctx context.Context, _ api.ListAccountNamesRequestObject) (api.ListAccountNamesResponseObject, error) {
	names, err := s.store.ListAccountNames(ctx)
	if err != nil {
		return nil, err
	}
	return api.ListAccountNames200JSONResponse(names), nil
}

func (s *Server) SetAccountName(ctx context.Context, req api.SetAccountNameRequestObject) (api.SetAccountNameResponseObject, error) {
	if !validAddress(req.Address) {
		return api.SetAccountName400JSONResponse{Code: api.ErrorCodeInvalid, Message: "address must be 0x followed by 40 hex digits"}, nil // the contract's pattern rejects this first; unreachable
	}
	addr := strings.ToLower(req.Address)
	name := ""
	if req.Body != nil {
		name = strings.TrimSpace(req.Body.Name)
	}
	if name == "" || len([]rune(name)) > nodeNameMax {
		return api.SetAccountName400JSONResponse(nameRequiredError), nil
	}
	d := map[string]any{"address": addr, "name": name}
	if err := s.audit(ctx, "account_name_set", d); err != nil {
		return nil, err
	}
	if err := s.store.SetAccountName(ctx, addr, name); err != nil {
		s.auditFailedInternal(ctx, "account_name_set", d, err)
		return nil, err
	}
	return api.SetAccountName204Response{}, nil
}

func (s *Server) DeleteAccountName(ctx context.Context, req api.DeleteAccountNameRequestObject) (api.DeleteAccountNameResponseObject, error) {
	if !validAddress(req.Address) {
		// the contract's pattern rejects this first with the same error; no audit, so arbitrary strings are not written into the record
		return api.DeleteAccountName400JSONResponse(apierr.New(api.ErrorCodeInvalid, api.ErrorReasonFieldFormat, map[string]string{"field": "address"})), nil
	}
	addr := strings.ToLower(req.Address)
	d := map[string]any{"address": addr}
	if err := s.audit(ctx, "account_name_deleted", d); err != nil {
		return nil, err
	}
	if err := s.store.DeleteAccountName(ctx, addr); err != nil {
		s.auditFailedInternal(ctx, "account_name_deleted", d, err)
		return nil, err
	}
	return api.DeleteAccountName204Response{}, nil
}
