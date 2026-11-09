// Package wsclient maintains a persistent mTLS WebSocket to the platform (dialed by the agent), running yamux:
// the control plane only does cert rotation (renew -> csr -> cert, new key hot-swapped); the data plane
// carries reverse requests opened by the platform. Those with X-Target are forwarded as-is to the node
// locally (the platform calls Besu JSON-RPC / metrics directly); those without only reach host metrics
// (see mux.go). The agent writes no Besu files, never restarts Besu, and holds no Besu credentials.
package wsclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"besu-manager/agent/internal/certstore"
	"besu-manager/agent/internal/version"
)

// ProtocolVersion is the protocol version between this agent and the platform (tunnel, control messages,
// data-plane paths); the platform rejects the connection if it is below its minimum
// (MinAgentProtocol in backend/internal/pki).
// Bump it on any incompatible change to those three, otherwise a mismatch fails silently; change both sides
// together and adjust the minimum the platform accepts.
const ProtocolVersion = 1

// Header sent to the platform on connect (same name on the platform side, see backend/internal/pki/ws.go).
const (
	protocolHeader = "X-Besu-Agent-Protocol"
	versionHeader  = "X-Besu-Agent-Version"
)

// wsMsg is a control channel message (fields match wsMsg in backend/internal/pki/ws.go; a JSON contract
// maintained by hand on both sides).
type wsMsg struct {
	Type     string `json:"type"`
	CSRPEM   string `json:"csrPem,omitempty"`
	CertPEM  string `json:"certPem,omitempty"`
	ChainPEM string `json:"chainPem,omitempty"`
}

type Client struct {
	platformAddr string
	store        *certstore.Store

	pendingKey *ecdsa.PrivateKey // new key awaiting its signed cert during renew

	// handler receives reverse requests without X-Target (host metrics).
	handler http.Handler

	// AllowPorts: ports X-Target forwarding may use (ALLOW_PORTS); empty = all loopback ports.
	AllowPorts map[string]bool
}

func New(platformAddr string, cs *certstore.Store, handler http.Handler) *Client {
	return &Client{platformAddr: platformAddr, store: cs, handler: handler}
}

