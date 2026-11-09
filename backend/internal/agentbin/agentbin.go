// Package agentbin is the single implementation of "agent binary file path + SHA-256 checksum",
// shared by the public HTTP endpoint (internal/server/install.go) and the mTLS endpoint (internal/pki/joinapi.go).
// The two must not each keep their own copy of the hash/path logic (the checksum semantics would drift).
package agentbin

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
)

// filename is the binary file name for an arch (the naming produced by the platform-side build-agent.sh).
func filename(arch string) string { return "besu-agent-linux-" + arch }

// Path validates and builds the binary path. arch is limited to an allowlist (amd64/arm64), so there is no path traversal risk;
// when dir is unset or the file does not exist it returns ("", false), and callers uniformly answer 404.
func Path(dir, arch string) (string, bool) {
	if arch != "amd64" && arch != "arm64" {
		return "", false
	}
	if dir == "" {
		return "", false
	}
	f := filepath.Join(dir, filename(arch))
	if _, err := os.Stat(f); err != nil {
		return "", false
	}
	return f, true
}

// VersionPath is the path of the .version file next to the binary (produced by build-agent.sh; content = version string).
// The platform version endpoint reads it instead of executing the binary, because binaries cannot run cross-arch (arm64 does not run on an amd64 platform).
// arch also goes through the allowlist; when dir is unset or the .version file does not exist it returns ("", false).
func VersionPath(dir, arch string) (string, bool) {
	if arch != "amd64" && arch != "arm64" {
		return "", false
	}
	if dir == "" {
		return "", false
	}
	f := filepath.Join(dir, filename(arch)+".version")
	if _, err := os.Stat(f); err != nil {
		return "", false
	}
	return f, true
}

// SHA256 streams the file and computes its SHA-256 hex on the fly (the file is small and rarely requested, so no cache; a replaced file always gets the latest hash).
func SHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
