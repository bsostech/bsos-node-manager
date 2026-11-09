package server

import (
	"context"
	"errors"

	"besu-manager/backend/internal/api"
)

// SetNodeLogLevel: PUT /nodes/{id}/log-level -> admin_changeLogLevel. Takes effect immediately and
// is not persisted.
func (s *Server) SetNodeLogLevel(ctx context.Context, req api.SetNodeLogLevelRequestObject) (api.SetNodeLogLevelResponseObject, error) {
	if _, err := s.requireOwnNode(ctx, req.Id); err != nil {
		if errors.Is(err, ErrNodeNotFound) {
			return api.SetNodeLogLevel404JSONResponse(nodeNotFoundError), nil
		}
		return nil, err
	}
	if req.Body == nil || !req.Body.Level.Valid() {
		return api.SetNodeLogLevel400JSONResponse{Code: api.ErrorCodeInvalid, Message: "level must be OFF / FATAL / ERROR / WARN / INFO / DEBUG / TRACE / ALL"}, nil // the contract's enum rejects this first; unreachable
	}
	level := string(req.Body.Level)
	// No old level to record: Besu has no RPC to read the current log level, and the platform does
	// not store it (a restart reverts to the startup flags)
	d := map[string]any{"node_id": req.Id, "level": level}
	if err := s.audit(ctx, "node_log_level_set", d); err != nil {
		return nil, err
	}
	if err := s.ops.SetLogLevel(ctx, req.Id, level); err != nil {
		e := causeError(api.ErrorCodeInternal, api.ErrorReasonLogLevelFailed, err)
		s.auditFailedError(ctx, "node_log_level_set", d, e)
		return api.SetNodeLogLevel502JSONResponse(e), nil
	}
	return api.SetNodeLogLevel204Response{}, nil
}
