// Package join implements agent bootstrap:
// it verifies the platform by pinning the root CA digest in the join token (against MITM),
// presents the secret in exchange for a leaf cert issued by the embedded CA, and persists the material for mTLS.
package join

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"besu-manager/agent/internal/certstore"
	"besu-manager/agent/internal/durable"
)

type Options struct {
	PlatformAddr string // platform mTLS listener(host:port)
	Token        string // BSOS-1-sha256:<root digest>-<secret>; the node id is bound when the token is created, and the platform reads it from the token
	StateDir     string // directory holding the cert material

	// ReEnroll: set true when the node is already in the platform inventory but its cert is no longer valid
	// (host rebuilt, cert lost or revoked), to call /pki/v1/re-enroll instead. A still-valid cert goes
	// through WS renew and does not need this; join rejects an existing node id, so without this flag the
	// only way back after the cert is invalid is to delete the node and onboard again (breaking the audit
	// and inventory history).
	ReEnroll bool
}

func parseTokenDigest(token string) ([]byte, error) {
	// format: BSOS-1-sha256:<64 hex>-<secret>
	const marker = "sha256:"
	i := strings.Index(token, marker)
	if !strings.HasPrefix(token, "BSOS-1-") || i < 0 || len(token) < i+len(marker)+64 {
		return nil, fmt.Errorf("malformed token")
	}
	return hex.DecodeString(token[i+len(marker) : i+len(marker)+64])
}

// pinnedTLSConfig does not use the system trust store; it requires the chain presented by the platform to
// contain a root matching the digest, and the leaf to verify with that root as the trust anchor (merely
// finding a matching digest is not enough).
func pinnedTLSConfig(digest []byte) *tls.Config {
	// nosemgrep: problem-based-packs.insecure-transport.go-stdlib.bypass-tls-verification.bypass-tls-verification -- identity is in the cert Subject O/OU, not the hostname, so hostname is not verified; chain verification is done by the custom VerifyPeerCertificate below, and TLS 1.3 is pinned as the minimum
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // custom verification below; do not remove this explanation
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			var leaf *x509.Certificate
			inters := x509.NewCertPool()
			roots := x509.NewCertPool()
			for i, der := range rawCerts {
				cert, err := x509.ParseCertificate(der)
				if err != nil {
					return err
				}
				sum := sha256.Sum256(der)
				switch {
				case i == 0:
					leaf = cert
				case bytes.Equal(sum[:], digest):
					roots.AddCert(cert)
				default:
					inters.AddCert(cert)
				}
			}
			if leaf == nil {
				return fmt.Errorf("no peer certificate")
			}
			if len(roots.Subjects()) == 0 { //nolint:staticcheck // only an emptiness check
				return fmt.Errorf("platform CA digest mismatch (possible man-in-the-middle)")
			}
			if _, err := leaf.Verify(x509.VerifyOptions{
				Roots: roots, Intermediates: inters,
				KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			}); err != nil {
				return fmt.Errorf("verify platform certificate chain: %w", err)
			}
			// Read OU only after the chain verifies to the pinned root (verify the chain, then read fields). Without
			// this check, any leaf signed by the same CA would count as "platform", including a node's own.
			// Compare OU but not O: at join time the agent does not know its org id yet (it has no cert),
			// and the organization is already pinned by the root digest. wsclient compares both because it has
			// a certstore.
			id, err := certstore.IdentityFromCert(leaf)
			if err != nil {
				return fmt.Errorf("read platform certificate identity: %w", err)
			}
			if id.OU != "platform" {
				return fmt.Errorf("peer is not a platform (OU=%q)", id.OU)
			}
			return nil
		},
	}
}

type joinResponse struct {
	CertPEM  string    `json:"certPem"`
	ChainPEM string    `json:"chainPem"`
	NotAfter time.Time `json:"notAfter"`
}

func Run(ctx context.Context, o Options) error {
	digest, err := parseTokenDigest(o.Token)
	if err != nil {
		return err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return err
	}

	payload := map[string]any{
		"token":  o.Token,
		"csrPem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})),
	}
	body, _ := json.Marshal(payload)
	hc := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: pinnedTLSConfig(digest)},
	}
	path := "/pki/v1/join"
	if o.ReEnroll {
		path = "/pki/v1/re-enroll"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://"+o.PlatformAddr+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("join rejected (%d): %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var jr joinResponse
	if err := json.NewDecoder(resp.Body).Decode(&jr); err != nil {
		return err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(o.StateDir, 0o700); err != nil {
		return err
	}
	// A state directory created just now must also survive a power cut, not only the files in it
	if err := durable.SyncDir(filepath.Dir(o.StateDir)); err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := certstore.WriteFiles(o.StateDir, keyPEM, []byte(jr.CertPEM), []byte(jr.ChainPEM)); err != nil {
		return err
	}
	fmt.Printf("join complete: certificate valid until %s, material stored in %s\n", jr.NotAfter.Format(time.RFC3339), o.StateDir)
	// Only a fresh join can be picked up by a running agent; a re-enroll requires the agent to be offline.
	if !o.ReEnroll {
		fmt.Println("If besu-agent is running, it switches to the new certificate on its next reconnect (within about 30 seconds); no restart needed.")
	}
	return nil
}
