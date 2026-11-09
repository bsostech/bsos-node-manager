package ca

import (
	"crypto/ecdsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
)

// The role and org come from O/OU of a signed cert; a cert missing either, or carrying an unknown role, is
// refused instead of being read as some default role.
func TestRoleFromCert(t *testing.T) {
	c := newTestCA(t, testMaster(t))
	agent := issueTestLeaf(t, c, CertProfile{Role: RoleAgent, NodeID: "node-a1"})
	platform := issueTestLeaf(t, c, CertProfile{Role: RolePlatform})
	if id, err := RoleFromCert(agent); err != nil || id != (Identity{Role: RoleAgent, Org: "org-a", CN: "node-a1"}) {
		t.Errorf("agent: %+v err=%v", id, err)
	}
	if id, err := RoleFromCert(platform); err != nil || id != (Identity{Role: RolePlatform, Org: "org-a", CN: "platform"}) {
		t.Errorf("platform: %+v err=%v", id, err)
	}
	for name, subj := range map[string]pkix.Name{
		"缺 O":  {OrganizationalUnit: []string{"node"}, CommonName: "node-a1"},
		"缺 OU": {Organization: []string{"org-a"}, CommonName: "node-a1"},
		"未知角色": {Organization: []string{"org-a"}, OrganizationalUnit: []string{"admin"}, CommonName: "node-a1"},
	} {
		if id, err := RoleFromCert(&x509.Certificate{Subject: subj}); err == nil {
			t.Errorf("%s 應被拒,得 %+v", name, id)
		}
	}
}

// The signer's public key is the intermediate's: leaves it signs verify against that key.
func TestSignerPublicKeyIsIntermediate(t *testing.T) {
	c := newTestCA(t, testMaster(t))
	pub, ok := c.signer.PublicKey().(*ecdsa.PublicKey)
	if !ok || !pub.Equal(c.Inter.PublicKey) {
		t.Fatalf("signer 公鑰應等於 intermediate 的:%T", c.signer.PublicKey())
	}
	leaf := issueTestLeaf(t, c, CertProfile{Role: RoleAgent, NodeID: "node-a1"})
	if err := leaf.CheckSignatureFrom(c.Inter); err != nil {
		t.Fatal(err)
	}
}
