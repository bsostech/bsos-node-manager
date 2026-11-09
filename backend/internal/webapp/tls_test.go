package webapp

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCertPair writes a fresh self-signed cert/key for cn and stamps the cert file with mtime. Returns the
// cert DER.
func writeCertPair(t *testing.T, certFile, keyFile, cn string, mtime time.Time) []byte {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{cn}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(certFile, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return der
}

// Manual-cert mode rotates without a restart: a cert file with a newer mtime is picked up on the next
// handshake; a broken replacement keeps serving the previous cert instead of failing handshakes.
func TestCertReloaderPicksUpRotation(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	t0 := time.Now().Add(-time.Hour)
	first := writeCertPair(t, certFile, keyFile, "a.example", t0)

	r, err := newCertReloader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	served := func() []byte {
		c, err := r.GetCertificate(nil)
		if err != nil || c == nil {
			t.Fatalf("GetCertificate: %v", err)
		}
		return c.Certificate[0]
	}
	if !bytes.Equal(served(), first) {
		t.Fatal("應出示啟動時載入的憑證")
	}

	second := writeCertPair(t, certFile, keyFile, "b.example", t0.Add(time.Minute))
	if !bytes.Equal(served(), second) {
		t.Fatal("憑證檔換過(mtime 較新)應改出示新的")
	}

	if err := os.WriteFile(certFile, []byte("not a cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(certFile, t0.Add(2*time.Minute), t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(served(), second) {
		t.Fatal("新檔壞掉時應沿用舊憑證")
	}
}

// A missing cert file or a key that does not match refuses to start, and so does Serve in manual mode or
// with an unknown mode.
func TestCertReloaderRefusesBadFiles(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if _, err := newCertReloader(certFile, keyFile); err == nil {
		t.Fatal("憑證檔不存在應回錯")
	}
	writeCertPair(t, certFile, keyFile, "a.example", time.Now())
	otherKey := filepath.Join(dir, "other.key")
	writeCertPair(t, filepath.Join(dir, "other.crt"), otherKey, "b.example", time.Now())
	if _, err := newCertReloader(certFile, otherKey); err == nil {
		t.Fatal("金鑰跟憑證對不上應回錯")
	}
	if err := Serve(context.Background(), Config{Mode: ModeManual, CertFile: filepath.Join(dir, "missing.crt"), KeyFile: keyFile}, nil); err == nil {
		t.Fatal("manual 模式憑證載入失敗應回錯,不該開始監聽")
	}
	if err := Serve(context.Background(), Config{Mode: "tls"}, nil); err == nil {
		t.Fatal("未知 TLS_MODE 應回錯")
	}
}

// Bodies over MaxRequestBody fail to read instead of being buffered whole; bodies at the cap still read.
// Every phase of reading a request has a deadline.
func TestNewServerLimits(t *testing.T) {
	h := NewServer(":0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		}
	})).Handler
	for size, want := range map[int]int{MaxRequestBody: http.StatusOK, MaxRequestBody + 1: http.StatusRequestEntityTooLarge} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(make([]byte, size))))
		if rec.Code != want {
			t.Errorf("body of %d bytes: got %d, want %d", size, rec.Code, want)
		}
	}
	if s := NewServer(":0", nil); s.ReadHeaderTimeout == 0 || s.ReadTimeout == 0 || s.IdleTimeout == 0 {
		t.Fatalf("timeouts must be set: %+v", s)
	}
}

// Every response carries the security headers; HSTS only when the site is served over TLS.
func TestSecurityHeaders(t *testing.T) {
	for _, overTLS := range []bool{false, true} {
		rec := httptest.NewRecorder()
		securityHeaders(http.NotFoundHandler(), overTLS).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		h := rec.Header()
		if h.Get("X-Frame-Options") != "DENY" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("overTLS=%v: missing headers %v", overTLS, h)
		}
		if got := h.Get("Strict-Transport-Security") != ""; got != overTLS {
			t.Errorf("overTLS=%v: HSTS present=%v", overTLS, got)
		}
	}
}

// Cancelling the context stops the listener but lets a request already in flight finish.
func TestServeUntilFinishesInFlightRequest(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	srv := NewServer("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, "done")
	}))
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- ServeUntil(ctx, srv, func() error { return srv.Serve(ln) }) }()

	got := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String())
		if err != nil {
			got <- "error: " + err.Error()
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		got <- string(b)
	}()
	<-started
	cancel()
	if body := <-got; body != "done" {
		t.Fatalf("in-flight request got %q, want done", body)
	}
	if err := <-served; err != nil {
		t.Fatalf("ServeUntil = %v, want nil after a clean shutdown", err)
	}
	if _, err := http.Get("http://" + ln.Addr().String()); err == nil {
		t.Fatal("listener still accepts connections after shutdown")
	}
}
