package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"sort"
	"time"
)

// Node JSON-RPC authentication is "the platform signs JWTs, nodes hold only the public key".
// Besu verifies signatures with --rpc-http-authentication-jwt-public-key-file, reads method permissions from
// the token's permissions claim, and rejects tokens without exp outright. Where the private key lives is set by RPC_KEY_BACKEND:
// Core has only db (the private key is stored in the DB encrypted with the master key, see secret.go); other backends are registered by Enterprise.
//
// The algorithm is pinned to RS256: Besu 26.7.1 only accepts RS256/384/512 and ES256/384/512, no Ed25519.
const (
	RpcJwtAlgorithm = "RS256"
	rpcKeyBits      = 2048
)

// RpcKeyBackend opens a signing backend and returns a crypto.Signer. The private key may not be in platform memory (Vault / HSM),
// so the platform can only ask it to sign. A missing key must return an error, not generate one (except the db backend, see LoadOrCreateRpcKey).
type RpcKeyBackend func(ctx context.Context) (crypto.Signer, error)

// OpenRpcSigner opens one of the backends available in this build (backends: name -> backend, assembled and passed in by main) by name and runs the startup self-check.
// An unknown name fails instead of silently falling back to db.
func OpenRpcSigner(ctx context.Context, name string, backends map[string]RpcKeyBackend) (*RpcSigner, error) {
	open, ok := backends[name]
	if !ok {
		names := make([]string, 0, len(backends))
		for n := range backends {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("RPC_KEY_BACKEND=%q is unknown (this build has: %v)", name, names)
	}
	signer, err := open(ctx)
	if err != nil {
		return nil, fmt.Errorf("rpc key backend %s: %w", name, err)
	}
	return NewRpcSigner(signer)
}

// RpcSigningKeyStore is the db backend's persistence (a single row); implemented by the store package.
type RpcSigningKeyStore interface {
	GetRpcSigningKey(ctx context.Context) (keyEnc []byte, publicPEM string, err error)
	SaveRpcSigningKey(ctx context.Context, keyEnc []byte, publicPEM, algorithm string) error
}

// RpcSigner signs tokens with the crypto.Signer provided by the backend. Besides Sign it exposes only the public key and its fingerprint;
// the public key is always derived from Signer.Public(), never read from the copy stored in the DB.
type RpcSigner struct {
	signer    crypto.Signer
	publicPEM string
	digest    string
}

// NewRpcSigner wraps a crypto.Signer and self-checks it: it signs a fixed payload and verifies it with the signer's own public key.
// When the backend is misconfigured (insufficient permissions, wrong parameters), Besu cannot verify what it signs yet the backend itself reports no error, so this is the only place to catch it.
func NewRpcSigner(signer crypto.Signer) (*RpcSigner, error) {
	pub, ok := signer.Public().(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("rpc signing key is not RSA (%s requires RSA)", RpcJwtAlgorithm)
	}
	pubPEM, der, err := publicKeyPEM(pub)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte("bsos rpc signer self-test"))
	sig, err := signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("rpc signing key self-check: sign: %w", err)
	}
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		return nil, fmt.Errorf("rpc signing key self-check: signature does not verify with its own public key (wrong backend parameters?): %w", err)
	}
	sum := sha256.Sum256(der)
	return &RpcSigner{
		signer:    signer,
		publicPEM: pubPEM,
		digest:    hex.EncodeToString(sum[:]),
	}, nil
}

