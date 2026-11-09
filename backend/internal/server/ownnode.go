package server

import (
	"context"
	"errors"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/store"
)

// ErrNodeNotFound: no such node; handlers return their own 404 type based on it.
var ErrNodeNotFound = errors.New("node not found")

// nodeNotFoundError is the 404 every node handler returns (different response types, same content).
var nodeNotFoundError = apierr.New(api.ErrorCodeNotFound, api.ErrorReasonNodeNotFound, nil)

// requireOwnNode loads a node; nonexistent -> ErrNodeNotFound; DB errors are returned as is (nodes
// holds only our own nodes).
func (s *Server) requireOwnNode(ctx context.Context, id string) (*store.Node, error) {
	n, err := s.store.GetNode(ctx, id)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return nil, ErrNodeNotFound
	}
	return n, nil
}
