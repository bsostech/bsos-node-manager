package auth

import (
	"errors"
	"strings"
	"testing"

	"besu-manager/backend/internal/api"
)

func TestValidatePassword(t *testing.T) {
	for _, tc := range []struct {
		user, pw string
		want     api.ErrorReason
	}{
		{"alice", "short-pass", api.ErrorReasonPasswordTooShort},
		{"alice", strings.Repeat("密", 25), api.ErrorReasonPasswordTooLong},      // 25 chars = 75 bytes, over bcrypt's 72
		{"alice", "my-ALICE-password", api.ErrorReasonPasswordContainsUsername}, // case-insensitive
		{"alice", "1QAZ2WSX3EDC", api.ErrorReasonPasswordCommon},                // blocklist, case-insensitive
		{"alice", "correct horse battery", ""},
		{"alice", strings.Repeat("密", 24), ""}, // 24 chars = 72 bytes, exactly the limit
	} {
		err := ValidatePassword(tc.user, tc.pw)
		var pe *PasswordError
		if tc.want == "" {
			if err != nil {
				t.Errorf("%q: unexpected %v", tc.pw, err)
			}
			continue
		}
		if !errors.As(err, &pe) || pe.Reason != tc.want {
			t.Errorf("%q: got %v, want %q", tc.pw, err, tc.want)
		}
	}
	if len(commonPasswords) < 400 {
		t.Fatalf("黑名單只載入 %d 筆", len(commonPasswords))
	}
}

func TestGeneratePassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		pw := GeneratePassword("admin")
		if len(pw) != 16 || ValidatePassword("admin", pw) != nil || seen[pw] {
			t.Fatalf("%q", pw)
		}
		seen[pw] = true
	}
}
