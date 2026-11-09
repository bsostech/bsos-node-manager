package pki

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/ca"
	"besu-manager/backend/internal/store"
)

// wsMsg: Agent<->Platform control-channel message. Every other platform operation on a node goes over
// JSON-RPC; the control channel carries only cert rotation: platform->agent renew (asks the agent for a CSR)
// and cert (the new cert), and agent->platform csr.
type wsMsg struct {
	Type     string    `json:"type"`
	CSRPEM   string    `json:"csrPem,omitempty"`
	CertPEM  string    `json:"certPem,omitempty"`
	ChainPEM string    `json:"chainPem,omitempty"`
	NotAfter time.Time `json:"notAfter,omitempty"`
}

type agentConn struct {
	ws     *websocket.Conn
	serial string // leaf cert serial at connect time (used to disconnect on revocation)

	// Multiplexing over one WebSocket (see mux.go): the control plane uses the dedicated ctrl stream, and
	// each data-plane request runs ordinary HTTP on a new stream. When the connection ends, sess.Close()
	// makes every in-flight request get an error at once, so we do not have to track who is waiting.
	sess *yamux.Session
	ctrl *ctrlStream
	tr   *http.Transport
}

type hub struct {
	mu    sync.Mutex
	conns map[string]*agentConn // node id → conn
}

func (h *hub) set(nodeID string, c *agentConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if old, ok := h.conns[nodeID]; ok {
		_ = old.ws.Close(websocket.StatusGoingAway, "replaced")
	}
	h.conns[nodeID] = c
}

func (h *hub) remove(nodeID string, c *agentConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns[nodeID] == c {
		delete(h.conns, nodeID)
	}
}

func (h *hub) get(nodeID string) (*agentConn, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.conns[nodeID]
	return c, ok
}

func (h *hub) snapshot() map[string]*agentConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]*agentConn, len(h.conns))
	for k, v := range h.conns {
		out[k] = v
	}
	return out
}

// controlWriteTimeout: upper bound for sending one control message (cert / renew). Do not raise it:
// RunRotation sends to each connection in turn, so one slow node would delay the others.
const controlWriteTimeout = 5 * time.Second

func sendControl(ctx context.Context, c *agentConn, m wsMsg) error {
	return c.ctrl.send(ctx, m, controlWriteTimeout)
}

// MinAgentProtocol: the lowest agent protocol version the platform accepts. On every connection the agent
// sends its version in agentProtocolHeader (ProtocolVersion in agent/internal/wsclient); missing means 0.
// Raise it only for incompatible protocol changes, and change both sides together.
const MinAgentProtocol = 1

// Headers the agent sends on connect (same names on the agent side; see agent/internal/wsclient/wsclient.go).
const (
	agentProtocolHeader = "X-Besu-Agent-Protocol"
	agentVersionHeader  = "X-Besu-Agent-Version"
)

// AgentOutdatedAt: the last time this node was rejected for an agent version that is too old; nil = never (or
// it has since connected with a compatible version).
// CloseAgents tells every connected agent the platform is going away, so each one starts reconnecting right
// away instead of waiting for the TCP connection to time out. It returns once every close is done; each
// waits at most a few seconds for the agent's reply, and they run in parallel.
func (s *Service) CloseAgents() {
	var wg sync.WaitGroup
	for _, c := range s.hub.snapshot() {
		wg.Go(func() { _ = c.ws.Close(websocket.StatusGoingAway, "platform shutting down") })
	}
	wg.Wait()
}

func (s *Service) AgentOutdatedAt(nodeID string) *time.Time {
	v, ok := s.outdated.Load(nodeID)
	if !ok {
		return nil
	}
	t := v.(time.Time)
	return &t
}

// agentProtocolOK: whether the agent's protocol version is new enough. If too old (or missing, treated as 0),
// respond with 426 and a clear reason before upgrading to WebSocket, and record the rejection time, instead
// of letting it connect and then fail silently; false means a response was already written.
func (s *Service) agentProtocolOK(w http.ResponseWriter, r *http.Request, nodeID string) bool {
	p, _ := strconv.Atoi(r.Header.Get(agentProtocolHeader))
	if p >= MinAgentProtocol {
		return true
	}
	s.outdated.Store(nodeID, time.Now().UTC())
	log.Printf("pki: agent %s protocol version %d is too old (platform requires >= %d, build %q), connection rejected", nodeID, p, MinAgentProtocol, r.Header.Get(agentVersionHeader))
	http.Error(w, fmt.Sprintf("agent protocol version %d is too old, platform requires %d or later; upgrade the agent (upgrade-agent.sh)", p, MinAgentProtocol), http.StatusUpgradeRequired)
	return false
}

