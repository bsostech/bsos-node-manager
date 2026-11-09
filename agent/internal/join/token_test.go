package join

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"strings"
	"testing"

	"besu-manager/agent/internal/certstore/certtest"
)

func TestParseTokenDigest(t *testing.T) {
	digestHex := strings.Repeat("ab", 32) // 64 hex chars = 32 bytes
	good := "BSOS-1-sha256:" + digestHex + "-thesecret"
	d, err := parseTokenDigest(good)
	if err != nil {
		t.Fatalf("合法 token 不該出錯:%v", err)
	}
	if hex.EncodeToString(d) != digestHex {
		t.Fatalf("digest 解析錯:%x", d)
	}

	bad := []struct{ name, token string }{
		{"缺 BSOS-1- 前綴", "sha256:" + digestHex + "-s"},
		{"缺 sha256: 標記", "BSOS-1-" + digestHex + "-s"},
		{"digest 太短", "BSOS-1-sha256:abcd-s"},
		{"digest 差幾個字", "BSOS-1-sha256:" + strings.Repeat("ab", 30) + "-s"},
		{"digest 非 hex", "BSOS-1-sha256:" + strings.Repeat("zz", 32) + "-s"},
		{"空字串", ""},
	}
	for _, c := range bad {
		if _, err := parseTokenDigest(c.token); err == nil {
			t.Errorf("%s:應出錯卻成功", c.name)
		}
	}
}

// genCA: a self-signed root, its DER and its key.
func genCA(t *testing.T, cn string) (*x509.Certificate, []byte, *ecdsa.PrivateKey) {
	t.Helper()
	key := certtest.Key(t)
	tmpl := certtest.CATemplate(cn)
	cert := certtest.Sign(t, tmpl, tmpl, key.Public(), key)
	return cert, cert.Raw, key
}

// genLeaf: a serverAuth leaf signed directly by the root, as DER. ou sets the identity: "platform" for the
// platform, "node" for a node. Subject O/OU must be set: pinnedTLSConfig reads them after verifying the chain.
func genLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, ou string) []byte {
	t.Helper()
	return certtest.Sign(t, &x509.Certificate{
		Subject:     pkix.Name{Organization: []string{"org-a"}, OrganizationalUnit: []string{ou}, CommonName: ou},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, ca, certtest.Key(t).Public(), caKey).Raw
}

// pinnedTLSConfig's VerifyPeerCertificate passes only if the digest matches and the leaf verifies to that root.
func TestPinnedVerifyPeerCertificate(t *testing.T) {
	root, rootDER, rootKey := genCA(t, "platform-root")
	leafDER := genLeaf(t, root, rootKey, "platform")
	digest := sha256.Sum256(rootDER)

	verify := func(digest []byte, chain ...[]byte) error {
		return pinnedTLSConfig(digest).VerifyPeerCertificate(chain, nil)
	}

	// 1) correct digest + full chain -> pass
	if err := verify(digest[:], leafDER, rootDER); err != nil {
		t.Errorf("正確 digest+鏈應放行,卻 %v", err)
	}

	// 2) digest mismatch (MITM brings its own CA) -> reject
	wrong := sha256.Sum256([]byte("attacker"))
	if err := verify(wrong[:], leafDER, rootDER); err == nil {
		t.Error("digest 不符應被擋(MITM)")
	}

	// 3) leaf only, no matching root in the chain -> reject
	if err := verify(digest[:], leafDER); err == nil {
		t.Error("鏈中無 pinned root 應被擋")
	}

	// 4) digest matches a root, but the leaf is not signed by it -> chain verification fails, reject
	//    (attacker presents the root [whose digest is pinned] paired with a leaf from elsewhere)
	otherRoot, otherRootDER, _ := genCA(t, "other-root")
	_ = otherRoot
	otherDigest := sha256.Sum256(otherRootDER)
	if err := verify(otherDigest[:], leafDER, otherRootDER); err == nil {
		t.Error("葉憑證未由 pinned root 簽發應被擋")
	}

	// 5) a **node** cert signed by the same CA impersonating the platform -> reject.
	//    Neither digest pinning nor chain verification catches this: the cert really is signed by this root,
	//    so the digest naturally matches. The OU check after chain verification is what stops it. Node leaf
	//    certs also do not carry serverAuth (ca.CertProfile); the two checks are independent. This test
	//    deliberately gives it serverAuth to show that "even if the EKU check fails, OU still stops it".
	nodeLeafDER := genLeaf(t, root, rootKey, "node")
	if err := verify(digest[:], nodeLeafDER, rootDER); err == nil {
		t.Error("節點憑證冒充平台應被擋(OU 檢查)")
	}
}
