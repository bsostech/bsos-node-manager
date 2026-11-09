package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// At-rest encryption for connector secrets (LDAP bindPassword / OIDC clientSecret).
// It shares the master key (CA_MASTER_KEY) with the embedded CA private key, but derives a separate key with a different HKDF info,
// so the keys for the two purposes never overlap (domain separation). Blob format: salt(16)||nonce(12)||ciphertext.
const secretHKDFInfo = "bsos-auth-secret"

// EncryptSecret/DecryptSecret are exported so other packages can reuse the same "admin settings page secret at rest" scheme
// (currently: the SMTP password in internal/alerting, which is likewise a third-party integration secret on the platform settings page).
func EncryptSecret(master, plaintext []byte) ([]byte, error) { return encryptSecret(master, plaintext) }
func DecryptSecret(master, blob []byte) ([]byte, error)      { return decryptSecret(master, blob) }

func encryptSecret(master, plaintext []byte) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	gcm, err := secretGCM(master, salt)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(append(salt, nonce...), gcm.Seal(nil, nonce, plaintext, nil)...), nil
}

func decryptSecret(master, blob []byte) ([]byte, error) {
	if len(blob) < 16+12 {
		return nil, fmt.Errorf("encrypted secret blob too short")
	}
	salt, rest := blob[:16], blob[16:]
	gcm, err := secretGCM(master, salt)
	if err != nil {
		return nil, err
	}
	nonce, ct := rest[:gcm.NonceSize()], rest[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}

func secretGCM(master, salt []byte) (cipher.AEAD, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, master, salt, []byte(secretHKDFInfo)), key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
