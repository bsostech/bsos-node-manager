package ca

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"fmt"
	"time"
)

// Signer is the only entry point for signing; implementations must not expose the private key to callers.
// The default is embeddedSigner (software); **pkcs11Signer (HSM) is not implemented, only this interface reserves the slot**,
// to be switched via config if it is ever built.
type Signer interface {
	SignCertificate(ctx context.Context, csr *x509.CertificateRequest, profile CertProfile) ([]byte, error)
	PublicKey() crypto.PublicKey
}

// embeddedSigner signs with the intermediate private key. The type is crypto.Signer rather than *ecdsa.PrivateKey:
// x509.CreateCertificate already takes a crypto.Signer, so moving the intermediate into an HSM later only means swapping issuerKey.
type embeddedSigner struct {
	issuerCert *x509.Certificate
	issuerKey  crypto.Signer
}

func (s *embeddedSigner) PublicKey() crypto.PublicKey { return s.issuerKey.Public() }

func (s *embeddedSigner) SignCertificate(_ context.Context, csr *x509.CertificateRequest, profile CertProfile) ([]byte, error) {
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("csr signature: %w", err)
	}
	tmpl, err := profile.template(time.Now().UTC())
	if err != nil {
		return nil, err
	}
	return x509.CreateCertificate(rand.Reader, tmpl, s.issuerCert, csr.PublicKey, s.issuerKey)
}
