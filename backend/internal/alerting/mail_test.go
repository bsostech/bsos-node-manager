package alerting

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
)

// mailServer: a fake SMTP server that can run EHLO -> (STARTTLS) -> AUTH -> MAIL -> RCPT -> DATA -> QUIT, recording the commands and mail it receives.
type mailServer struct {
	host, port string
	pool       *x509.CertPool // the test CA that issued this server's certificate
	mu         sync.Mutex
	cmds       []string
	data       string
	mailReply  string // non-empty = reply to MAIL with this line (e.g. 553 sender rejected)
	rcptReply  string // non-empty = reply to RCPT with this line (e.g. 550 no such recipient)
	endReply   string // non-empty = reply with this line after the mail body (e.g. 554 rejected)
	dropQuit   bool   // drop the connection on QUIT without replying (mail already accepted)
	authTLS    bool   // advertise AUTH only once the connection is encrypted (as Gmail and M365 do on 587)
	authMechs  string // non-empty = advertise these AUTH mechanisms instead of PLAIN LOGIN
	authReply  string // non-empty = reply to AUTH with this line (e.g. 530 STARTTLS first)
}

func (m *mailServer) commands() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strings.Join(m.cmds, "|")
}

// testCA: an internal CA that issues mail server certificates.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T, name string) *testCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return &testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// startMailServer: starttls = advertise and accept STARTTLS; implicit = TLS from the first byte (the port 465 style). The certificate SAN is the bound IP.
func startMailServer(t *testing.T, starttls, implicit bool) *mailServer {
	return startMailServerCA(t, newTestCA(t, "fake CA"), starttls, implicit)
}

// startMailServerCA: as startMailServer, with the server certificate issued by ca.
func startMailServerCA(t *testing.T, ca *testCA, starttls, implicit bool) *mailServer {
	t.Helper()
	ip := outboundIP(t)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "fake"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP(ip)}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	m := &mailServer{pool: x509.NewCertPool()}
	m.pool.AddCert(ca.cert)
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}

	ln, err := net.Listen("tcp", ip+":0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	m.host, m.port, _ = net.SplitHostPort(ln.Addr().String())
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go m.serve(conn, tlsCfg, starttls, implicit)
		}
	}()
	return m
}

func (m *mailServer) serve(conn net.Conn, tlsCfg *tls.Config, starttls, implicit bool) {
	defer func() { conn.Close() }()
	if implicit {
		conn = tls.Server(conn, tlsCfg)
	}
	br := bufio.NewReader(conn)
	w := func(s string) { conn.Write([]byte(s + "\r\n")) }
	w("220 fake ESMTP")
	secure := implicit
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		up := strings.ToUpper(cmd)
		m.mu.Lock()
		m.cmds = append(m.cmds, strings.Fields(up + " x")[0])
		m.mu.Unlock()
		switch {
		case strings.HasPrefix(up, "EHLO"):
			w("250-fake greets")
			if starttls && !secure {
				w("250-STARTTLS")
			}
			if m.authTLS && !secure {
				w("250 SIZE 1000000")
			} else if m.authMechs != "" {
				w("250 AUTH " + m.authMechs)
			} else {
				w("250 AUTH PLAIN LOGIN")
			}
		case strings.HasPrefix(up, "STARTTLS"):
			w("220 go ahead")
			conn = tls.Server(conn, tlsCfg)
			br = bufio.NewReader(conn)
			secure = true
		case strings.HasPrefix(up, "AUTH") && m.authReply != "":
			w(m.authReply)
		case strings.HasPrefix(up, "AUTH"):
			w("235 2.7.0 ok")
		case strings.HasPrefix(up, "DATA"):
			w("354 go")
			var b strings.Builder
			for {
				l, err := br.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			m.mu.Lock()
			m.data = b.String()
			m.mu.Unlock()
			if m.endReply != "" {
				w(m.endReply)
				continue
			}
			w("250 queued")
		case strings.HasPrefix(up, "MAIL") && m.mailReply != "":
			w(m.mailReply)
		case strings.HasPrefix(up, "RCPT") && m.rcptReply != "":
			w(m.rcptReply)
		case strings.HasPrefix(up, "QUIT"):
			if !m.dropQuit {
				w("221 bye")
			}
			return
		default:
			w("250 ok")
		}
	}
}

