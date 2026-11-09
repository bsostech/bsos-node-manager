package pki

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"besu-manager/backend/internal/agentbin"
	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/ca"
	"besu-manager/backend/internal/reqinfo"
	"besu-manager/backend/internal/store"
)

// Router: routes for the agent-facing endpoint (:8443, mTLS listener).
// agentBinaryDir = AGENT_BINARY_DIR (the platform server and pki share the same env; main.go passes it in,
// pki does not read it itself).
func (s *Service) Router(agentBinaryDir string) http.Handler {
	s.agentBinaryDir = agentBinaryDir
	r := chi.NewRouter()
	r.Use(middleware.Logger, middleware.Recoverer)
	r.Post("/pki/v1/join", s.handleJoin)
	// Re-enrollment: the same token ceremony, but the guard is the opposite of join (the node must already
	// exist and be offline right now). Two separate paths rather than a flag on join: join's "node id must
	// not exist" is its core security property, and letting a parameter bypass it would turn that property
	// into a runtime option.
	r.Post("/pki/v1/re-enroll", s.handleReEnroll)
	r.Get("/pki/v1/ws", s.handleWS)
	// mTLS upgrade path: the agent comes back to the platform with its existing identity to fetch the
	// binary/version/checksum (it verifies the platform cert chains to the embedded CA root, which binds it to the
	// "real platform" and blocks MITM; public HTTP + SHA-256 only guards against transport corruption, since a MITM
	// can swap both the binary and the hash). All three hang off the same mTLS listener, and each handler enforces
	// the client cert via requireAgent (any valid agent cert is allowed, no extra authorization).
	r.Get("/pki/v1/agent-binary/{arch}", s.handleAgentBinary)
	r.Get("/pki/v1/agent-binary/{arch}/version", s.handleAgentBinaryVersion)
	r.Get("/pki/v1/agent-binary/{arch}.sha256", s.handleAgentBinaryChecksum)
	return r
}

// requireAgent: mirrors handleWS's mTLS client cert check. ServerTLSConfig has already verified the chain to
// this org's root (VerifyClientCertIfGiven + ClientCAs); here we confirm a cert "was actually presented and
// its chain verified", and that the role/org is an agent of this org. On success returns nodeID; otherwise
// writes the response code and returns ("", false).
func (s *Service) requireAgent(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 {
		http.Error(w, "client certificate required", http.StatusUnauthorized)
		return "", false
	}
	nodeID, ok := s.nodeIDFromCert(r.TLS.PeerCertificates[0])
	if !ok {
		http.Error(w, "unexpected identity", http.StatusForbidden)
		return "", false
	}
	return nodeID, true
}