// LoadOrCreateRpcKey is the db backend. It loads the signing key from the DB, or generates a key pair and stores it if there is none.
// Rotating the key pair has no automated flow: every node's public key file must be replaced and Besu restarted; that is an ops task, not done here.
func LoadOrCreateRpcKey(ctx context.Context, st RpcSigningKeyStore, master []byte) (*rsa.PrivateKey, error) {
	keyEnc, _, err := st.GetRpcSigningKey(ctx)
	if err != nil {
		return nil, err
	}
	if keyEnc == nil {
		key, err := rsa.GenerateKey(rand.Reader, rpcKeyBits)
		if err != nil {
			return nil, err
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, err
		}
		enc, err := encryptSecret(master, der)
		if err != nil {
			return nil, err
		}
		pubPEM, _, err := publicKeyPEM(&key.PublicKey)
		if err != nil {
			return nil, err
		}
		if err := st.SaveRpcSigningKey(ctx, enc, pubPEM, RpcJwtAlgorithm); err != nil {
			return nil, err
		}
		// The insert is ON CONFLICT DO NOTHING: when two instances start at once only one wins. Read back from the DB and use the winner's key,
		// otherwise the loser would sign with its own generated key and the nodes' public key would not verify it.
		if keyEnc, _, err = st.GetRpcSigningKey(ctx); err != nil {
			return nil, err
		}
		if keyEnc == nil {
			return nil, fmt.Errorf("rpc signing key: cannot read back after write")
		}
	}
	der, err := decryptSecret(master, keyEnc)
	if err != nil {
		return nil, fmt.Errorf("rpc signing key: decrypt (was CA_MASTER_KEY changed?): %w", err)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("rpc signing key: not an RSA private key")
	}
	return key, nil
}

// Sign signs a token for node RPC. permissions go into the claim as is (Besu accepts ns:* / ns:method / *:*);
// exp is mandatory because Besu rejects tokens without exp, so there are no "permanent" tokens; for a long-lived one, raise ttl.
//
// No aud: Besu's JWT verification has no audience setting and does not check this field. All nodes of one organization use the same platform
// public key, so a token already works on every auth-enabled node in the organization. sub = the username, purely a label.
//
// The JWT is assembled by hand (RFC 7515 compact): JWT libraries' RS256 only takes an in-memory *rsa.PrivateKey,
// while with the key in Vault / HSM only a crypto.Signer is available.
func (s *RpcSigner) Sign(subject string, permissions []string, ttl time.Duration) (token, id string, exp time.Time, err error) {
	if ttl <= 0 {
		return "", "", time.Time{}, fmt.Errorf("rpc token: ttl must be greater than 0")
	}
	// the ID goes into jti and the issuance record stores the same value, to trace back which token it is; Besu does not read jti
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", "", time.Time{}, err
	}
	id = "tk_" + hex.EncodeToString(b)
	now := time.Now().UTC()
	exp = now.Add(ttl).Truncate(time.Second) // same precision as the exp claim, so the response, the record and the token all agree
	header, err := json.Marshal(map[string]string{"alg": RpcJwtAlgorithm, "typ": "JWT"})
	if err != nil {
		return "", "", time.Time{}, err
	}
	payload, err := json.Marshal(map[string]any{
		"permissions": permissions,
		"sub":         subject,
		"jti":         id,
		"iat":         now.Unix(),
		"exp":         exp.Unix(),
	})
	if err != nil {
		return "", "", time.Time{}, err
	}
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(header) + "." + enc.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := s.signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("sign rpc token: %w", err)
	}
	return signingInput + "." + enc.EncodeToString(sig), id, exp, nil
}

// Algorithm returns the value for Besu's --rpc-http-authentication-jwt-algorithm.
func (s *RpcSigner) Algorithm() string { return RpcJwtAlgorithm }

// PublicKeyPEM returns the contents of the file the node side places next to Besu.
func (s *RpcSigner) PublicKeyPEM() string { return s.publicPEM }

// Digest returns the SHA-256 (hex) of the public key DER, so people can confirm both sides hold the same key.
func (s *RpcSigner) Digest() string { return s.digest }

// publicKeyPEM returns the public key's PKIX PEM (the file given to nodes) and DER (for the fingerprint).
func publicKeyPEM(pub *rsa.PublicKey) (string, []byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", nil, err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), der, nil
}
