package auth

import "testing"

func TestSecretRoundTrip(t *testing.T) {
	master := []byte("0123456789abcdef0123456789abcdef")
	plain := []byte("s3cr3t-bind-password")
	blob, err := encryptSecret(master, plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if string(blob) == string(plain) {
		t.Fatal("ciphertext 等於明文")
	}
	got, err := decryptSecret(master, blob)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(got) != string(plain) {
		t.Fatalf("round-trip 不符:got %q", got)
	}
	// tampering with the ciphertext must make GCM verification fail
	blob[len(blob)-1] ^= 0xff
	if _, err := decryptSecret(master, blob); err == nil {
		t.Fatal("竄改後仍解密成功(GCM 未把關)")
	}
}
