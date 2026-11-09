package ca

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"slices"
	"testing"
	"time"
)

func testMaster(t *testing.T) []byte {
	t.Helper()
	m := make([]byte, 32)
	if _, err := rand.Read(m); err != nil {
		t.Fatal(err)
	}
	return m
}

func newTestCA(t *testing.T, master []byte) *CA {
	t.Helper()
	mat, err := Bootstrap(master, "org-a", time.Now().UTC())
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	c, err := FromMaterial(master, "org-a", mat)
	if err != nil {
		t.Fatalf("FromMaterial: %v", err)
	}
	return c
}

func issueTestLeaf(t *testing.T, c *CA, profile CertProfile) *x509.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	csr, _ := x509.ParseCertificateRequest(csrDER)
	der, err := c.IssueLeaf(context.Background(), csr, profile)
	if err != nil {
		t.Fatalf("IssueLeaf: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

// A signed leaf cert must verify with the root as trust anchor and the intermediate as intermediate.
func TestLeafChainVerifies(t *testing.T) {
	c := newTestCA(t, testMaster(t))
	leaf := issueTestLeaf(t, c, CertProfile{Role: RoleAgent, NodeID: "node-a1"})

	roots := x509.NewCertPool()
	roots.AddCert(c.Root)
	inters := x509.NewCertPool()
	inters.AddCert(c.Inter)
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: roots, Intermediates: inters,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("chain verify: %v", err)
	}
}

// wantSubject asserts the cert Subject is encoded as O=org, OU=role, CN=name (replacing the old SPIFFE SAN).
func wantSubject(t *testing.T, leaf *x509.Certificate, org, ou, cn string) {
	t.Helper()
	if len(leaf.URIs) != 0 {
		t.Fatalf("不該再有 URI SAN(已改用 Subject O/OU): %v", leaf.URIs)
	}
	if got := leaf.Subject.Organization; len(got) != 1 || got[0] != org {
		t.Fatalf("O 不對: 要 %q 得 %v", org, got)
	}
	if got := leaf.Subject.OrganizationalUnit; len(got) != 1 || got[0] != ou {
		t.Fatalf("OU 不對: 要 %q 得 %v", ou, got)
	}
	if leaf.Subject.CommonName != cn {
		t.Fatalf("CN 不對: 要 %q 得 %q", cn, leaf.Subject.CommonName)
	}
}

// Subject O/OU/CN and lifetime must match the profile.
func TestLeafSubjectAndTTL(t *testing.T) {
	c := newTestCA(t, testMaster(t))
	leaf := issueTestLeaf(t, c, CertProfile{Role: RoleAgent, NodeID: "node-a1"})

	wantSubject(t, leaf, "org-a", "node", "node-a1")
	ttl := leaf.NotAfter.Sub(leaf.NotBefore)
	if ttl < 72*time.Hour || ttl > 73*time.Hour { // 72h + 5min clock tolerance
		t.Fatalf("unexpected ttl: %v", ttl)
	}
	if leaf.SerialNumber.Sign() <= 0 {
		t.Fatal("missing serial")
	}

	plat := issueTestLeaf(t, c, CertProfile{Role: RolePlatform})
	wantSubject(t, plat, "org-a", "platform", "platform")

}

// CA certs: NotBefore is backdated 5 minutes to tolerate clock skew; the intermediate has pathlen=0 and cannot sign further CAs.
func TestCACertsSkewAndPathLen(t *testing.T) {
	c := newTestCA(t, testMaster(t))
	for name, cert := range map[string]*x509.Certificate{"root": c.Root, "inter": c.Inter} {
		if d := time.Since(cert.NotBefore); d < 5*time.Minute {
			t.Errorf("%s NotBefore 只往前 %v,want ≥ 5m", name, d)
		}
	}
	if c.Inter.MaxPathLen != 0 || !c.Inter.MaxPathLenZero {
		t.Errorf("中繼 pathlen 應為 0: MaxPathLen=%d Zero=%v", c.Inter.MaxPathLen, c.Inter.MaxPathLenZero)
	}
	if c.Root.MaxPathLen != 1 {
		t.Errorf("root pathlen 應為 1,得 %d", c.Root.MaxPathLen)
	}
}

// Leaf cert NotBefore is backdated 1 minute to tolerate clock skew.
func TestLeafToleratesClockSkew(t *testing.T) {
	leaf := issueTestLeaf(t, newTestCA(t, testMaster(t)), CertProfile{Role: RoleAgent, NodeID: "node-a1"})
	if d := time.Since(leaf.NotBefore); d < time.Minute {
		t.Errorf("NotBefore 只往前 %v,want ≥ 1m", d)
	}
}

// Nodes do not listen for inbound connections, so leaf certs must not carry hostnames; identity is
// expressed only by Subject O/OU/CN. This pins that down: if an agent cert ever grows a SAN, someone has
// written some "the node is reachable" assumption back in. The only exception is the platform cert's
// <org id>.platform.internal: not a host address, just a fixed label so the verifying side's
// standard TLS has a ServerName to compare against. IP SANs are still not allowed.
func TestLeafHasNoHostSAN(t *testing.T) {
	c := newTestCA(t, testMaster(t))

	agent := issueTestLeaf(t, c, CertProfile{Role: RoleAgent, NodeID: "node-a1"})
	if len(agent.IPAddresses) != 0 || len(agent.DNSNames) != 0 {
		t.Errorf("agent 葉憑證不該有 IP/DNS SAN: ip=%v dns=%v", agent.IPAddresses, agent.DNSNames)
	}
	wantSubject(t, agent, "org-a", "node", "node-a1")

	plat := issueTestLeaf(t, c, CertProfile{Role: RolePlatform})
	if len(plat.IPAddresses) != 0 || len(plat.DNSNames) != 1 || plat.DNSNames[0] != "org-a.platform.internal" {
		t.Errorf("platform 葉憑證只該帶 org-a.platform.internal: ip=%v dns=%v", plat.IPAddresses, plat.DNSNames)
	}
}

// EKU differs by role. The agent may only be a client: nodes do not listen, and agent and platform
// leaf certs chain to the same root, so an agent cert carrying serverAuth would pass the platform's
// verification on the join path. This test pins that invariant; the platform needs both (server on
// 8443 / 8544, client reserved for outbound dials).
func TestLeafExtKeyUsageByRole(t *testing.T) {
	c := newTestCA(t, testMaster(t))

	agent := issueTestLeaf(t, c, CertProfile{Role: RoleAgent, NodeID: "node-a1"})
	if slices.Contains(agent.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		t.Error("agent 葉憑證不得帶 serverAuth —— 節點端沒有任何監聽")
	}
	if !slices.Contains(agent.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
		t.Error("agent 葉憑證必須帶 clientAuth(平台以 mTLS 驗它)")
	}

	platform := issueTestLeaf(t, c, CertProfile{Role: RolePlatform})
	if !slices.Contains(platform.ExtKeyUsage, x509.ExtKeyUsageServerAuth) ||
		!slices.Contains(platform.ExtKeyUsage, x509.ExtKeyUsageClientAuth) {
		t.Errorf("platform 葉憑證兩種都要,得 %v", platform.ExtKeyUsage)
	}
}

// A wrong master key must fail to decrypt the private key (private keys are encrypted at rest).
func TestWrongMasterKeyFails(t *testing.T) {
	master := testMaster(t)
	mat, err := Bootstrap(master, "org-a", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FromMaterial(testMaster(t), "org-a", mat); err == nil {
		t.Fatal("expected decrypt failure with wrong master key")
	}
	// the encrypted blob must not contain the plaintext private key
	keyDER, _ := decryptKey(master, mat.IntKeyEnc)
	if bytes.Contains(mat.IntKeyEnc, keyDER[:16]) {
		t.Fatal("key material leaked in plaintext")
	}
	// a truncated blob must return an error, not panic
	if _, err := decryptKey(master, make([]byte, 10)); err == nil {
		t.Fatal("expected error for short blob")
	}
}

// The root digest is stable and 64 hex chars (used by the join token to prevent MITM).
func TestRootDigest(t *testing.T) {
	master := testMaster(t)
	mat, _ := Bootstrap(master, "org-a", time.Now().UTC())
	c1, _ := FromMaterial(master, "org-a", mat)
	c2, _ := FromMaterial(master, "org-a", mat)
	if len(c1.RootDigest()) != 64 || c1.RootDigest() != c2.RootDigest() {
		t.Fatalf("digest unstable: %s vs %s", c1.RootDigest(), c2.RootDigest())
	}
}
