package wsclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"besu-manager/agent/internal/certstore"
	"besu-manager/agent/internal/certstore/certtest"
)

// agentStore: a certstore loaded from disk as after join: node cert n1 of c's org.
func agentStore(t *testing.T, c *certtest.CA) (*certstore.Store, string) {
	t.Helper()
	dir := t.TempDir()
	k := certtest.Key(t)
	der, _ := x509.MarshalECPrivateKey(k)
	leaf := c.Leaf(t, "node", "n1", k.Public(), x509.ExtKeyUsageClientAuth)
	for name, data := range map[string][]byte{
		"agent.key": certstore.PEMBlock("EC PRIVATE KEY", der),
		"agent.crt": certstore.PEMBlock("CERTIFICATE", leaf.Raw),
		"ca.pem":    c.Bundle(),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := certstore.Load(dir)
	if err != nil || s == nil {
		t.Fatalf("certstore.Load: %v", err)
	}
	return s, dir
}

// Rotation on the agent side: renew makes it send a CSR for a fresh key; the cert the platform returns for
// that key is swapped in (memory and disk) and presented on the next handshake. A cert that arrives
// without a pending key, or for a different key, is refused and the current cert stays.
func TestRenewThenCertRotatesKey(t *testing.T) {
	ca := certtest.NewCA(t, "org-a")
	cs, dir := agentStore(t, ca)
	c := New("platform:8443", cs, nil)
	ctrl, platformCtrl, _ := ctrlPair(t, nil)
	original := cs.Certificate().Leaf.Raw
	current := func() []byte {
		got, err := c.dialTLSConfig().GetClientCertificate(nil)
		if err != nil {
			t.Fatal(err)
		}
		return got.Leaf.Raw
	}

	c.dispatch(ctrl, wsMsg{Type: "cert", CertPEM: "x", ChainPEM: string(ca.Bundle())})
	if !bytes.Equal(current(), original) {
		t.Fatal("沒有等待中的金鑰時不該換憑證")
	}

	c.dispatch(ctrl, wsMsg{Type: "renew"})
	_ = platformCtrl.SetReadDeadline(time.Now().Add(5 * time.Second))
	var m wsMsg
	if err := json.NewDecoder(bufio.NewReader(platformCtrl)).Decode(&m); err != nil || m.Type != "csr" {
		t.Fatalf("renew 後應送出 csr:%+v err=%v", m, err)
	}
	b, _ := pem.Decode([]byte(m.CSRPEM))
	if b == nil {
		t.Fatalf("csr 訊息沒有 CSR:%q", m.CSRPEM)
	}
	csr, err := x509.ParseCertificateRequest(b.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		t.Fatalf("CSR 要是新金鑰自簽的:%v", err)
	}

	// a cert for some other key: refused, the pending key is kept for the right one
	wrong := ca.Leaf(t, "node", "n1", certtest.Key(t).Public(), x509.ExtKeyUsageClientAuth)
	c.dispatch(ctrl, wsMsg{Type: "cert", CertPEM: string(certstore.PEMBlock("CERTIFICATE", wrong.Raw)), ChainPEM: string(ca.Bundle())})
	if !bytes.Equal(current(), original) {
		t.Fatal("金鑰對不上的憑證不該換上")
	}

	issued := ca.Leaf(t, "node", "n1", csr.PublicKey, x509.ExtKeyUsageClientAuth)
	issuedPEM := certstore.PEMBlock("CERTIFICATE", issued.Raw)
	c.dispatch(ctrl, wsMsg{Type: "cert", CertPEM: string(issuedPEM), ChainPEM: string(ca.Bundle())})
	if !bytes.Equal(current(), issued.Raw) {
		t.Fatal("平台簽回的憑證應熱替換上去")
	}
	if onDisk, _ := os.ReadFile(filepath.Join(dir, "agent.crt")); !bytes.Equal(onDisk, issuedPEM) {
		t.Fatal("新憑證應寫回磁碟,重啟後才不會用回舊的")
	}
	if c.pendingKey != nil {
		t.Fatal("換上後等待中的金鑰應清掉")
	}
}

// The agent accepts only its own org's platform: a server cert chaining to the root through the current
// intermediate with O=<org>, OU=platform. Anything else fails the handshake.
func TestDialTLSConfigVerifiesPlatform(t *testing.T) {
	ca := certtest.NewCA(t, "org-a")
	cs, _ := agentStore(t, ca)
	cfg := New("platform:8443", cs, nil).dialTLSConfig()
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %x", cfg.MinVersion)
	}
	other := certtest.NewCA(t, "org-a") // same org name, different root
	pub := certtest.Key(t).Public()
	chain := func(leaf *x509.Certificate, c *certtest.CA) [][]byte { return [][]byte{leaf.Raw, c.Inter.Raw} }
	rot := ca.Rotated(t) // same root, an intermediate the agent's bundle does not carry

	for _, tc := range []struct {
		name string
		raw  [][]byte
		ok   bool
	}{
		{"本單位 platform", chain(ca.Leaf(t, "platform", "platform", pub, x509.ExtKeyUsageServerAuth), ca), true},
		{"沒有憑證", nil, false},
		{"node 角色", chain(ca.Leaf(t, "node", "n2", pub, x509.ExtKeyUsageServerAuth), ca), false},
		{"別的單位", chain((&certtest.CA{Org: "org-b", Inter: ca.Inter, InterKey: ca.InterKey}).Leaf(t, "platform", "platform", pub, x509.ExtKeyUsageServerAuth), ca), false},
		{"別的 root", chain(other.Leaf(t, "platform", "platform", pub, x509.ExtKeyUsageServerAuth), other), false},
		{"bundle 外的 intermediate", chain(rot.Leaf(t, "platform", "platform", pub, x509.ExtKeyUsageServerAuth), rot), false},
		{"只有 clientAuth", chain(ca.Leaf(t, "platform", "platform", pub, x509.ExtKeyUsageClientAuth), ca), false},
	} {
		err := cfg.VerifyPeerCertificate(tc.raw, nil)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err=%v want ok=%v", tc.name, err, tc.ok)
		}
	}
}

