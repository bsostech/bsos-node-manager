package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
)

func readBundle(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = b
	}
}

// The same set of agent files must produce an identical bundle every time (the onboarding page
// shows the sha256 before downloading); it changes only when the agent changes. The sha256 from the
// API matches the downloaded content, and the SHA256SUMS in the bundle matches every file.
func TestAgentBundle(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"besu-agent-linux-amd64": "amd64-bin", "besu-agent-linux-arm64": "arm64-bin", "besu-agent-linux-amd64.version": "2.0.0\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{agentBinaryDir: dir}
	r, err := s.GetAgentBundle(context.Background(), api.GetAgentBundleRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	info := r.(api.GetAgentBundle200JSONResponse)
	if info.InstallShSha256 != sha256Hex(installScript) || info.Bundle == nil || info.Bundle.File != "besu-agent-2.0.0-linux.tar.gz" || len(info.Bundle.Arches) != 2 {
		t.Fatalf("info %+v %+v", info, info.Bundle)
	}

	// The checksum in the online install command must match what GET /install.sh actually serves
	wi := httptest.NewRecorder()
	InstallScriptHandler().ServeHTTP(wi, httptest.NewRequest("GET", "/install.sh", nil))
	if sha256Hex(wi.Body.Bytes()) != info.InstallShSha256 {
		t.Fatal("installShSha256 does not match served install.sh")
	}

	w := httptest.NewRecorder()
	AgentBundleHandler(dir).ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/platform/agent-bundle.tar.gz", nil))
	if w.Code != 200 || sha256Hex(w.Body.Bytes()) != info.Bundle.Sha256 {
		t.Fatalf("download %d, sha mismatch", w.Code)
	}

	// Clear the cache and build again; the bytes must be identical
	bundleCache.b = nil
	b2, err := loadAgentBundle(dir)
	if err != nil || b2.sha256 != info.Bundle.Sha256 {
		t.Fatalf("not reproducible: %v", err)
	}

	files := readBundle(t, w.Body.Bytes())
	root := "besu-agent-2.0.0/"
	for _, n := range []string{"install.sh", "upgrade-agent.sh", "besu-agent-linux-amd64", "besu-agent-linux-arm64", "SHA256SUMS", "VERSION", "README.txt"} {
		if _, ok := files[root+n]; !ok {
			t.Fatalf("missing %s in %v", n, files)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(string(files[root+"SHA256SUMS"])), "\n") {
		sum, name, _ := strings.Cut(line, "  ")
		if sha256Hex(files[root+name]) != sum {
			t.Errorf("SHA256SUMS wrong for %s", name)
		}
	}

	// A changed agent (size or time changed) must be rebuilt
	later := time.Now().Add(time.Hour)
	if err := os.WriteFile(filepath.Join(dir, "besu-agent-linux-arm64"), []byte("arm64-bin-v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(filepath.Join(dir, "besu-agent-linux-arm64"), later, later)
	if b3, _ := loadAgentBundle(dir); b3.sha256 == info.Bundle.Sha256 {
		t.Fatal("bundle not rebuilt after agent changed")
	}
}

// No agent binary: the API gives only install.sh's checksum, and the download returns 404.
func TestAgentBundleNoBinaries(t *testing.T) {
	s := &Server{agentBinaryDir: t.TempDir()}
	r, _ := s.GetAgentBundle(context.Background(), api.GetAgentBundleRequestObject{})
	if info := r.(api.GetAgentBundle200JSONResponse); info.Bundle != nil || info.InstallShSha256 == "" {
		t.Fatalf("info %+v", info)
	}
	w := httptest.NewRecorder()
	AgentBundleHandler("").ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 404 {
		t.Fatalf("got %d", w.Code)
	}
}

// The version string goes into directory and file names: alphanumerics and . - _ are kept,
// everything else (including / : @ [ ` { right next to the alphanumeric ranges) becomes an
// underscore.
func TestAgentBundleVersionSanitised(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"aAzZ09.-_", "aAzZ09.-_"}, {"mM5", "mM5"},
		{"1/2", "1_2"}, {"1:2", "1_2"}, {"1@2", "1_2"}, {"1[2", "1_2"}, {"1`2", "1_2"}, {"1{2", "1_2"}, {"1 2", "1_2"},
	} {
		dir := t.TempDir()
		for name, body := range map[string]string{"besu-agent-linux-amd64": "bin", "besu-agent-linux-amd64.version": c.in} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		b, err := buildAgentBundle(dir, []string{"amd64"})
		if err != nil {
			t.Fatal(err)
		}
		if b.file != "besu-agent-"+c.want+"-linux.tar.gz" || b.version != c.in {
			t.Errorf("版本 %q:file=%q version=%q,want 檔名含 besu-agent-%s", c.in, b.file, b.version, c.want)
		}
	}
}

// Only .version changes (agent file untouched): the cache key changes with it and a bundle with the
// new version number is built.
func TestAgentBundleRebuildsOnVersionChange(t *testing.T) {
	dir := t.TempDir()
	vp := filepath.Join(dir, "besu-agent-linux-amd64.version")
	if err := os.WriteFile(filepath.Join(dir, "besu-agent-linux-amd64"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vp, []byte("2.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := loadAgentBundle(dir); err != nil || b.version != "2.0.0" {
		t.Fatalf("第一次:%+v %v", b, err)
	}
	if err := os.WriteFile(vp, []byte("2.0.10\n"), 0o644); err != nil { // different length: does not rely on modification-time precision
		t.Fatal(err)
	}
	if b, err := loadAgentBundle(dir); err != nil || b.version != "2.0.10" || b.file != "besu-agent-2.0.10-linux.tar.gz" {
		t.Fatalf("只換 .version 應重打:%+v %v", b, err)
	}
}
