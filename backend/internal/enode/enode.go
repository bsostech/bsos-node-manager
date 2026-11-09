// Package enode parses Besu/Ethereum enode URLs and derives the validator address from their public key.
//
// An enode holds the uncompressed secp256k1 public key, and the validator address is exactly the last 20 bytes of its keccak256.
// The mapping can be computed locally with a plain hash: no curve arithmetic, and no need to trust anyone else's claimed mapping.
// The platform uses it to decide whether a connected peer is an on-chain validator (admin_peers public keys are proven by the RLPx handshake; see peerobs.go).
package enode

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/crypto/sha3"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
)

// pubKeyHexLen: an uncompressed secp256k1 public key without the 0x04 prefix is 64 bytes = 128 hex characters.
const pubKeyHexLen = 128

// ParseError is a malformed enode. Reason says which rule failed (the API response reason), Params holds the values in that sentence; Error() is the English sentence.
type ParseError struct {
	Reason api.ErrorReason
	Params map[string]string
}

func (e *ParseError) Error() string { return apierr.Message(e.Reason, e.Params) }

func parseErr(r api.ErrorReason, kv ...string) error {
	p := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		p[kv[i]] = kv[i+1]
	}
	return &ParseError{Reason: r, Params: p}
}

// PubKeyHex extracts the public key from an enode URL (lowercase hex, no 0x).
// Accepted shape: enode://<128 hex>@host:port[?discport=N]; case-insensitive.
func PubKeyHex(rawEnode string) (string, error) {
	s := strings.TrimSpace(rawEnode)
	const scheme = "enode://"
	if !strings.HasPrefix(strings.ToLower(s), scheme) {
		return "", parseErr(api.ErrorReasonEnodeScheme, "value", strconv.Quote(rawEnode))
	}
	s = s[len(scheme):]
	at := strings.IndexByte(s, '@')
	if at < 0 {
		return "", parseErr(api.ErrorReasonEnodeNoHost, "value", strconv.Quote(rawEnode))
	}
	pub := strings.ToLower(s[:at])
	if len(pub) != pubKeyHexLen {
		return "", parseErr(api.ErrorReasonEnodePubkeyLength, "got", strconv.Itoa(len(pub)), "value", strconv.Quote(rawEnode))
	}
	if _, err := hex.DecodeString(pub); err != nil {
		return "", parseErr(api.ErrorReasonEnodePubkeyHex, "value", strconv.Quote(rawEnode))
	}
	return pub, nil
}

// secpP is the field prime of secp256k1, whose curve is y² = x³ + 7 (mod p).
var secpP, _ = new(big.Int).SetString("fffffffffffffffffffffffffffffffffffffffffffffffffffffffefffffc2f", 16)

// OnCurvePubKeyHex is PubKeyHex plus a check that the key is a point on secp256k1. Use it for enodes a user
// types in: Besu will not dial a key that is not ("Invalid point coordinates"), so a mistyped key would
// otherwise be stored as a peer that never connects. Keys Besu itself reports are valid by construction.
func OnCurvePubKeyHex(rawEnode string) (string, error) {
	pub, err := PubKeyHex(rawEnode)
	if err == nil && !onCurve(pub) {
		return "", parseErr(api.ErrorReasonEnodePubkeyInvalid, "value", strconv.Quote(rawEnode))
	}
	return pub, err
}

// onCurve reports whether pub (128 hex characters, as PubKeyHex returns it) is a point on secp256k1.
func onCurve(pub string) bool {
	b, err := hex.DecodeString(pub)
	if err != nil || len(b) != pubKeyHexLen/2 {
		return false
	}
	x, y := new(big.Int).SetBytes(b[:32]), new(big.Int).SetBytes(b[32:])
	if x.Cmp(secpP) >= 0 || y.Cmp(secpP) >= 0 {
		return false
	}
	lhs := new(big.Int).Mul(y, y)
	rhs := new(big.Int).Mul(x, x)
	rhs.Mul(rhs, x).Add(rhs, big.NewInt(7))
	return lhs.Mod(lhs, secpP).Cmp(rhs.Mod(rhs, secpP)) == 0
}

// Port returns the enode's TCP port (the P2P listen port; ?discport= is the UDP discovery port and does not count). Returns an error if it cannot parse or the port is invalid.
func Port(rawEnode string) (int, error) {
	u, err := url.Parse(strings.TrimSpace(rawEnode))
	if err != nil {
		return 0, err
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("invalid enode port: %q", rawEnode)
	}
	return p, nil
}

// Address derives the validator address from the enode's public key (0x + 40 lowercase hex characters).
// address = keccak256(pubkey 64 bytes)[12:32], matching Besu's `public-key export-address`.
func Address(rawEnode string) (string, error) {
	pub, err := PubKeyHex(rawEnode)
	if err != nil {
		return "", err
	}
	return AddressFromPubKey(pub)
}

// AddressFromPubKey derives the address from a 128-hex-character public key (case-insensitive); returns an error on wrong length or characters.
func AddressFromPubKey(pub string) (string, error) {
	if len(pub) != pubKeyHexLen {
		return "", fmt.Errorf("public key must be %d hex characters, got %d", pubKeyHexLen, len(pub))
	}
	raw, err := hex.DecodeString(pub)
	if err != nil {
		return "", fmt.Errorf("public key is not valid hex")
	}
	h := sha3.NewLegacyKeccak256()
	h.Write(raw)
	return "0x" + hex.EncodeToString(h.Sum(nil)[12:]), nil
}

// ValidHostPort checks the host:port other nodes will dial. The host must be an IP literal (Besu accepts hostnames in an enode only with the experimental --Xdns-enabled flag, which the platform does not support),
// and it must not be an address other nodes cannot reach: unspecified (0.0.0.0 / ::), loopback, multicast.
func ValidHostPort(hostPort string) error {
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return parseErr(api.ErrorReasonEnodeHostPort, "detail", err.Error())
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return parseErr(api.ErrorReasonEnodeHostNotIp, "value", strconv.Quote(host))
	}
	if ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() {
		return parseErr(api.ErrorReasonEnodeHostUnroutable, "value", strconv.Quote(host))
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return parseErr(api.ErrorReasonEnodePortRange, "value", strconv.Quote(port))
	}
	return nil
}

// NormalizeAddress normalizes addresses for comparison (trim whitespace, drop 0x, lowercase).
// On-chain reports and local records do not necessarily agree on case and prefix, so always pass through here before comparing.
func NormalizeAddress(addr string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(addr)), "0x")
}