// handleWS: entry point for the agent's persistent connection (requires mTLS client cert verification).
func (s *Service) handleWS(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
		http.Error(w, "client certificate required", http.StatusUnauthorized)
		return
	}
	leaf := r.TLS.PeerCertificates[0]
	nodeID, ok := s.nodeIDFromCert(leaf)
	if !ok {
		http.Error(w, "unexpected identity", http.StatusForbidden)
		return
	}

	if !s.agentProtocolOK(w, r, nodeID) {
		return
	}

	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	s.outdated.Delete(nodeID)
	ctx := r.Context()

	// The message type must be Binary on both ends (yamux needs a byte stream; a peer sending Text gets
	// closed by NetConn outright).
	sess, err := yamux.Client(websocket.NetConn(ctx, ws, websocket.MessageBinary), muxConfig())
	if err != nil {
		return
	}
	defer sess.Close()

	// WARNING: do not reorder: the agent waits for the control stream before handing the session to its HTTP
	// server. It must also be opened before hub.set; once registered, others start sending control messages
	// on this connection.
	ctrlConn, err := sess.Open()
	if err != nil {
		return
	}
	conn := &agentConn{
		ws: ws, serial: leaf.SerialNumber.String(),
		sess: sess, ctrl: newCtrlStream(ctrlConn, sess), tr: agentTransport(sess),
	}
	s.hub.set(nodeID, conn)
	defer s.hub.remove(nodeID, conn)
	defer conn.tr.CloseIdleConnections()
	log.Printf("pki: agent %s connected (serial=%s…, version=%s)", nodeID, conn.serial[:12], r.Header.Get(agentVersionHeader))

	dec := json.NewDecoder(ctrlConn)
	for {
		var m wsMsg
		if err := dec.Decode(&m); err != nil {
			// Broken JSON cannot be skipped: the decoder's state is unrecoverable and every later message
			// would fail to decode.
			return
		}
		switch m.Type {
		case "csr":
			if err := s.handleRenewCSR(ctx, conn, nodeID, m.CSRPEM); err != nil {
				log.Printf("pki: renew %s failed: %v", nodeID, err)
			}

		}
	}
}

// handleRenewCSR: rotation: the agent sends a CSR for its new key, and we sign and push the cert back.
func (s *Service) handleRenewCSR(ctx context.Context, conn *agentConn, nodeID, csrPEM string) error {
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil {
		return fmt.Errorf("bad csr")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return err
	}
	leafDER, err := s.CA.IssueLeaf(ctx, csr, ca.CertProfile{
		Role: ca.RoleAgent, NodeID: nodeID, TTL: s.LeafTTL,
	})
	if err != nil {
		return err
	}
	leaf, _ := x509.ParseCertificate(leafDER)
	// Signed but not yet stored or sent, so not in effect: if the audit write fails, throw this cert away
	// (fail-closed)
	d := map[string]any{"node_id": nodeID, "serial": leaf.SerialNumber.String(), "not_after": leaf.NotAfter}
	if err := s.Store.Audit(ctx, "renew", d); err != nil {
		return fmt.Errorf("write renew audit entry, certificate not issued: %w", err)
	}
	failed := func(err error) error { // best effort: the opening entry is already written; record that this did not complete
		d := failedDetail(api.ErrorReasonOperationFailed, map[string]string{"detail": err.Error()})
		d["node_id"], d["serial"] = nodeID, leaf.SerialNumber.String()
		_ = s.Store.Audit(ctx, "renew", d)
		return err
	}
	if err := s.Store.InsertIssuedCert(ctx, store.IssuedCert{
		Serial: leaf.SerialNumber.String(), Subject: leaf.Subject.String(), NodeID: &nodeID,
		NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter, CertPEM: certPEM(leafDER),
	}); err != nil {
		return failed(err)
	}
	log.Printf("pki: renewed %s → serial %s…", nodeID, leaf.SerialNumber.String()[:8])
	if err := sendControl(ctx, conn, wsMsg{
		Type: "cert", CertPEM: certPEM(leafDER), ChainPEM: string(s.CA.ChainPEM()), NotAfter: leaf.NotAfter,
	}); err != nil {
		return failed(err)
	}
	return nil
}

func (s *Service) nodeIDFromCert(leaf *x509.Certificate) (string, bool) {
	id, err := ca.RoleFromCert(leaf)
	if err != nil || id.Role != ca.RoleAgent || id.Org != s.CA.Org {
		return "", false
	}
	return id.CN, true
}

func (s *Service) denySnapshot() []string {
	s.denyMu.RLock()
	defer s.denyMu.RUnlock()
	out := make([]string, 0, len(s.deny))
	for sn := range s.deny {
		out = append(out, sn)
	}
	return out
}

// ── background loops ────────────────────────────────────────────────

