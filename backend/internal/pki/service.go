// Package pki wires the embedded CA into the platform runtime: loads/bootstraps the CA, issues the platform's own
// leaf cert, and provides the TLS config and join API for the agent-facing endpoint (:8443 mTLS listener).
package pki

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
	"fmt"
	"log"
	"sync"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/ca"
	"besu-manager/backend/internal/store"
)

type Service struct {
	CA      *ca.CA
	Store   *store.Store
	Org     string
	LeafTTL time.Duration // leaf cert lifetime (default 72h; <24h is only for rotation demos and logs a warning at startup)

	leafMu sync.RWMutex
	leaf   tls.Certificate // platform leaf cert (with intermediate + root chain); RunRotation renews it before expiry
	leafX  *x509.Certificate
	hub    *hub // agent WS connections

	agentBinaryDir string // AGENT_BINARY_DIR (read by the mTLS agent-binary endpoints; passed in by Router)

	master []byte // CA_MASTER_KEY

	denyMu sync.RWMutex
	deny   map[string]bool // revoked serial cache (deny list)

	// quarantineState: the quarantine state last written to the DB, kept only to avoid rewriting it every 2
	// seconds (see syncQuarantine). The authoritative value is nodes.quarantined_at.
	quarantineMu    sync.Mutex
	quarantineState map[string]bool

	// outdated: node id -> the last time it was rejected for an agent protocol version that is too old (see
	// handleWS). Memory only: after a platform restart, the agent's next reconnect (backoff capped at 30
	// seconds) is rejected again and recorded again.
	outdated sync.Map

	rootOnline    bool      // root private key is in the DB (self-signed bootstrap); decides what the expiry warning recommends
	interWarnedAt time.Time // when the intermediate expiry warning was last printed; only the RunRotation goroutine reads/writes it
}

