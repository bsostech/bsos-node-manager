package ca

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"
)

// Material from GenerateOffline: the root private key goes back to the caller and stays out of Material,
// yet it still loads and issues leaf certs whose chain verifies (an offline root does not affect daily issuance).
func TestGenerateOfflineRootNotInMaterial(t *testing.T) {
	master := testMaster(t)
	mat, rootKeyPEM, err := GenerateOffline(master, "org-a", time.Now().UTC())
	if err != nil {
		t.Fatalf("GenerateOffline: %v", err)
	}
	if len(mat.RootKeyEnc) != 0 {
		t.Fatalf("root 私鑰不應進 Material(離線),卻有 %d bytes", len(mat.RootKeyEnc))
	}
	if len(rootKeyPEM) == 0 {
		t.Fatal("應回傳 root 私鑰 PEM 供帶離")
	}

	c, err := FromMaterial(master, "org-a", mat)
	if err != nil {
		t.Fatalf("FromMaterial: %v", err)
	}
	leaf := issueTestLeaf(t, c, CertProfile{Role: RoleAgent, NodeID: "node-x"})
	roots := x509.NewCertPool()
	roots.AddCert(c.Root)
	inters := x509.NewCertPool()
	inters.AddCert(c.Inter)
	// This verifies the chain, not the EKU, so use Any.
	// It cannot be omitted: when omitted, x509 requires serverAuth by default, and the agent leaf cert only has clientAuth.
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots: roots, Intermediates: inters,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		t.Fatalf("離線 root 簽出的葉憑證應可驗鏈: %v", err)
	}
}

// ImportMaterial round trip: use GenerateOffline to build trusted material as the "external PKI",
// re-import its root cert / inter cert / inter private key; the result should be equivalent with the root still offline.
func TestImportMaterialRoundTrip(t *testing.T) {
	master := testMaster(t)
	src, _, err := GenerateOffline(master, "org-a", time.Now().UTC())
	if err != nil {
		t.Fatalf("GenerateOffline: %v", err)
	}
	srcCA, err := FromMaterial(master, "org-a", src)
	if err != nil {
		t.Fatalf("FromMaterial(src): %v", err)
	}
	interKeyPEM := decryptedInterKeyPEM(t, master, src)

	mat, err := ImportMaterial(master, src.RootCertPEM, src.IntCertPEM, interKeyPEM)
	if err != nil {
		t.Fatalf("ImportMaterial: %v", err)
	}
	if len(mat.RootKeyEnc) != 0 {
		t.Fatal("匯入後 root 應離線(無私鑰)")
	}
	c, err := FromMaterial(master, "org-a", mat)
	if err != nil {
		t.Fatalf("FromMaterial(imported): %v", err)
	}
	if c.RootDigest() != srcCA.RootDigest() {
		t.Fatal("匯入後 root digest 應與來源一致")
	}
	if _, err := c.IssueLeaf(context.Background(), testCSR(t), CertProfile{Role: RolePlatform}); err != nil {
		t.Fatalf("匯入後應能以 intermediate 簽發: %v", err)
	}
}

// A mismatched intermediate private key and cert must be rejected (guards against the wrong key).
func TestImportMaterialMismatchedInterKey(t *testing.T) {
	master := testMaster(t)
	a, _, _ := GenerateOffline(master, "org-a", time.Now().UTC())
	b, _, _ := GenerateOffline(master, "org-a", time.Now().UTC())
	// a's root/inter certs paired with b's inter private key
	wrongKeyPEM := decryptedInterKeyPEM(t, master, b)
	if _, err := ImportMaterial(master, a.RootCertPEM, a.IntCertPEM, wrongKeyPEM); err == nil {
		t.Fatal("私鑰與憑證不符應被拒絕")
	}
}

// An intermediate not signed by that root must be rejected (guards against stitched-together material that does not form a chain).
func TestImportMaterialForeignIntermediate(t *testing.T) {
	master := testMaster(t)
	a, _, _ := GenerateOffline(master, "org-a", time.Now().UTC())
	b, _, _ := GenerateOffline(master, "org-a", time.Now().UTC())
	bKeyPEM := decryptedInterKeyPEM(t, master, b)
	// a's root paired with b's inter (b's is signed by b-root, not a-root)
	if _, err := ImportMaterial(master, a.RootCertPEM, b.IntCertPEM, bKeyPEM); err == nil {
		t.Fatal("intermediate 非此 root 簽發應被拒絕")
	}
}

// helpers ───────────────────────────────────────────────────

func decryptedInterKeyPEM(t *testing.T, master []byte, m *Material) []byte {
	t.Helper()
	der, err := decryptKey(master, m.IntKeyEnc)
	if err != nil {
		t.Fatalf("decrypt inter key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

func testCSR(t *testing.T) *x509.CertificateRequest {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		t.Fatal(err)
	}
	return csr
}

func TestCheckOrgID(t *testing.T) {
	for id, ok := range map[string]bool{
		"example.com": true, "org-a": true, "bsos-node-manager-3f9a2c": true, "a": true, "org_b": true,
		"": false, "Org-A": false, "-a": false, "a-": false, "a..b": false, "單位": false, "a b": false, ".a": false,
	} {
		if err := CheckOrgID(id); (err == nil) != ok {
			t.Errorf("CheckOrgID(%q) err=%v, want ok=%v", id, err, ok)
		}
	}
}
