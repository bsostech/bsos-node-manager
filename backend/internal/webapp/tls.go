package webapp

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// stagingDirectoryURL: Let's Encrypt staging CA (to test the flow first and avoid production rate limits;
// browsers do not trust its certs).
const stagingDirectoryURL = "https://acme-staging-v02.api.letsencrypt.org/directory"

// TLSMode: matches the three-way choice in up.sh/setup.sh (--acme / --cert+--key / --http) and decides how the
// server terminates TLS.
type TLSMode string

const (
	ModeACME   TLSMode = "acme"   // automatic HTTPS on the public internet (Let's Encrypt, TLS-ALPN-01, needs only 443)
	ModeManual TLSMode = "manual" // manual certs (public certbot/purchased, or signed by an internal corporate CA)
	ModeHTTP   TLSMode = "http"   // plain HTTP (the intranet/leased line is trusted, or TLS is already terminated in front)
)

// Config: settings shared by all three modes, assembled by main.go from env.
type Config struct {
	Mode     TLSMode
	Addr     string // TLS listen address (fixed :443 for acme/manual); ignored in http mode, which uses HTTPAddr
	HTTPAddr string // plain HTTP listen address, or in acme/manual mode the address used only for 80->443 redirects
	Domain   string // required for acme (HostPolicy); optional for manual/http, used only to build full URLs for redirects
	Email    string // required for acme (Let's Encrypt account/expiry notices)
	CacheDir string // acme cert cache directory (must be writable and persist across restarts; same persistence convention as the CA material)
	Staging  bool   // optional for acme: use the LE staging CA to test the flow first (avoids production rate limits; browsers do not trust the cert)
	CertFile string // required for manual
	KeyFile  string // required for manual
}

// MaxRequestBody caps every request body. Login and agent join are reachable without credentials, and a body
// is decoded whole into memory: one 400 MB body took the server to 3 GB, past the 2 GB minimum host.
// The largest real body (an account allowlist) is far below this.
const MaxRequestBody = 1 << 20

// NewServer: the server for every listener. A client gets a bounded time to send its request, so slow or idle
// connections cannot pile up. No WriteTimeout: some handlers wait on node RPC calls longer than a fixed bound.
func NewServer(addr string, h http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: http.MaxBytesHandler(h, MaxRequestBody),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
}

// securityHeaders: the site is never framed and has no reason to send a Referer elsewhere. HSTS only over TLS
// (browsers ignore it on plain HTTP), and for one day: up.sh can switch an install back to --http, and a longer
// max-age would keep browsers that visited before locked out of it.
func securityHeaders(h http.Handler, overTLS bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		if overTLS {
			hd.Set("Strict-Transport-Security", "max-age=86400")
		}
		h.ServeHTTP(w, r)
	})
}

// shutdownGrace: how long a stopping server lets requests in flight finish. Shorter than the 10 seconds Docker
// and systemd wait before killing the process, so the rest of the shutdown still fits.
const shutdownGrace = 5 * time.Second

// ServeUntil runs listen (srv's ListenAndServe or ListenAndServeTLS) until it fails or ctx is cancelled; on
// cancel it stops accepting connections and gives requests in flight shutdownGrace to finish; requests
// still running after that are cut. Stopping returns nil.
func ServeUntil(ctx context.Context, srv *http.Server, listen func() error) error {
	errc := make(chan error, 1)
	go func() { errc <- listen() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil {
			log.Printf("webapp: %s: requests still running after %s were cut: %v", srv.Addr, shutdownGrace, err)
			_ = srv.Close()
		}
		return nil
	}
}

