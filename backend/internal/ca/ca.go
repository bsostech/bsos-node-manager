// Package ca implements the platform's embedded CA: two tiers (root 10y / intermediate 5y; renewal
// in rotate.go), private keys encrypted at rest, a Signer interface, and leaf certs with an O/OU/CN
// Subject (72h by default). Generating material (Bootstrap) is separate from persisting it: the store
// layer only keeps Material's encrypted blobs.
package ca

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

const (
	rootTTL         = 10 * 365 * 24 * time.Hour
	intermediateTTL = 5 * 365 * 24 * time.Hour // renewal needs the root to sign, which is a manual ceremony when the root is offline, so stretch it to once every few years
	DefaultLeafTTL  = 72 * time.Hour
)

// Material is the CA's persistable material. Private keys are always encrypted blobs (crypto.go), never plaintext.
type Material struct {
	RootCertPEM []byte
	RootKeyEnc  []byte // used only at bootstrap / root rotation, never decrypted day to day (the root can be offline)
	IntCertPEM  []byte
	IntKeyEnc   []byte
}

// CA is the runtime object. Day-to-day issuance uses only the intermediate (via Signer); the root private key is not in memory.
type CA struct {
	Org    string
	Root   *x509.Certificate
	Inter  *x509.Certificate
	signer Signer
}

// Bootstrap generates fresh two-tier CA material (called once, on first start).
func Bootstrap(master []byte, org string, now time.Time) (*Material, error) {
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	rootTmpl := caTemplate(fmt.Sprintf("%s root CA", org), now, rootTTL, 1)
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, rootKey.Public(), rootKey)
	if err != nil {
		return nil, fmt.Errorf("create root: %w", err)
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		return nil, err
	}

	intKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	intTmpl := caTemplate(intermediateCN(org), now, intermediateTTL, 0)
	intDER, err := x509.CreateCertificate(rand.Reader, intTmpl, rootCert, intKey.Public(), rootKey)
	if err != nil {
		return nil, fmt.Errorf("create intermediate: %w", err)
	}

	rootKeyDER, err := x509.MarshalECPrivateKey(rootKey)
	if err != nil {
		return nil, err
	}
	intKeyDER, err := x509.MarshalECPrivateKey(intKey)
	if err != nil {
		return nil, err
	}
	rootKeyEnc, err := encryptKey(master, rootKeyDER)
	if err != nil {
		return nil, err
	}
	intKeyEnc, err := encryptKey(master, intKeyDER)
	if err != nil {
		return nil, err
	}
	return &Material{
		RootCertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}),
		RootKeyEnc:  rootKeyEnc,
		IntCertPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: intDER}),
		IntKeyEnc:   intKeyEnc,
	}, nil
}

// FromMaterial loads the CA: decrypts the intermediate private key and builds the embeddedSigner.
func FromMaterial(master []byte, org string, m *Material) (*CA, error) {
	rootCert, err := parseCertPEM(m.RootCertPEM)
	if err != nil {
		return nil, fmt.Errorf("root cert: %w", err)
	}
	intCert, err := parseCertPEM(m.IntCertPEM)
	if err != nil {
		return nil, fmt.Errorf("intermediate cert: %w", err)
	}
	intKeyDER, err := decryptKey(master, m.IntKeyEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt intermediate key (wrong master key?): %w", err)
	}
	intKey, err := x509.ParseECPrivateKey(intKeyDER)
	if err != nil {
		return nil, err
	}
	return &CA{
		Org:    org,
		Root:   rootCert,
		Inter:  intCert,
		signer: &embeddedSigner{issuerCert: intCert, issuerKey: intKey},
	}, nil
}

// IssueLeaf issues a leaf cert signed by the intermediate and returns its DER.
// Identity is encoded as Subject O/OU/CN (see profile.go). O comes from this CA's Org, so every leaf
// issued by one CA carries the same org.
func (c *CA) IssueLeaf(ctx context.Context, csr *x509.CertificateRequest, profile CertProfile) ([]byte, error) {
	profile.Org = c.Org
	if profile.TTL <= 0 {
		profile.TTL = DefaultLeafTTL
	}
	return c.signer.SignCertificate(ctx, csr, profile)
}

// RootDigest is the SHA-256 (hex) of the root cert, used by the join token to prevent MITM.
func (c *CA) RootDigest() string {
	sum := sha256.Sum256(c.Root.Raw)
	return hex.EncodeToString(sum[:])
}

// ChainPEM is intermediate + root (the chain presented by TLS endpoints / the trust bundle).
func (c *CA) ChainPEM() []byte {
	out := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Inter.Raw})
	return append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Root.Raw})...)
}

func caTemplate(cn string, now time.Time, ttl time.Duration, maxPathLen int) *x509.Certificate {
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	return &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(ttl),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            maxPathLen,
		MaxPathLenZero:        maxPathLen == 0,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
}

func parseCertPEM(p []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(p)
	if block == nil {
		return nil, fmt.Errorf("no PEM block")
	}
	return x509.ParseCertificate(block.Bytes)
}
