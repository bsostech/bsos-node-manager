package pki

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"besu-manager/backend/internal/ca"
	"besu-manager/backend/internal/store"
)

// pkiTestStore: test DB with an isolated schema; skipped when TEST_DATABASE_URL is not set locally.
func pkiTestStore(t *testing.T) *store.Store {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL absent")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || !strings.Contains(u.Path, "test") {
		t.Fatal("expected local test database")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("pki_test_%s", strings.ToLower(rand.Text()))
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`); admin.Close() })
	st := store.New(pool)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}

// The platform leaf cert is renewed once it hits the 1/3-lifetime threshold and left alone before that; the
// TLS endpoint then presents the new one.
func TestRenewPlatformLeaf(t *testing.T) {
	st := pkiTestStore(t)
	ctx := context.Background()
	s, err := Load(ctx, st, testMaster(t), "org-test", 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// The presented cert is the one returned by GetCertificate: compare its DER, not internal fields.
	served := func() string {
		c, _ := s.CrossOrgTLSConfig().GetCertificate(nil)
		return string(c.Certificate[0])
	}
	first, orig := served(), s.leafX

	s.renewPlatformLeafIfDue(ctx, orig.NotBefore.Add(time.Hour))
	if served() != first {
		t.Fatal("renewed too early")
	}
	s.renewPlatformLeafIfDue(ctx, orig.NotAfter.Add(-time.Hour))
	if served() == first {
		t.Fatal("not renewed past the 1/3 threshold")
	}
}

// A configured leaf TTL is honoured; zero (which time.ParseDuration accepts) falls back to the default
// instead of issuing agent certs that are already expired.
func TestLoadLeafTTL(t *testing.T) {
	ctx := context.Background()
	st := pkiTestStore(t)
	master := testMaster(t)
	for _, tc := range []struct{ in, want time.Duration }{{time.Hour, time.Hour}, {0, ca.DefaultLeafTTL}} {
		s, err := Load(ctx, st, master, "org-test", tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if s.LeafTTL != tc.want {
			t.Errorf("Load(%v).LeafTTL = %v, want %v", tc.in, s.LeafTTL, tc.want)
		}
	}
}

// Root in DB and intermediate close to expiry: Load renews with the same private key and writes it back; with
// the root offline nothing changes.
func TestLoadRenewsIntermediateWhenDue(t *testing.T) {
	ctx := context.Background()
	master := testMaster(t)
	// Intermediate has only 30 days left: create the CA "five years minus 30 days" ago
	built := time.Now().UTC().Add(-(5*365*24*time.Hour - 30*24*time.Hour))
	for _, tc := range []struct {
		name    string
		mat     func() (*ca.Material, error)
		renewed bool
	}{
		{"root 在 DB", func() (*ca.Material, error) { return ca.Bootstrap(master, "org-test", built) }, true},
		{"root 離線", func() (*ca.Material, error) {
			m, _, err := ca.GenerateOffline(master, "org-test", built)
			return m, err
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := pkiTestStore(t)
			mat, err := tc.mat()
			if err != nil {
				t.Fatal(err)
			}
			old, _ := ca.FromMaterial(master, "org-test", mat)
			if err := st.SaveCAMaterial(ctx, mat, old.Root.NotAfter, old.Inter.NotAfter); err != nil {
				t.Fatal(err)
			}
			s, err := Load(ctx, st, master, "org-test", 72*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := st.GetCAMaterial(ctx)
			stored, _ := ca.FromMaterial(master, "org-test", got)
			if tc.renewed {
				if time.Until(s.CA.Inter.NotAfter) < 4*365*24*time.Hour || !stored.Inter.NotAfter.Equal(s.CA.Inter.NotAfter) {
					t.Fatalf("應續簽並寫回:載入 %s、DB %s", s.CA.Inter.NotAfter, stored.Inter.NotAfter)
				}
				if string(got.IntKeyEnc) != string(mat.IntKeyEnc) {
					t.Error("續簽不該換私鑰")
				}
			} else if !s.CA.Inter.NotAfter.Equal(old.Inter.NotAfter) {
				t.Errorf("root 離線不該續簽:%s → %s", old.Inter.NotAfter, s.CA.Inter.NotAfter)
			}
		})
	}
}

// agentChain: signs an agent leaf cert with c and returns the chain after TLS verification (leaf ->
// intermediate -> root).
func agentChain(t *testing.T, c *ca.CA) []*x509.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	csr, _ := x509.ParseCertificateRequest(csrDER)
	der, err := c.IssueLeaf(context.Background(), csr, ca.CertProfile{Role: ca.RoleAgent, NodeID: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return []*x509.Certificate{leaf, c.Inter, c.Root}
}

// After a key-compromise rotation (`--new-key`) replaces the intermediate private key and writes it back, a
// restart loads the new one; agent certs signed by the old key are rejected even though they chain to the
// same root, while certs from a routine renewal (same private key) still pass.
func TestNewKeyRotationRejectsOldIntermediate(t *testing.T) {
	ctx := context.Background()
	st := pkiTestStore(t)
	master := testMaster(t)
	s, err := Load(ctx, st, master, "org-test", 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	oldChain := agentChain(t, s.CA)
	verify := s.ServerTLSConfig().VerifyPeerCertificate
	if err := verify([][]byte{oldChain[0].Raw}, [][]*x509.Certificate{oldChain}); err != nil {
		t.Fatalf("目前 intermediate 簽的應通過:%v", err)
	}

	// Routine renewal: private key unchanged, old certs still pass
	mat, _ := st.GetCAMaterial(ctx)
	renewed, err := ca.RenewIntermediate(master, mat, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	rc, _ := ca.FromMaterial(master, "org-test", renewed)
	if err := st.UpdateIntermediate(ctx, renewed.IntCertPEM, renewed.IntKeyEnc, rc.Inter.NotAfter); err != nil {
		t.Fatal(err)
	}
	s, err = Load(ctx, st, master, "org-test", 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ServerTLSConfig().VerifyPeerCertificate([][]byte{oldChain[0].Raw}, [][]*x509.Certificate{oldChain}); err != nil {
		t.Errorf("同一把私鑰續簽後,舊憑證應照常通過:%v", err)
	}

	// Key-compromise rotation (`--new-key`): replace the private key
	mat, _ = st.GetCAMaterial(ctx)
	rotated, err := ca.RotateIntermediate(master, mat, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	nc, _ := ca.FromMaterial(master, "org-test", rotated)
	if err := st.UpdateIntermediate(ctx, rotated.IntCertPEM, rotated.IntKeyEnc, nc.Inter.NotAfter); err != nil {
		t.Fatal(err)
	}
	s, err = Load(ctx, st, master, "org-test", 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !s.CA.Inter.Equal(nc.Inter) {
		t.Fatal("重啟後應載入新的 intermediate")
	}
	verify = s.ServerTLSConfig().VerifyPeerCertificate
	if err := verify([][]byte{oldChain[0].Raw}, [][]*x509.Certificate{oldChain}); err == nil {
		t.Error("舊私鑰簽的 agent 憑證應被拒")
	}
	newChain := agentChain(t, s.CA)
	if err := verify([][]byte{newChain[0].Raw}, [][]*x509.Certificate{newChain}); err != nil {
		t.Errorf("新 intermediate 簽的應通過:%v", err)
	}
}

// 8443 issues no session tickets: resumed connections skip VerifyPeerCertificate, so leaving them on would
// bypass revocation and intermediate checks.
func TestServerTLSNoResumption(t *testing.T) {
	ctx := context.Background()
	st := pkiTestStore(t)
	s, err := Load(ctx, st, testMaster(t), "org-test", 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	csr, _ := x509.ParseCertificateRequest(csrDER)
	der, err := s.CA.IssueLeaf(ctx, csr, ca.CertProfile{Role: ca.RoleAgent, NodeID: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	clientCert := tls.Certificate{Certificate: [][]byte{der, s.CA.Inter.Raw}, PrivateKey: key}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", s.ServerTLSConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = c.(*tls.Conn).Handshake(); buf := make([]byte, 1); _, _ = c.Read(buf); c.Close() }()
		}
	}()

	cache := tls.NewLRUClientSessionCache(4)
	dial := func() tls.ConnectionState {
		// nosemgrep: problem-based-packs.insecure-transport.go-stdlib.bypass-tls-verification.bypass-tls-verification -- the test only checks session resumption, not the platform identity
		c, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
			InsecureSkipVerify: true, Certificates: []tls.Certificate{clientCert}, ClientSessionCache: cache, MinVersion: tls.VersionTLS13,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.Write([]byte{0})
		_, _ = c.Read(make([]byte, 1)) // the server closes after reading; before reaching EOF the client processes NewSessionTicket (if any)
		st := c.ConnectionState()
		c.Close()
		return st
	}
	dial()
	if dial().DidResume {
		t.Error("第二次連線恢復了 session,VerifyPeerCertificate 被略過")
	}
}
