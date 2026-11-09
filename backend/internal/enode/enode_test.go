package enode

import (
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"testing"

	"besu-manager/backend/internal/api"
)

// A valid public-key shape of 128 hex characters (content arbitrary; derivation is just a hash).
// Built with Repeat rather than typed by hand: hand-typing easily adds or drops a few characters, and length is the only shape requirement here.
var testPub = strings.Repeat("ab3f6c1d2e4b5a6978c0d1e2f3041526", 4)

// Keccak-256 and SHA3-256 differ only in padding; using the wrong one makes every derived address wrong while the shape looks perfectly normal (still 40 hex characters).
// Pin it with an externally sourced answer: the public key and address below were both produced by Besu
// for a test node (its key.pub and address files).
func TestAddressKnownVector(t *testing.T) {
	const a1 = "enode://98f47e4807289e219b5a7ed048d6eeefd35431f7793d9b8fc7777bb32fa0e849c9d7d4d3d33884db2a20c927b333f06b03c7781e3cb50a433b4e6d3f4d1fb9ec@172.28.0.21:30303"
	if got, err := Address(a1); err != nil || got != "0x120cee331c15b3f6e129a80c1540167bd2f9a733" {
		t.Fatalf("Address(node-a1) = %q, %v", got, err)
	}
}

// Keys Besu produced (the test node above) and the secp256k1 generator are on the curve; changing one digit of y,
// all zeros, and coordinates at or above the field prime are not.
func TestOnCurve(t *testing.T) {
	if _, err := OnCurvePubKeyHex("enode://" + testPub + "@10.20.1.11:30303"); err == nil {
		t.Fatal("OnCurvePubKeyHex accepted a key off the curve")
	}
	const a1 = "98f47e4807289e219b5a7ed048d6eeefd35431f7793d9b8fc7777bb32fa0e849c9d7d4d3d33884db2a20c927b333f06b03c7781e3cb50a433b4e6d3f4d1fb9ec"
	const g = "79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798483ada7726a3c4655da4fbfc0e1108a8fd17b448a68554199c47d08ffb10d4b8"
	for _, c := range []struct {
		name, pub string
		want      bool
	}{
		{"node-a1", a1, true},
		{"generator", g, true},
		{"y off by one", g[:127] + "9", false},
		{"all zeros", strings.Repeat("0", 128), false},
		{"x at the prime", "fffffffffffffffffffffffffffffffffffffffffffffffffffffffefffffc2f" + g[64:], false},
		{"arbitrary hex", testPub, false},
		{"too short", g[:126], false},
	} {
		if got := onCurve(c.pub); got != c.want {
			t.Errorf("%s: onCurve = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPubKeyHex(t *testing.T) {
	good := "enode://" + testPub + "@10.20.1.11:30303"
	pub, err := PubKeyHex(good)
	if err != nil {
		t.Fatalf("合法 enode 不應報錯: %v", err)
	}
	if pub != testPub {
		t.Fatalf("公鑰取錯\n得 %s\nwant %s", pub, testPub)
	}

	// Case-insensitive, and a discport query parameter does not affect public key extraction.
	mixed := "ENODE://" + strings.ToUpper(testPub) + "@10.20.1.11:30303?discport=0"
	if pub, err := PubKeyHex(mixed); err != nil || pub != testPub {
		t.Fatalf("大寫 + discport 應可解析,得 (%q, %v)", pub, err)
	}
}

func TestPubKeyHexRejects(t *testing.T) {
	cases := []struct {
		name, in string
		want     api.ErrorReason
	}{
		{"缺前綴", testPub + "@10.20.1.11:30303", api.ErrorReasonEnodeScheme},
		{"缺 @", "enode://" + testPub, api.ErrorReasonEnodeNoHost},
		{"公鑰太短", "enode://abcd@10.20.1.11:30303", api.ErrorReasonEnodePubkeyLength},
		{"公鑰非十六進位", "enode://" + strings.Repeat("z", 128) + "@10.20.1.11:30303", api.ErrorReasonEnodePubkeyHex},
		{"空字串", "", api.ErrorReasonEnodeScheme},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := PubKeyHex(c.in)
			if err == nil {
				t.Fatalf("預期報錯,但通過")
			}
			var pe *ParseError
			if !errors.As(err, &pe) || pe.Reason != c.want {
				t.Fatalf("reason 應為 %s,得 %v", c.want, err)
			}
		})
	}
}

func TestAddressShape(t *testing.T) {
	addr, err := Address("enode://" + testPub + "@10.20.1.11:30303")
	if err != nil {
		t.Fatalf("不應報錯: %v", err)
	}
	if !strings.HasPrefix(addr, "0x") || len(addr) != 42 {
		t.Fatalf("地址形狀應為 0x + 40 個十六進位字元,得 %q", addr)
	}
	if _, err := hex.DecodeString(addr[2:]); err != nil {
		t.Fatalf("地址非合法十六進位: %q", addr)
	}
}

// Public keys differing by one character must derive different addresses; otherwise nothing is being verified.
func TestAddressIsPubKeyDependent(t *testing.T) {
	a, err := Address("enode://" + testPub + "@10.20.1.11:30303")
	if err != nil {
		t.Fatal(err)
	}
	altered := "b" + testPub[1:]
	b, err := Address("enode://" + altered + "@10.20.1.11:30303")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("不同公鑰推導出相同地址 %q,推導未真正使用公鑰", a)
	}
}

// For nodes at different locations, changing the port or IP does not affect derivation (identity is bound to the public key, not the address).
func TestAddressIgnoresHostPort(t *testing.T) {
	// Check the error on both sides: otherwise if both fail, a and b are both empty strings and the test "passes".
	a, err := Address("enode://" + testPub + "@10.20.1.11:30303")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Address("enode://" + testPub + "@203.0.113.9:31313")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("同一公鑰在不同位址應推導出相同地址,得 %q / %q", a, b)
	}
}

// The port is the one from host:port, not discport; missing or out-of-range ports return an error.
func TestPort(t *testing.T) {
	if p, err := Port("enode://" + testPub + "@10.0.0.5:30304?discport=30305"); err != nil || p != 30304 {
		t.Fatalf("p=%d err=%v", p, err)
	}
	if p, err := Port("enode://" + testPub + "@[::1]:30303"); err != nil || p != 30303 {
		t.Fatalf("IPv6: p=%d err=%v", p, err)
	}
	for _, want := range []int{1, 65535} {
		if p, err := Port("enode://" + testPub + "@10.0.0.5:" + strconv.Itoa(want)); err != nil || p != want {
			t.Errorf("埠 %d: p=%d err=%v", want, p, err)
		}
	}
	for _, bad := range []string{"enode://" + testPub + "@10.0.0.5", "enode://" + testPub + "@10.0.0.5:0", "enode://" + testPub + "@10.0.0.5:65536", "enode://" + testPub + "@10.0.0.5:70000"} {
		if _, err := Port(bad); err == nil {
			t.Errorf("%s 應回錯", bad)
		}
	}
}