// Load loads (or bootstraps on first run) the CA and issues the platform's own leaf cert. The caller has already
// checked org with ca.CheckOrgID.
func Load(ctx context.Context, st *store.Store, master []byte, org string, leafTTL time.Duration) (*Service, error) {
	if len(master) != 32 {
		return nil, fmt.Errorf("CA master key must be 32 bytes (64 hex characters)")
	}
	mat, err := st.GetCAMaterial(ctx)
	if err != nil {
		return nil, err
	}
	if mat == nil {
		now := time.Now().UTC()
		mat, err = ca.Bootstrap(master, org, now)
		if err != nil {
			return nil, err
		}
		// not_after is derived from the material, to avoid defining the lifetime twice
		tmp, err := ca.FromMaterial(master, org, mat)
		if err != nil {
			return nil, err
		}
		// Record before storing (fail-closed): if the audit write fails, do not create the CA and fail startup
		if err := st.Audit(ctx, "ca_bootstrap", map[string]any{
			"org": org, "root_digest": tmp.RootDigest(),
		}); err != nil {
			return nil, fmt.Errorf("write ca_bootstrap audit entry, CA not created: %w", err)
		}
		if err := st.SaveCAMaterial(ctx, mat, tmp.Root.NotAfter, tmp.Inter.NotAfter); err != nil {
			d := failedDetail(api.ErrorReasonOperationFailed, map[string]string{"detail": err.Error()})
			d["root_digest"] = tmp.RootDigest()
			_ = st.Audit(ctx, "ca_bootstrap", d)
			return nil, err
		}
	}

	mat = renewIntermediateIfDue(ctx, st, master, mat)

	c, err := ca.FromMaterial(master, org, mat)
	if err != nil {
		return nil, err
	}
	if mat.RootOnline() {
		log.Printf("pki: ⚠️ root private key is stored encrypted in the DB (self-signed bootstrap), fit only for development and trials; for production run cactl init-offline or cactl import before the first start so the root never enters the system")
	}
	warnIntermediateExpiry(c.Inter, mat.RootOnline(), time.Now().UTC())
	if leafTTL <= 0 {
		leafTTL = ca.DefaultLeafTTL
	}
	s := &Service{
		CA: c, Store: st, Org: org, LeafTTL: leafTTL,
		master:        master,
		deny:          map[string]bool{},
		hub:           &hub{conns: map[string]*agentConn{}},
		rootOnline:    mat.RootOnline(),
		interWarnedAt: time.Now().UTC(), // just printed once above
	}
	if err := s.issuePlatformLeaf(ctx); err != nil {
		return nil, err
	}
	if err := s.RefreshDenyList(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// renewIntermediateIfDue: when the root is in the DB and the intermediate has less than ca.RenewBefore left,
// renew it with the same private key; only done at startup. Failure does not block startup: the old one stays
// in use and the expiry warning reminds; if the audit write fails, no renewal.
func renewIntermediateIfDue(ctx context.Context, st *store.Store, master []byte, mat *ca.Material) *ca.Material {
	if !mat.RootOnline() {
		return mat
	}
	old, err := ca.FromMaterial(master, "", mat)
	if err != nil || time.Until(old.Inter.NotAfter) >= ca.RenewBefore {
		return mat
	}
	nm, err := ca.RenewIntermediate(master, mat, time.Now().UTC())
	if err != nil {
		log.Printf("pki: renew intermediate, keeping the old one: %v", err)
		return mat
	}
	renewed, err := ca.FromMaterial(master, "", nm)
	if err != nil {
		log.Printf("pki: load renewed intermediate, keeping the old one: %v", err)
		return mat
	}
	detail := map[string]any{"old_not_after": old.Inter.NotAfter, "new_not_after": renewed.Inter.NotAfter}
	if err := st.Audit(ctx, "ca_intermediate_renewed", detail); err != nil {
		log.Printf("pki: write intermediate renewal audit entry, skipping this renewal: %v", err)
		return mat
	}
	if err := st.UpdateIntermediate(ctx, nm.IntCertPEM, nm.IntKeyEnc, renewed.Inter.NotAfter); err != nil {
		_ = st.Audit(ctx, "ca_intermediate_renewed", failedDetail(api.ErrorReasonOperationFailed, map[string]string{"detail": err.Error()}))
		log.Printf("pki: save renewed intermediate, keeping the old one: %v", err)
		return mat
	}
	log.Printf("pki: intermediate renewed, expiry %s -> %s", old.Inter.NotAfter.Format(time.RFC3339), renewed.Inter.NotAfter.Format(time.RFC3339))
	return nm
}

// warnIntermediateExpiry: prints a warning when the intermediate has less than ca.RenewBefore left. On the
// day it expires every agent must join again.
func warnIntermediateExpiry(inter *x509.Certificate, rootOnline bool, now time.Time) {
	left := inter.NotAfter.Sub(now)
	if left >= ca.RenewBefore {
		return
	}
	how := "run cactl rotate-intermediate --csr-out to produce a CSR for the root holder to sign, then import it with --inter-cert and restart"
	if rootOnline {
		how = "restarting the server renews it automatically, or run cactl rotate-intermediate"
	}
	log.Printf("pki: ⚠️ intermediate CA expires at %s (%d days left); after that every agent must re-join. %s",
		inter.NotAfter.Format(time.RFC3339), int(left.Hours()/24), how)
}

// warnIntermediateDaily: reminds at most once a day while running (called every RunRotation round).
func (s *Service) warnIntermediateDaily(now time.Time) {
	if now.Sub(s.interWarnedAt) < 24*time.Hour {
		return
	}
	s.interWarnedAt = now
	warnIntermediateExpiry(s.CA.Inter, s.rootOnline, now)
}

// issuePlatformLeaf: on every start the platform self-issues a short-lived leaf cert (key kept only in memory).
func (s *Service) issuePlatformLeaf(ctx context.Context) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		return err
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return err
	}
	leafDER, err := s.CA.IssueLeaf(ctx, csr, ca.CertProfile{Role: ca.RolePlatform})
	if err != nil {
		return err
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return err
	}
	if err := s.Store.InsertIssuedCert(ctx, store.IssuedCert{
		Serial: leaf.SerialNumber.String(), Subject: leaf.Subject.String(),
		NotBefore: leaf.NotBefore, NotAfter: leaf.NotAfter, CertPEM: certPEM(leafDER),
	}); err != nil {
		return err
	}
	// The chain includes the root: agent join pins the root via the digest in the token, so the root must be
	// in the presented chain
	s.leafMu.Lock()
	s.leaf = tls.Certificate{
		Certificate: [][]byte{leafDER, s.CA.Inter.Raw, s.CA.Root.Raw},
		PrivateKey:  key,
	}
	s.leafX = leaf
	s.leafMu.Unlock()
	return nil
}

