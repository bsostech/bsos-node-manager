package pki

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/hashicorp/yamux"
)

// Multiplexing over a single WebSocket (platform side).
//
// WARNING: the three values below must match the constants of the same names in
// agent/internal/wsclient/mux.go. **Change them here and you must change them there too.** A mismatch does
// not fail on the spot; it silently skews liveness and backpressure (e.g. one side pings every 30 seconds
// while the other declares the link dead after 20). See that file for why each value was chosen.
const (
	muxWriteTimeout = 45 * time.Second
	muxWindow       = 256 * 1024
	muxKeepAlive    = 30 * time.Second

	// requestTimeout: default upper bound for the platform fetching one piece of data from an agent (callers
	// can shorten it with their own ctx).
	requestTimeout = 8 * time.Second

	// maxResponseBytes: cap on the response body. yamux's window is flow control, not a size cap, so we still have
	// to enforce this ourselves; otherwise a broken agent could make the platform io.ReadAll until memory runs out.
	maxResponseBytes = 8 << 20
)

func muxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.KeepAliveInterval = muxKeepAlive
	cfg.ConnectionWriteTimeout = muxWriteTimeout
	cfg.MaxStreamWindowSize = muxWindow
	cfg.LogOutput = io.Discard // disconnects are logged in handleWS; no need for yamux to log them again
	return cfg
}

// ctrlStream: the outbound end of the control-plane stream.
//
// Uses a ctx-aware semaphore rather than a mutex: RunRotation sends to each connection in turn, so if waiting
// for the lock were not bounded by a timeout, one slow node would delay every other node.
type ctrlStream struct {
	s    net.Conn
	sess *yamux.Session
	sem  chan struct{}
}

func newCtrlStream(s net.Conn, sess *yamux.Session) *ctrlStream {
	return &ctrlStream{s: s, sess: sess, sem: make(chan struct{}, 1)}
}

// send: sends one control message.
//
// WARNING: a returned error **does not mean the peer did not receive it**; yamux still flushes already-queued
// frames after a write timeout. Callers must not roll back state based on it (e.g. clearing pending dispatch
// records); we always close the whole session here and let the reconnect realign both sides.
func (c *ctrlStream) send(ctx context.Context, m wsMsg, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.s.SetWriteDeadline(dl)
		defer func() { _ = c.s.SetWriteDeadline(time.Time{}) }()
	}
	if err := json.NewEncoder(c.s).Encode(m); err != nil {
		// The only safe response: tear down the whole session and let both ends realign on reconnect. Just
		// logging and carrying on is not an option: the error does not mean the peer missed it, and a
		// half-written JSON message would make every message after it undecodable.
		c.sess.Close()
		return err
	}
	return nil
}

// agentTransport: wraps the yamux session as an HTTP transport that only reaches this agent. Each request
// goes over a new stream; pairing, flow control and timeouts are handled by yamux and net/http.
func agentTransport(sess *yamux.Session) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return sess.Open()
		},
		// There is only one peer (the other end of this session), so a small connection pool is enough.
		MaxIdleConns:        4,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     60 * time.Second,
	}
}