// Run keeps the connection up (reconnecting with exponential backoff).
func (c *Client) Run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		// Re-read the cert from disk before each connect: if join ran again at runtime, the new cert is swapped
		// in here. Swap is only called synchronously in the connected read loop, and this runs only after
		// connectAndLoop returns, so the two never run concurrently.
		if changed, err := c.store.Reload(); err != nil {
			log.Printf("wsclient: cannot reload certificate, keeping the current one: %v", err)
		} else if changed {
			log.Printf("wsclient: certificate on disk changed, switching to it")
		}
		if err := c.connectAndLoop(ctx); err != nil && ctx.Err() == nil {
			log.Printf("wsclient: %v (reconnecting in %s)", err, backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (c *Client) connectAndLoop(ctx context.Context) error {
	hc := &http.Client{
		Timeout:   0, // long-lived WS connection
		Transport: &http.Transport{TLSClientConfig: c.dialTLSConfig()},
	}
	hdr := http.Header{}
	hdr.Set(protocolHeader, strconv.Itoa(ProtocolVersion))
	hdr.Set(versionHeader, version.Version)
	// connection-level ctx: dialing, the data plane and the control plane all hang off it and stop together
	// when the connection ends. It is not derived from ctx: cancelling the dial context or the context NetConn
	// reads under drops the TCP connection without a close frame. When the agent is stopping, a connection
	// still being dialed is abandoned; an open one gets a close frame first, so the platform sees the node
	// leave at once.
	connCtx, connCancel := context.WithCancel(context.WithoutCancel(ctx))
	defer connCancel()
	var open atomic.Pointer[websocket.Conn]
	go func() {
		select {
		case <-ctx.Done():
			if conn := open.Load(); conn != nil {
				_ = conn.Close(websocket.StatusGoingAway, "agent stopping")
			}
			connCancel()
		case <-connCtx.Done():
		}
	}()
	// on handshake failure coder/websocket still returns resp (the first 1024 bytes of body are readable);
	// check for nil before using it below
	// nosemgrep: trailofbits.go.invalid-usage-of-modified-variable.invalid-usage-of-modified-variable
	conn, resp, err := websocket.Dial(connCtx, "wss://"+c.platformAddr+"/pki/v1/ws", &websocket.DialOptions{HTTPClient: hc, HTTPHeader: hdr})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUpgradeRequired { // the platform says this agent is too old; the reason is in the body
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return fmt.Errorf("platform rejected this agent version (protocol %d): %s", ProtocolVersion, strings.TrimSpace(string(b)))
		}
		return err
	}
	// loopErr: why the read loop ended. The platform sends a reason when it closes the connection (cert
	// revoked, replaced by a new connection), but under yamux that reason is not readable; the stream just
	// ends in EOF. Log it here, otherwise the disconnect reason is lost on the agent side.
	var loopErr error
	defer func() {
		if st := websocket.CloseStatus(loopErr); st != -1 {
			log.Printf("wsclient: platform closed the connection (%v)", st)
		}
		conn.Close(websocket.StatusNormalClosure, "")
	}()
	log.Printf("wsclient: connected to platform %s", c.platformAddr)

	open.Store(conn)
	if ctx.Err() != nil { // stopped while dialing: the watcher may have missed the connection
		_ = conn.Close(websocket.StatusGoingAway, "agent stopping")
		return nil
	}

	// the message type must be Binary on both ends (the platform closes the connection on Text).
	sess, err := yamux.Server(websocket.NetConn(connCtx, conn, websocket.MessageBinary), muxConfig())
	if err != nil {
		return err
	}
	defer sess.Close()

	// Warning: the order cannot change: once the session is handed to http.Server it swallows every Accept,
	// so a control stream the platform opens later is parsed as HTTP and gets a 400, breaking the control
	// plane. So accept the control stream first, then start the data plane.
	actx, acancel := context.WithTimeout(connCtx, ctrlAcceptTimeout)
	ctrlConn, err := sess.AcceptStreamWithContext(actx)
	acancel()
	if err != nil {
		return err
	}
	srv := c.serveRequests(sess)
	defer srv.Close()

	ctrl := newCtrlStream(ctrlConn, sess)
	dec := json.NewDecoder(ctrlConn)
	for {
		var m wsMsg
		if loopErr = dec.Decode(&m); loopErr != nil {
			// malformed JSON cannot be skipped: the decoder state is unrecoverable and every later message would
			// fail to decode. The close reason is taken from loopErr by the defer above.
			return loopErr
		}
		c.dispatch(ctrl, m)
	}
}

// dispatch handles one inbound control message.
func (c *Client) dispatch(ctrl *ctrlStream, m wsMsg) {
	switch m.Type {
	case "renew":
		if err := c.sendCSR(ctrl); err != nil {
			log.Printf("wsclient: renew csr: %v", err)
		}
	case "cert":
		if err := c.applyCert(m); err != nil {
			log.Printf("wsclient: apply cert: %v", err)
		} else {
			log.Printf("wsclient: certificate rotated (hot swap, connection kept)")
		}
	}
}

// dialTLSConfig skips hostname verification (identity is in Subject O/OU) and does custom chain
// verification plus a platform identity check.
func (c *Client) dialTLSConfig() *tls.Config {
	// nosemgrep: problem-based-packs.insecure-transport.go-stdlib.bypass-tls-verification.bypass-tls-verification -- identity is in the cert Subject O/OU, not the hostname, so hostname is not verified; chain verification is done by the custom VerifyPeerCertificate below, and TLS 1.3 is pinned as the minimum
	return &tls.Config{
		MinVersion:           tls.VersionTLS13,
		GetClientCertificate: c.store.GetClientCertificate,
		InsecureSkipVerify:   true, // custom verification below
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return fmt.Errorf("no peer cert")
			}
			leaf, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return err
			}
			inters := x509.NewCertPool()
			for _, der := range rawCerts[1:] {
				if cert, err := x509.ParseCertificate(der); err == nil {
					inters.AddCert(cert)
				}
			}
			chains, err := leaf.Verify(x509.VerifyOptions{
				Roots: c.store.Roots(), Intermediates: inters,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			if err != nil {
				return err
			}
			if !c.store.IssuedByCurrentIntermediate(chains) {
				return fmt.Errorf("platform certificate is not signed by the current intermediate")
			}
			// read O/OU only after the chain verifies to this organization's root (verify the chain, then read fields).
			id, err := certstore.IdentityFromCert(leaf)
			if err != nil {
				return err
			}
			if id.Org != c.store.Org() || id.OU != "platform" {
				return fmt.Errorf("peer is not this organization's platform")
			}
			return nil
		},
	}
}

func (c *Client) sendCSR(ctrl *ctrlStream) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return err
	}
	// pendingKey is touched only by the read loop goroutine, so no lock is needed.
	c.pendingKey = key
	return ctrl.send(context.Background(), wsMsg{
		Type: "csr", CSRPEM: string(certstore.PEMBlock("CERTIFICATE REQUEST", csrDER)),
	}, controlWriteTimeout)
}

func (c *Client) applyCert(m wsMsg) error {
	if c.pendingKey == nil {
		return fmt.Errorf("no pending key")
	}
	keyDER, err := x509.MarshalECPrivateKey(c.pendingKey)
	if err != nil {
		return err
	}
	err = c.store.Swap(certstore.PEMBlock("EC PRIVATE KEY", keyDER), []byte(m.CertPEM), []byte(m.ChainPEM))
	if err == nil {
		c.pendingKey = nil
	}
	return err
}
