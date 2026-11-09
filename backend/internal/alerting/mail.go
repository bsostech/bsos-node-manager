package alerting

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
)

// Mail sent by the platform itself (currently only the temporary password mail). Separate from alert mail: alert mail is sent by Alertmanager and configured in alert_settings;
// this set is read only from the deployment env, has no UI and is not stored in the DB.

// Security: connection encryption, chosen per mail set. Both sets default to TLS on port 465; otherwise alert mail
// defaults to none (DefaultAlertSecurity) and password mail to STARTTLS (RFC 8314).
type Security string

const (
	SecurityNone     Security = "none"
	SecurityStartTLS Security = "starttls"
	SecurityTLS      Security = "tls"
)

// DefaultAlertSecurity: alert mail without an explicit choice. Port 465 is implicit TLS; anything else is
// plain text, because alert mail usually goes through an intranet relay, often with a self-signed
// certificate that the Alertmanager image does not trust.
func DefaultAlertSecurity(port int) Security {
	if port == 465 {
		return SecurityTLS
	}
	return SecurityNone
}

// MailConfig: connection and sender settings. SMTP.To is unused (each mail gets its own recipient). RootCAs nil = system trust store.
type MailConfig struct {
	SMTP
	RootCAs *x509.CertPool
}

// SMTPCA: the internal CA certificates from SMTP_CA_FILE, shared by alert mail and password mail.
// When set, STARTTLS and TLS trust only these CAs, not the system store: Alertmanager's ca_file
// replaces its trust store the same way, and the connection test must answer what Alertmanager will see.
type SMTPCA struct {
	PEM  []byte // copied next to alertmanager.yml for Alertmanager's ca_file
	Pool *x509.CertPool
}

// Roots: the pool for tls.Config.RootCAs; nil (system store) when no CA file is set.
func (c *SMTPCA) Roots() *x509.CertPool {
	if c == nil {
		return nil
	}
	return c.Pool
}

// SMTPCAFromEnv: reads SMTP_CA_FILE. Returns nil when unset; an error when the file cannot be read or
// holds no PEM certificate (the caller refuses to start).
func SMTPCAFromEnv() (*SMTPCA, error) {
	f := os.Getenv("SMTP_CA_FILE")
	if f == "" {
		return nil, nil
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return nil, fmt.Errorf("SMTP_CA_FILE: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("SMTP_CA_FILE has no usable PEM certificate: %s", f)
	}
	return &SMTPCA{PEM: b, Pool: pool}, nil
}

// AccountMailFromEnv: reads ACCOUNT_SMTP_*. Returns nil without HOST (no password mail); returns an error if set but invalid (the caller refuses to start).
// ca is the SMTP_CA_FILE trust (nil = system store).
func AccountMailFromEnv(ca *SMTPCA) (*MailConfig, error) {
	host := strings.TrimSpace(os.Getenv("ACCOUNT_SMTP_HOST"))
	if host == "" {
		return nil, nil
	}
	c := MailConfig{SMTP: SMTP{Host: host, Port: 25, Username: os.Getenv("ACCOUNT_SMTP_USERNAME")}}
	if p := os.Getenv("ACCOUNT_SMTP_PORT"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("ACCOUNT_SMTP_PORT is invalid: %q", p)
		}
		c.Port = n
	}
	from, err := mail.ParseAddress(os.Getenv("ACCOUNT_SMTP_FROM"))
	if err != nil {
		return nil, fmt.Errorf("ACCOUNT_SMTP_FROM is required and must be an email address: %w", err)
	}
	c.From = from.Address
	pw, pwFile := os.Getenv("ACCOUNT_SMTP_PASSWORD"), os.Getenv("ACCOUNT_SMTP_PASSWORD_FILE")
	switch {
	case pw != "" && pwFile != "":
		return nil, errors.New("set only one of ACCOUNT_SMTP_PASSWORD and ACCOUNT_SMTP_PASSWORD_FILE")
	case pwFile != "":
		b, err := os.ReadFile(pwFile)
		if err != nil {
			return nil, fmt.Errorf("ACCOUNT_SMTP_PASSWORD_FILE: %w", err)
		}
		pw = strings.TrimRight(string(b), "\r\n")
	}
	c.Password = pw
	switch sec := Security(os.Getenv("ACCOUNT_SMTP_SECURITY")); sec {
	case "":
		c.Security = SecurityStartTLS
		if c.Port == 465 {
			c.Security = SecurityTLS
		}
	case SecurityNone, SecurityStartTLS, SecurityTLS:
		c.Security = sec
	default:
		return nil, fmt.Errorf("ACCOUNT_SMTP_SECURITY must be tls, starttls or none: %q", sec)
	}
	c.RootCAs = ca.Roots()
	return &c, nil
}

