package pki

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"

	"besu-manager/backend/internal/ca"
	"besu-manager/backend/internal/store"
	"besu-manager/backend/internal/store/storetest"
)

// issueCert: a leaf from c for profile, in the shape a client presents it (leaf + intermediate).
func issueCert(t *testing.T, c *ca.CA, p ca.CertProfile) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	csr, _ := x509.ParseCertificateRequest(csrDER)
	der, err := c.IssueLeaf(context.Background(), csr, p)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der, c.Inter.Raw}, PrivateKey: key}, leaf
}

// agentListener: the agent-facing router behind the real 8443 TLS config, as main.go serves it. The channel
// receives one value each time a request handler returns, so a test can wait for a WS handler to finish.
func agentListener(t *testing.T, s *Service, binDir string) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	router := s.Router(binDir)
	exited := make(chan struct{}, 64)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		router.ServeHTTP(w, r)
		exited <- struct{}{}
	}))
	srv.TLS = s.ServerTLSConfig()
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, exited
}

// agentClient: trusts only this org's root (the agent's view of the platform) and presents cert; nil = no
// client cert.
func agentClient(t *testing.T, s *Service, cert *tls.Certificate) *http.Client {
	pool := x509.NewCertPool()
	pool.AddCert(s.CA.Root)
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, ServerName: ca.PlatformServerName(s.CA.Org)}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	tr := &http.Transport{TLSClientConfig: cfg}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}
}

// dialWS: opens the agent WebSocket; code is the HTTP status of a refused upgrade (0 = no HTTP response,
// e.g. the TLS handshake failed).
func dialWS(t *testing.T, srv *httptest.Server, c *http.Client, proto string) (ws *websocket.Conn, code int, err error) {
	t.Helper()
	hdr := http.Header{}
	if proto != "" {
		hdr.Set(agentProtocolHeader, proto)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, "wss"+strings.TrimPrefix(srv.URL, "https")+"/pki/v1/ws", &websocket.DialOptions{HTTPClient: c, HTTPHeader: hdr})
	if resp != nil {
		code = resp.StatusCode
	}
	if ws != nil {
		t.Cleanup(func() { ws.CloseNow() })
	}
	return ws, code, err
}

// agentSide: the agent end of an accepted connection, set up as the agent does it: accept the control
// stream first, then serve data-plane requests with handler on the rest of the session.
func agentSide(t *testing.T, ws *websocket.Conn, handler http.Handler) (ctrl net.Conn, sess *yamux.Session) {
	t.Helper()
	sess, err := yamux.Server(websocket.NetConn(t.Context(), ws, websocket.MessageBinary), muxConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctrl, err = sess.AcceptStreamWithContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if handler != nil {
		srv := &http.Server{Handler: handler}
		go srv.Serve(sess)
		t.Cleanup(func() { srv.Close() })
	}
	return ctrl, sess
}

// waitFor polls cond until it holds or 5 seconds pass.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("等不到:%s", what)
}

func waitExit(t *testing.T, exited <-chan struct{}) {
	t.Helper()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("WS handler 沒有結束")
	}
}

