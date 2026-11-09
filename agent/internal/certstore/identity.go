package certstore

import (
	"crypto/x509"
	"fmt"
)

// Identity is the identity read from a cert Subject (O/OU/CN).
// It matches the backend ca.Identity encoding: O=org id, OU=role (node/platform/prometheus/admin),
// CN=entity name (human-readable). CN must never be used for security decisions.
type Identity struct {
	Org string // O = org id
	OU  string // role
	CN  string // entity name
}

// IdentityFromCert reads O/OU/CN from the cert Subject.
// It only reads fields and does not verify the chain; callers must verify the cert to a trusted root first
// (verify the chain, then read fields).
// Change the role encoding only here (the single source of truth within the agent module).
func IdentityFromCert(cert *x509.Certificate) (Identity, error) {
	if len(cert.Subject.Organization) == 0 {
		return Identity{}, fmt.Errorf("certificate has no O (organization)")
	}
	if len(cert.Subject.OrganizationalUnit) == 0 {
		return Identity{}, fmt.Errorf("certificate has no OU (role)")
	}
	return Identity{
		Org: cert.Subject.Organization[0],
		OU:  cert.Subject.OrganizationalUnit[0],
		CN:  cert.Subject.CommonName,
	}, nil
}
