package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"time"
)

// Intermediate renewal: routine renewal keeps the same private key; switching keys is only for compromise response, and every agent must rejoin.

// RenewBefore: once the intermediate has less than this left, renew it if the root is in the DB, or warn if the root is offline.
const RenewBefore = 90 * 24 * time.Hour

// RootOnline: the root private key is in the DB (self-signed bootstrap). It is empty for init-offline / import material.
func (m *Material) RootOnline() bool { return len(m.RootKeyEnc) > 0 }

// RenewIntermediate re-signs a new cert for the same intermediate private key using the root in the DB.
// Key and name stay the same, so already-issued leaf certs still verify and agents are unaffected.
func RenewIntermediate(master []byte, m *Material, now time.Time) (*Material, error) {
	return reissueIntermediate(master, m, now, false)
}

// RotateIntermediate is for compromise response: it uses the root in the DB to sign an intermediate with a new private key.
// Platform and agents trust only the current intermediate public key, so certs signed by the old key become invalid immediately and every agent must rejoin.
func RotateIntermediate(master []byte, m *Material, now time.Time) (*Material, error) {
	return reissueIntermediate(master, m, now, true)
}

func reissueIntermediate(master []byte, m *Material, now time.Time, newKey bool) (*Material, error) {
	if !m.RootOnline() {
		return nil, fmt.Errorf("root private key is not in the DB; the root holder must sign the intermediate (via CSR)")
	}
	root, err := parseCertPEM(m.RootCertPEM)
	if err != nil {
		return nil, fmt.Errorf("root cert: %w", err)
	}
	old, err := parseCertPEM(m.IntCertPEM)
	if err != nil {
		return nil, fmt.Errorf("intermediate cert: %w", err)
	}
	rootKey, err := decryptECKey(master, m.RootKeyEnc)
	if err != nil {
		return nil, fmt.Errorf("root key: %w", err)
	}

	keyEnc := m.IntKeyEnc
	var key *ecdsa.PrivateKey
	if newKey {
		if key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			return nil, err
		}
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			return nil, err
		}
		if keyEnc, err = encryptKey(master, der); err != nil {
			return nil, err
		}
	} else if key, err = decryptECKey(master, m.IntKeyEnc); err != nil {
		return nil, fmt.Errorf("intermediate key: %w", err)
	}

	tmpl := caTemplate(old.Subject.CommonName, now, intermediateTTL, 0)
	if tmpl.NotAfter.After(root.NotAfter) {
		tmpl.NotAfter = root.NotAfter // never sign an intermediate that outlives the root
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, root, key.Public(), rootKey)
	if err != nil {
		return nil, fmt.Errorf("sign intermediate: %w", err)
	}
	return &Material{
		RootCertPEM: m.RootCertPEM,
		RootKeyEnc:  m.RootKeyEnc,
		IntCertPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		IntKeyEnc:   keyEnc,
	}, nil
}

// IntermediateCSR is the first step when the root is offline: the CSR goes to the root holder to sign.
// Routine renewal builds the CSR from the existing private key (keyPEM is nil); newKey is for compromise response and generates a new private key, returned as PEM for the caller to write to a file.
func IntermediateCSR(master []byte, m *Material, org string, newKey bool) (keyPEM, csrPEM []byte, err error) {
	var key *ecdsa.PrivateKey
	if newKey {
		if key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			return nil, nil, err
		}
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			return nil, nil, err
		}
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	} else if key, err = decryptECKey(master, m.IntKeyEnc); err != nil {
		return nil, nil, fmt.Errorf("intermediate key: %w", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: intermediateCN(org)},
	}, key)
	if err != nil {
		return nil, nil, err
	}
	return keyPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}), nil
}

// ReplaceIntermediate is the second step when the root is offline: it imports the intermediate signed by the root holder.
// keyPEM nil = keep the existing private key (the cert's public key must match it); non-nil = a new private key for compromise response.
// Validation matches import (signed by the current root, IsCA, key matches), plus it must be within its validity period now. The root is unchanged.
func ReplaceIntermediate(master []byte, m *Material, interCertPEM, keyPEM []byte, now time.Time) (*Material, error) {
	if keyPEM == nil {
		key, err := decryptECKey(master, m.IntKeyEnc)
		if err != nil {
			return nil, fmt.Errorf("intermediate key: %w", err)
		}
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			return nil, err
		}
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	}
	nm, err := ImportMaterial(master, m.RootCertPEM, interCertPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	inter, err := parseCertPEM(nm.IntCertPEM)
	if err != nil {
		return nil, err
	}
	if now.Before(inter.NotBefore) || now.After(inter.NotAfter) {
		return nil, fmt.Errorf("intermediate is outside its validity period (%s to %s)",
			inter.NotBefore.Format(time.RFC3339), inter.NotAfter.Format(time.RFC3339))
	}
	nm.RootKeyEnc = m.RootKeyEnc
	return nm, nil
}

// intermediateCN is the intermediate's name; bootstrap, init-offline and renewal CSRs all use it so the name stays consistent.
func intermediateCN(org string) string { return fmt.Sprintf("%s intermediate CA", org) }

func decryptECKey(master, blob []byte) (*ecdsa.PrivateKey, error) {
	der, err := decryptKey(master, blob)
	if err != nil {
		return nil, err
	}
	return x509.ParseECPrivateKey(der)
}