// Only a verified agent cert of this org, not revoked, from a new-enough agent gets a connection. Every
// refusal leaves the node offline, so the platform never routes requests to it.
func TestAgentChannelRefusesWithoutAgentIdentity(t *testing.T) {
	ctx := context.Background()
	st := pkiTestStore(t)
	s, err := Load(ctx, st, testMaster(t), "org-test", 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := agentListener(t, s, "")

	platform, _ := issueCert(t, s.CA, ca.CertProfile{Role: ca.RolePlatform})
	// Same CA keys loaded under another org id: the chain verifies, only O differs.
	mat, _ := st.GetCAMaterial(ctx)
	otherOrg, err := ca.FromMaterial(s.master, "org-other", mat)
	if err != nil {
		t.Fatal(err)
	}
	foreign, _ := issueCert(t, otherOrg, ca.CertProfile{Role: ca.RoleAgent, NodeID: "vn-01"})
	revoked, revokedLeaf := issueCert(t, s.CA, ca.CertProfile{Role: ca.RoleAgent, NodeID: "vn-01"})
	s.addDeny(revokedLeaf.SerialNumber.String())
	valid, _ := issueCert(t, s.CA, ca.CertProfile{Role: ca.RoleAgent, NodeID: "vn-01"})

	for _, tc := range []struct {
		name  string
		cert  *tls.Certificate
		proto string
		code  int // 0 = refused in the TLS handshake
	}{
		{"沒有 client 憑證", nil, "1", http.StatusUnauthorized},
		{"platform 角色憑證", &platform, "1", http.StatusForbidden},
		{"別的單位的 agent 憑證", &foreign, "1", http.StatusForbidden},
		{"已撤銷的 agent 憑證", &revoked, "1", 0},
		{"agent 協定太舊", &valid, "", http.StatusUpgradeRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, code, err := dialWS(t, srv, agentClient(t, s, tc.cert), tc.proto)
			if err == nil || ws != nil || code != tc.code {
				t.Fatalf("應被拒:code=%d(want %d)err=%v", code, tc.code, err)
			}
			if ids := s.OnlineNodeIDs(); len(ids) != 0 {
				t.Fatalf("被拒的連線不該上線:%v", ids)
			}
			if _, ok := s.AgentTransport("vn-01"); ok {
				t.Fatal("被拒的節點不該拿得到 transport")
			}
		})
	}
}

// A connected agent is registered under the node ID from its certificate. A renew CSR is signed for that
// same node, whatever the CSR claims: the issued cert, the DB row and the audit entry all name the
// cert's node, and the agent gets the new cert and chain back on the control stream.
func TestAgentChannelRenewBindsCertNode(t *testing.T) {
	ctx := context.Background()
	st := pkiTestStore(t)
	s, err := Load(ctx, st, testMaster(t), "org-test", 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := agentListener(t, s, "")
	events := storetest.SinkForTest(t, nil)

	cert, _ := issueCert(t, s.CA, ca.CertProfile{Role: ca.RoleAgent, NodeID: "vn-01"})
	ws, code, err := dialWS(t, srv, agentClient(t, s, &cert), "1")
	if err != nil {
		t.Fatalf("合法 agent 應連得上:code=%d err=%v", code, err)
	}
	ctrl, _ := agentSide(t, ws, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "metrics of "+r.URL.Path)
	}))
	waitFor(t, "vn-01 上線", func() bool { return slices.Equal(s.OnlineNodeIDs(), []string{"vn-01"}) })

	// The data plane reaches this agent through the transport the platform hands out.
	rt, ok := s.AgentTransport("vn-01")
	if !ok {
		t.Fatal("上線節點應拿得到 transport")
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+RequestHost+HostMetricsPath, nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "metrics of "+HostMetricsPath {
		t.Fatalf("transport 沒打到這台 agent:%q", body)
	}
	if _, ok := s.AgentTransport("vn-02"); ok {
		t.Fatal("沒連線的節點不該拿得到 transport")
	}

	// A broken CSR is dropped without an answer; the valid one after it names another node in its own
	// subject, in the same O/OU/CN form the CA writes into issued certs.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{
		CommonName: "vn-02", Organization: []string{"org-test"}, OrganizationalUnit: []string{"node"},
	}}, key)
	csrPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))
	enc := json.NewEncoder(ctrl)
	for _, m := range []map[string]string{
		{"type": "csr", "csrPem": "not a csr"},
		{"type": "csr", "csrPem": csrPEM},
	} {
		if err := enc.Encode(m); err != nil {
			t.Fatal(err)
		}
	}
	_ = ctrl.SetReadDeadline(time.Now().Add(5 * time.Second))
	var got wsMsg
	if err := json.NewDecoder(ctrl).Decode(&got); err != nil || got.Type != "cert" {
		t.Fatalf("應收到 cert:%+v err=%v", got, err)
	}
	b, _ := pem.Decode([]byte(got.CertPEM))
	if b == nil {
		t.Fatalf("cert 訊息沒有憑證:%q", got.CertPEM)
	}
	leaf, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	id, err := ca.RoleFromCert(leaf)
	if err != nil || id.CN != "vn-01" || id.Org != "org-test" || id.Role != ca.RoleAgent {
		t.Fatalf("續簽憑證身分應是連線憑證的 vn-01:%+v err=%v", id, err)
	}
	if pub, ok := leaf.PublicKey.(*ecdsa.PublicKey); !ok || !pub.Equal(&key.PublicKey) {
		t.Fatal("續簽憑證應簽給 CSR 的公鑰")
	}
	if got.ChainPEM != string(s.CA.ChainPEM()) || !got.NotAfter.Equal(leaf.NotAfter) {
		t.Fatalf("chain / notAfter 不對:%q %s", got.ChainPEM, got.NotAfter)
	}

	certs, err := st.LatestCertPerNode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 1 || *certs[0].NodeID != "vn-01" || certs[0].Serial != leaf.SerialNumber.String() {
		t.Fatalf("DB 應只記一張屬於 vn-01 的新憑證:%+v", certs)
	}
	if !slices.Equal(*events, []string{"renew"}) {
		t.Fatalf("壞 CSR 不該留稽核,合法的記一筆 renew:%v", *events)
	}
}