// After the platform rotates the intermediate key and the agent swaps to the new bundle, a platform cert
// from the old intermediate is refused even though it still chains to the same root.
func TestDialTLSConfigRefusesReplacedIntermediate(t *testing.T) {
	ca := certtest.NewCA(t, "org-a")
	cs, _ := agentStore(t, ca)
	c := New("platform:8443", cs, nil)
	pub := certtest.Key(t).Public()
	oldPlat := [][]byte{ca.Leaf(t, "platform", "platform", pub, x509.ExtKeyUsageServerAuth).Raw, ca.Inter.Raw}
	if err := c.dialTLSConfig().VerifyPeerCertificate(oldPlat, nil); err != nil {
		t.Fatalf("換之前應接受:%v", err)
	}

	rot := ca.Rotated(t)
	k := certtest.Key(t)
	der, _ := x509.MarshalECPrivateKey(k)
	leaf := rot.Leaf(t, "node", "n1", k.Public(), x509.ExtKeyUsageClientAuth)
	if err := cs.Swap(certstore.PEMBlock("EC PRIVATE KEY", der), certstore.PEMBlock("CERTIFICATE", leaf.Raw), rot.Bundle()); err != nil {
		t.Fatal(err)
	}
	if err := c.dialTLSConfig().VerifyPeerCertificate(oldPlat, nil); err == nil {
		t.Fatal("舊 intermediate 簽的平台憑證不該再被接受")
	}
	newPlat := [][]byte{rot.Leaf(t, "platform", "platform", pub, x509.ExtKeyUsageServerAuth).Raw, rot.Inter.Raw}
	if err := c.dialTLSConfig().VerifyPeerCertificate(newPlat, nil); err != nil {
		t.Fatalf("新 intermediate 簽的應接受:%v", err)
	}
}

