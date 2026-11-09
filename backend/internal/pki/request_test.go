package pki

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
)

// wsPair: a pair of real WebSocket connections. Returns (agent end, platform end).
func wsPair(t *testing.T) (agentEnd, platformEnd *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		accepted <- c
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.CloseNow() })
	server := <-accepted
	t.Cleanup(func() { server.CloseNow() })
	return client, server
}

// muxPair: stacks yamux on both ends of a real connection and sets up the control stream as in production: the
// platform Opens first, the agent Accepts first, and only then does the agent hand the session to the HTTP server.
//
// Returns the platform-side agentConn (can be fed directly to Service) and the agent-side session.
func muxPair(t *testing.T, handler http.Handler) (*agentConn, *yamux.Session) {
	t.Helper()
	agentWS, platformWS := wsPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	agentSess, err := yamux.Server(websocket.NetConn(ctx, agentWS, websocket.MessageBinary), muxConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agentSess.Close() })

	platformSess, err := yamux.Client(websocket.NetConn(ctx, platformWS, websocket.MessageBinary), muxConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { platformSess.Close() })

	opened := make(chan error, 1)
	var ctrl net.Conn
	go func() {
		var e error
		ctrl, e = platformSess.Open()
		opened <- e
	}()
	actx, acancel := context.WithTimeout(ctx, 5*time.Second)
	defer acancel()
	agentCtrl, err := agentSess.AcceptStreamWithContext(actx)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-opened; err != nil {
		t.Fatal(err)
	}
	_ = agentCtrl // these tests do not use the control plane, but it must be set up first, or Serve would swallow it

	if handler != nil {
		srv := &http.Server{Handler: handler}
		go srv.Serve(agentSess)
		t.Cleanup(func() { srv.Close() })
	}

	conn := &agentConn{ws: platformWS, sess: platformSess, ctrl: newCtrlStream(ctrl, platformSess), tr: agentTransport(platformSess)}
	t.Cleanup(conn.tr.CloseIdleConnections)
	return conn, agentSess
}

// The platform gets the agent's response over the session: status code and body come back unchanged, far
// larger than the old 32 KB cap.
func TestRequestOverSession(t *testing.T) {
	big := strings.Repeat("besu_metric 1\n", 8000) // ~112 KB, spanning several yamux windows
	conn, _ := muxPair(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != HostMetricsPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(big))
	}))
	s := &Service{hub: &hub{conns: map[string]*agentConn{"node-a": conn}}}

	status, body, err := s.Request(context.Background(), "node-a", HostMetricsPath)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusOK || body != big {
		t.Fatalf("status = %d, bodyLen = %d(want 200 / %d)", status, len(body), len(big))
	}
}

// A non-200 returned by the agent handler itself (503 before the first collection) is passed through, not
// turned into a transport error.
func TestRequestPassesThroughNon200(t *testing.T) {
	conn, _ := muxPair(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "尚未採集", http.StatusServiceUnavailable)
	}))
	s := &Service{hub: &hub{conns: map[string]*agentConn{"node-a": conn}}}

	status, _, err := s.Request(context.Background(), "node-a", HostMetricsPath)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
}

// Once the session is closed, in-flight and subsequent requests fail immediately without waiting for their
// own timeouts. (A silent disconnect does not take this path; that waits for keepalive.)
func TestRequestFailsAfterSessionClose(t *testing.T) {
	conn, agentSess := muxPair(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	s := &Service{hub: &hub{conns: map[string]*agentConn{"node-a": conn}}}
	agentSess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if _, _, err := s.Request(ctx, "node-a", HostMetricsPath); err == nil {
		t.Fatal("session 已關,應回錯誤")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("不該等到逾時:耗時 %s", elapsed)
	}
}

// Node offline: error immediately, without using up the caller's timeout.
func TestRequestOfflineFailsImmediately(t *testing.T) {
	s := &Service{hub: &hub{conns: map[string]*agentConn{}}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	if _, _, err := s.Request(ctx, "node-missing", HostMetricsPath); err == nil {
		t.Fatal("未連線應回錯誤")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("不該等待:耗時 %s", elapsed)
	}
}

// Exceeding the size cap must return an error, not treat the truncated body as a normal result; half-written
// metrics would silently pollute the time series.
func TestRequestRejectsOversizedResponse(t *testing.T) {
	conn, _ := muxPair(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chunk := strings.Repeat("x", 64<<10)
		for written := 0; written <= maxResponseBytes; written += len(chunk) {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
		}
	}))
	s := &Service{hub: &hub{conns: map[string]*agentConn{"node-a": conn}}}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _, err := s.Request(ctx, "node-a", HostMetricsPath)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("應回 ErrResponseTooLarge,得 %v", err)
	}
}

// A response exactly at the cap is not too large.
func TestRequestAcceptsResponseAtLimit(t *testing.T) {
	conn, _ := muxPair(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBytes)))
	}))
	s := &Service{hub: &hub{conns: map[string]*agentConn{"node-a": conn}}}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	status, body, err := s.Request(ctx, "node-a", HostMetricsPath)
	if err != nil || status != http.StatusOK || len(body) != maxResponseBytes {
		t.Fatalf("status=%d len=%d err=%v,want 200 / %d", status, len(body), err, maxResponseBytes)
	}
}

// PostLoopback: the body and the caller's headers (e.g. Authorization) reach the agent unchanged. The fake
// agent echoes back what it received.
func TestPostLoopbackForwardsBodyAndHeader(t *testing.T) {
	conn, _ := muxPair(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write([]byte(r.Header.Get("Authorization") + "|" + r.Header.Get("X-Target") + "|" + string(b)))
	}))
	s := &Service{hub: &hub{conns: map[string]*agentConn{"node-a": conn}}}

	body := `{"jsonrpc":"2.0","id":0,"method":"eth_blockNumber"}`
	status, resp, err := s.PostLoopback(context.Background(), "node-a", 8545, []byte(body), http.Header{"Authorization": {"Bearer tok"}})
	if err != nil || status != http.StatusOK {
		t.Fatalf("status=%d err=%v", status, err)
	}
	if want := "Bearer tok|127.0.0.1:8545|" + body; string(resp) != want {
		t.Fatalf("agent 收到 %q,want %q", resp, want)
	}
}
