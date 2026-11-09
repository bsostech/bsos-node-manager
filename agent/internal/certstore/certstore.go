// Package certstore manages the agent's leaf cert material: load, hot swap (atomic), and persist.
// tls.Config's GetClientCertificate reads the current value,
// so rotation swaps without a restart and without dropping existing connections.
package certstore

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"besu-manager/agent/internal/durable"
)

const (
	keyFile    = "agent.key"
	certFile   = "agent.crt"
	bundleFile = "ca.pem"
)

type Store struct {
	dir       string
	cur       atomic.Pointer[tls.Certificate]
	roots     atomic.Pointer[x509.CertPool]
	interKeys atomic.Pointer[[][]byte] // public keys (SubjectPublicKeyInfo) of the intermediates in the bundle
	org       string
	nodeID    string
}

// Load loads from the directory written by join; if the directory does not exist (not joined yet) it returns (nil, nil).
func Load(dir string) (*Store, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, certFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, keyFile))
	if err != nil {
		return nil, err
	}
	bundlePEM, err := os.ReadFile(filepath.Join(dir, bundleFile))
	if err != nil {
		return nil, err
	}

	roots, interKeys, err := trustFromBundle(bundlePEM)
	if err != nil {
		return nil, err
	}
	cert, err := buildCert(keyPEM, certPEM, bundlePEM)
	if err != nil {
		return nil, err
	}
	id, err := IdentityFromCert(cert.Leaf)
	if err != nil {
		return nil, err
	}
	if id.OU != "node" {
		return nil, fmt.Errorf("agent certificate OU must be node, got %q", id.OU)
	}

	s := &Store{dir: dir, org: id.Org, nodeID: id.CN}
	s.cur.Store(cert)
	s.roots.Store(roots)
	s.interKeys.Store(&interKeys)
	return s, nil
}

// trustFromBundle takes only the self-signed root in the bundle as a trust anchor; the intermediate is not an
// anchor, only its public key is recorded.
// The bundle is the intermediate + root sent by the platform. Verifying the platform requires both a chain to
// the root and that the intermediate that signed the platform cert is this one (IssuedByCurrentIntermediate):
// once the platform replaces the intermediate after a key compromise, the old one is no longer accepted.
func trustFromBundle(bundlePEM []byte) (*x509.CertPool, [][]byte, error) {
	pool := x509.NewCertPool()
	var interKeys [][]byte
	n := 0
	rest := bundlePEM
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		if !isSelfSigned(c) {
			interKeys = append(interKeys, c.RawSubjectPublicKeyInfo)
			continue
		}
		pool.AddCert(c)
		n++
	}
	if n == 0 {
		return nil, nil, fmt.Errorf("ca.pem has no self-signed root certificate")
	}
	if len(interKeys) == 0 {
		return nil, nil, fmt.Errorf("ca.pem has no intermediate certificate")
	}
	return pool, interKeys, nil
}

// IssuedByCurrentIntermediate requires that, in the verified chain, the intermediate that signed the leaf has
// a public key from the current bundle.
func (s *Store) IssuedByCurrentIntermediate(chains [][]*x509.Certificate) bool {
	keys := *s.interKeys.Load()
	for _, ch := range chains {
		if len(ch) < 2 {
			continue
		}
		for _, k := range keys {
			if bytes.Equal(ch[1].RawSubjectPublicKeyInfo, k) {
				return true
			}
		}
	}
	return false
}

// isSelfSigned: Subject equal to Issuer is not enough (the fields can be copied); the signature must verify
// with the cert's own public key.
func isSelfSigned(c *x509.Certificate) bool {
	return c.IsCA && bytes.Equal(c.RawSubject, c.RawIssuer) && c.CheckSignatureFrom(c) == nil
}

// buildCert: the presented chain is leaf + intermediate(s) so the peer can build to the root; the root need
// not be included.
func buildCert(keyPEM, certPEM, bundlePEM []byte) (*tls.Certificate, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, err
	}
	cert.Leaf = leaf
	rest := bundlePEM
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		if !isSelfSigned(c) { // not self-signed = intermediate
			cert.Certificate = append(cert.Certificate, c.Raw)
		}
	}
	return &cert, nil
}