// ALLOW_PORTS parsing: spaces around entries are ignored and ports are normalized to plain decimal; unset
// (empty) means no restriction (an empty set); a value of only spaces is not unset. Anything else that is not a 1–65535 port, or a non-empty value with
// no ports at all, is an error rather than an empty set that would open every port.
func TestParseAllowPorts(t *testing.T) {
	for in, want := range map[string][]string{
		"":                {},
		"8545":            {"8545"},
		" 8545 ,, 9545 ,": {"8545", "9545"},
		"08545,1,65535":   {"8545", "1", "65535"},
	} {
		got, err := ParseAllowPorts(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if len(got) != len(want) {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
		for _, p := range want {
			if !got[p] {
				t.Errorf("%q: missing %s in %v", in, p, got)
			}
		}
	}
	for _, in := range []string{" ", ",", " , ", "abc", "0", "99999", "65536", "8545x", "8545,abc", "-1", "+8545", "85 45"} {
		if got, err := ParseAllowPorts(in); err == nil {
			t.Errorf("%q: want error, got %v", in, got)
		}
	}
}

// The data plane the agent serves on the session applies ALLOW_PORTS and the path allowlist, and stops
// when the server is closed.
func TestServeRequestsAppliesAllowlist(t *testing.T) {
	ctrl, _, platformSess := ctrlPair(t, nil)
	c := New("platform:8443", nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("host " + r.URL.Path))
	}))
	c.AllowPorts = map[string]bool{"8545": true}
	srv := c.serveRequests(ctrl.sess)

	do := func(path, target string) (int, string) {
		t.Helper()
		stream, err := platformSess.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		req, _ := http.NewRequest(http.MethodGet, "http://agent"+path, nil)
		if target != "" {
			req.Header.Set(targetHeader, target)
		}
		if err := req.Write(stream); err != nil {
			t.Fatal(err)
		}
		_ = stream.SetReadDeadline(time.Now().Add(5 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(stream), req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return resp.StatusCode, buf.String()
	}
	if code, body := do("/host-metrics", ""); code != http.StatusOK || body != "host /host-metrics" {
		t.Fatalf("host metrics: %d %q", code, body)
	}
	if code, _ := do("/debug/pprof", ""); code != http.StatusNotFound {
		t.Fatalf("清單外路徑: %d", code)
	}
	if code, _ := do("/", "127.0.0.1:9545"); code != http.StatusForbidden {
		t.Fatalf("ALLOW_PORTS 外的埠: %d", code)
	}

	// connectAndLoop closes the server when the connection ends; that also closes the session it serves on.
	srv.Close()
	if !ctrl.sess.IsClosed() {
		t.Fatal("資料面關掉後 session 應一起關")
	}
}

// Stopping the agent closes the tunnel with a close frame (going away), not by dropping the connection, so
// the platform sees the node leave at once instead of waiting for a read to fail.
func TestRunSendsCloseFrameWhenStopped(t *testing.T) {
	ca := certtest.NewCA(t, "org-a")
	cs, _ := agentStore(t, ca)
	key := certtest.Key(t)
	leaf := ca.Leaf(t, "platform", "platform", key.Public(), x509.ExtKeyUsageServerAuth)

	accepted := make(chan struct{})
	status := make(chan websocket.StatusCode, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		close(accepted)
		_, _, err = conn.Read(context.Background())
		status <- websocket.CloseStatus(err)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leaf.Raw, ca.Inter.Raw}, PrivateKey: key}}, MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	defer srv.Close()

	c := New(strings.TrimPrefix(srv.URL, "https://"), cs, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("agent 沒有連上")
	}
	time.Sleep(200 * time.Millisecond) // the platform accepts before the agent's dial returns; stop only once it is connected
	cancel()
	select {
	case got := <-status:
		if got != websocket.StatusGoingAway {
			t.Fatalf("平台收到的關閉狀態 = %v,應為 going away", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("平台沒收到關閉")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run 沒有結束")
	}
}