func (m *mailServer) config(security Security, trust bool) MailConfig {
	port, _ := strconv.Atoi(m.port)
	c := MailConfig{SMTP: SMTP{Host: m.host, Port: port, Username: "u", Password: "p", From: "noreply@org-a.example", Security: security}}
	if trust {
		c.RootCAs = m.pool
	}
	return c
}

// All three encryption modes can send; with STARTTLS credentials are sent only after the upgrade; mail headers and the quoted-printable body are correct.
func TestSendMailEncrypted(t *testing.T) {
	for _, tc := range []struct {
		name               string
		security           Security
		starttls, implicit bool
		wantBeforeAuth     string
	}{
		{"starttls", SecurityStartTLS, true, false, "EHLO|STARTTLS|EHLO|AUTH"},
		{"tls", SecurityTLS, false, true, "EHLO|AUTH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := startMailServer(t, tc.starttls, tc.implicit)
			if err := SendMail(context.Background(), m.config(tc.security, true), "alice@org-a.example", "臨時密碼", "帳號 alice\n密碼 Abc-123"); err != nil {
				t.Fatal(err)
			}
			if got := m.commands(); !strings.HasPrefix(got, tc.wantBeforeAuth+"|MAIL|RCPT|DATA") {
				t.Fatalf("命令順序 %s", got)
			}
			m.mu.Lock()
			data := m.data
			m.mu.Unlock()
			for _, want := range []string{"To: alice@org-a.example", "Subject: =?utf-8?q?", "Content-Transfer-Encoding: quoted-printable", "Date: "} {
				if !strings.Contains(data, want) {
					t.Errorf("信件缺 %q:\n%s", want, data)
				}
			}
		})
	}
}

// starttls configured but not advertised by the server: fails, and neither credentials nor mail are sent (no fallback to plaintext).
func TestSendMailStartTLSRequired(t *testing.T) {
	m := startMailServer(t, false, false)
	err := SendMail(context.Background(), m.config(SecurityStartTLS, true), "alice@org-a.example", "s", "b")
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("要失敗並點出 STARTTLS:%v", err)
	}
	if r, _, _ := apierr.ReasonOf(err); r != api.ErrorReasonSmtpStarttlsUnsupported {
		t.Errorf("reason %q,應為 smtp_starttls_unsupported", r)
	}
	if got := m.commands(); strings.Contains(got, "AUTH") || strings.Contains(got, "MAIL") {
		t.Fatalf("不該送出帳密或信:%s", got)
	}
}

// A server certificate from an internal CA: trusted with that CA in RootCAs; without any CA file it fails
// with smtp_cert_untrusted, and with a CA file that holds a different CA with smtp_cert_not_in_ca, on both
// STARTTLS and TLS.
func TestSendMailInternalCA(t *testing.T) {
	ca, other := newTestCA(t, "internal CA"), newTestCA(t, "other CA")
	otherPool := x509.NewCertPool()
	otherPool.AddCert(other.cert)
	for _, tc := range []struct {
		name               string
		security           Security
		starttls, implicit bool
	}{
		{"starttls", SecurityStartTLS, true, false},
		{"tls", SecurityTLS, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := startMailServerCA(t, ca, tc.starttls, tc.implicit)
			if err := CheckMail(context.Background(), m.config(tc.security, true)); err != nil {
				t.Fatalf("trusted internal CA: %v", err)
			}
			// Without RootCAs, macOS hands verification to the system, which does not report an unknown
			// issuer as x509.UnknownAuthorityError; the server runs on Linux, where Go verifies itself.
			if runtime.GOOS != "darwin" {
				err := CheckMail(context.Background(), m.config(tc.security, false))
				if r, p, _ := apierr.ReasonOf(err); r != api.ErrorReasonSmtpCertUntrusted || len(p) != 0 {
					t.Errorf("no CA file: err %v reason %q params %v, want smtp_cert_untrusted", err, r, p)
				}
			}
			cfg := m.config(tc.security, false)
			cfg.RootCAs = otherPool
			if r, _, _ := apierr.ReasonOf(CheckMail(context.Background(), cfg)); r != api.ErrorReasonSmtpCertNotInCa {
				t.Errorf("CA file without the issuer: reason %q, want smtp_cert_not_in_ca", r)
			}
		})
	}
}

