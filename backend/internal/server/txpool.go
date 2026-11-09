package server

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/rpcops"
)

// GetNodeTxPool: GET /nodes/{id}/txpool -> reads the transaction pool live through the agent
// tunnel. The platform does not store it or audit it (an open tab polls every few seconds);
// rejection reasons come separately from our own Prometheus, null if not connected.
func (s *Server) GetNodeTxPool(ctx context.Context, req api.GetNodeTxPoolRequestObject) (api.GetNodeTxPoolResponseObject, error) {
	if _, err := s.requireOwnNode(ctx, req.Id); err != nil {
		if errors.Is(err, ErrNodeNotFound) {
			return api.GetNodeTxPool404JSONResponse(nodeNotFoundError), nil
		}
		return nil, err
	}
	from := ""
	if req.Params.From != nil {
		if !policyAddressPattern.MatchString(*req.Params.From) {
			return api.GetNodeTxPool400JSONResponse{Code: api.ErrorCodeInvalid, Message: "from must be an address (0x + 40 hex)"}, nil // the contract's pattern rejects this first; unreachable
		}
		from = *req.Params.From
	}
	p, err := s.ops.TxPool(ctx, req.Id, from)
	if err != nil {
		e := causeError(api.ErrorCodeInternal, api.ErrorReasonTxpoolFailed, err)
		if errors.Is(err, rpcops.ErrNoTxPool) {
			e = apierr.New(api.ErrorCodeInternal, api.ErrorReasonTxpoolApiDisabled, nil)
		}
		return api.GetNodeTxPool502JSONResponse(e), nil
	}
	out := txPoolResponse(p, time.Now().UTC())
	if s.prom != nil && !p.Syncing {
		// If Prometheus is slow or down only the rejection reasons are missing; the snapshot is
		// still returned
		if rej, err := s.prom.TxPoolRejected(ctx, req.Id); err == nil {
			out.Rejected = &rej
		} else {
			slog.Warn("txpool: query rejection reasons", "node", req.Id, "err", err)
		}
	}
	return api.GetNodeTxPool200JSONResponse(out), nil
}

// txPoolResponse converts the rpcops snapshot -> the contract shape (pure function).
func txPoolResponse(p rpcops.TxPool, now time.Time) api.NodeTxPool {
	pending, queued := p.Totals()
	out := api.NodeTxPool{
		AsOf: now, Syncing: p.Syncing, MaxSize: p.MaxSize,
		LocalCount: p.LocalCount, RemoteCount: p.RemoteCount, Truncated: p.Truncated,
		Totals:  api.TxPoolTotals{Pending: pending, Queued: queued, OldestAddedAt: p.Oldest},
		Senders: make([]api.TxPoolSender, len(p.Senders)),
	}
	for i, s := range p.Senders {
		txs := make([]api.TxPoolTx, len(s.Txs))
		for j, t := range s.Txs {
			txs[j] = api.TxPoolTx{Hash: t.Hash, Nonce: int64(t.Nonce), To: t.To, AddedAt: t.AddedAt.UTC(), Local: t.Local}
		}
		out.Senders[i] = api.TxPoolSender{Address: s.Address, NextNonce: int64(s.NextNonce), Txs: txs}
	}
	return out
}
