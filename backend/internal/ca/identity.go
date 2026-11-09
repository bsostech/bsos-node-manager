package ca

import (
	"crypto/x509"
	"fmt"
)

// Identity is the identity (O/OU/CN) read from a cert whose chain has been verified.
// Note: Org/Role may be used for security decisions; CN is only a human-readable label and must never be used for authorization.
type Identity struct {
	Role Role   // decoded from OU
	Org  string // O = org id
	CN   string // entity name (human-readable)
}

// RoleFromCert reads the identity (role + organization) from the cert Subject's O/OU.
// It only reads fields and does not verify the chain; callers must first leaf.Verify up to a trusted root
// before calling it (verify the chain, then read fields). Changing the role encoding touches only this and ouForRole.
func RoleFromCert(cert *x509.Certificate) (Identity, error) {
	if len(cert.Subject.Organization) == 0 {
		return Identity{}, fmt.Errorf("certificate has no O (organization)")
	}
	if len(cert.Subject.OrganizationalUnit) == 0 {
		return Identity{}, fmt.Errorf("certificate has no OU (role)")
	}
	var role Role
	switch ou := cert.Subject.OrganizationalUnit[0]; ou {
	case "node":
		role = RoleAgent
	case "platform":
		role = RolePlatform
	default:
		return Identity{}, fmt.Errorf("unknown role OU %q", ou)
	}
	return Identity{
		Role: role,
		Org:  cert.Subject.Organization[0],
		CN:   cert.Subject.CommonName,
	}, nil
}
