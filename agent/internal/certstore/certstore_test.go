package certstore

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"besu-manager/agent/internal/certstore/certtest"
)

// newLeaf: a leaf cert n1 signed by ca's intermediate; ou = node (the agent itself) or platform (the peer to verify).
func newLeaf(t *testing.T, ca *certtest.CA, ou string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	return newLeafFor(t, ca, ou, "n1")
}

// newLeafFor: newLeaf with a chosen CN.
func newLeafFor(t *testing.T, ca *certtest.CA, ou, cn string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	k := certtest.Key(t)
	return ca.Leaf(t, ou, cn, k.Public(), x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth), k
}

func verify(leaf *x509.Certificate, roots *x509.CertPool, inters ...*x509.Certificate) error {
	pool := x509.NewCertPool()
	for _, c := range inters {
		pool.AddCert(c)
	}
	_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

// bundle = intermediate + root: only the root is a trust anchor. Verification fails if the peer does not
// include the intermediate, which shows the intermediate is not used as an anchor.
func TestTrustFromBundleOnlyTrustsRoot(t *testing.T) {
	ca := certtest.NewCA(t, "org-a")
	pool, _, err := trustFromBundle(ca.Bundle())
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := newLeaf(t, ca, "platform")
	if err := verify(leaf, pool); err == nil {
		t.Error("沒附 intermediate 不該驗過:intermediate 被當成信任錨了")
	}
	if err := verify(leaf, pool, ca.Inter); err != nil {
		t.Errorf("附 intermediate 應驗過:%v", err)
	}
}

func TestTrustFromBundleRequiresRootAndIntermediate(t *testing.T) {
	ca := certtest.NewCA(t, "org-a")
	if _, _, err := trustFromBundle(PEMBlock("CERTIFICATE", ca.Inter.Raw)); err == nil {
		t.Error("只有 intermediate 的 bundle 應回錯")
	}
	if _, _, err := trustFromBundle(PEMBlock("CERTIFICATE", ca.Root.Raw)); err == nil {
		t.Error("只有 root 的 bundle 應回錯")
	}
}

// Issuer copied from Subject but signed by another key: not self-signed, must not enter the pool.
func TestTrustFromBundleRejectsFakeSelfSigned(t *testing.T) {
	other := certtest.Key(t)
	tmpl := certtest.CATemplate("fake root")
	tmpl.Issuer = tmpl.Subject
	parent := certtest.CATemplate("fake root") // same-named parent, signed by a different key
	fake := certtest.Sign(t, tmpl, parent, certtest.Key(t).Public(), other)
	if _, _, err := trustFromBundle(PEMBlock("CERTIFICATE", fake.Raw)); err == nil {
		t.Error("簽章對不上自己公鑰的憑證不該被當成 root")
	}
}

// After Swap to a bundle from another CA, the trust pool follows: platform certs from the old CA fail, the
// new ones pass.
func TestSwapRebuildsRoots(t *testing.T) {
	dir := t.TempDir()
	oldCA, newCA := certtest.NewCA(t, "org-a"), certtest.NewCA(t, "org-a")

	write := func(ca *certtest.CA) (keyPEM, certPEM []byte) {
		leaf, k := newLeaf(t, ca, "node")
		der, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		return PEMBlock("EC PRIVATE KEY", der), PEMBlock("CERTIFICATE", leaf.Raw)
	}
	keyPEM, certPEM := write(oldCA)
	for name, data := range map[string][]byte{keyFile: keyPEM, certFile: certPEM, bundleFile: oldCA.Bundle()} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	oldPlat, _ := newLeaf(t, oldCA, "platform")
	newPlat, _ := newLeaf(t, newCA, "platform")
	if err := verify(oldPlat, s.Roots(), oldCA.Inter); err != nil {
		t.Fatalf("Load 後應信任舊 CA:%v", err)
	}

	keyPEM, certPEM = write(newCA)
	if err := s.Swap(keyPEM, certPEM, newCA.Bundle()); err != nil {
		t.Fatal(err)
	}
	if err := verify(oldPlat, s.Roots(), oldCA.Inter); err == nil {
		t.Error("Swap 後不該再信任舊 CA")
	}
	if err := verify(newPlat, s.Roots(), newCA.Inter); err != nil {
		t.Errorf("Swap 後應信任新 CA:%v", err)
	}
	if tmp, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(tmp) > 0 {
		t.Errorf("Swap 後留下暫存檔:%v", tmp)
	}
	if again, err := Load(dir); err != nil {
		t.Errorf("Swap 寫下的檔案讀不回來:%v", err)
	} else if err := verify(newPlat, again.Roots(), newCA.Inter); err != nil {
		t.Errorf("重新載入後應信任新 CA:%v", err)
	}
	if got := len(s.Certificate().Certificate); got != 2 {
		t.Errorf("出示鏈應為 leaf + intermediate,得 %d 張", got)
	}
}

// Intermediate key replaced under the same root (compromise response): after Swap to the new bundle, platform
// certs signed by the old intermediate are rejected even though they still chain to the root; an intermediate
// re-issued with the same key is still accepted.
func TestIssuedByCurrentIntermediate(t *testing.T) {
	dir := t.TempDir()
	ca := certtest.NewCA(t, "org-a")
	leafKey := func(c *certtest.CA) ([]byte, []byte) {
		leaf, k := newLeaf(t, c, "node")
		der, _ := x509.MarshalECPrivateKey(k)
		return PEMBlock("EC PRIVATE KEY", der), PEMBlock("CERTIFICATE", leaf.Raw)
	}
	keyPEM, certPEM := leafKey(ca)
	for name, data := range map[string][]byte{keyFile: keyPEM, certFile: certPEM, bundleFile: ca.Bundle()} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	chains := func(leaf *x509.Certificate, inter *x509.Certificate) [][]*x509.Certificate {
		pool := x509.NewCertPool()
		pool.AddCert(inter)
		ch, err := leaf.Verify(x509.VerifyOptions{Roots: s.Roots(), Intermediates: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
		if err != nil {
			t.Fatalf("鏈應驗得到 root:%v", err)
		}
		return ch
	}
	oldPlat, _ := newLeaf(t, ca, "platform")
	if !s.IssuedByCurrentIntermediate(chains(oldPlat, ca.Inter)) {
		t.Fatal("目前 intermediate 簽的平台憑證應接受")
	}

	// intermediate re-issued with the same key: accepted
	same := certtest.Sign(t, certtest.CATemplate("org-a intermediate"), ca.Root, ca.InterKey.Public(), ca.RootKey)
	if !s.IssuedByCurrentIntermediate(chains(oldPlat, same)) {
		t.Error("同一把私鑰重簽的 intermediate 應接受")
	}

	// key replaced: once the new intermediate is in the bundle, the old one is rejected
	rotated := ca.Rotated(t)
	keyPEM, certPEM = leafKey(rotated)
	if err := s.Swap(keyPEM, certPEM, rotated.Bundle()); err != nil {
		t.Fatal(err)
	}
	if s.IssuedByCurrentIntermediate(chains(oldPlat, ca.Inter)) {
		t.Error("換私鑰後,舊 intermediate 簽的平台憑證不該再被接受")
	}
	newPlat, _ := newLeaf(t, rotated, "platform")
	if !s.IssuedByCurrentIntermediate(chains(newPlat, rotated.Inter)) {
		t.Error("新 intermediate 簽的平台憑證應接受")
	}
}

// Reload: join ran again at runtime (the new cert is only on disk). Unchanged files are a no-op; a new cert
// for the same node is swapped in; another node's cert or a corrupt file is rejected and the old one kept.
func TestReload(t *testing.T) {
	dir := t.TempDir()
	ca := certtest.NewCA(t, "org-a")
	write := func(cn string, certPEM []byte) {
		leaf, k := newLeafFor(t, ca, "node", cn)
		der, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		if certPEM == nil {
			certPEM = PEMBlock("CERTIFICATE", leaf.Raw)
		}
		for name, data := range map[string][]byte{keyFile: PEMBlock("EC PRIVATE KEY", der), certFile: certPEM, bundleFile: ca.Bundle()} {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("n1", nil)
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := s.Certificate().Leaf.Raw

	if changed, err := s.Reload(); err != nil || changed {
		t.Fatalf("檔案沒變不該換: changed=%v err=%v", changed, err)
	}

	write("n1", nil) // same node re-joins
	if changed, err := s.Reload(); err != nil || !changed || bytes.Equal(s.Certificate().Leaf.Raw, first) {
		t.Fatalf("同一個節點的新憑證應換上: changed=%v err=%v", changed, err)
	}
	second := s.Certificate().Leaf.Raw

	// only the bundle changed (a half-finished join first yields the new leaf with the old ca.pem, the bundle
	// arrives later): a changed chain must also be swapped in
	newInter := certtest.Sign(t, certtest.CATemplate("org-a intermediate 2"), ca.Root, ca.InterKey.Public(), ca.RootKey)
	if err := os.WriteFile(filepath.Join(dir, bundleFile), (&certtest.CA{Root: ca.Root, Inter: newInter}).Bundle(), 0o600); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.Reload(); err != nil || !changed || !bytes.Equal(s.Certificate().Certificate[1], newInter.Raw) {
		t.Fatalf("bundle 換了應換上新的出示鏈: changed=%v err=%v", changed, err)
	}

	write("n2", nil) // another node
	if changed, err := s.Reload(); err == nil || changed || !bytes.Equal(s.Certificate().Leaf.Raw, second) {
		t.Fatalf("別的節點的憑證不該收: changed=%v err=%v", changed, err)
	}

	write("n1", []byte("not a cert")) // corrupt file (or a half-finished join)
	if changed, err := s.Reload(); err == nil || changed || !bytes.Equal(s.Certificate().Leaf.Raw, second) {
		t.Fatalf("壞檔不該收: changed=%v err=%v", changed, err)
	}
	if s.NodeID() != "n1" {
		t.Fatalf("nodeID 不該變: %s", s.NodeID())
	}
}

// A write that fails partway leaves the existing key, cert and bundle untouched.
func TestWriteFilesKeepsOldSetOnFailure(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFiles(dir, []byte("old-key"), []byte("old-cert"), []byte("old-ca")); err != nil {
		t.Fatal(err)
	}
	// A directory where the cert's temporary file should go makes that write fail after the key's.
	if err := os.Mkdir(filepath.Join(dir, certFile+".tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFiles(dir, []byte("new-key"), []byte("new-cert"), []byte("new-ca")); err == nil {
		t.Fatal("寫入暫存檔失敗卻沒回錯誤")
	}
	for name, want := range map[string]string{keyFile: "old-key", certFile: "old-cert", bundleFile: "old-ca"} {
		if got, _ := os.ReadFile(filepath.Join(dir, name)); string(got) != want {
			t.Errorf("%s = %q,應保留舊的 %q", name, got, want)
		}
	}
}
