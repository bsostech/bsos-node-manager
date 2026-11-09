package alerting

import (
	"bufio"
	"context"
	"encoding/base64"
	"net"
	"strings"
	"testing"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
)

// fakeSMTP: a minimal SMTP server that speaks just enough to run EHLO -> AUTH -> QUIT.
// Binding 127.0.0.1 would be blocked by safeDialTCP's loopback guard, so the test dials a real LAN address instead
// (outboundIP takes the address of this machine's outbound interface); this also proves the guard blocks only loopback, not private networks.
func fakeSMTP(t *testing.T, authLine string) (host string, port string) {
	t.Helper()
	ln, err := net.Listen("tcp", outboundIP(t)+":0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				br := bufio.NewReader(conn)
				w := func(s string) { conn.Write([]byte(s + "\r\n")) }
				w("220 fake ESMTP")
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					cmd := strings.ToUpper(strings.TrimSpace(line))
					switch {
					case strings.HasPrefix(cmd, "EHLO"):
						w("250-fake greets")
						w("250 " + authLine)
					case strings.HasPrefix(cmd, "AUTH LOGIN"):
						w("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")))
						br.ReadString('\n')
						w("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")))
						br.ReadString('\n')
						w("235 2.7.0 ok")
					case strings.HasPrefix(cmd, "QUIT"):
						w("221 bye")
						return
					default:
						w("250 ok")
					}
				}
			}()
		}
	}()

	h, p, _ := net.SplitHostPort(ln.Addr().String())
	return h, p
}

func outboundIP(t *testing.T) string {
	t.Helper()
	c, err := net.Dial("udp", "192.0.2.1:1") // TEST-NET-1; no packet is actually sent
	if err != nil {
		t.Skip("取不到對外介面位址,略過")
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String()
}

// Without encryption the outcome follows the server's mechanism order, the way Alertmanager picks: the
// first mechanism it knows wins. LOGIN first passes; PLAIN first fails even when LOGIN is listed too,
// because Go's plainAuth refuses to send credentials unencrypted and Alertmanager does not try the next one.
// Unknown mechanisms and CRAM-MD5 (no secret is ever configured) are skipped.
func TestSMTPAuthFollowsServerMechanismOrder(t *testing.T) {
	for _, tc := range []struct {
		authLine string
		ok       bool
	}{
		{"AUTH LOGIN PLAIN", true},
		{"AUTH CRAM-MD5 XOAUTH2 LOGIN PLAIN", true},
		{"AUTH PLAIN LOGIN", false},
		{"AUTH PLAIN", false},
		{"AUTH CRAM-MD5 PLAIN LOGIN", false},
	} {
		host, port := fakeSMTP(t, tc.authLine)
		err := TestSMTP(context.Background(), SMTP{Host: host, Port: atoi(t, port), Username: "u", Password: "p", Security: SecurityNone}, nil)
		if tc.ok {
			if err != nil {
				t.Errorf("%s: 應該通過,卻得到 %v", tc.authLine, err)
			}
			continue
		}
		mechs := strings.TrimPrefix(tc.authLine, "AUTH ")
		if r, p, _ := apierr.ReasonOf(err); r != api.ErrorReasonSmtpAuthLoginRequired || p["mechs"] != mechs {
			t.Errorf("%s: err %v reason %q params %v,應為 smtp_auth_login_required 帶 mechs", tc.authLine, err, r, p)
		}
	}
}

// Username without a password: Alertmanager skips PLAIN and LOGIN and never logs in, so the test must not pass either.
func TestSMTPAuthEmptyPassword(t *testing.T) {
	host, port := fakeSMTP(t, "AUTH LOGIN PLAIN")
	err := TestSMTP(context.Background(), SMTP{Host: host, Port: atoi(t, port), Username: "u", Security: SecurityNone}, nil)
	if r, _, _ := apierr.ReasonOf(err); r != api.ErrorReasonSmtpPasswordRequired {
		t.Fatalf("err %v reason %q,應為 smtp_password_required", err, r)
	}
}

// Server advertises no AUTH at all but the user filled in an account -> also reject, and suggest leaving it blank.
func TestSMTPAuthRejectsWhenServerHasNoAuth(t *testing.T) {
	host, port := fakeSMTP(t, "SIZE 1000000")
	err := TestSMTP(context.Background(), SMTP{Host: host, Port: atoi(t, port), Username: "u", Password: "p"}, nil)
	if err == nil {
		t.Fatal("不支援 AUTH 卻填了帳號,應該被擋下")
	}
	if !strings.Contains(err.Error(), "leave the username empty") {
		t.Errorf("錯誤訊息應該建議把帳號留空,實際:%v", err)
	}
	if r, _, _ := apierr.ReasonOf(err); r != api.ErrorReasonSmtpAuthUnsupported {
		t.Errorf("reason %q,應為 smtp_auth_unsupported", r)
	}
}

// No account -> AUTH is never touched, so any server should pass (the norm for internal relays).
func TestSMTPNoAuthWhenUsernameEmpty(t *testing.T) {
	host, port := fakeSMTP(t, "SIZE 1000000")
	if err := TestSMTP(context.Background(), SMTP{Host: host, Port: atoi(t, port)}, nil); err != nil {
		t.Fatalf("沒填帳號時不該碰 AUTH,卻得到:%v", err)
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// Host does not resolve: the dial check returns conn_resolve_failed with the host in params (.invalid is guaranteed not to resolve).
func TestSMTPResolveFailureReason(t *testing.T) {
	err := TestSMTP(context.Background(), SMTP{Host: "nope.invalid", Port: 25}, nil)
	if r, p, _ := apierr.ReasonOf(err); r != api.ErrorReasonConnResolveFailed || p["host"] != "nope.invalid" || p["detail"] == "" {
		t.Errorf("reason %q params %v,應為 conn_resolve_failed 帶 host、detail", r, p)
	}
}
