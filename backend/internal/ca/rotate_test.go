package ca

import (
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"
)

// verifyLeaf checks that leaf verifies up to root via inter.
func verifyLeaf(t *testing.T, leaf, inter, root *x509.Certificate) error {
	t.Helper()
	roots, inters := x509.NewCertPool(), x509.NewCertPool()
	roots.AddCert(root)
	inters.AddCert(inter)
	_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inters, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}})
	return err
}

func bootstrapMat(t *testing.T, master []byte) *Material {
	t.Helper()
	mat, err := Bootstrap(master, "org-a", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return mat
}

// Re-signing with the same private key: the cert changes, the key does not, so a leaf signed by the old intermediate also verifies under the new one
// (after the platform renews, leaf certs sent by agents need no re-signing).
func TestRenewIntermediateKeepsKey(t *testing.T) {
	master := testMaster(t)
	mat, err := Bootstrap(master, "org-a", time.Now().UTC().Add(-24*time.Hour)) // CA created a day ago
	if err != nil {
		t.Fatal(err)
	}
	oldCA, _ := FromMaterial(master, "org-a", mat)
	leaf := issueTestLeaf(t, oldCA, CertProfile{Role: RoleAgent, NodeID: "n1"})

	nm, err := RenewIntermediate(master, mat, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	newCA, err := FromMaterial(master, "org-a", nm)
	if err != nil {
		t.Fatal(err)
	}
	if newCA.Inter.SerialNumber.Cmp(oldCA.Inter.SerialNumber) == 0 {
		t.Fatal("應是一張新憑證")
	}
	if !newCA.Inter.NotAfter.After(oldCA.Inter.NotAfter) {
		t.Errorf("新 intermediate 應更晚到期:%s vs %s", newCA.Inter.NotAfter, oldCA.Inter.NotAfter)
	}
	if string(nm.IntKeyEnc) != string(mat.IntKeyEnc) || string(nm.RootCertPEM) != string(mat.RootCertPEM) {
		t.Error("續簽不該動到 intermediate 私鑰或 root")
	}
	if err := verifyLeaf(t, leaf, newCA.Inter, newCA.Root); err != nil {
		t.Errorf("舊葉憑證應能掛在續簽後的 intermediate 底下:%v", err)
	}
}

// Key switch: leaves signed by the new intermediate verify; old leaves still verify under the old intermediate (no outage before expiry).
func TestRotateIntermediateNewKey(t *testing.T) {
	master := testMaster(t)
	mat := bootstrapMat(t, master)
	oldCA, _ := FromMaterial(master, "org-a", mat)
	oldLeaf := issueTestLeaf(t, oldCA, CertProfile{Role: RoleAgent, NodeID: "n1"})

	nm, err := RotateIntermediate(master, mat, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if string(nm.IntKeyEnc) == string(mat.IntKeyEnc) {
		t.Fatal("換發應產新私鑰")
	}
	newCA, err := FromMaterial(master, "org-a", nm)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyLeaf(t, issueTestLeaf(t, newCA, CertProfile{Role: RoleAgent, NodeID: "n1"}), newCA.Inter, newCA.Root); err != nil {
		t.Errorf("新 intermediate 簽的葉應驗得過:%v", err)
	}
	if err := verifyLeaf(t, oldLeaf, oldCA.Inter, newCA.Root); err != nil {
		t.Errorf("舊葉在舊 intermediate 底下應仍驗得過:%v", err)
	}
	if verifyLeaf(t, oldLeaf, newCA.Inter, newCA.Root) == nil {
		t.Error("舊葉不該掛在新金鑰的 intermediate 底下")
	}
}

// The intermediate is never signed to outlive the root.
func TestReissueCappedAtRootExpiry(t *testing.T) {
	master := testMaster(t)
	mat := bootstrapMat(t, master)
	root, _ := parseCertPEM(mat.RootCertPEM)
	nm, err := RenewIntermediate(master, mat, root.NotAfter.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	inter, _ := parseCertPEM(nm.IntCertPEM)
	if inter.NotAfter.After(root.NotAfter) {
		t.Errorf("intermediate 到期 %s 晚於 root %s", inter.NotAfter, root.NotAfter)
	}
}

// signCSR is the root holder's side: it signs the CSR into an intermediate with the offline root.
func signCSR(t *testing.T, csrPEM []byte, root *x509.Certificate, rootKey any) []byte {
	t.Helper()
	block, _ := pem.Decode(csrPEM)
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, caTemplate(csr.Subject.CommonName, time.Now().UTC(), intermediateTTL, 0), root, csr.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func offlineCA(t *testing.T, master []byte, org string) (*Material, *x509.Certificate, any) {
	t.Helper()
	mat, rootKeyPEM, err := GenerateOffline(master, org, time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	rootKey, err := parseECPrivateKeyPEM(rootKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := parseCertPEM(mat.RootCertPEM)
	return mat, root, rootKey
}

// Routine renewal with the root offline: the CSR uses the existing private key, only the cert comes back changed, and old leaves still verify.
func TestOfflineRootReplaceIntermediateSameKey(t *testing.T) {
	master := testMaster(t)
	mat, root, rootKey := offlineCA(t, master, "org-a")
	if _, err := RenewIntermediate(master, mat, time.Now().UTC()); err == nil {
		t.Fatal("root 離線時不該能自動續簽")
	}
	oldCA, _ := FromMaterial(master, "org-a", mat)
	oldLeaf := issueTestLeaf(t, oldCA, CertProfile{Role: RoleAgent, NodeID: "n1"})

	keyPEM, csrPEM, err := IntermediateCSR(master, mat, "org-a", false)
	if err != nil {
		t.Fatal(err)
	}
	if keyPEM != nil {
		t.Fatal("例行換發不該產新私鑰")
	}
	nm, err := ReplaceIntermediate(master, mat, signCSR(t, csrPEM, root, rootKey), nil, time.Now().UTC())
	if err != nil {
		t.Fatalf("匯入應成功:%v", err)
	}
	if nm.RootOnline() || string(nm.RootCertPEM) != string(mat.RootCertPEM) {
		t.Error("匯入後 root 應不變且仍離線")
	}
	c, err := FromMaterial(master, "org-a", nm)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyLeaf(t, oldLeaf, c.Inter, c.Root); err != nil {
		t.Errorf("同一把私鑰換發後,舊葉應掛得上新 intermediate:%v", err)
	}
}

// Compromise response with the root offline: CSR for a new private key; the import must carry the new key; old leaves do not chain to the new intermediate.
func TestOfflineRootReplaceIntermediateNewKey(t *testing.T) {
	master := testMaster(t)
	mat, root, rootKey := offlineCA(t, master, "org-a")
	oldCA, _ := FromMaterial(master, "org-a", mat)
	oldLeaf := issueTestLeaf(t, oldCA, CertProfile{Role: RoleAgent, NodeID: "n1"})

	keyPEM, csrPEM, err := IntermediateCSR(master, mat, "org-a", true)
	if err != nil || keyPEM == nil {
		t.Fatalf("應產新私鑰:%v", err)
	}
	cert := signCSR(t, csrPEM, root, rootKey)
	if _, err := ReplaceIntermediate(master, mat, cert, nil, time.Now().UTC()); err == nil {
		t.Fatal("新私鑰簽的憑證不帶新私鑰匯入應被拒(公鑰對不上現有私鑰)")
	}
	nm, err := ReplaceIntermediate(master, mat, cert, keyPEM, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	c, _ := FromMaterial(master, "org-a", nm)
	if verifyLeaf(t, oldLeaf, c.Inter, c.Root) == nil {
		t.Error("舊葉不該掛在新私鑰的 intermediate 底下")
	}
}

// Importing an intermediate not signed by this root: rejected.
func TestReplaceIntermediateRejectsForeignRoot(t *testing.T) {
	master := testMaster(t)
	mat, _, _ := offlineCA(t, master, "org-a")
	_, otherRoot, otherKey := offlineCA(t, master, "org-b")
	_, csrPEM, err := IntermediateCSR(master, mat, "org-a", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReplaceIntermediate(master, mat, signCSR(t, csrPEM, otherRoot, otherKey), nil, time.Now().UTC()); err == nil {
		t.Error("別的 root 簽的 intermediate 應被拒")
	}
}
