package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"io"
	"regexp"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type memKeyStore struct {
	keyEnc []byte
	pub    string
	saves  int
}

func (m *memKeyStore) GetRpcSigningKey(context.Context) ([]byte, string, error) {
	return m.keyEnc, m.pub, nil
}
func (m *memKeyStore) SaveRpcSigningKey(_ context.Context, k []byte, pub, _ string) error {
	m.keyEnc, m.pub, m.saves = k, pub, m.saves+1
	return nil
}

func testMaster() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

// loadOrCreateRpcSigner wraps the db backend into an RpcSigner, as the server does at startup when the backend is db.
func loadOrCreateRpcSigner(ctx context.Context, st RpcSigningKeyStore, master []byte) (*RpcSigner, error) {
	key, err := LoadOrCreateRpcKey(ctx, st, master)
	if err != nil {
		return nil, err
	}
	return NewRpcSigner(key)
}

// The first call generates and stores a key; the second loads the same one (same public key fingerprint) without writing to the DB again.
func TestLoadOrCreateRpcKeyPersists(t *testing.T) {
	st := &memKeyStore{}
	s1, err := loadOrCreateRpcSigner(context.Background(), st, testMaster())
	if err != nil {
		t.Fatal(err)
	}
	s2, err := loadOrCreateRpcSigner(context.Background(), st, testMaster())
	if err != nil {
		t.Fatal(err)
	}
	if st.saves != 1 {
		t.Fatalf("應只存一次,存了 %d 次", st.saves)
	}
	if s1.Digest() == "" || s1.Digest() != s2.Digest() {
		t.Fatalf("兩次載入指紋不同: %s vs %s", s1.Digest(), s2.Digest())
	}
	// A changed master key cannot decrypt: this is the safeguard behind "changing CA_MASTER_KEY replaces every signature", not a bug.
	other := testMaster()
	other[0] ^= 0xff
	if _, err := loadOrCreateRpcSigner(context.Background(), st, other); err == nil {
		t.Fatal("不同 master 應解密失敗")
	}
}

// A signed token must verify with the public key (the very PEM given to nodes) and carry permissions and exp;
// this is exactly the order of Besu's checks.
func TestSignVerifiableWithPublicKey(t *testing.T) {
	s, err := loadOrCreateRpcSigner(context.Background(), &memKeyStore{}, testMaster())
	if err != nil {
		t.Fatal(err)
	}
	perms := []string{"eth:*", "admin:peers"}
	tok, id, exp, err := s.Sign("reader", perms, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(exp); d < 59*time.Minute || d > 61*time.Minute {
		t.Fatalf("exp 不在一小時左右: %v", d)
	}

	block, _ := pem.Decode([]byte(s.PublicKeyPEM()))
	pubAny, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pub := pubAny.(*rsa.PublicKey)
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(tok, claims, func(tk *jwt.Token) (any, error) { return pub, nil },
		jwt.WithValidMethods([]string{RpcJwtAlgorithm}), jwt.WithExpirationRequired())
	if err != nil || !parsed.Valid {
		t.Fatalf("公鑰驗簽失敗: %v", err)
	}
	got, _ := claims["permissions"].([]any)
	if len(got) != 2 || got[0] != "eth:*" || got[1] != "admin:peers" {
		t.Fatalf("permissions claim 不符: %v", claims["permissions"])
	}
	if e, _ := claims["exp"].(float64); !exp.Equal(time.Unix(int64(e), 0)) {
		t.Fatalf("回傳的 exp 要跟 claim 一致(到秒): %v / %v", exp, claims["exp"])
	}
	if _, hasAud := claims["aud"]; hasAud || claims["sub"] != "reader" {
		t.Fatalf("不該帶 aud、sub 要是使用者名稱: %v / %v", claims["aud"], claims["sub"])
	}
	if claims["jti"] != id || !regexp.MustCompile(`^tk_[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("jti 要是回傳的代號(tk_ + 12 碼 hex): %v / %q", claims["jti"], id)
	}
	if _, id2, _, _ := s.Sign("reader", perms, time.Hour); id2 == id {
		t.Fatal("兩次簽發的代號不該相同")
	}
	sum := sha256.Sum256(block.Bytes)
	if s.Digest() != hex.EncodeToString(sum[:]) {
		t.Fatal("Digest 不是公鑰 DER 的 sha256")
	}
	if _, _, _, err := s.Sign("reader", perms, 0); err == nil {
		t.Fatal("ttl=0 應拒絕")
	}
}

// opaqueSigner exposes only the crypto.Signer interface, simulating Vault / HSM, where the platform cannot get a *rsa.PrivateKey.
type opaqueSigner struct {
	key crypto.Signer
	pub crypto.PublicKey // can deliberately be set to the wrong public key
}

func (o opaqueSigner) Public() crypto.PublicKey { return o.pub }
func (o opaqueSigner) Sign(r io.Reader, d []byte, opts crypto.SignerOpts) ([]byte, error) {
	return o.key.Sign(r, d, opts)
}

func newRSA(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// A backend that provides only a crypto.Signer can still sign tokens Besu accepts; the public key is derived from the Signer.
func TestSignWithOpaqueSigner(t *testing.T) {
	k := newRSA(t)
	s, err := NewRpcSigner(opaqueSigner{key: k, pub: &k.PublicKey})
	if err != nil {
		t.Fatal(err)
	}
	tok, _, _, err := s.Sign("dapp", []string{"eth:*"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jwt.Parse(tok, func(*jwt.Token) (any, error) { return &k.PublicKey, nil },
		jwt.WithValidMethods([]string{RpcJwtAlgorithm}), jwt.WithExpirationRequired())
	if err != nil || !parsed.Valid {
		t.Fatalf("驗簽失敗: %v", err)
	}
	if parsed.Header["typ"] != "JWT" {
		t.Errorf("header typ 應為 JWT: %v", parsed.Header)
	}
}

// Self-check: if what the backend signs does not match its claimed public key (wrong parameters, wrong key) -> fail at startup; non-RSA -> fail.
func TestNewRpcSignerSelfTest(t *testing.T) {
	k, other := newRSA(t), newRSA(t)
	if _, err := NewRpcSigner(opaqueSigner{key: k, pub: &other.PublicKey}); err == nil {
		t.Error("簽章對不上公鑰應失敗")
	}
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if _, err := NewRpcSigner(ec); err == nil {
		t.Error("非 RSA 應失敗")
	}
}

// An unknown name fails instead of silently falling back to db; a known one opens normally.
func TestOpenRpcSigner(t *testing.T) {
	k := newRSA(t)
	backends := map[string]RpcKeyBackend{"test-mem": func(context.Context) (crypto.Signer, error) { return k, nil }}
	if _, err := OpenRpcSigner(context.Background(), "vault-not-registered", backends); err == nil {
		t.Error("未註冊的名稱應失敗")
	}
	s, err := OpenRpcSigner(context.Background(), "test-mem", backends)
	if err != nil {
		t.Fatal(err)
	}
	if s.PublicKeyPEM() == "" || s.Digest() == "" {
		t.Error("公鑰與指紋應從 Signer 算出")
	}
}
