// Package certtest: throwaway certificate chains for tests only (imported only by _test.go files, never in
// production binaries).
package certtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// CA: a root + intermediate for one org, enough to stand in for the platform's CA.
type CA struct {
	Org               string
	Root, Inter       *x509.Certificate
	RootKey, InterKey *ecdsa.PrivateKey
}

// Key: a fresh P-256 key.
func Key(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// Sign: signs tmpl, first overwriting its serial with a random one and its validity with an hour either side of now.
func Sign(t testing.TB, tmpl, parent *x509.Certificate, pub any, key *ecdsa.PrivateKey) *x509.Certificate {
	t.Helper()
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	tmpl.SerialNumber = serial
	tmpl.NotBefore, tmpl.NotAfter = time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// CATemplate: a CA certificate template with Subject CN=cn, for Sign.
func CATemplate(cn string) *x509.Certificate {
	return &x509.Certificate{Subject: pkix.Name{CommonName: cn}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
}

// NewCA: a self-signed root and an intermediate it signs, both named after org.
func NewCA(t testing.TB, org string) *CA {
	t.Helper()
	c := &CA{Org: org, RootKey: Key(t), InterKey: Key(t)}
	rt := CATemplate(org + " root")
	c.Root = Sign(t, rt, rt, c.RootKey.Public(), c.RootKey)
	c.Inter = Sign(t, CATemplate(org+" intermediate"), c.Root, c.InterKey.Public(), c.RootKey)
	return c
}

// Rotated: same root, new intermediate key (the platform's key-compromise rotation).
func (c *CA) Rotated(t testing.TB) *CA {
	t.Helper()
	r := &CA{Org: c.Org, Root: c.Root, RootKey: c.RootKey, InterKey: Key(t)}
	r.Inter = Sign(t, CATemplate(c.Org+" intermediate 2"), c.Root, r.InterKey.Public(), c.RootKey)
	return r
}

// Bundle: the intermediate then the root, PEM-encoded, as ca.pem holds them.
func (c *CA) Bundle() []byte {
	return append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Inter.Raw}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Root.Raw})...)
}

// Leaf: a leaf signed by the intermediate with Subject O=org, OU=ou, for pub.
func (c *CA) Leaf(t testing.TB, ou, cn string, pub any, eku ...x509.ExtKeyUsage) *x509.Certificate {
	t.Helper()
	return Sign(t, &x509.Certificate{
		Subject:     pkix.Name{CommonName: cn, Organization: []string{c.Org}, OrganizationalUnit: []string{ou}},
		ExtKeyUsage: eku,
	}, c.Inter, pub, c.InterKey)
}