// handleAgentBinary: GET /pki/v1/agent-binary/{arch} (mTLS) returns the agent binary. Path/checksum logic is
// shared with the public endpoint through agentbin rather than duplicated.
func (s *Service) handleAgentBinary(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAgent(w, r); !ok {
		return
	}
	f, ok := agentbin.Path(s.agentBinaryDir, chi.URLParam(r, "arch"))
	if !ok {
		http.Error(w, "agent binary not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, f)
}

// handleAgentBinaryVersion: GET /pki/v1/agent-binary/{arch}/version (mTLS) returns the contents of the
// sibling .version file.
func (s *Service) handleAgentBinaryVersion(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAgent(w, r); !ok {
		return
	}
	vp, ok := agentbin.VersionPath(s.agentBinaryDir, chi.URLParam(r, "arch"))
	if !ok {
		http.Error(w, "agent version info not found", http.StatusNotFound)
		return
	}
	data, err := os.ReadFile(vp)
	if err != nil {
		http.Error(w, "agent version info not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}

// handleAgentBinaryChecksum: GET /pki/v1/agent-binary/{arch}.sha256 (mTLS) computes SHA-256 on the fly.
func (s *Service) handleAgentBinaryChecksum(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAgent(w, r); !ok {
		return
	}
	f, ok := agentbin.Path(s.agentBinaryDir, chi.URLParam(r, "arch"))
	if !ok {
		http.Error(w, "agent binary not found", http.StatusNotFound)
		return
	}
	sum, err := agentbin.SHA256(f)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(sum + "\n"))
}

// joinRequest: the agent sends only the token and the CSR. The node id is bound when the token is created
// (join_tokens.node_id); the on-chain identity (enode, validator address) is read later by the platform from
// the node's net_enode, so join does not accept it.
type joinRequest struct {
	Token  string `json:"token"`
	CSRPEM string `json:"csrPem"`
}

type joinResponse struct {
	CertPEM  string    `json:"certPem"`
	ChainPEM string    `json:"chainPem"` // intermediate + root (the agent's trust bundle)
	NotAfter time.Time `json:"notAfter"`
}

// enrollment: the result of the front half shared by join and re-enroll.
type enrollment struct {
	token  *store.JoinToken
	nodeID string
	req    joinRequest
	// audit: records one audit entry for this attempt (event is join or re_enroll); fields are added to
	// token_hash and node_id
	audit func(fields map[string]any) error
}

// failedDetail: the fields of a result=failed audit entry. reason is an error code the audit page
// translates; values in its sentence go in reason_params.
func failedDetail(reason api.ErrorReason, params map[string]string) map[string]any {
	d := map[string]any{"result": "failed", "reason": string(reason)}
	if len(params) > 0 {
		d["reason_params"] = params
	}
	return d
}

// tokenStatusReasons: why a join token that is no longer pending was refused, by its effective status.
var tokenStatusReasons = map[string]api.ErrorReason{
	"expired": api.ErrorReasonJoinTokenExpired,
	"used":    api.ErrorReasonJoinTokenUsed,
	"revoked": api.ErrorReasonJoinTokenRevoked,
}

// openEnrollment: the front half shared by join and re-enroll: decode the body, look up the join token, its
// status, the bound node id, and the source IP. Returning nil means a response was already written and the
// caller just returns. Rejections have no side effects, so they are rejected even if the audit write fails;
// for the allowed case the caller writes the audit entry first and only then acts (fail-closed).
func (s *Service) openEnrollment(w http.ResponseWriter, r *http.Request, event string) *enrollment {
	ctx := r.Context()
	e := &enrollment{}
	if err := json.NewDecoder(r.Body).Decode(&e.req); err != nil || e.req.Token == "" || e.req.CSRPEM == "" {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return nil
	}
	e.audit = func(fields map[string]any) error {
		d := map[string]any{"token_hash": tokenHash(e.req.Token)} // source IP is added by store.Audit
		// The node is unknown until a join token bound to one is found; an empty field would show as a blank node line.
		if e.nodeID != "" {
			d["node_id"] = e.nodeID
		}
		for k, v := range fields {
			d[k] = v
		}
		return s.Store.Audit(ctx, event, d)
	}

	t, err := s.Store.GetJoinTokenByHash(ctx, tokenHash(e.req.Token))
	if err != nil {
		http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
		return nil
	}
	if t == nil {
		_ = e.audit(failedDetail(api.ErrorReasonJoinTokenNotFound, nil))
		http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
		return nil
	}
	e.token = t
	// Fill in node_id first so rejected records still show whose token it was; record the actual status (an
	// expired token's DB column still says pending)
	e.nodeID = t.NodeID
	if st := t.EffectiveStatus(time.Now().UTC()); st != "pending" {
		_ = e.audit(failedDetail(tokenStatusReasons[st], nil))
		http.Error(w, `{"error":"token not usable"}`, http.StatusUnauthorized)
		return nil
	}
	if !checkSource(w, r, t, e.audit) {
		return nil
	}
	return e
}

// parseCSR: parses the CSR sent by the agent; a bad one gets 400, and nil means a response was already written.
func parseCSR(w http.ResponseWriter, csrPEM string) *x509.CertificateRequest {
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil {
		http.Error(w, `{"error":"bad csr"}`, http.StatusBadRequest)
		return nil
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		http.Error(w, `{"error":"bad csr"}`, http.StatusBadRequest)
		return nil
	}
	return csr
}

// fail: a step with side effects failed partway: record a failed audit entry and return 500.
func (e *enrollment) fail(w http.ResponseWriter, err error) {
	_ = e.audit(failedDetail(api.ErrorReasonOperationFailed, map[string]string{"detail": err.Error()}))
	http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
}

// finishEnrollment: the back half shared by join and re-enroll: store the new cert, mark the join token used,
// run each path's own wrap-up (afterStore), and return the new cert. Any failing step records a failed audit
// entry and returns 500.
func (s *Service) finishEnrollment(ctx context.Context, w http.ResponseWriter, e *enrollment, leafDER []byte, leaf *x509.Certificate, afterStore func() error) {
	err := s.Store.InsertIssuedCert(ctx, store.IssuedCert{
		Serial: leaf.SerialNumber.String(), Subject: leaf.Subject.String(), NodeID: &e.nodeID,
		NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter, CertPEM: certPEM(leafDER),
	})
	if err == nil {
		err = s.Store.MarkJoinTokenUsed(ctx, e.token.ID)
	}
	if err == nil {
		err = afterStore()
	}
	if err != nil {
		e.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(joinResponse{
		CertPEM:  certPEM(leafDER),
		ChainPEM: string(s.CA.ChainPEM()),
		NotAfter: leaf.NotAfter,
	})
}

// handleJoin: exchanges a join token for a leaf cert. The digest guards against MITM during the TLS handshake
// on the agent side; here we verify the secret (hash of the whole token).
func (s *Service) handleJoin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	e := s.openEnrollment(w, r, "join")
	if e == nil {
		return
	}

	// join only creates new nodes: reject node ids that already exist. Otherwise a leaked token could collide with
	// an existing id, obtain that node's valid agent cert and update its identity fields (= node identity
	// takeover). Cert rotation goes through WS renew (with the existing cert), not join; the right way to
	// reinstall/DR is to delete the node first (freeing the id + revoking the old cert) and onboard again.
	existing, err := s.Store.GetNode(ctx, e.nodeID)
	if err != nil {
		http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
		return
	}
	if existing != nil {
		_ = e.audit(failedDetail(api.ErrorReasonJoinNodeExists, nil))
		http.Error(w, `{"error":"node id already exists; delete the node first to re-onboard"}`, http.StatusConflict)
		return
	}
	csr := parseCSR(w, e.req.CSRPEM)
	if csr == nil {
		return
	}

	leafDER, err := s.CA.IssueLeaf(ctx, csr, ca.CertProfile{
		Role: ca.RoleAgent, NodeID: e.nodeID, TTL: s.LeafTTL,
	})
	if err != nil {
		_ = e.audit(failedDetail(api.ErrorReasonCertIssueFailed, map[string]string{"detail": err.Error()}))
		http.Error(w, `{"error":"issue failed"}`, http.StatusBadRequest)
		return
	}
	leaf, _ := x509.ParseCertificate(leafDER)
	// Signed but not yet stored or handed out, so not in effect: if the audit write fails, throw this cert away.
	// Like renew, this opening entry carries no result; a later step that fails adds a failed one.
	if err := e.audit(map[string]any{"serial": leaf.SerialNumber.String()}); err != nil {
		http.Error(w, `{"error":"audit unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	// join automatically registers the node. Called unconditionally: this is the only way this org's node
	// enters the inventory; the on-chain identity is read later by the collector over RPC.
	s.finishEnrollment(ctx, w, e, leafDER, leaf, func() error {
		return s.Store.UpsertOwnNode(ctx, e.nodeID, s.Org, e.token.RpcPort, e.token.MetricsPort, e.token.NodeName)
	})
}

// sourceAllowed: checks the join token's source-IP allowlist. An empty list means no restriction; with a
// non-empty list, a source IP that cannot be determined or falls in no range is blocked. Only checked at
// join / re-enroll time: after enrollment the identity is the cert, and WS connections ignore IP (a node
// changing IP or a standby host taking over is unaffected).
func sourceAllowed(ip string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	addr := net.ParseIP(ip)
	if addr == nil {
		return false
	}
	nets, err := reqinfo.ParseCIDRs(strings.Join(allowed, ","))
	if err != nil {
		return false // normalized before being stored; if it reads back broken, treat it as no match and block
	}
	for _, n := range nets {
		if n.Contains(addr) {
			return true
		}
	}
	return false
}

// checkSource: when the join token carries an allowlist, checks the request source; on mismatch writes a denial
// audit entry and returns 403. Returning false means a response was already written and the caller just returns.
func checkSource(w http.ResponseWriter, r *http.Request, t *store.JoinToken, audit func(map[string]any) error) bool {
	info, _ := reqinfo.From(r.Context())
	if sourceAllowed(info.IP, t.AllowedSources) {
		return true
	}
	_ = audit(failedDetail(api.ErrorReasonJoinSourceNotAllowed, nil)) // the entry's ip field already names the source
	http.Error(w, `{"error":"source ip not allowed for this token"}`, http.StatusForbidden)
	return false
}

// reEnrollReject: the guard decision at the re-enroll entry point. Extracted as a pure function for the same
// reason as other decision logic in the package: the handler needs a live *store.Store and WS hub to run (it
// needs a DB-backed test), but the decision itself can be tested once extracted.
type reEnrollReject string

const (
	reEnrollOK       reEnrollReject = ""
	reEnrollNotFound reEnrollReject = "not_found"
	reEnrollNotOwn   reEnrollReject = "not_own"
	reEnrollOnline   reEnrollReject = "online"
)

// reEnrollCheck: which nodes may be reissued a cert. Each of the three checks rules out one case:
//
//   - **Must already be in the inventory**: the opposite of join. If it is not, it should go through join
//     instead of quietly creating a row here; otherwise this endpoint could also create new nodes, and the
//     guards on both paths would be meaningless.
//   - **Must be a node of this org**: nodes whose org is not this org are never reissued (nodes now holds
//     only this org's nodes; this check is a safety net).
//   - **Must not still be connected to the platform**: this is the defense replacing join's "node id must not
//     exist". A node that can still connect has no need to re-enroll, and a healthy node being taken over is
//     necessarily connected, so this check rules out exactly the scenario join's rule protects against.
func reEnrollCheck(n *store.Node, selfOrg string, online bool) reEnrollReject {
	if n == nil {
		return reEnrollNotFound
	}
	if n.Org != selfOrg {
		return reEnrollNotOwn
	}
	if online {
		return reEnrollOnline
	}
	return reEnrollOK
}

// handleReEnroll: POST /pki/v1/re-enroll issues a new cert to a node that is already in the inventory.
//
// Why it exists: after the node host is rebuilt, or the cert is lost or revoked, the old cert no longer
// works, so WS renew is not an option (that path requires presenting a valid cert first); and join rejects
// existing node ids. Without this endpoint the only way to recover is to delete the node and onboard it
// again, which also breaks the audit and inventory history.
//
// join blocks "a leaked token colliding with an existing id to take over a node's identity" with "node id
// must not exist". This endpoint has to relax that, so it uses an equally strong substitute: **the node must
// not currently have a live WS connection**. A node that can still reach the platform has no need to
// re-enroll, and a healthy node being taken over is necessarily connected, so this check rules out that
// attack scenario directly.
//
// The old cert is always revoked: otherwise it stays valid until expiry, and "the node keeps running on its
// old cert" is exactly one of the symptoms this flow exists to fix. Revocation makes the action irreversible,
// which is also why the "offline" guard above has to exist.
func (s *Service) handleReEnroll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	e := s.openEnrollment(w, r, "re_enroll")
	if e == nil {
		return
	}

	existing, err := s.Store.GetNode(ctx, e.nodeID)
	if err != nil {
		http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
		return
	}
	_, online := s.hub.snapshot()[e.nodeID]
	switch reEnrollCheck(existing, s.Org, online) {
	case reEnrollNotFound:
		_ = e.audit(failedDetail(api.ErrorReasonNodeNotFound, nil))
		http.Error(w, `{"error":"node not found; use join to onboard a new node"}`, http.StatusNotFound)
		return
	case reEnrollNotOwn:
		_ = e.audit(failedDetail(api.ErrorReasonNodeNotOwn, nil))
		http.Error(w, `{"error":"node is not owned by this org"}`, http.StatusConflict)
		return
	case reEnrollOnline:
		_ = e.audit(failedDetail(api.ErrorReasonReEnrollNodeOnline, nil))
		http.Error(w, `{"error":"node is still connected; re-enrollment is only for nodes that cannot reach the platform"}`, http.StatusConflict)
		return
	}
	csr := parseCSR(w, e.req.CSRPEM)
	if csr == nil {
		return
	}

	// Revoking the old cert is the first action with side effects: record the allowed attempt first, and if
	// that write fails, change nothing
	if err := e.audit(nil); err != nil {
		http.Error(w, `{"error":"audit unavailable"}`, http.StatusServiceUnavailable)
		return
	}

	// Revoke the old one first, then sign the new one: in the opposite order there would be a window where
	// both certs are valid at once.
	revoked, err := s.Store.RevokeNodeCerts(ctx, e.nodeID, "re-enroll", "re-enroll")
	if err != nil {
		e.fail(w, err)
		return
	}

	leafDER, err := s.CA.IssueLeaf(ctx, csr, ca.CertProfile{
		Role: ca.RoleAgent, NodeID: e.nodeID, TTL: s.LeafTTL,
	})
	if err != nil {
		_ = e.audit(failedDetail(api.ErrorReasonCertIssueFailed, map[string]string{"detail": err.Error()})) // the old cert is revoked; this reissue did not complete
		http.Error(w, `{"error":"issue failed"}`, http.StatusBadRequest)
		return
	}
	leaf, _ := x509.ParseCertificate(leafDER)
	s.finishEnrollment(ctx, w, e, leafDER, leaf, func() error {
		// The inventory row and its id stay untouched (that is exactly why we do not "delete and re-add"); if
		// the enode changed after the host rebuild, the collector updates it the next time it reads
		// net_enode. The quarantine mark is a leftover of "the previous cert was rejected" and is meaningless
		// after the cert is reissued; if not cleared, the UI keeps showing quarantined until the collector's
		// next successful snapshot clears it passively.
		if err := s.Store.ClearNodeQuarantine(ctx, e.nodeID); err != nil {
			return err
		}
		// The new serial and the revocation count are only known at this point, so add one more entry (best
		// effort: the allowed entry is already written)
		_ = e.audit(map[string]any{"result": "done", "serial": leaf.SerialNumber.String(), "revoked_certs": revoked})
		return nil
	})
}

func pemEncode(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}
