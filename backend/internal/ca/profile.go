package ca

import (
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"time"
)

type Role string

const (
	RoleAgent    Role = "agent"
	RolePlatform Role = "platform"
)

// CertProfile centralizes the leaf cert lifetime/EKU/SAN template;
// callers must not assemble cert fields themselves.
type CertProfile struct {
	Role   Role
	Org    string // e.g. org-a
	NodeID string // required for RoleAgent
	TTL    time.Duration
}

// Identity encoding, written into the X.509 Subject DN:
//
//	O  (Organization)        = organization identifier (the org id)
//	OU (Organizational Unit) = role (node / platform / admin)
//	CN (Common Name)         = concrete entity name (human-readable label only, never for security decisions)
//
// Verification always goes "verify the chain, then read O/OU"; CN must never be used for authorization.
func ouForRole(r Role) (string, error) {
	switch r {
	case RoleAgent:
		return "node", nil
	case RolePlatform:
		return "platform", nil
	default:
		return "", fmt.Errorf("unknown role %q", r)
	}
}

// Subject builds the X.509 Subject DN for a role (O=org id, OU=role, CN=entity name).
func (p CertProfile) Subject() (pkix.Name, error) {
	ou, err := ouForRole(p.Role)
	if err != nil {
		return pkix.Name{}, err
	}
	var cn string
	switch p.Role {
	case RoleAgent:
		if p.NodeID == "" {
			return pkix.Name{}, fmt.Errorf("agent profile requires NodeID")
		}
		cn = p.NodeID
	case RolePlatform:
		cn = "platform"
	}
	return pkix.Name{
		Organization:       []string{p.Org},
		OrganizationalUnit: []string{ou},
		CommonName:         cn,
	}, nil
}

// PlatformServerName is the SAN on the platform leaf cert. .internal is a TLD ICANN reserves for private networks, so it cannot collide with public names.
func PlatformServerName(org string) string { return org + ".platform.internal" }

// template builds the leaf cert template. EKU depends on the role (see the switch below), not one size fits all:
// nodes do not listen at all (the agent only dials out), so the agent must not be able to get serverAuth.
func (p CertProfile) template(now time.Time) (*x509.Certificate, error) {
	subject, err := p.Subject()
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      subject,
		NotBefore:    now.Add(-1 * time.Minute), // tolerate clock skew
		NotAfter:     now.Add(p.TTL),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	switch p.Role {
	case RolePlatform:
		// 8443 (and cross-org 8544) are servers; clientAuth is reserved for the platform's outbound connections.
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}
		// The verifying side checks 8544 with standard TLS (RootCAs + ServerName) but connects by IP or internal address, with no hostname to compare;
		// give it a fixed name derived from the org id so ServerName has something to match. It is only a label; no DNS resolution happens.
		tmpl.DNSNames = []string{PlatformServerName(p.Org)}
	case RoleAgent:
		// Dials out only; never accepts connections.
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	return tmpl, nil
}
