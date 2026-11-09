package server

import (
	"context"
	"errors"

	"besu-manager/backend/internal/api"
)

// GetNodeLogs: GET /nodes/{id}/logs -> reads the agent's in-memory log buffer through the agent
// tunnel. The platform does not store it and does not audit it (the frontend polls every few
// seconds while the logs tab is open, so auditing would flood the records).
func (s *Server) GetNodeLogs(ctx context.Context, req api.GetNodeLogsRequestObject) (api.GetNodeLogsResponseObject, error) {
	if _, err := s.requireOwnNode(ctx, req.Id); err != nil {
		if errors.Is(err, ErrNodeNotFound) {
			return api.GetNodeLogs404JSONResponse(nodeNotFoundError), nil
		}
		return nil, err
	}
	limit := 0
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	l, err := s.col.Logs(ctx, req.Id, req.Params.After, limit)
	if err != nil {
		return api.GetNodeLogs502JSONResponse(causeError(api.ErrorCodeInternal, api.ErrorReasonNodeLogsFailed, err)), nil
	}
	return api.GetNodeLogs200JSONResponse(l), nil
}