// The reason split itself, independent of which verifier the platform uses: unknown issuer is untrusted
// without a CA file and not-in-CA with one; other verification failures keep the step's reason and detail.
func TestTLSFailedReasons(t *testing.T) {
	unknown := &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}
	if r, p, _ := apierr.ReasonOf(tlsFailed(MailConfig{}, api.ErrorReasonSmtpStarttlsFailed, "STARTTLS", unknown)); r != api.ErrorReasonSmtpCertUntrusted || len(p) != 0 {
		t.Errorf("no CA file: %q %v", r, p)
	}
	if r, _, _ := apierr.ReasonOf(tlsFailed(MailConfig{RootCAs: x509.NewCertPool()}, api.ErrorReasonTlsHandshakeFailed, "TLS handshake", unknown)); r != api.ErrorReasonSmtpCertNotInCa {
		t.Errorf("CA file: %q", r)
	}
	host := &tls.CertificateVerificationError{Err: x509.HostnameError{Certificate: &x509.Certificate{}, Host: "mail"}}
	if r, p, _ := apierr.ReasonOf(tlsFailed(MailConfig{}, api.ErrorReasonTlsHandshakeFailed, "TLS handshake", host)); r != api.ErrorReasonTlsHandshakeFailed || p["detail"] == "" {
		t.Errorf("hostname mismatch: %q %v", r, p)
	}
}

// A CA file may hold several CAs (two mail servers, or a public CA kept alongside the internal one):
// servers issued by either one pass.
func TestSMTPCABundle(t *testing.T) {
	ca1, ca2 := newTestCA(t, "CA one"), newTestCA(t, "CA two")
	f := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(f, append(append([]byte{}, ca1.pem...), ca2.pem...), 0o600)
	t.Setenv("SMTP_CA_FILE", f)
	bundle, err := SMTPCAFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	for _, ca := range []*testCA{ca1, ca2} {
		m := startMailServerCA(t, ca, true, false)
		cfg := m.config(SecurityStartTLS, false)
		cfg.RootCAs = bundle.Roots()
		if err := CheckMail(context.Background(), cfg); err != nil {
			t.Errorf("%s: %v", ca.cert.Subject.CommonName, err)
		}
	}
}

func TestSMTPCAFromEnv(t *testing.T) {
	t.Setenv("SMTP_CA_FILE", "")
	if ca, err := SMTPCAFromEnv(); ca != nil || err != nil || ca.Roots() != nil {
		t.Fatalf("unset: %v %v", ca, err)
	}
	dir := t.TempDir()
	t.Setenv("SMTP_CA_FILE", filepath.Join(dir, "missing.pem"))
	if _, err := SMTPCAFromEnv(); err == nil {
		t.Fatal("unreadable file must be refused")
	}
	bad := filepath.Join(dir, "bad.pem")
	os.WriteFile(bad, []byte("not a cert"), 0o600)
	t.Setenv("SMTP_CA_FILE", bad)
	if _, err := SMTPCAFromEnv(); err == nil {
		t.Fatal("file without PEM must be refused")
	}
	good := filepath.Join(dir, "ca.pem")
	ca := newTestCA(t, "internal CA")
	os.WriteFile(good, ca.pem, 0o600)
	t.Setenv("SMTP_CA_FILE", good)
	got, err := SMTPCAFromEnv()
	if err != nil || got.Roots() == nil || string(got.PEM) != string(ca.pem) {
		t.Fatalf("valid file: %+v %v", got, err)
	}
	// Only the file's CAs, never merged with the system store (Alertmanager's ca_file does not merge either)
	want := x509.NewCertPool()
	want.AddCert(ca.cert)
	if !got.Roots().Equal(want) {
		t.Fatal("pool must hold only the CAs from SMTP_CA_FILE")
	}
}

// No encryption against a server that lists PLAIN before LOGIN: Alertmanager picks PLAIN, which Go
// refuses on plain text, and never tries LOGIN. Password mail follows the same rule, so it fails before
// any credentials or mail are sent.
func TestSendMailNonePlainFirstFails(t *testing.T) {
	m := startMailServer(t, false, false)
	err := SendMail(context.Background(), m.config(SecurityNone, false), "alice@org-a.example", "s", "b")
	if r, p, _ := apierr.ReasonOf(err); r != api.ErrorReasonSmtpAuthLoginRequired || p["mechs"] != "PLAIN LOGIN" {
		t.Fatalf("err %v reason %q params %v,應為 smtp_auth_login_required", err, r, p)
	}
	if got := m.commands(); strings.Contains(got, "AUTH") || strings.Contains(got, "MAIL") {
		t.Fatalf("不該送出帳密或信:%s", got)
	}
}

