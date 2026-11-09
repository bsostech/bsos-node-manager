package alerting

import (
	"context"
	"strings"
	"testing"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
)

// These two functions carry security-related dry-run guarantees (no mail sent, no real webhook called) + SSRF protection, worth pinning down with tests.

func TestWebhookConnectivityRejectsBadURL(t *testing.T) {
	cases := []string{"", "not a url", "ftp://example.com", "https://"}
	for _, u := range cases {
		if err := TestWebhookConnectivity(context.Background(), u); err == nil {
			t.Errorf("TestWebhookConnectivity(%q) = nil, want error", u)
		}
	}
}

func TestWebhookConnectivityRejectsLoopback(t *testing.T) {
	err := TestWebhookConnectivity(context.Background(), "http://127.0.0.1:65535/hook")
	if err == nil {
		t.Fatal("expected loopback target to be rejected")
	}
	if !strings.Contains(err.Error(), "loopback/link-local") {
		t.Errorf("error = %q, want it to mention the loopback/link-local guard", err.Error())
	}
	if r, p, _ := apierr.ReasonOf(err); r != api.ErrorReasonConnAddressBlocked || p["address"] != "127.0.0.1" {
		t.Errorf("reason %q params %v, want conn_address_blocked with the address", r, p)
	}
}

func TestWebhookConnectivityAllowsPrivateRange(t *testing.T) {
	// 10.x/172.16.x/192.168.x must not be blocked by safeDialTCP at the resolve stage: this tool is deployed on-premises and internal relays are a normal setup.
	// The connection itself will fail (nothing listens at this address), but the error must be a timeout/refusal, not a range rejection.
	err := TestWebhookConnectivity(context.Background(), "http://10.255.255.1:65535/hook")
	if err == nil {
		t.Fatal("expected connection to an unreachable address to fail")
	}
	if strings.Contains(err.Error(), "reserved loopback") {
		t.Errorf("private-range target was rejected by the SSRF guard, want it to only fail at dial: %q", err.Error())
	}
}

func TestSMTPRejectsLoopback(t *testing.T) {
	err := TestSMTP(context.Background(), SMTP{Host: "127.0.0.1", Port: 25}, nil)
	if err == nil {
		t.Fatal("expected loopback target to be rejected")
	}
	if !strings.Contains(err.Error(), "loopback/link-local") {
		t.Errorf("error = %q, want it to mention the loopback/link-local guard", err.Error())
	}
	if r, p, _ := apierr.ReasonOf(err); r != api.ErrorReasonConnAddressBlocked || p["address"] != "127.0.0.1" {
		t.Errorf("reason %q params %v, want conn_address_blocked with the address", r, p)
	}
}
