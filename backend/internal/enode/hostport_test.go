package enode

import (
	"errors"
	"testing"

	"besu-manager/backend/internal/api"
)

func TestValidHostPort(t *testing.T) {
	for bad, want := range map[string]api.ErrorReason{
		"203.0.113.7":        api.ErrorReasonEnodeHostPort,
		"":                   api.ErrorReasonEnodeHostPort,
		"node.example:30303": api.ErrorReasonEnodeHostNotIp,
		"203.0.113.7:0":      api.ErrorReasonEnodePortRange,
		"203.0.113.7:70000":  api.ErrorReasonEnodePortRange,
		"203.0.113.7:65536":  api.ErrorReasonEnodePortRange,
		"0.0.0.0:30303":      api.ErrorReasonEnodeHostUnroutable,
		"127.0.0.1:30303":    api.ErrorReasonEnodeHostUnroutable,
		"[::]:30303":         api.ErrorReasonEnodeHostUnroutable,
		"224.0.0.1:30303":    api.ErrorReasonEnodeHostUnroutable,
	} {
		var pe *ParseError
		if err := ValidHostPort(bad); !errors.As(err, &pe) || pe.Reason != want {
			t.Errorf("%q 應被拒且 reason 為 %s,得 %v", bad, want, err)
		}
	}
	// the trailing %q of the original Chinese sentence goes into params.value
	var pe *ParseError
	if errors.As(ValidHostPort("node.example:30303"), &pe); pe.Params["value"] != `"node.example"` {
		t.Errorf("params.value 應為帶引號的主機,得 %#v", pe.Params)
	}
	for _, ok := range []string{"203.0.113.7:30303", "[2001:db8::1]:30303", "203.0.113.7:1", "203.0.113.7:65535"} {
		if err := ValidHostPort(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}