// connect: dial (blocking loopback and link-local, see safeDialTCP) -> EHLO -> encrypt per Security -> AUTH if there is an account.
// With starttls, if the server does not advertise it or the upgrade fails, return an error rather than falling back to plaintext. The timeout comes from ctx (covers dialing and all later reads/writes).
func connect(ctx context.Context, c MailConfig) (*smtp.Client, error) {
	conn, err := safeDialTCP(ctx, c.Host, strconv.Itoa(c.Port))
	if err != nil { // failures from the dial check already carry a reason (Wrap does not overwrite it); Go's original text for refused connections and timeouts goes into detail
		return nil, apierr.Wrap(api.ErrorReasonConnDialFailed, map[string]string{"detail": err.Error()}, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	// we dial the resolved IP, so the hostname must be given explicitly for certificate verification
	tlsCfg := &tls.Config{ServerName: c.Host, RootCAs: c.RootCAs, MinVersion: tls.VersionTLS12}
	if c.Security == SecurityTLS {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, tlsFailed(c, api.ErrorReasonTlsHandshakeFailed, "TLS handshake", err)
		}
		conn = tc
	}
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		conn.Close()
		return nil, apierr.Wrap(api.ErrorReasonSmtpHandshakeFailed, map[string]string{"detail": err.Error()}, fmt.Errorf("SMTP handshake: %w", err))
	}
	fail := func(err error) (*smtp.Client, error) {
		cl.Close()
		return nil, err
	}
	if err := cl.Hello("localhost"); err != nil {
		return fail(apierr.Wrap(api.ErrorReasonSmtpEhloFailed, map[string]string{"detail": err.Error()}, fmt.Errorf("EHLO: %w", err)))
	}
	if c.Security == SecurityStartTLS {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return fail(apierr.Wrap(api.ErrorReasonSmtpStarttlsUnsupported, nil, errors.New("SMTP server does not support STARTTLS; choose TLS if it encrypts from the first byte (usually port 465), otherwise no encryption")))
		}
		if err := cl.StartTLS(tlsCfg); err != nil { // on success Go resends EHLO, so AUTH advertised only after encryption is visible
			return fail(tlsFailed(c, api.ErrorReasonSmtpStarttlsFailed, "STARTTLS", err))
		}
	}
	if c.Username == "" {
		return cl, nil
	}
	ok, mechs := cl.Extension("AUTH")
	if !ok {
		return fail(apierr.Wrap(api.ErrorReasonSmtpAuthUnsupported, nil, errors.New("SMTP server offers no login on this connection; choose STARTTLS or TLS if it offers one only after encryption, otherwise leave the username empty")))
	}
	_, encrypted := cl.TLSConnectionState()
	auth, err := pickAuth(mechs, encrypted, c.SMTP)
	if err != nil {
		return fail(err)
	}
	if err := cl.Auth(auth); err != nil {
		// Servers that want STARTTLS before login still list AUTH in the clear, then refuse it with
		// "530 Must issue a STARTTLS command first"; say which setting fixes it instead of the raw reply.
		var te *textproto.Error
		if !encrypted && errors.As(err, &te) && te.Code == 530 && strings.Contains(strings.ToUpper(te.Msg), "STARTTLS") {
			return fail(apierr.Wrap(api.ErrorReasonSmtpStarttlsRequired, nil, fmt.Errorf("SMTP auth: %w", err)))
		}
		return fail(apierr.Wrap(api.ErrorReasonSmtpAuthFailed, map[string]string{"detail": err.Error()}, fmt.Errorf("SMTP auth: %w", err)))
	}
	return cl, nil
}

// tlsFailed: an unknown issuer gets its own reason, split by whether an internal CA file is set, since the
// fix differs (add the CA, or the CA file holds the wrong one); any other TLS failure keeps reason with the raw text.
func tlsFailed(c MailConfig, reason api.ErrorReason, step string, err error) error {
	var unknown x509.UnknownAuthorityError
	if errors.As(err, &unknown) {
		reason = api.ErrorReasonSmtpCertUntrusted
		if c.RootCAs != nil {
			reason = api.ErrorReasonSmtpCertNotInCa
		}
		return apierr.Wrap(reason, nil, fmt.Errorf("%s: %w", step, err))
	}
	return apierr.Wrap(reason, map[string]string{"detail": err.Error()}, fmt.Errorf("%s: %w", step, err))
}

