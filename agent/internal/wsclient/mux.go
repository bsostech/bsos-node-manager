package wsclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
)

// Multiplexing over one WebSocket: the control plane uses one dedicated stream (JSON frames, same shape as
// before), and the data plane runs plain HTTP on one stream per request. Request/response pairing is HTTP's
// job, flow control and liveness are yamux's; none of it is hand-written.
const (
	// muxWriteTimeout: the limit for yamux to queue a frame into its send loop, which is also the keepalive
	// ping limit.
	//
	// Warning: this cannot be the default 10 seconds. It governs more than heartbeats: every chunk of every
	// active stream sits in the same send queue, so on a slow link "data queued ahead of the ping" makes the
	// keepalive time out, the session closes itself, and it reconnects every minute. This project assumes
	// remote nodes behind NAT, so a fast link cannot be assumed.
	//
	// This value is the only knob: yamux rejects windows below 256 KB (at construction), so "shrink the window
	// to buy headroom" is not an option. 45 seconds + 256 KB window x two active streams survives down to
	// about 90 kbit/s, which covers the worst links seen in practice.
	muxWriteTimeout = 45 * time.Second

	// muxWindow: per-stream receive window (backpressure). 256 KB is yamux's minimum and also its default.
	muxWindow = 256 * 1024

	// muxKeepAlive: how often to send a ping. Time to detect a disconnect = this value + muxWriteTimeout
	// (waiting for the ACK).
	muxKeepAlive = 30 * time.Second

	// controlWriteTimeout: the limit for one control message from "start waiting for other messages to finish
	// writing" to "written out".
	//
	// Warning: it covers waiting for the semaphore and writing, but **not the step where yamux actually queues
	// the frame into its send loop**; that step is bounded by muxWriteTimeout. So this value means "never let
	// a control message hang indefinitely", not an end-to-end guarantee.
	controlWriteTimeout = 10 * time.Second

	// ctrlAcceptTimeout: the limit for waiting for the platform to open the control stream. The platform opens
	// it before registering in the hub, so this guards against "connected but the peer is not following the
	// protocol", not a wait on the normal path.
	ctrlAcceptTimeout = 30 * time.Second
)

// muxConfig is the yamux config. Warning: the platform side has same-named constants in
// backend/internal/pki/mux.go, and **both sides must match**; change one, change the other (they cannot be
// shared across modules).
func muxConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.KeepAliveInterval = muxKeepAlive
	cfg.ConnectionWriteTimeout = muxWriteTimeout
	cfg.MaxStreamWindowSize = muxWindow
	// By default yamux prints its own errors to stderr, emitting "Failed to read header: context canceled" on
	// every disconnect. Disconnects are handled and logged in connectAndLoop; no need to repeat it here.
	cfg.LogOutput = io.Discard
	return cfg
}

// ctrlStream is the outbound end of the control stream.
//
// It uses a ctx-aware semaphore instead of a mutex: the platform sends cert rotation requests to each
// connection sequentially, and if waiting for the lock had no timeout, one slow node would delay all
// the others, which is exactly what the timeout split was meant to solve.
type ctrlStream struct {
	s    net.Conn
	sess *yamux.Session
	sem  chan struct{}
}

func newCtrlStream(s net.Conn, sess *yamux.Session) *ctrlStream {
	return &ctrlStream{s: s, sess: sess, sem: make(chan struct{}, 1)}
}

// send sends one control message. timeout covers both "waiting for other messages to finish" and "writing
// out".
//
// Warning: an error **does not mean the peer did not receive it**: yamux still sends queued frames after
// ConnectionWriteTimeout. So callers must not compensate as if it was "not delivered"; send closes the whole
// session itself so the reconnect resynchronizes both sides.
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
		// The only safe response: close the whole session and let both sides resync via reconnect.
		// Logging and carrying on is not an option: the error does not mean the peer did not receive it, and
		// a half-written JSON message would make every later one undecodable.
		c.sess.Close()
		return err
	}
	return nil
}

// requestPaths are the paths the platform may request without X-Target: host metrics, log buffer,
// log blocks, version and the Besu P2P port probe.
var requestPaths = map[string]bool{
	"/host-metrics": true,
	"/logs":         true,
	"/logblocks":    true,
	"/agent-status": true,
}

// targetHeader tells the agent which node-local address to forward to. The port is a column in the
// platform's nodes table; the agent does not know where Besu is.
const targetHeader = "X-Target"

// allowlist: with X-Target, it checks the target is loopback (and the port when allowPorts is non-empty) and
// forwards as-is; without it, only requestPaths pass and everything else is 404.
// Only the literal 127.0.0.1 / ::1 is accepted: hostnames need resolution and other 127.0.0.0/8 addresses
// behave differently across OSes, so neither is accepted.
func allowlist(h http.Handler, allowPorts map[string]bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if target := r.Header.Get(targetHeader); target != "" {
			host, port, err := net.SplitHostPort(target)
			if err != nil || (host != "127.0.0.1" && host != "::1") || (len(allowPorts) > 0 && !allowPorts[port]) {
				http.Error(w, "target not allowed", http.StatusForbidden)
				return
			}
			proxy := &httputil.ReverseProxy{
				Rewrite: func(pr *httputil.ProxyRequest) {
					pr.Out.URL.Scheme = "http"
					pr.Out.URL.Host = target
					pr.Out.Host = target
					pr.Out.Header.Del(targetHeader)
				},
			}
			proxy.ServeHTTP(w, r)
			return
		}
		if !requestPaths[r.URL.Path] {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// ParseAllowPorts parses ALLOW_PORTS="<port>,<port>" into a set; empty string = no port restriction.
// Ports are keyed in plain decimal form ("08545" -> "8545") because allowlist and agentstatus look up the
// port string as the platform sends it. A non-empty value that yields no ports is an error: an empty set
// means "everything open", so a typo such as "," or " " must not silently lift the restriction. Only an
// unset (empty) value means no restriction.
func ParseAllowPorts(v string) (map[string]bool, error) {
	out := map[string]bool{}
	if v == "" {
		return out, nil
	}
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil || n == 0 {
			return nil, fmt.Errorf("ALLOW_PORTS: port %q is not a number in 1-65535", p)
		}
		out[strconv.FormatUint(n, 10)] = true
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ALLOW_PORTS=%q lists no ports; to allow all ports, leave it unset", v)
	}
	return out, nil
}

// serveRequests is the data plane: one plain HTTP exchange on each stream the platform opens.
// The response Content-Type, status code and headers all go back as-is, with no manual reassembly.
func (c *Client) serveRequests(sess *yamux.Session) *http.Server {
	srv := &http.Server{
		Handler:           allowlist(c.handler, c.AllowPorts),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := srv.Serve(sess); err != nil && err != http.ErrServerClosed {
			log.Printf("wsclient: data plane ended: %v", err)
		}
	}()
	return srv
}