// Serve: starts listening according to the mode and blocks until an error or until ctx is cancelled (then it
// shuts down gracefully and returns nil).
func Serve(ctx context.Context, cfg Config, handler http.Handler) error {
	handler = securityHeaders(handler, cfg.Mode != ModeHTTP)
	switch cfg.Mode {
	case ModeHTTP:
		log.Printf("webapp: plain HTTP listening on %s", cfg.HTTPAddr)
		// nosemgrep: go.lang.security.audit.net.use-tls.use-tls -- TLS_MODE=http is an explicit option (intranet/leased line, or TLS already terminated in front); the other occurrence is the 80->443 redirect itself
		srv := NewServer(cfg.HTTPAddr, handler)
		return ServeUntil(ctx, srv, srv.ListenAndServe)
	case ModeACME:
		mgr := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(cfg.Domain),
			Cache:      autocert.DirCache(cfg.CacheDir),
			Email:      cfg.Email,
		}
		if cfg.Staging {
			mgr.Client = &acme.Client{DirectoryURL: stagingDirectoryURL}
			log.Printf("webapp: using the LE staging CA (browsers do not trust its certificates, for testing the flow only)")
		}
		go serveRedirect(ctx, cfg.HTTPAddr, cfg.Domain)
		srv := NewServer(cfg.Addr, handler)
		srv.TLSConfig = mgr.TLSConfig()
		log.Printf("webapp: ACME (TLS-ALPN-01) listening on %s (domain=%s)", cfg.Addr, cfg.Domain)
		return ServeUntil(ctx, srv, func() error { return srv.ListenAndServeTLS("", "") })
	case ModeManual:
		reloader, err := newCertReloader(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return err
		}
		go serveRedirect(ctx, cfg.HTTPAddr, cfg.Domain)
		// MinVersion is set to TLS 1.2 explicitly: Go's default floats between versions, and this is a
		// public-facing site whose floor should not depend on the Go version used to compile. 1.2 rather than
		// 1.3 so internal clients still in service are not excluded.
		srv := NewServer(cfg.Addr, handler)
		srv.TLSConfig = &tls.Config{
			GetCertificate: reloader.GetCertificate,
			MinVersion:     tls.VersionTLS12,
		}
		log.Printf("webapp: manual certificate listening on %s (cert=%s)", cfg.Addr, cfg.CertFile)
		return ServeUntil(ctx, srv, func() error { return srv.ListenAndServeTLS("", "") })
	default:
		return fmt.Errorf("webapp: unknown TLS_MODE %q (one of acme, manual, http)", cfg.Mode)
	}
}

// serveRedirect: minimal 80->443 redirect (for user convenience; never called in plain HTTP mode).
func serveRedirect(ctx context.Context, addr, domain string) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := domain
		if host == "" {
			host = r.Host
		}
		// nosemgrep: go.lang.security.injection.open-redirect.open-redirect -- always redirects to this site's https://<configured domain or Host>, keeping only the path; this is the 80->443 redirect, not a user-controlled destination
		http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
	// nosemgrep: go.lang.security.audit.net.use-tls.use-tls -- TLS_MODE=http is an explicit option (intranet/leased line, or TLS already terminated in front); the other occurrence is the 80->443 redirect itself
	srv := NewServer(addr, securityHeaders(h, false))
	if err := ServeUntil(ctx, srv, srv.ListenAndServe); err != nil {
		log.Printf("webapp: :80 redirect listener (443 main site unaffected): %v", err)
	}
}

// certReloader: checks the cert file's mtime on every handshake and rereads only when it changed, supporting
// cert rotation without a restart (no fsnotify/SIGHUP needed).
type certReloader struct {
	certFile, keyFile string

	mu      sync.RWMutex
	cert    *tls.Certificate
	modTime time.Time
}

func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile}
	if err := r.reload(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *certReloader) reload() error {
	fi, err := os.Stat(r.certFile)
	if err != nil {
		return fmt.Errorf("read certificate %s: %w", r.certFile, err)
	}
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("load certificate %s/%s: %w", r.certFile, r.keyFile, err)
	}
	r.mu.Lock()
	r.cert = &cert
	r.modTime = fi.ModTime()
	r.mu.Unlock()
	return nil
}

func (r *certReloader) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	fi, err := os.Stat(r.certFile)
	if err == nil {
		r.mu.RLock()
		changed := fi.ModTime().After(r.modTime)
		r.mu.RUnlock()
		if changed {
			if err := r.reload(); err != nil {
				log.Printf("webapp: reload certificate, keeping the old one: %v", err)
			}
		}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cert, nil
}
