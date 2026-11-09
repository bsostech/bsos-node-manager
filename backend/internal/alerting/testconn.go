package alerting

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
)

// safeDialTCP resolves all IPs of host and refuses the connection if any is loopback/link-local (blocking cloud metadata
// endpoints such as 169.254.169.254, and localhost); once checked it dials the resolved IP directly (not the hostname again), avoiding
// the DNS rebinding TOCTOU window. RFC1918 private ranges (10.x/172.16.x/192.168.x) are deliberately allowed: this tool is deployed on-premises,
// and SMTP/webhook relays on private networks are a normal setup (the UI's host placeholder is smtp.org-a.internal);
// callers are also operators with nodes:onboard/members:manage permission, not anonymous input.
func safeDialTCP(ctx context.Context, host, port string) (net.Conn, error) {
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, apierr.Wrap(api.ErrorReasonConnResolveFailed, map[string]string{"host": host, "detail": err.Error()}, fmt.Errorf("resolve host %s: %w", host, err))
	}
	var target net.IP
	for _, a := range ips {
		ip := a.IP
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return nil, apierr.Wrap(api.ErrorReasonConnAddressBlocked, map[string]string{"address": ip.String()}, fmt.Errorf("target address %s is in a reserved loopback/link-local range, connection blocked", ip))
		}
		if target == nil {
			target = ip
		}
	}
	if target == nil {
		return nil, apierr.Wrap(api.ErrorReasonConnNoAddress, map[string]string{"host": host}, fmt.Errorf("no address found for %s", host))
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(target.String(), port))
}

// TestSMTP: only connects + (with credentials) authenticates, never sends: testing settings should not actually bother recipients.
// In order: TCP dial (with timeout) -> EHLO -> encrypt per s.Security -> AUTH if credentials are set -> QUIT, the same path
// Alertmanager takes before it sends. MAIL/RCPT/DATA are deliberately never issued, so the whole process never produces a single mail.
// ca is the trust Alertmanager gets through ca_file (nil = system store).
func TestSMTP(ctx context.Context, s SMTP, ca *SMTPCA) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return CheckMail(ctx, MailConfig{SMTP: s, RootCAs: ca.Roots()})
}

// loginAuth: the SMTP LOGIN mechanism (not an RFC standard but widely supported: the server sends "Username:"/"Password:" challenges
// and the client answers each). Alertmanager uses it when LOGIN is the first mechanism it knows in the server's list.
type loginAuth struct{ username, password string }

func (a loginAuth) Start(_ *smtp.ServerInfo) (string, []byte, error) {
	return "LOGIN", nil, nil
}

func (a loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	// Server challenge strings have no standard wording (commonly "Username:" / "Password:", with case and colon not guaranteed),
	// so compare lowercased and look for the keyword.
	switch p := strings.ToLower(string(fromServer)); {
	case strings.Contains(p, "user"):
		return []byte(a.username), nil
	case strings.Contains(p, "pass"):
		return []byte(a.password), nil
	default:
		return nil, fmt.Errorf("SMTP server sent an unrecognized LOGIN challenge: %q", fromServer)
	}
}

// TestWebhookConnectivity: only TCP dial + (for https) TLS handshake; no HTTP request is sent.
// Alertmanager actually sends its own webhook JSON schema, not Slack's {text:...} format, and what the receiving server
// does with the content (whether it actually pushes anything) is outside our control. Stopping at the connection layer is what guarantees this test
// never triggers a visible action on any receiver (Slack/Teams/custom endpoints).
func TestWebhookConnectivity(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return apierr.Wrap(api.ErrorReasonWebhookTestUrlInvalid, nil, errors.New("invalid URL: must be a full http(s):// URL"))
	}

	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	conn, err := safeDialTCP(ctx, u.Hostname(), port)
	if err != nil {
		return err
	}
	defer conn.Close()

	if u.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return apierr.Wrap(api.ErrorReasonTlsHandshakeFailed, map[string]string{"detail": err.Error()}, fmt.Errorf("TLS handshake: %w", err))
		}
	}
	return nil
}

// testMessage: content of the test send. The Telegram and Slack tests really send a message; that is exactly what needs confirming (the bot is in the group,
// the webhook points at the right channel). Unlike the webhook test, which only tests connectivity, these two channels have a fixed format, so we know what arrives.
// Sent in the platform's notification language, like real alerts.
var testMessage = Text{"BSOS 節點治理工具測試訊息:此通知管道設定完成。", "BSOS Node Manager test message: this notification channel is set up."}

// telegramAPI: Telegram Bot API base URL (replaced by a fake server in tests).
var telegramAPI = "https://api.telegram.org"

// safeHTTPClient: dials through safeDialTCP (blocking loopback and link-local), does not follow redirects, 8s timeout.
func safeHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			return safeDialTCP(ctx, host, port)
		}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// netErr: strips the URL from http errors before returning them to the operator: the Telegram URL contains the bot token, and the Slack webhook URL is itself the credential.
func netErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// TestTelegram: getMe to check the token, then send one test message to the chat. Errors distinguish token, chat and network problems.
func TestTelegram(ctx context.Context, t Telegram, lang api.NotifyLanguage) error {
	if t.BotToken == "" || t.ChatID == 0 {
		return fmt.Errorf("bot token and chat id are both required")
	}
	hc := &http.Client{Timeout: 8 * time.Second} // the address is always Telegram, so safeDial is not needed
	call := func(method string, body any) (int, string, error) {
		b, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, telegramAPI+"/bot"+t.BotToken+"/"+method, bytes.NewReader(b))
		if err != nil {
			return 0, "", apierr.Wrap(api.ErrorReasonTelegramTokenFormat, nil, errors.New("invalid bot token format"))
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := hc.Do(req)
		if err != nil {
			ne := netErr(err)
			return 0, "", apierr.Wrap(api.ErrorReasonTelegramUnreachable, map[string]string{"url": telegramAPI, "detail": ne.Error()}, fmt.Errorf("cannot reach Telegram (%s): %w", telegramAPI, ne))
		}
		defer resp.Body.Close()
		var r struct {
			Description string `json:"description"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&r)
		return resp.StatusCode, r.Description, nil
	}
	code, desc, err := call("getMe", struct{}{})
	if err != nil {
		return err
	}
	if code == http.StatusUnauthorized || code == http.StatusNotFound {
		return apierr.Wrap(api.ErrorReasonTelegramTokenInvalid, nil, errors.New("bot token is invalid, check it with BotFather"))
	}
	if code != http.StatusOK {
		return telegramStatus(code, desc)
	}
	msg := map[string]any{"chat_id": t.ChatID, "text": testMessage.In(lang)}
	if t.MessageThreadID != nil {
		msg["message_thread_id"] = *t.MessageThreadID
	}
	code, desc, err = call("sendMessage", msg)
	if err != nil {
		return err
	}
	switch {
	case code == http.StatusOK:
		return nil
	case code == http.StatusForbidden:
		return apierr.Wrap(api.ErrorReasonTelegramForbidden, map[string]string{"description": desc}, fmt.Errorf("bot is not allowed to post in this chat, check that the bot was added to the group (%s)", desc))
	case code == http.StatusBadRequest && strings.Contains(strings.ToLower(desc), "chat not found"):
		return apierr.Wrap(api.ErrorReasonTelegramChatNotFound, nil, errors.New("chat id not found, check the chat id and that the bot is in the group"))
	case code == http.StatusBadRequest && strings.Contains(strings.ToLower(desc), "thread"):
		return apierr.Wrap(api.ErrorReasonTelegramThreadNotFound, map[string]string{"description": desc}, fmt.Errorf("topic id not found (%s)", desc))
	default:
		return telegramStatus(code, desc)
	}
}

// telegramStatus: Telegram returned an unexpected status code; desc is Telegram's English description.
func telegramStatus(code int, desc string) error {
	return apierr.Wrap(api.ErrorReasonTelegramHttpStatus, map[string]string{"status": strconv.Itoa(code), "description": desc}, fmt.Errorf("Telegram responded %d: %s", code, desc))
}

// TestSlack: sends one test message to the incoming webhook.
func TestSlack(ctx context.Context, webhookURL string, lang api.NotifyLanguage) error {
	if err := ValidateSlackURL(webhookURL); err != nil {
		return err
	}
	b, _ := json.Marshal(map[string]string{"text": testMessage.In(lang)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("invalid webhook URL")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := safeHTTPClient().Do(req)
	if err != nil {
		ne := netErr(err)
		return apierr.Wrap(api.ErrorReasonSlackUnreachable, map[string]string{"detail": ne.Error()}, fmt.Errorf("cannot reach Slack: %w", ne))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	// Slack errors are plain-text codes (invalid_token, no_service, channel_not_found...)
	msg := strings.TrimSpace(string(body))
	return apierr.Wrap(api.ErrorReasonSlackHttpStatus, map[string]string{"status": strconv.Itoa(resp.StatusCode), "body": msg}, fmt.Errorf("Slack responded %d: %s", resp.StatusCode, msg))
}

// slackHosts: Slack incoming webhooks only live on these two hosts (standard and GovSlack).
// Restricting the host keeps the test send from becoming "POST to any internal https service on the operator's behalf" (custom webhook tests only dial, never send a request).
var slackHosts = map[string]bool{"hooks.slack.com": true, "hooks.slack-gov.com": true}

// ValidateSlackURL: must be https, on a Slack incoming webhook host, with no credentials and no custom port.
func ValidateSlackURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || !slackHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("Slack webhook URL must be a full URL starting with https://hooks.slack.com/ or https://hooks.slack-gov.com/")
	}
	return nil
}