// A second connection for the same node replaces the first: the old one is closed, and when its handler
// unwinds it must not unregister the new one. Once the last connection ends the node goes offline.
func TestAgentChannelReplacedConnection(t *testing.T) {
	ctx := context.Background()
	st := pkiTestStore(t)
	s, err := Load(ctx, st, testMaster(t), "org-test", 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv, exited := agentListener(t, s, "")
	connect := func() (*websocket.Conn, net.Conn, string) {
		cert, leaf := issueCert(t, s.CA, ca.CertProfile{Role: ca.RoleAgent, NodeID: "vn-01"})
		ws, code, err := dialWS(t, srv, agentClient(t, s, &cert), "1")
		if err != nil {
			t.Fatalf("code=%d err=%v", code, err)
		}
		ctrl, _ := agentSide(t, ws, nil)
		serial := leaf.SerialNumber.String()
		waitFor(t, "新連線註冊", func() bool { c, ok := s.hub.get("vn-01"); return ok && c.serial == serial })
		return ws, ctrl, serial
	}
	_, first, _ := connect()
	secondWS, _, second := connect()

	_ = first.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := first.Read(make([]byte, 1)); err == nil || isTimeout(err) {
		t.Fatalf("被取代的舊連線應被關掉:%v", err)
	}
	waitExit(t, exited)
	if c, ok := s.hub.get("vn-01"); !ok || c.serial != second {
		t.Fatal("舊連線收尾時把新連線也註銷了")
	}

	// The second connection ends: the node goes offline.
	secondWS.CloseNow()
	waitExit(t, exited)
	if ids := s.OnlineNodeIDs(); len(ids) != 0 {
		t.Fatalf("連線都斷了應離線:%v", ids)
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// Renew is fail-closed: if the opening audit entry cannot be written, no cert is stored or sent; if storing
// fails after it, a failed entry is added and nothing is sent. The connection is nil on purpose: reaching
// the send step would panic.
func TestRenewCSRFailClosed(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	csrPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))

	for _, tc := range []struct {
		name   string
		sink   func(t *testing.T, cancel context.CancelFunc) *[]string
		events []string
	}{
		{"稽核寫不進去", func(t *testing.T, _ context.CancelFunc) *[]string { return storetest.FailAuditForTest(t) }, []string{"renew"}},
		{"存憑證失敗", func(t *testing.T, cancel context.CancelFunc) *[]string {
			var events []string
			saved := store.AuditSink
			store.AuditSink = func(_ context.Context, _ store.AuditDB, event string, d map[string]any) error {
				if r, _ := d["result"].(string); r != "" {
					event += ":" + r
				}
				events = append(events, event)
				cancel() // the next DB write (storing the cert) fails
				return nil
			}
			t.Cleanup(func() { store.AuditSink = saved })
			return &events
		}, []string{"renew", "renew:failed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := pkiTestStore(t)
			s, err := Load(context.Background(), st, testMaster(t), "org-test", 72*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			events := tc.sink(t, cancel)
			if err := s.handleRenewCSR(ctx, nil, "vn-01", csrPEM); err == nil {
				t.Fatal("應回錯誤")
			}
			if certs, _ := st.LatestCertPerNode(context.Background()); len(certs) != 0 {
				t.Fatalf("失敗時不該存憑證:%+v", certs)
			}
			if !slices.Equal(*events, tc.events) {
				t.Fatalf("audit = %v, want %v", *events, tc.events)
			}
		})
	}
}

// Quarantine follows the revocation of each node's latest cert: revoking marks the node, a newer unrevoked
// cert clears it, and nodes whose cert is fine are left alone.
func TestSyncQuarantine(t *testing.T) {
	s, st := enrollFixture(t, "n")
	if w := postEnroll(t, s.handleJoin); w.Code != http.StatusOK {
		t.Fatalf("join = %d %s", w.Code, w.Body)
	}
	ctx := context.Background()
	name := "other"
	if err := st.UpsertOwnNode(ctx, "vn-02", "org-test", 8545, 9545, &name); err != nil {
		t.Fatal(err)
	}
	insert := func(nodeID, serial string, notBefore time.Time) {
		t.Helper()
		if err := st.InsertIssuedCert(ctx, store.IssuedCert{Serial: serial, Subject: "CN=" + nodeID, NodeID: &nodeID,
			NotBefore: notBefore, NotAfter: notBefore.Add(72 * time.Hour), CertPEM: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	insert("vn-02", "vn-02-serial", time.Now().UTC())
	quarantined := func(id string) bool {
		t.Helper()
		n, err := st.GetNode(ctx, id)
		if err != nil || n == nil {
			t.Fatalf("node %s: %v %v", id, n, err)
		}
		return n.QuarantinedAt != nil
	}

	if _, err := st.RevokeNodeCerts(ctx, "vn-01", "retired", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshDenyList(ctx); err != nil {
		t.Fatal(err)
	}
	s.syncQuarantine(ctx)
	if !quarantined("vn-01") || quarantined("vn-02") {
		t.Fatalf("撤銷的 vn-01 應隔離、vn-02 不受影響:vn-01=%v vn-02=%v", quarantined("vn-01"), quarantined("vn-02"))
	}

	insert("vn-01", "vn-01-new", time.Now().UTC().Add(time.Hour))
	s.syncQuarantine(ctx)
	if quarantined("vn-01") {
		t.Fatal("最新憑證沒撤銷,隔離應解除")
	}
}

// The deny watcher compares the snapshot before and after each refresh; a revocation added in-process
// must show up in it.
func TestDenySnapshotIncludesAddedRevocation(t *testing.T) {
	s := &Service{deny: map[string]bool{"a": true}}
	before := s.denySnapshot()
	s.addDeny("b")
	after := s.denySnapshot()
	if sameSerialSet(before, after) || !sameSerialSet(after, []string{"a", "b"}) || !s.IsRevoked("b") {
		t.Fatalf("before=%v after=%v", before, after)
	}
}

// addDeny puts a serial on the cached deny list directly, without going through the database.
func (s *Service) addDeny(serial string) {
	s.denyMu.Lock()
	s.deny[serial] = true
	s.denyMu.Unlock()
}
