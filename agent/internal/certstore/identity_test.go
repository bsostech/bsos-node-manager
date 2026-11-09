package certstore

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
)

// IdentityFromCert takes O/OU/CN from the cert Subject; a missing O or OU is always rejected.
func TestIdentityFromCert(t *testing.T) {
	mk := func(o, ou, cn string) *x509.Certificate {
		name := pkix.Name{CommonName: cn}
		if o != "" {
			name.Organization = []string{o}
		}
		if ou != "" {
			name.OrganizationalUnit = []string{ou}
		}
		return &x509.Certificate{Subject: name}
	}

	id, err := IdentityFromCert(mk("org-a.bsos", "node", "node-7"))
	if err != nil {
		t.Fatalf("合法 Subject 不該出錯:%v", err)
	}
	if id.Org != "org-a.bsos" || id.OU != "node" || id.CN != "node-7" {
		t.Errorf("解析錯:%+v", id)
	}

	bad := []struct{ name, o, ou string }{
		{"缺 O", "", "node"},
		{"缺 OU", "org-a.bsos", ""},
	}
	for _, c := range bad {
		if _, err := IdentityFromCert(mk(c.o, c.ou, "x")); err == nil {
			t.Errorf("%s:應出錯卻成功", c.name)
		}
	}
}