// pickAuth: the same choice Alertmanager makes when it sends: walk the server's mechanisms in the order
// it lists them and take the first one Alertmanager knows. If that is PLAIN on a plain-text connection,
// Go's smtp.PlainAuth refuses to send the credentials and Alertmanager does not go on to LOGIN, so mail
// never leaves; fail here instead, so a passing test means mail really sends.
func pickAuth(mechs string, encrypted bool, c SMTP) (smtp.Auth, error) {
	if c.Password == "" { // Alertmanager skips PLAIN and LOGIN without a password, and CRAM-MD5 without a secret, so nothing is left
		return nil, apierr.Wrap(api.ErrorReasonSmtpPasswordRequired, nil, errors.New("SMTP username is set but the password is empty"))
	}
	for _, m := range strings.Fields(mechs) {
		switch m {
		case "PLAIN":
			if !encrypted {
				return nil, apierr.Wrap(api.ErrorReasonSmtpAuthLoginRequired, map[string]string{"mechs": mechs}, fmt.Errorf("SMTP server lists %s; PLAIN comes first and needs encryption: choose STARTTLS or TLS", mechs))
			}
			return smtp.PlainAuth("", c.Username, c.Password, c.Host), nil // host must match the one given to NewClient; PlainAuth compares them
		case "LOGIN":
			return loginAuth{c.Username, c.Password}, nil
		}
		// CRAM-MD5 needs smtp_auth_secret, which is never rendered, so Alertmanager skips it like any unknown mechanism.
	}
	return nil, apierr.Wrap(api.ErrorReasonSmtpAuthMechUnsupported, map[string]string{"mechs": mechs}, fmt.Errorf("unsupported SMTP auth mechanism %s (only PLAIN and LOGIN are supported)", mechs))
}

// CheckMail: connect + authenticate without sending (used at startup to validate the password mail settings).
func CheckMail(ctx context.Context, c MailConfig) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cl, err := connect(ctx, c)
	if err != nil {
		return err
	}
	defer cl.Close()
	return cl.Quit()
}

// SendMail: sends one plain-text mail. to must be a single validated address (the caller checked it with net/mail, so it has no CRLF).
func SendMail(ctx context.Context, c MailConfig, to, subject, body string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cl, err := connect(ctx, c)
	if err != nil {
		return err
	}
	defer cl.Close()
	if err := cl.Mail(c.From); err != nil {
		return apierr.Wrap(api.ErrorReasonSmtpSenderRejected, map[string]string{"detail": err.Error()}, fmt.Errorf("sender rejected: %w", err))
	}
	if err := cl.Rcpt(to); err != nil {
		return apierr.Wrap(api.ErrorReasonSmtpRecipientRejected, map[string]string{"detail": err.Error()}, fmt.Errorf("recipient rejected: %w", err))
	}
	w, err := cl.Data()
	if err != nil {
		return apierr.Wrap(api.ErrorReasonSmtpDataFailed, map[string]string{"detail": err.Error()}, fmt.Errorf("DATA: %w", err))
	}
	if _, err := w.Write(message(c.From, to, subject, body, time.Now())); err != nil {
		return apierr.Wrap(api.ErrorReasonSmtpWriteFailed, map[string]string{"detail": err.Error()}, fmt.Errorf("write message body: %w", err))
	}
	if err := w.Close(); err != nil {
		return apierr.Wrap(api.ErrorReasonSmtpNotAccepted, map[string]string{"detail": err.Error()}, fmt.Errorf("server did not accept the message: %w", err))
	}
	_ = cl.Quit() // the server has already accepted the mail; an error while closing does not count as a send failure (otherwise admins would think it was not delivered)
	return nil
}

// message: headers + quoted-printable body (does not depend on the peer supporting 8BITMIME); the subject is RFC 2047 encoded.
func message(from, to, subject, body string, now time.Time) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n",
		from, to, mime.QEncoding.Encode("utf-8", subject), now.Format(time.RFC1123Z))
	qp := quotedprintable.NewWriter(&b)
	qp.Write([]byte(strings.ReplaceAll(body, "\n", "\r\n")))
	qp.Close()
	return b.Bytes()
}