// RunRotation: pushes renew to online nodes with less than 1/3 of their lifetime left.
func (s *Service) RunRotation(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.renewPlatformLeafIfDue(ctx, time.Now().UTC())
			s.warnIntermediateDaily(time.Now().UTC())
			certs, err := s.Store.LatestCertPerNode(ctx)
			if err != nil {
				continue
			}
			conns := s.hub.snapshot()
			now := time.Now().UTC()
			for _, c := range certs {
				if c.NodeID == nil {
					continue
				}
				conn, online := conns[*c.NodeID]
				if !online {
					continue
				}
				life := c.NotAfter.Sub(c.NotBefore)
				if c.NotAfter.Sub(now) < life/3 {
					log.Printf("pki: rotating %s (cert %s… expires %s)", *c.NodeID, c.Serial[:8], c.NotAfter.Format(time.RFC3339))
					// The send layer already tore down the session on error, so nothing to compensate here: missing
					// one rotation is not fatal; after reconnect the next ticker round sends it again.
					_ = sendControl(ctx, conn, wsMsg{Type: "renew"})
				}
			}
		}
	}
}

// syncQuarantine: converges "this node's latest cert has been revoked" into nodes.quarantined_at.
//
// Revocation is an action the platform takes itself, so the fact holds the moment it is revoked; no need to
// wait for the node to dial in and fail the handshake. Deciding it at the handshake would also degrade the
// meaning to "revoked **and** still dialing": a node whose cert is revoked but which is already shut down
// would not be marked, even though it is revoked.
//
// Writes to the DB only when the state changes (quarantineState is in-process write suppression; the
// authoritative value is always in the DB).
func (s *Service) syncQuarantine(ctx context.Context) {
	certs, err := s.Store.LatestCertPerNode(ctx)
	if err != nil {
		return
	}
	for _, c := range certs {
		if c.NodeID == nil {
			continue
		}
		revoked := s.IsRevoked(c.Serial)
		if !s.quarantineChanged(*c.NodeID, revoked) {
			continue
		}
		var werr error
		if revoked {
			werr = s.Store.MarkNodeQuarantined(ctx, *c.NodeID)
		} else {
			werr = s.Store.ClearNodeQuarantine(ctx, *c.NodeID)
		}
		if werr != nil {
			// On write failure clear the marker and retry next round (2 seconds later); otherwise it would
			// not retry until the state changes again, and "revoked" is a state that never changes again.
			s.forgetQuarantineState(*c.NodeID)
			log.Printf("pki: update quarantine state of node %s: %v", *c.NodeID, werr)
		}
	}
}

// quarantineChanged: whether this node's quarantine state differs from what was last written to the DB (and
// records the new value). Purely write suppression: the deny watcher runs every 2 seconds, and without it
// would issue an UPDATE per node.
func (s *Service) quarantineChanged(nodeID string, revoked bool) bool {
	s.quarantineMu.Lock()
	defer s.quarantineMu.Unlock()
	if s.quarantineState == nil {
		s.quarantineState = map[string]bool{}
	}
	if known, seen := s.quarantineState[nodeID]; seen && known == revoked {
		return false
	}
	s.quarantineState[nodeID] = revoked
	return true
}

func (s *Service) forgetQuarantineState(nodeID string) {
	s.quarantineMu.Lock()
	defer s.quarantineMu.Unlock()
	delete(s.quarantineState, nodeID)
}

// sameSerialSet: whether two serial lists are the same set (order-independent). The deny list has no
// duplicates, so comparing lengths plus checking each entry is enough.
func sameSerialSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, s := range a {
		set[s] = struct{}{}
	}
	for _, s := range b {
		if _, ok := set[s]; !ok {
			return false
		}
	}
	return true
}

// RunDenyWatch: polls the deny list (offboarding and re-enrollment write revocations to the DB); on change it
// updates the cache and disconnects revoked peers immediately (<5s to take effect).
func (s *Service) RunDenyWatch(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			before := s.denySnapshot()
			if err := s.RefreshDenyList(ctx); err != nil {
				continue
			}
			list := s.denySnapshot()
			// Compare set contents, not just length: one entry expires while a new revocation is added in the
			// same tick -> same length, different contents, and checking only length would miss
			// this revocation, so the revoked node would not be kicked.
			if sameSerialSet(before, list) {
				continue
			}
			for nodeID, conn := range s.hub.snapshot() {
				// Revocation takes effect right here: this Close plus the rejected reconnect handshake. The
				// agent has no deny list to check, and does not need one: it does not listen on anything.
				if s.IsRevoked(conn.serial) {
					log.Printf("pki: closing revoked agent %s", nodeID)
					_ = conn.ws.Close(websocket.StatusPolicyViolation, "certificate revoked")
				}
			}
			s.syncQuarantine(ctx)
		}
	}
}