// renewPlatformLeafIfDue: replaces the platform leaf cert once it has less than 1/3 of its lifetime left
// (same rotation threshold as agents). 8443 / 8544 both get the current cert through GetCertificate;
// established connections are unaffected, so renewal needs no disconnect.
func (s *Service) renewPlatformLeafIfDue(ctx context.Context, now time.Time) {
	s.leafMu.RLock()
	cur := s.leafX
	s.leafMu.RUnlock()
	if cur == nil || cur.NotAfter.Sub(now) >= cur.NotAfter.Sub(cur.NotBefore)/3 {
		return
	}
	if err := s.issuePlatformLeaf(ctx); err != nil {
		log.Printf("pki: renew platform leaf certificate (will retry next round): %v", err)
		return
	}
	log.Printf("pki: platform leaf certificate renewed (old one expires %s)", cur.NotAfter.Format(time.RFC3339))
}

func (s *Service) currentLeaf(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.leafMu.RLock()
	defer s.leafMu.RUnlock()
	c := s.leaf
	return &c, nil
}

// CrossOrgTLSConfig: for 8544. Same platform cert, but client certs are neither requested nor verified: the
// viewer's identity is determined by the bearer access key, not mTLS.
func (s *Service) CrossOrgTLSConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, GetCertificate: s.currentLeaf}
}

func (s *Service) ServerTLSConfig() *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(s.CA.Root)
	return &tls.Config{
		MinVersion:     tls.VersionTLS13,
		GetCertificate: s.currentLeaf,
		ClientAuth:     tls.VerifyClientCertIfGiven,
		ClientCAs:      pool,
		// VerifyPeerCertificate is not called on resumed sessions: with session tickets on, anyone who once
		// held a cert could use a resumed connection to bypass the revocation and intermediate checks below.
		// Agents never resume sessions anyway, so turning them off costs nothing.
		SessionTicketsDisabled: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, chains [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return nil
			}
			leaf, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return err
			}
			if s.IsRevoked(leaf.SerialNumber.String()) {
				return fmt.Errorf("certificate revoked")
			}
			if !throughCurrentInter(chains, s.CA.Inter) {
				return fmt.Errorf("certificate not issued by the current intermediate")
			}
			return nil
		},
	}
}

// throughCurrentInter: the intermediate that signed the leaf must have the current public key. ClientCAs
// holds only the root, and verifying up to the root alone would also accept certs signed by an old
// intermediate replaced in a key-compromise rotation (`--new-key`).
func throughCurrentInter(chains [][]*x509.Certificate, inter *x509.Certificate) bool {
	for _, ch := range chains {
		if len(ch) >= 2 && bytes.Equal(ch[1].RawSubjectPublicKeyInfo, inter.RawSubjectPublicKeyInfo) {
			return true
		}
	}
	return false
}

// ── deny list cache ───────────────────────────────────────────

func (s *Service) RefreshDenyList(ctx context.Context) error {
	serials, err := s.Store.RevokedSerials(ctx)
	if err != nil {
		return err
	}
	m := make(map[string]bool, len(serials))
	for _, sn := range serials {
		m[sn] = true
	}
	s.denyMu.Lock()
	s.deny = m
	s.denyMu.Unlock()
	return nil
}

func (s *Service) IsRevoked(serial string) bool {
	s.denyMu.RLock()
	defer s.denyMu.RUnlock()
	return s.deny[serial]
}

// ── helpers ──────────────────────────────────────────────────

func certPEM(der []byte) string {
	return string(pemEncode("CERTIFICATE", der))
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
