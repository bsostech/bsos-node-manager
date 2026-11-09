package server

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
)

// nodeNameMax: display name limit, shared by our org's nodes, peer_names and account_names (same as
// maxLength in openapi).
const nodeNameMax = 64

// nameRequiredError: a peer / account name that is only whitespace (the contract's minLength cannot
// catch an empty string after trim; overlong is caught first by maxLength).
var nameRequiredError = apierr.New(api.ErrorCodeInvalid, api.ErrorReasonNameRequired, map[string]string{"limit": strconv.Itoa(nodeNameMax)})

// normNodeName: trims surrounding whitespace; empty string means not provided (nil). Non-empty msg
// = invalid, returned directly to the user.
func normNodeName(raw *string) (name *string, msg string) {
	if raw == nil {
		return nil, ""
	}
	v := strings.TrimSpace(*raw)
	if v == "" {
		return nil, ""
	}
	if len([]rune(v)) > nodeNameMax {
		return nil, fmt.Sprintf("name must be at most %d characters", nodeNameMax)
	}
	return &v, ""
}

// SetNodeName: PUT /nodes/{id}/name. Names of our org's nodes live only in nodes.name, not in peer_names.
func (s *Server) SetNodeName(ctx context.Context, req api.SetNodeNameRequestObject) (api.SetNodeNameResponseObject, error) {
	n, err := s.requireOwnNode(ctx, req.Id)
	if err != nil {
		if errors.Is(err, ErrNodeNotFound) {
			return api.SetNodeName404JSONResponse(nodeNotFoundError), nil
		}
		return nil, err
	}
	var raw *string
	if req.Body != nil {
		raw = req.Body.Name
	}
	name, msg := normNodeName(raw)
	if msg != "" {
		return api.SetNodeName400JSONResponse{Code: api.ErrorCodeInvalid, Message: msg}, nil // the contract's maxLength rejects this first; unreachable
	}
	d := map[string]any{"node_id": req.Id, "before": map[string]any{"name": n.Name}, "after": map[string]any{"name": name}}
	if err := s.audit(ctx, "node_name_set", d); err != nil {
		return nil, err
	}
	found, err := s.store.SetNodeName(ctx, req.Id, name)
	if err != nil {
		s.auditFailedInternal(ctx, "node_name_set", d, err)
		return nil, err
	}
	if !found { // node deleted between the check and the write
		s.auditFailedError(ctx, "node_name_set", d, nodeNotFoundError)
		return api.SetNodeName404JSONResponse(nodeNotFoundError), nil
	}
	return api.SetNodeName204Response{}, nil
}