// No encryption against a server that lists AUTH in the clear but refuses it with 530 until STARTTLS:
// the reason names the setting to change rather than the raw reply; another 530 keeps smtp_auth_failed.
func TestCheckMailStartTLSRequiredBeforeAuth(t *testing.T) {
	ctx := context.Background()
	m := startMailServer(t, true, false)
	m.authMechs = "LOGIN"
	m.authReply = "530 5.7.0 Must issue a STARTTLS command first"
	if r, _, _ := apierr.ReasonOf(CheckMail(ctx, m.config(SecurityNone, true))); r != api.ErrorReasonSmtpStarttlsRequired {
		t.Fatalf("530 STARTTLS 應為 smtp_starttls_required,得到 %q", r)
	}
	m.authReply = "530 5.7.1 Authentication required"
	if r, _, _ := apierr.ReasonOf(CheckMail(ctx, m.config(SecurityNone, true))); r != api.ErrorReasonSmtpAuthFailed {
		t.Fatalf("其他 530 應為 smtp_auth_failed,得到 %q", r)
	}
}

// The connection check takes the configured encryption: a STARTTLS server passes with starttls and fails
// with none (it lists PLAIN first); an implicit-TLS server on a port other than 465 passes with tls.
func TestCheckMailUsesConfiguredSecurity(t *testing.T) {
	ctx := context.Background()
	st := startMailServer(t, true, false)
	if err := CheckMail(ctx, st.config(SecurityStartTLS, true)); err != nil {
		t.Fatalf("starttls: %v", err)
	}
	if r, _, _ := apierr.ReasonOf(CheckMail(ctx, st.config(SecurityNone, true))); r != api.ErrorReasonSmtpAuthLoginRequired {
		t.Fatalf("none 對 PLAIN 優先的伺服器應為 smtp_auth_login_required,得到 %q", r)
	}
	// A server that offers AUTH only after STARTTLS: none cannot log in at all, starttls can.
	req := startMailServer(t, true, false)
	req.authTLS = true
	if r, _, _ := apierr.ReasonOf(CheckMail(ctx, req.config(SecurityNone, true))); r != api.ErrorReasonSmtpAuthUnsupported {
		t.Fatalf("none 對 STARTTLS 後才給 AUTH 的伺服器應為 smtp_auth_unsupported,得到 %q", r)
	}
	if err := CheckMail(ctx, req.config(SecurityStartTLS, true)); err != nil {
		t.Fatalf("starttls 對 STARTTLS 後才給 AUTH 的伺服器應通過:%v", err)
	}
	im := startMailServer(t, false, true)
	if im.port == "465" {
		t.Skip("隨機埠剛好是 465")
	}
	if err := CheckMail(ctx, im.config(SecurityTLS, true)); err != nil {
		t.Fatalf("tls on port %s: %v", im.port, err)
	}
	if got := im.commands(); !strings.HasPrefix(got, "EHLO|AUTH") {
		t.Fatalf("命令順序 %s", got)
	}
}

