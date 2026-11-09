package certstore

import (
	"bytes"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"besu-manager/agent/internal/certstore/certtest"
)

// The TLS client hook reads the current cert on every handshake: after Swap it presents the new cert
// without a restart. The org id comes from the cert's O.
func TestClientCertFollowsSwap(t *testing.T) {
	dir := t.TempDir()
	ca := certtest.NewCA(t, "org-a")
	pair := func() (keyPEM, certPEM []byte, leaf *x509.Certificate) {
		leaf, k := newLeaf(t, ca, "node")
		der, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		return PEMBlock("EC PRIVATE KEY", der), PEMBlock("CERTIFICATE", leaf.Raw), leaf
	}
	keyPEM, certPEM, first := pair()
	for name, data := range map[string][]byte{keyFile: keyPEM, certFile: certPEM, bundleFile: ca.Bundle()} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Org() != "org-a" {
		t.Fatalf("Org = %q", s.Org())
	}
	presented := func() []byte {
		c, err := s.GetClientCertificate(nil)
		if err != nil {
			t.Fatal(err)
		}
		return c.Certificate[0]
	}
	if !bytes.Equal(presented(), first.Raw) {
		t.Fatal("應出示載入的憑證")
	}

	keyPEM, certPEM, second := pair()
	if err := s.Swap(keyPEM, certPEM, ca.Bundle()); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(presented(), second.Raw) {
		t.Fatal("Swap 後下一次握手應出示新憑證")
	}
}
