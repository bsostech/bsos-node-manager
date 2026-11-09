package pki

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
)

// Reverse requests (reverse tunnel): when the platform needs data, it opens a yamux stream on the WebSocket
// the agent already established and runs one ordinary HTTP exchange over it.
const (
	// The only path allowed when the agent request carries no X-Target (the agent keeps its own allowlist
	// too): host metrics.
	// Accept: application/json returns HardwareMetrics; otherwise the Prometheus text format.
	HostMetricsPath = "/host-metrics"

	// RequestHost: the fake host name in the URL. What actually decides the destination is this connection's
	// transport, but http.Transport needs a host as the connection-pool key. The metrics proxy must use the
	// same value when it rewrites URLs.
	RequestHost = "agent"
)

// Request: fetches data from the online agent of the given node and returns the agent handler's HTTP status
// code and body.
//
// The timeout comes from the caller's ctx (requestTimeout if none); an offline node errors immediately
// instead of waiting for the timeout. Callers treat that as "could not fetch", the same meaning as failing to
// fetch a snapshot before this change.
//
// error covers only transport-level problems (not connected, timeout, send failure); a non-200 returned by
// the agent handler itself (e.g. 503 before the first collection) is passed through in status, not turned
// into an error.
func (s *Service) Request(ctx context.Context, nodeID, path string) (status int, body string, err error) {
	// Everything the platform reads itself is JSON (the agent's /host-metrics returns JSON or Prometheus text
	// based on Accept).
	st, b, err := s.roundTrip(ctx, nodeID, http.MethodGet, path, nil, http.Header{"Accept": {"application/json"}})
	return st, string(b), err
}

// PostLoopback: relays a POST via the agent, unchanged, to the node-local 127.0.0.1:<port> (the agent only
// checks that it is loopback). It lets the platform call the node's JSON-RPC directly; headers are passed
// through as-is (e.g. Authorization). Status code and error semantics are the same as Request.
func (s *Service) PostLoopback(ctx context.Context, nodeID string, port int, body []byte, header http.Header) (status int, resp []byte, err error) {
	h := header.Clone()
	if h == nil {
		h = http.Header{}
	}
	h.Set("X-Target", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	h.Set("Content-Type", "application/json")
	return s.roundTrip(ctx, nodeID, http.MethodPost, "/", body, h)
}

func (s *Service) roundTrip(ctx context.Context, nodeID, method, path string, body []byte, header http.Header) (int, []byte, error) {
	conn, ok := s.hub.get(nodeID)
	if !ok {
		return 0, nil, apierr.Wrap(api.ErrorReasonNodeUnreachable, map[string]string{"nodeId": nodeID}, fmt.Errorf("agent %s: %w", nodeID, ErrAgentOffline))
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, requestTimeout)
		defer cancel()
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+RequestHost+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if header != nil {
		req.Header = header
	}
	resp, err := conn.tr.RoundTrip(req)
	if err != nil {
		return 0, nil, fmt.Errorf("agent %s: %w", nodeID, err)
	}
	defer resp.Body.Close()
	// yamux's window is flow control, not a size cap: a broken agent can still feed in an unbounded body.
	// Read one extra byte to tell "exactly at the cap" from "over the cap"; truncated data must not be
	// returned as success, since half-written metrics would silently pollute the time series.
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return 0, nil, fmt.Errorf("agent %s: %w", nodeID, err)
	}
	if len(b) > maxResponseBytes {
		return 0, nil, fmt.Errorf("agent %s: %w", nodeID, ErrResponseTooLarge)
	}
	return resp.StatusCode, b, nil
}

// ErrAgentOffline: the node's agent is not currently connected to the platform (the request was never sent).
var ErrAgentOffline = errors.New("not connected")

// ErrResponseTooLarge: the agent's response exceeded maxResponseBytes. Callers treat that fetch as failed
// instead of taking truncated data as a normal result. (The metrics proxy does not go through here: it
// streams the response through, and Prometheus's body_size_limit is the backstop.)
var ErrResponseTooLarge = errors.New("agent response exceeds the size limit")

// AgentTransport: the transport for sending HTTP requests to the given node (i.e. the yamux session on that
// connection). Returns false when the node is offline, so the caller returns 503 instead of letting RoundTrip fail
// and become a 502: alerting on the Prometheus side can then tell "could not scrape" from "proxy is broken".
//
// Do not hand out the *agentConn itself: the connection's lifecycle belongs to handleWS; outsiders only need
// "send a request to this node".
func (s *Service) AgentTransport(nodeID string) (http.RoundTripper, bool) {
	conn, ok := s.hub.get(nodeID)
	if !ok {
		return nil, false
	}
	return conn.tr, true
}

// OnlineNodeIDs: nodes currently connected to the platform (used by Enterprise to pull log chunks periodically).
func (s *Service) OnlineNodeIDs() []string {
	conns := s.hub.snapshot()
	out := make([]string, 0, len(conns))
	for id := range conns {
		out = append(out, id)
	}
	return out
}
