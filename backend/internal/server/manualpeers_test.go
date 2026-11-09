package server

import (
	"errors"
	"strings"
	"testing"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/enode"
)

func TestParseManualEnode(t *testing.T) {
	// The secp256k1 generator, mixed case: parsing lowercases it.
	pub := "79BE667EF9DCBBAC55A06295CE870B07029BFCDB2DCE28D959F2815B16F81798483ada7726a3c4655da4fbfc0e1108a8fd17b448a68554199c47d08ffb10d4b8"
	en, got, err := parseManualEnode("  enode://" + pub + "@10.0.0.9:30303 ")
	if err != nil || got != strings.ToLower(pub) || en != "enode://"+strings.ToLower(pub)+"@10.0.0.9:30303" {
		t.Fatalf("en=%q pub=%q err=%v", en, got, err)
	}
	// Deployments whose discovery port differs from the TCP port: discport must be kept
	if en, _, err := parseManualEnode("enode://" + pub + "@10.0.0.9:30303?discport=30301"); err != nil || !strings.HasSuffix(en, "@10.0.0.9:30303?discport=30301") {
		t.Fatalf("discport: en=%q err=%v", en, err)
	}
	// discport=0 means the other side has discovery off (Besu's notation) and must be kept too;
	// leading zeros are normalized
	if en, _, err := parseManualEnode("enode://" + pub + "@10.0.0.9:30303?discport=00"); err != nil || !strings.HasSuffix(en, "?discport=0") {
		t.Fatalf("discport=0: en=%q err=%v", en, err)
	}
	// discport up to 65535 is accepted; 65536 returns enode_discport
	if en, _, err := parseManualEnode("enode://" + pub + "@10.0.0.9:30303?discport=65535"); err != nil || !strings.HasSuffix(en, "?discport=65535") {
		t.Fatalf("discport=65535: en=%q err=%v", en, err)
	}
	var pe *enode.ParseError
	if _, _, err := parseManualEnode("enode://" + pub + "@10.0.0.9:30303?discport=65536"); !errors.As(err, &pe) || pe.Reason != api.ErrorReasonEnodeDiscport {
		t.Fatalf("discport=65536 應 enode_discport:%v", err)
	}
	// 128 hex characters but not a point on secp256k1: Besu would never dial it
	if _, _, err := parseManualEnode("enode://" + strings.Repeat("ab", 64) + "@10.0.0.9:30303"); !errors.As(err, &pe) || pe.Reason != api.ErrorReasonEnodePubkeyInvalid {
		t.Fatalf("off-curve key should be enode_pubkey_invalid: %v", err)
	}
	for _, bad := range []string{
		"enode://" + pub + "@node-b2.example:30303", // Besu does not accept host names
		"enode://" + pub + "@127.0.0.1:30303",
		"enode://" + pub + "@10.0.0.9",
		"enode://abcd@10.0.0.9:30303",
		"10.0.0.9:30303",
		"enode://" + pub + "@10.0.0.9:30303?discport=abc",
		"enode://" + pub + "@10.0.0.9:30303?discport=70000",
		"enode://" + pub + "@10.0.0.9:30303?foo=1",
		"enode://" + pub + "@10.0.0.9:30303?discport=+5",     // Besu accepts digits only
		"enode://" + pub + "@10.0.0.9:30303?discport=030301", // more than 5 digits
		"enode://" + pub + "@10.0.0.9:30303?discport=1&x=2",
	} {
		if _, _, err := parseManualEnode(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
