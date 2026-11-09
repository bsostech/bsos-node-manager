package server

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/pki"
	"besu-manager/backend/internal/rpcops"
)

type txLogOps struct {
	NodeOps
	txErr, levelErr error
}

func (o txLogOps) TxPool(context.Context, string, string) (rpcops.TxPool, error) {
	return rpcops.TxPool{}, o.txErr
}

func (o txLogOps) SetLogLevel(context.Context, string, string) error { return o.levelErr }

// offlineRequester: the agent is not connected (same error as pki.Service.Request returns).
type offlineRequester struct{}

func (offlineRequester) Request(_ context.Context, nodeID, _ string) (int, string, error) {
	return 0, "", apierr.Wrap(api.ErrorReasonNodeUnreachable, map[string]string{"nodeId": nodeID}, fmt.Errorf("agent %s: %w", nodeID, pki.ErrAgentOffline))
}

// Miscellaneous node page errors return reason and params.
func TestNodeMiscErrorReasons(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := asAdmin(context.Background())
	if err := s.store.UpsertOwnNode(ctx, "node-a1", "org-test", 8545, 9545, nil); err != nil {
		t.Fatal(err)
	}
	reasonOf := func(r any, err error) api.Error {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		switch v := r.(type) {
		case api.GetNode404JSONResponse:
			return api.Error(v)
		case api.GetNodeAlerts404JSONResponse:
			return api.Error(v)
		case api.SetNodeLogLevel404JSONResponse:
			return api.Error(v)
		case api.SetNodeLogLevel502JSONResponse:
			return api.Error(v)
		case api.GetNodeTxPool502JSONResponse:
			return api.Error(v)
		case api.GetNodeLogs502JSONResponse:
			return api.Error(v)
		case api.GetNodeSeries503JSONResponse:
			return api.Error(v)
		case api.SetAccountName400JSONResponse:
			return api.Error(v)
		case api.SetPeerName400JSONResponse:
			return api.Error(v)
		case api.SetPeerName409JSONResponse:
			return api.Error(v)
		}
		t.Fatalf("不是錯誤回應: %#v", r)
		return api.Error{}
	}
	expect := func(e api.Error, want api.ErrorReason, k, v string) {
		t.Helper()
		if !hasReason(e, want) {
			t.Fatalf("要 %s,得 %#v", want, e)
		}
		if k != "" && (e.Params == nil || (*e.Params)[k] != v) {
			t.Fatalf("%s params[%s] 要 %q,得 %#v", want, k, v, e.Params)
		}
	}

	expect(reasonOf(s.GetNode(ctx, api.GetNodeRequestObject{Id: "nope"})), api.ErrorReasonNodeNotFound, "", "")
	expect(reasonOf(s.GetNodeAlerts(ctx, api.GetNodeAlertsRequestObject{Id: "nope"})), api.ErrorReasonNodeNotFound, "", "")
	expect(reasonOf(s.SetNodeLogLevel(ctx, api.SetNodeLogLevelRequestObject{Id: "nope", Body: &api.NodeLogLevelRequest{Level: api.LogLevelInfo}})), api.ErrorReasonNodeNotFound, "", "")

	s.ops = txLogOps{levelErr: errors.New("節點 RPC 沒有回應"), txErr: errors.New("節點 RPC 沒有回應")}
	expect(reasonOf(s.SetNodeLogLevel(ctx, api.SetNodeLogLevelRequestObject{Id: "node-a1", Body: &api.NodeLogLevelRequest{Level: api.LogLevelInfo}})), api.ErrorReasonLogLevelFailed, "detail", "節點 RPC 沒有回應")
	expect(reasonOf(s.GetNodeTxPool(ctx, api.GetNodeTxPoolRequestObject{Id: "node-a1"})), api.ErrorReasonTxpoolFailed, "detail", "節點 RPC 沒有回應")
	// Agent not connected: take the root cause (node unreachable), not "cannot read txpool + raw
	// text"
	offline := apierr.Wrap(api.ErrorReasonNodeUnreachable, map[string]string{"nodeId": "node-a1"}, fmt.Errorf("agent node-a1: %w", pki.ErrAgentOffline))
	s.ops = txLogOps{txErr: apierr.Wrap(api.ErrorReasonNodeRpcNoResponse, map[string]string{"detail": offline.Error()}, fmt.Errorf("%w: %w", rpcops.ErrNoResponse, offline)), levelErr: offline}
	expect(reasonOf(s.GetNodeTxPool(ctx, api.GetNodeTxPoolRequestObject{Id: "node-a1"})), api.ErrorReasonNodeUnreachable, "nodeId", "node-a1")
	audits := recordAudit(t)
	expect(reasonOf(s.SetNodeLogLevel(ctx, api.SetNodeLogLevelRequestObject{Id: "node-a1", Body: &api.NodeLogLevelRequest{Level: api.LogLevelInfo}})), api.ErrorReasonNodeUnreachable, "nodeId", "node-a1")
	// The audit stores the same reason
	if rows := audits("node_log_level_set"); len(rows) == 0 || rows[len(rows)-1]["reason"] != string(api.ErrorReasonNodeUnreachable) {
		t.Fatalf("稽核沒存 node_unreachable: %v", rows)
	}
	s.ops = txLogOps{txErr: rpcops.ErrNoTxPool}
	expect(reasonOf(s.GetNodeTxPool(ctx, api.GetNodeTxPoolRequestObject{Id: "node-a1"})), api.ErrorReasonTxpoolApiDisabled, "", "")

	logs := func() (api.GetNodeLogsResponseObject, error) {
		return s.GetNodeLogs(ctx, api.GetNodeLogsRequestObject{Id: "node-a1"})
	}
	s.col = collector.New(s.store, downRequester{}, allowRPC{})
	expect(reasonOf(logs()), api.ErrorReasonNodeLogsFailed, "detail", "no host metrics")
	s.col = collector.New(s.store, offlineRequester{}, allowRPC{})
	expect(reasonOf(logs()), api.ErrorReasonNodeUnreachable, "nodeId", "node-a1")

	s.prom = nil
	expect(reasonOf(s.GetNodeSeries(ctx, api.GetNodeSeriesRequestObject{Id: "node-a1", Params: api.GetNodeSeriesParams{Metric: "cpu"}})), api.ErrorReasonMonitoringNotConfigured, "", "")

	addr := "0x" + "ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12"
	expect(reasonOf(s.SetAccountName(ctx, api.SetAccountNameRequestObject{Address: addr, Body: &api.PeerNameRequest{Name: "  "}})), api.ErrorReasonNameRequired, "limit", "64")
	expect(reasonOf(s.SetPeerName(ctx, api.SetPeerNameRequestObject{Address: addr, Body: &api.PeerNameRequest{Name: "  "}})), api.ErrorReasonNameRequired, "limit", "64")
	en, own := mkPeer(t, "a1", "10.0.0.1:30303")
	if err := s.store.SetNodeIdentity(ctx, "node-a1", own, en); err != nil {
		t.Fatal(err)
	}
	expect(reasonOf(s.SetPeerName(ctx, api.SetPeerNameRequestObject{Address: own, Body: &api.PeerNameRequest{Name: "x"}})), api.ErrorReasonPeerIsOwnNode, "", "")
}
