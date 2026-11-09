package auth

import (
	"errors"
	"fmt"
	"testing"

	"besu-manager/backend/internal/auth/authtest"
)

func TestLogLevelRequiresNodeOperator(t *testing.T) {
	permission := permOf(t, authtest.Load(t, coreSpec), "PUT", "/api/v1/nodes/example/log-level")
	if permission != PermNodesOnboard {
		t.Fatalf("log-level permission=%q", permission)
	}
	if roleCan("viewer", permission) || roleCan("", permission) {
		t.Fatal("viewer or unauthenticated role can change log level")
	}
	if !roleCan("operator", permission) {
		t.Fatal("operator cannot change log level")
	}
}

// The reason a login failed is kept for the login log; externally it is still ErrBadCredentials (recognized even when wrapped).
func TestLoginFailureIsBadCredentials(t *testing.T) {
	for _, r := range []string{FailBadPassword, FailUnknownUser, FailDisabled, FailNoRole} {
		err := fmt.Errorf("wrapped: %w", &LoginFailure{Reason: r, Username: "carol"})
		var f *LoginFailure
		if !errors.Is(err, ErrBadCredentials) || !errors.As(err, &f) || f.Reason != r {
			t.Fatalf("%s: Is=%v As=%v", r, errors.Is(err, ErrBadCredentials), f)
		}
	}
}
