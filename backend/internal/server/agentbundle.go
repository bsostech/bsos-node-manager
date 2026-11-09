package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"besu-manager/backend/internal/agentbin"
	"besu-manager/backend/internal/api"
)

// Offline install bundle: install.sh, upgrade-agent.sh, the agent for each arch, SHA256SUMS,
// VERSION and README.txt packed into one tar.gz. For node hosts that cannot reach the platform over
// HTTPS, or when files must be reviewed before install; the agent still has to reach the platform
// on 8443 afterwards.
//
// Bundles built from the same agent files must be byte-identical: the join page shows the sha256
// before download, and two different results would not match. So tar header time, owner, mode and
// order are fixed, and the gzip header carries no file name or time. agent-bin files are swapped
// without restarting the platform (build-agent.sh), so the bundle is not built once at startup but
// cached by file size and mtime.

var bundleArches = []string{"amd64", "arm64"}

type agentBundle struct {
	key     string
	data    []byte
	sha256  string
	file    string
	version string
	arches  []string
}

var bundleCache struct {
	sync.Mutex
	b *agentBundle
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// loadAgentBundle: returns the bundle for the current agent files; nil if there is no agent for any arch.
func loadAgentBundle(dir string) (*agentBundle, error) {
	var key strings.Builder
	var arches []string
	for _, a := range bundleArches {
		p, ok := agentbin.Path(dir, a)
		if !ok {
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		arches = append(arches, a)
		fmt.Fprintf(&key, "%s:%d:%d;", a, st.Size(), st.ModTime().UnixNano())
		if vp, ok := agentbin.VersionPath(dir, a); ok { // swapping only .version also needs a rebuild (VERSION and file names change with it)
			if vs, err := os.Stat(vp); err == nil {
				fmt.Fprintf(&key, "v%d:%d;", vs.Size(), vs.ModTime().UnixNano())
			}
		}
	}
	if len(arches) == 0 {
		return nil, nil
	}
	// ponytail: packing happens inside the lock, so after an agent swap the first request waits for
	// it (two agents, a few seconds); other requests queue meanwhile. To go faster, pack in the
	// background and serve the old bundle first.
	bundleCache.Lock()
	defer bundleCache.Unlock()
	if b := bundleCache.b; b != nil && b.key == key.String() {
		return b, nil
	}
	b, err := buildAgentBundle(dir, arches)
	if err != nil {
		return nil, err
	}
	b.key = key.String()
	bundleCache.b = b
	return b, nil
}

func buildAgentBundle(dir string, arches []string) (*agentBundle, error) {
	version := "dev"
	for _, a := range arches {
		if vp, ok := agentbin.VersionPath(dir, a); ok {
			if v, err := os.ReadFile(vp); err == nil && strings.TrimSpace(string(v)) != "" {
				version = strings.TrimSpace(string(v))
				break
			}
		}
	}
	// the directory and file names appear in commands users paste, so keep only safe characters in the version string
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(".-_", r) {
			return r
		}
		return '_'
	}, version)
	root := "besu-agent-" + safe

	type entry struct {
		name string
		mode int64
		data []byte
	}
	files := []entry{{"install.sh", 0o755, installScript}, {"upgrade-agent.sh", 0o755, upgradeScript}}
	for _, a := range arches {
		p, _ := agentbin.Path(dir, a)
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		files = append(files, entry{"besu-agent-linux-" + a, 0o755, data})
	}
	var sums strings.Builder
	for _, f := range files {
		fmt.Fprintf(&sums, "%s  %s\n", sha256Hex(f.data), f.name) // sha256sum -c format
	}
	files = append(files,
		entry{"SHA256SUMS", 0o644, []byte(sums.String())},
		entry{"VERSION", 0o644, []byte(version + "\n")},
		entry{"README.txt", 0o644, []byte(bundleReadme(root))},
	)

	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.DefaultCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(gz)
	fixed := time.Unix(0, 0).UTC()
	hdr := func(name string, mode int64, size int64, typ byte) *tar.Header {
		return &tar.Header{Name: name, Mode: mode, Size: size, Typeflag: typ, ModTime: fixed, Uname: "root", Gname: "root", Format: tar.FormatPAX}
	}
	if err := tw.WriteHeader(hdr(root+"/", 0o755, 0, tar.TypeDir)); err != nil {
		return nil, err
	}
	for _, f := range files {
		if err := tw.WriteHeader(hdr(root+"/"+f.name, f.mode, int64(len(f.data)), tar.TypeReg)); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	data := buf.Bytes()
	return &agentBundle{data: data, sha256: sha256Hex(data), file: root + "-linux.tar.gz", version: version, arches: arches}, nil
}

func bundleReadme(root string) string {
	return `BSOS Node Agent offline bundle

On the host running Besu (as root):
  tar xzf ` + root + `-linux.tar.gz && cd ` + root + `
  sudo bash install.sh --local --platform <platform host[:8443]> --token <join code> [--runtime docker]

install.sh verifies the agent against SHA256SUMS and picks the binary for the host architecture (x86_64 / aarch64).
The agent must reach platform port 8443 (mTLS) to join. To upgrade later: sudo /usr/local/sbin/bsos-upgrade (connects to 8443 only).
Copy the full command (with the join code) from the platform's "Register node" page.
`
}

// GetAgentBundle: GET /platform/agent-bundle -> install.sh checksum and offline bundle info (see the contract).
func (s *Server) GetAgentBundle(_ context.Context, _ api.GetAgentBundleRequestObject) (api.GetAgentBundleResponseObject, error) {
	out := api.GetAgentBundle200JSONResponse{InstallShSha256: sha256Hex(installScript)}
	// a packing failure affects only the offline bundle; the install.sh checksum is still returned
	// and online install is unaffected
	b, err := loadAgentBundle(s.agentBinaryDir)
	if err != nil {
		log.Printf("agent bundle: %v", err)
		return out, nil
	}
	if b == nil {
		return out, nil
	}
	arches := make([]api.AgentBundleArches, 0, len(b.arches))
	for _, a := range b.arches {
		arches = append(arches, api.AgentBundleArches(a))
	}
	v := b.version
	out.AgentVersion = &v
	out.Bundle = &api.AgentBundle{File: b.file, Sha256: b.sha256, Arches: arches}
	return out, nil
}

// AgentBundleHandler: GET /api/v1/platform/agent-bundle.tar.gz (login required; contains no
// secrets, the join code is supplied separately by the user).
func AgentBundleHandler(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := loadAgentBundle(dir)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if b == nil {
			http.Error(w, "platform has no agent binary", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+b.file+`"`)
		http.ServeContent(w, r, b.file, time.Time{}, bytes.NewReader(b.data))
	}
}
