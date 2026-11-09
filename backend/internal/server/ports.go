package server

import (
	"context"
	"errors"

	"besu-manager/backend/internal/api"
)

// validPort is the TCP port range. Creating tokens and changing ports both go through here; the DB
// also has a CHECK on the same range as a backstop, so values read back need no validation.
func validPort(p int) bool { return p >= 1 && p <= 65535 }

// SetNodePorts: PUT /nodes/{id}/ports. The agent does not know which port Besu listens on; the
// platform sends X-Target: 127.0.0.1:<port> with every forwarded request, so when Besu's port
// changes it must be changed here too.
func (s *Server) SetNodePorts(ctx context.Context, req api.SetNodePortsRequestObject) (api.SetNodePortsResponseObject, error) {
	n, err := s.requireOwnNode(ctx, req.Id)
	if err != nil {
		if errors.Is(err, ErrNodeNotFound) {
			return api.SetNodePorts404JSONResponse(nodeNotFoundError), nil
		}
		return nil, err
	}
	if req.Body == nil || !validPort(req.Body.RpcPort) || !validPort(req.Body.MetricsPort) {
		return api.SetNodePorts400JSONResponse{Code: api.ErrorCodeInvalid, Message: "rpcPort / metricsPort must be 1-65535"}, nil // the contract's minimum / maximum rejects this first; unreachable
	}
	d := map[string]any{"node_id": req.Id,
		"before": map[string]any{"rpc_port": n.RpcPort, "metrics_port": n.MetricsPort},
		"after":  map[string]any{"rpc_port": req.Body.RpcPort, "metrics_port": req.Body.MetricsPort}}
	if err := s.audit(ctx, "node_ports_set", d); err != nil {
		return nil, err
	}
	found, err := s.store.SetNodePorts(ctx, req.Id, req.Body.RpcPort, req.Body.MetricsPort)
	if err != nil {
		s.auditFailedInternal(ctx, "node_ports_set", d, err)
		return nil, err
	}
	if !found { // node was deleted between the check and the write
		s.auditFailedError(ctx, "node_ports_set", d, nodeNotFoundError)
		return api.SetNodePorts404JSONResponse(nodeNotFoundError), nil
	}
	return api.SetNodePorts204Response{}, nil
}