func TestAccountMailFromEnv(t *testing.T) {
	clear := func() {
		for _, k := range []string{"HOST", "PORT", "USERNAME", "PASSWORD", "PASSWORD_FILE", "FROM", "SECURITY"} {
			t.Setenv("ACCOUNT_SMTP_"+k, "")
		}
	}
	clear()
	if c, err := AccountMailFromEnv(nil); c != nil || err != nil {
		t.Fatalf("沒設 HOST = 沒有密碼信:%v %v", c, err)
	}
	t.Setenv("ACCOUNT_SMTP_HOST", "smtp.org-a.example")
	if _, err := AccountMailFromEnv(nil); err == nil {
		t.Fatal("缺 FROM 要拒絕")
	}
	t.Setenv("ACCOUNT_SMTP_FROM", "noreply@org-a.example")
	if c, err := AccountMailFromEnv(nil); err != nil || c.Port != 25 || c.Security != SecurityStartTLS {
		t.Fatalf("預設 25 埠 STARTTLS:%+v %v", c, err)
	}
	t.Setenv("ACCOUNT_SMTP_PORT", "465")
	if c, _ := AccountMailFromEnv(nil); c.Security != SecurityTLS {
		t.Fatalf("465 預設直接 TLS:%+v", c)
	}
	t.Setenv("ACCOUNT_SMTP_SECURITY", "none")
	if c, _ := AccountMailFromEnv(nil); c.Security != SecurityNone {
		t.Fatalf("明確設 none:%+v", c)
	}
	t.Setenv("ACCOUNT_SMTP_SECURITY", "ssl")
	if _, err := AccountMailFromEnv(nil); err == nil {
		t.Fatal("不認得的 SECURITY 要拒絕")
	}
	t.Setenv("ACCOUNT_SMTP_SECURITY", "")
	dir := t.TempDir()
	pf := filepath.Join(dir, "pw")
	os.WriteFile(pf, []byte("s3cret\n"), 0o600)
	t.Setenv("ACCOUNT_SMTP_PASSWORD_FILE", pf)
	if c, _ := AccountMailFromEnv(nil); c.Password != "s3cret" {
		t.Fatalf("密碼檔:%q", c.Password)
	}
	t.Setenv("ACCOUNT_SMTP_PASSWORD", "x")
	if _, err := AccountMailFromEnv(nil); err == nil {
		t.Fatal("密碼與密碼檔都設要拒絕")
	}
	t.Setenv("ACCOUNT_SMTP_PASSWORD", "")
	if c, _ := AccountMailFromEnv(nil); c.RootCAs != nil {
		t.Fatal("no CA file: system store")
	}
	ca := &SMTPCA{Pool: x509.NewCertPool()}
	if c, _ := AccountMailFromEnv(ca); c.RootCAs != ca.Pool {
		t.Fatal("password mail must use the SMTP_CA_FILE pool")
	}
}

// Sender / recipient rejected, server did not accept: each carries its own reason with the server reply in detail; Error() is still the Chinese sentence.
func TestSendMailRejectedSteps(t *testing.T) {
	for _, tc := range []struct {
		name   string
		set    func(*mailServer)
		reason api.ErrorReason
		prefix string
	}{
		{"寄件人", func(m *mailServer) { m.mailReply = "553 5.7.1 sender not allowed" }, api.ErrorReasonSmtpSenderRejected, "sender rejected:"},
		{"收件人", func(m *mailServer) { m.rcptReply = "550 5.1.1 no such user" }, api.ErrorReasonSmtpRecipientRejected, "recipient rejected:"},
		{"沒收下", func(m *mailServer) { m.endReply = "554 5.6.0 message rejected" }, api.ErrorReasonSmtpNotAccepted, "server did not accept the message:"},
	} {
		m := startMailServer(t, true, false)
		tc.set(m)
		err := SendMail(context.Background(), m.config(SecurityStartTLS, true), "alice@org-a.example", "s", "b")
		if r, p, _ := apierr.ReasonOf(err); r != tc.reason || p["detail"] == "" || !strings.HasPrefix(err.Error(), tc.prefix) {
			t.Errorf("%s: err %v reason %q params %v", tc.name, err, r, p)
		}
	}
}

// Rejected at dial (port closed): conn_dial_failed, Go's original text in detail.
func TestSendMailDialRefused(t *testing.T) {
	m := startMailServer(t, false, false)
	ln, err := net.Listen("tcp", m.host+":0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close() // grab a port that was just freed and has no listener
	cfg := m.config(SecurityNone, true)
	cfg.Port, _ = strconv.Atoi(port)
	err = SendMail(context.Background(), cfg, "alice@org-a.example", "s", "b")
	if r, p, _ := apierr.ReasonOf(err); r != api.ErrorReasonConnDialFailed || !strings.Contains(p["detail"], "refused") {
		t.Fatalf("err %v reason %q params %v", err, r, p)
	}
}

// Mail accepted, but the server drops the connection on QUIT without replying: counts as sent, no error (otherwise admins would think it was not delivered).
func TestSendMailQuitErrorAfterAccepted(t *testing.T) {
	m := startMailServer(t, true, false)
	m.dropQuit = true
	if err := SendMail(context.Background(), m.config(SecurityStartTLS, true), "alice@org-a.example", "s", "b"); err != nil {
		t.Fatalf("信已收下就算寄出:%v", err)
	}
}
