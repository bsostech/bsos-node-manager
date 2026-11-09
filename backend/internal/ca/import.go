package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Custom / offline root support:
// By default Bootstrap self-signs a root and stores the root private key encrypted; a safer deployment
// keeps "the root private key never enters the system": the platform holds only an intermediate
// (signed by an external/offline root). This file offers two paths:
//   ImportMaterial   brings in an existing enterprise PKI's root cert + intermediate cert/private key.
//   GenerateOffline  generates a new two-tier CA on the spot, but returns the root private key for the caller to take away (not stored in the DB).
// Both produce Material with an empty RootKeyEnc (root offline); FromMaterial is unaffected
// (it only ever decrypts the intermediate private key).

// ImportMaterial validates and packages externally supplied CA material.
// rootKey is not needed (and should not be provided): the root stays in the offline environment that signed it.
func ImportMaterial(master, rootCertPEM, interCertPEM, interKeyPEM []byte) (*Material, error) {
	root, err := parseCertPEM(rootCertPEM)
	if err != nil {
		return nil, fmt.Errorf("root certificate: %w", err)
	}
	inter, err := parseCertPEM(interCertPEM)
	if err != nil {
		return nil, fmt.Errorf("intermediate certificate: %w", err)
	}
	if !root.IsCA {
		return nil, fmt.Errorf("root certificate IsCA is not true")
	}
	if err := root.CheckSignatureFrom(root); err != nil {
		return nil, fmt.Errorf("root is not self-signed (or not a valid CA): %w", err)
	}
	if !inter.IsCA {
		return nil, fmt.Errorf("intermediate certificate IsCA is not true")
	}
	if err := inter.CheckSignatureFrom(root); err != nil {
		return nil, fmt.Errorf("intermediate is not signed by this root: %w", err)
	}

	interKey, err := parseECPrivateKeyPEM(interKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("intermediate private key: %w", err)
	}
	interPub, ok := inter.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("intermediate certificate is not ECDSA")
	}
	if !interKey.PublicKey.Equal(interPub) {
		return nil, fmt.Errorf("intermediate private key does not match the certificate")
	}

	return packMaterial(master, root.Raw, inter.Raw, interKey)
}

// GenerateOffline generates a fresh two-tier CA but returns the root private key as PEM (for the caller to
// write to a file / take away) instead of putting it in Material: the DB only gets the root cert + intermediate cert/private key.
func GenerateOffline(master []byte, org string, now time.Time) (mat *Material, rootKeyPEM []byte, err error) {
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	rootTmpl := caTemplate(fmt.Sprintf("%s root CA", org), now, rootTTL, 1)
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, rootKey.Public(), rootKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create root: %w", err)
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		return nil, nil, err
	}

	intKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	intTmpl := caTemplate(intermediateCN(org), now, intermediateTTL, 0)
	intDER, err := x509.CreateCertificate(rand.Reader, intTmpl, rootCert, intKey.Public(), rootKey)
	if err != nil {
		return nil, nil, fmt.Errorf("create intermediate: %w", err)
	}

	rootKeyDER, err := x509.MarshalECPrivateKey(rootKey)
	if err != nil {
		return nil, nil, err
	}
	rootKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: rootKeyDER})

	mat, err = packMaterial(master, rootDER, intDER, intKey)
	if err != nil {
		return nil, nil, err
	}
	return mat, rootKeyPEM, nil
}

// packMaterial is the shared assembly: root cert (no private key) + intermediate cert/encrypted private key.
func packMaterial(master, rootDER, interDER []byte, interKey *ecdsa.PrivateKey) (*Material, error) {
	interKeyDER, err := x509.MarshalECPrivateKey(interKey)
	if err != nil {
		return nil, err
	}
	interKeyEnc, err := encryptKey(master, interKeyDER)
	if err != nil {
		return nil, err
	}
	return &Material{
		RootCertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}),
		RootKeyEnc:  []byte{}, // root offline: deliberately store no private key (non-nil, to avoid violating BYTEA NOT NULL)
		IntCertPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: interDER}),
		IntKeyEnc:   interKeyEnc,
	}, nil
}

// parseECPrivateKeyPEM accepts an ECDSA private key in SEC1 (EC PRIVATE KEY) or PKCS#8 form.
func parseECPrivateKeyPEM(p []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(p)
	if block == nil {
		return nil, fmt.Errorf("no PEM block")
	}
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("neither SEC1 nor PKCS#8: %w", err)
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not an ECDSA private key (this system issues P-256/ES256 leaf certificates)")
	}
	return ec, nil
}

// orgIDRe is the valid form of an org id. It is both the cert O and a label of the platform SAN `<id>.platform.internal`, so it follows hostname rules:
// lowercase alphanumerics and . - _, starting and ending with an alphanumeric.
var orgIDRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`)

// CheckOrgID: ORG_ID is the org id (used directly as the cert O and in the platform SAN), written into every cert when the CA is created, and cannot change afterwards.
// Invalid values are rejected rather than converted: converting would make the same org look different on its own platform and on others', and Chinese would become an empty string.
func CheckOrgID(id string) error {
	if id == "" {
		return fmt.Errorf("ORG_ID is not set: the organization id cannot change once the CA exists, set it explicitly (for example a company domain such as example.com)")
	}
	if !orgIDRe.MatchString(id) || strings.Contains(id, "..") {
		return fmt.Errorf("ORG_ID %q is invalid: use only lowercase letters, digits and . - _, starting and ending with a letter or digit (for example example.com)", id)
	}
	return nil
}
