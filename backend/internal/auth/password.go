package auth

import (
	"bufio"
	"context"
	"crypto/rand"
	_ "embed"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode/utf8"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/store"
)

// Password rules for local accounts (NIST SP 800-63B: the focus is length and a common-password blocklist; no composition rules or forced periodic changes).

const (
	passwordMinRunes = 12
	passwordMaxBytes = 72 // bcrypt only looks at the first 72 bytes and silently ignores the rest, so reject instead of truncating
)

//go:embed commonpasswords.txt
var commonPasswordsTxt string

var commonPasswords = func() map[string]bool {
	m := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(commonPasswordsTxt))
	for sc.Scan() {
		if w := strings.TrimSpace(sc.Text()); w != "" && !strings.HasPrefix(w, "#") {
			m[w] = true
		}
	}
	return m
}()

// PasswordError means the password breaks a rule; Reason says which one (the reason in the API response), and Error() is an English sentence (for logs).
type PasswordError struct{ Reason api.ErrorReason }

func (e *PasswordError) Error() string { return apierr.Message(e.Reason, nil) }

// ValidatePassword returns nil if the password satisfies the rules; otherwise it returns a *PasswordError naming the rule.
func ValidatePassword(username, pw string) error {
	switch lower := strings.ToLower(pw); {
	case utf8.RuneCountInString(pw) < passwordMinRunes:
		return &PasswordError{api.ErrorReasonPasswordTooShort}
	case len(pw) > passwordMaxBytes:
		return &PasswordError{api.ErrorReasonPasswordTooLong}
	case username != "" && strings.Contains(lower, strings.ToLower(username)):
		return &PasswordError{api.ErrorReasonPasswordContainsUsername}
	case commonPasswords[lower]:
		return &PasswordError{api.ErrorReasonPasswordCommon}
	}
	return nil
}

const generatedAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789" // drops the easily confused 0/O and 1/l/I

// GeneratePassword returns a system-generated temporary password (16 characters, about 93 bits) that always passes ValidatePassword.
func GeneratePassword(username string) string {
	for {
		b := make([]byte, 16)
		for i := range b {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(generatedAlphabet))))
			if err != nil {
				panic(err) // a crypto/rand failure means the system randomness source is broken; there is no acceptable fallback
			}
			b[i] = generatedAlphabet[n.Int64()]
		}
		if pw := string(b); ValidatePassword(username, pw) == nil {
			return pw
		}
	}
}

// ChangePassword changes the caller's own local password (the forced first-login change goes through here too). Non-local (SSO) accounts cannot change it.
// The new password must differ from the current one and is checked with ValidatePassword (a failure returns *PasswordError). Afterwards every other session of this account is logged out;
// the current one (currentToken) is kept and its "password change only" restriction is lifted. auditDetail is the success audit entry, written in the same transaction as the password change.
func (s *Service) ChangePassword(ctx context.Context, username, currentToken, current, next string, auditDetail map[string]any) error {
	u, err := s.store.GetLocalUser(ctx, username)
	if err != nil {
		return err
	}
	if u == nil {
		return ErrNotLocalUser
	}
	if bcryptCompare(u.PasswordHash, current) != nil {
		return ErrWrongPassword
	}
	// Reusing the current password would let the forced change after create or reset keep the temporary password the admin saw
	if next == current {
		return &PasswordError{api.ErrorReasonPasswordUnchanged}
	}
	if err := ValidatePassword(username, next); err != nil {
		return err
	}
	hash, err := bcryptHash(next)
	if err != nil {
		return err
	}
	err = s.store.ChangeOwnPassword(ctx, username, hash, currentToken, auditDetail)
	if errors.Is(err, store.ErrLocalUserNotFound) {
		return ErrNotLocalUser
	}
	return err
}

// HashPassword is for user management (create, reset).
func HashPassword(pw string) (string, error) { return bcryptHash(pw) }

var (
	ErrNotLocalUser  = fmt.Errorf("not a local account")
	ErrWrongPassword = fmt.Errorf("current password is wrong")
)