func (s *Store) Certificate() *tls.Certificate { return s.cur.Load() }
func (s *Store) Roots() *x509.CertPool         { return s.roots.Load() }
func (s *Store) Org() string                   { return s.org }
func (s *Store) NodeID() string                { return s.nodeID }

func (s *Store) GetClientCertificate(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
	return s.cur.Load(), nil
}

// WriteFiles stores the key, cert and CA bundle in dir. All three go to temporary files first and only
// then replace the old ones: a join or a rotation can be cut short (Ctrl-C, a stop, a reboot), and a
// truncated key or cert on disk means the agent cannot reconnect without a re-enroll. Renames do no
// I/O, so the window for a mixed old/new set is tiny. The temporary files are synced before the
// renames and the directory after them: otherwise a power cut can persist a rename before the data,
// leaving an empty file under the real name.
func WriteFiles(dir string, keyPEM, certPEM, bundlePEM []byte) error {
	files := []struct {
		name string
		data []byte
	}{{keyFile, keyPEM}, {certFile, certPEM}, {bundleFile, bundlePEM}}
	for _, f := range files {
		if err := durable.WriteFile(filepath.Join(dir, f.name+".tmp"), f.data); err != nil {
			return err
		}
	}
	for _, f := range files {
		if err := os.Rename(filepath.Join(dir, f.name+".tmp"), filepath.Join(dir, f.name)); err != nil {
			return err
		}
	}
	return durable.SyncDir(dir)
}

// Swap hot-swaps in a new cert and persists it (for rotation; the key is freshly generated). The trust pool is
// rebuilt from the new bundle, so after the platform replaces the intermediate or root there is no need to
// wait for an agent restart.
func (s *Store) Swap(keyPEM, certPEM, bundlePEM []byte) error {
	roots, interKeys, err := trustFromBundle(bundlePEM)
	if err != nil {
		return err
	}
	cert, err := buildCert(keyPEM, certPEM, bundlePEM)
	if err != nil {
		return err
	}
	if err := WriteFiles(s.dir, keyPEM, certPEM, bundlePEM); err != nil {
		return err
	}
	s.cur.Store(cert)
	s.roots.Store(roots)
	s.interKeys.Store(&interKeys)
	return nil
}

// Reload re-reads the cert from disk. When join runs again while the agent is running (re-joining after the
// node was deleted), the new cert is only on disk while memory still holds the old cert the platform revoked,
// so every reconnect is rejected; wsclient calls this before each reconnect to pick up the new one.
// If the presented chain (leaf + intermediate) is unchanged it returns (false, nil); if the files cannot be
// read or verified, or belong to another node / another organization, it returns an error and keeps the old one.
// It compares the whole chain, not just the leaf: a half-finished join may first yield a new leaf with the old
// ca.pem, and the bundle written afterwards must also be picked up.
func (s *Store) Reload() (bool, error) {
	fresh, err := Load(s.dir)
	if err != nil {
		return false, err
	}
	if fresh == nil {
		return false, fmt.Errorf("%s is missing", filepath.Join(s.dir, certFile))
	}
	cert := fresh.cur.Load()
	if sameChain(cert.Certificate, s.cur.Load().Certificate) {
		return false, nil
	}
	// nodeID / org are not atomic and cannot change at runtime; a cert with a different identity is
	// rejected (changing identity requires a restart)
	if fresh.nodeID != s.nodeID || fresh.org != s.org {
		return false, fmt.Errorf("certificate on disk belongs to %s/%s, not the running %s/%s", fresh.org, fresh.nodeID, s.org, s.nodeID)
	}
	s.cur.Store(cert)
	s.roots.Store(fresh.roots.Load())
	s.interKeys.Store(fresh.interKeys.Load())
	return true, nil
}

func sameChain(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// PEMBlock helper (used by wsclient to build the CSR).
func PEMBlock(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}
