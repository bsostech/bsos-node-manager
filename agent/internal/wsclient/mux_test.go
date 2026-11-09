package wsclient

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

// ctrlPair is a pair of yamux sessions joined by net.Pipe, with the control stream set up as in production
// (platform Opens, agent Accepts, and only then does the agent hand the session to the HTTP server).
// It returns the agent's control outlet plus the platform's control stream and session.
func ctrlPair(t *testing.T, handler http.Handler) (agentCtrl *ctrlStream, platformCtrl net.Conn, platformSess *yamux.Session) {
	t.Helper()
	a, b := net.Pipe()
	agentSess, err := yamux.Server(a, muxConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agentSess.Close() })
	platformSess, err = yamux.Client(b, muxConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { platformSess.Close() })

	opened := make(chan struct{})
	go func() {
		platformCtrl, err = platformSess.Open()
		close(opened)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ac, aerr := agentSess.AcceptStreamWithContext(ctx)
	if aerr != nil {
		t.Fatal(aerr)
	}
	<-opened
	if err != nil {
		t.Fatal(err)
	}
	if handler != nil {
		srv := &http.Server{Handler: allowlist(handler, nil)}
		go srv.Serve(agentSess)
		t.Cleanup(func() { srv.Close() })
	}
	return newCtrlStream(ac, agentSess), platformCtrl, platformSess
}

// The control and data planes do not block each other: a stalled large response does not hold up control
// messages.
//
// A local test network cannot catch this (local links are millisecond-scale, so everything looks "instant" whether or
// not it blocks), hence a unit test: the platform opens a data stream but deliberately does not read it,
// filling its window, then measures a control message round trip.
func TestControlStreamNotBlockedByStalledData(t *testing.T) {
	big := strings.Repeat("x", 2<<20) // 2 MB, far beyond the 256 KB window
	ctrl, platformCtrl, sess := ctrlPair(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(big))
	}))

	// open a data stream and send a request, then **do not read** the response: the window fills and the
	// agent-side write blocks.
	stalled, err := sess.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer stalled.Close()
	if _, err := stalled.Write([]byte("GET /host-metrics HTTP/1.1\r\nHost: agent\r\n\r\n")); err != nil {
		t.Fatal(err)
	}

	// control messages must still arrive in reasonable time.
	done := make(chan error, 1)
	go func() {
		done <- ctrl.send(context.Background(), wsMsg{Type: "csr", CSRPEM: "pem"}, controlWriteTimeout)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("控制訊息被資料面卡住了: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("控制訊息被資料面卡住了(逾時)")
	}

	var got wsMsg
	if err := json.NewDecoder(platformCtrl).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "csr" || got.CSRPEM != "pem" {
		t.Fatalf("unexpected message: %+v", got)
	}
}

// A successful send does not close the session: only a write failure does, and later control messages use
// the same one.
func TestControlSendKeepsSessionOnSuccess(t *testing.T) {
	ctrl, platformCtrl, _ := ctrlPair(t, nil)
	dec := json.NewDecoder(platformCtrl)
	for _, typ := range []string{"csr", "apply-result"} {
		if err := ctrl.send(context.Background(), wsMsg{Type: typ}, controlWriteTimeout); err != nil {
			t.Fatalf("send %s: %v", typ, err)
		}
		if ctrl.sess.IsClosed() {
			t.Fatalf("送 %s 成功後 session 被關了", typ)
		}
		var got wsMsg
		if err := dec.Decode(&got); err != nil || got.Type != typ {
			t.Fatalf("收到 %+v err=%v,want %s", got, err, typ)
		}
	}
}

// The data plane only lets through allowlisted paths: the whole router is injected, and there is no reason
// to expose even /healthz.
func TestDataPlaneRejectsUnlistedPath(t *testing.T) {
	_, _, sess := ctrlPair(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("不該打到 handler")
	}))

	stream, err := sess.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err := stream.Write([]byte("GET /healthz HTTP/1.1\r\nHost: agent\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(stream), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// The control stream must be set up **before** the session is handed to the HTTP server.
// Otherwise http.Server swallows every Accept, the platform's control stream is parsed as HTTP and gets a
// 400, and the control plane is broken; this test locks in that order.
func TestControlStreamMustPrecedeHTTPServe(t *testing.T) {
	a, b := net.Pipe()
	agentSess, err := yamux.Server(a, muxConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer agentSess.Close()
	platformSess, err := yamux.Client(b, muxConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer platformSess.Close()

	// hand the session to the HTTP server first (the wrong order).
	srv := &http.Server{Handler: http.NotFoundHandler()}
	go srv.Serve(agentSess)
	defer srv.Close()

	ctrl, err := platformSess.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	// the type does not matter; any control frame the platform ignores will do.
	if err := json.NewEncoder(ctrl).Encode(wsMsg{Type: "noop"}); err != nil {
		t.Fatal(err)
	}
	_ = ctrl.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 16)
	n, _ := ctrl.Read(buf)
	if n == 0 || !strings.HasPrefix(string(buf[:n]), "HTTP/") {
		t.Fatalf("預期被 HTTP 伺服器當成請求解析(這就是順序不能換的原因),得 %q", string(buf[:n]))
	}
}
