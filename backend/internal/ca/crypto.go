package ca

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"

	"crypto/sha256"
)

// encryptKey encrypts with AES-256-GCM under a key derived from the master key (HKDF-SHA256).
// Blob format: salt(16) || nonce(12) || ciphertext. Every private key written to storage goes through here.
func encryptKey(master, plaintext []byte) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	gcm, err := newGCM(master, salt)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	blob := append(append(salt, nonce...), gcm.Seal(nil, nonce, plaintext, nil)...)
	return blob, nil
}

func decryptKey(master, blob []byte) ([]byte, error) {
	if len(blob) < 16+12 {
		return nil, fmt.Errorf("encrypted key blob too short")
	}
	salt, rest := blob[:16], blob[16:]
	gcm, err := newGCM(master, salt)
	if err != nil {
		return nil, err
	}
	nonce, ct := rest[:gcm.NonceSize()], rest[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}

func newGCM(master, salt []byte) (cipher.AEAD, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, master, salt, []byte("bsos-ca-key")), key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
