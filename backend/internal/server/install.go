package server

import (
	_ "embed"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"

	"besu-manager/backend/internal/agentbin"
)

// install.sh is embedded in the binary and served through a public raw route (the curl command
// built by the frontend fetches it). It installs only the agent (systemd or docker) so a host
// already running Besu can join; see assets/install.sh.

//go:embed assets/install.sh
var installScript []byte

//go:embed assets/upgrade-agent.sh
var upgradeScript []byte

// InstallScriptHandler: GET /install.sh (public; the script itself has no secrets, the token is
// passed as an argument).
func InstallScriptHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		_, _ = w.Write(installScript)
	}
}

// UpgradeScriptHandler: GET /upgrade-agent.sh (public; no secrets; fetches the latest agent binary
// over mTLS, swaps the file and restarts).
func UpgradeScriptHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		_, _ = w.Write(upgradeScript)
	}
}

// AgentBinaryHandler: GET /besu-agent-linux-{arch} (public; install.sh fetches the agent binary on
// first install). Served from AGENT_BINARY_DIR; unset or file missing -> 404. Uses http.ServeFile
// to stream it (not reading the whole binary into memory), which also brings
// Content-Length/Range/If-Modified-Since support for free. (The mTLS upgrade path uses pki's
// /pki/v1/agent-binary/{arch} instead, which blocks man-in-the-middle; this public endpoint is only
// for the first install, which has no identity yet.)
func AgentBinaryHandler(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, ok := agentbin.Path(dir, chi.URLParam(r, "arch"))
		if !ok {
			http.Error(w, "agent binary not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeFile(w, r, f)
	}
}

// AgentBinaryChecksumHandler: GET /besu-agent-linux-{arch}.sha256 (public). It adds a supply-chain
// integrity check to the agent binary download, in the same spirit as the join token's out-of-band
// digest against MITM. After downloading the binary, install.sh compares it with this and aborts
// the install without swapping files on mismatch. The hash implementation is agentbin.SHA256.
func AgentBinaryChecksumHandler(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, ok := agentbin.Path(dir, chi.URLParam(r, "arch"))
		if !ok {
			http.Error(w, "agent binary not found", http.StatusNotFound)
			return
		}
		sum, err := agentbin.SHA256(f)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(sum + "\n"))
	}
}

// AgentBinaryVersionHandler: GET /besu-agent-linux-{arch}.version (public). Returns the contents of
// the sibling .version file produced by build-agent.sh (a plain-text version string). The version
// is not secret, so the public endpoint serves it too, letting a first install without an identity
// check it; no file -> 404 (honest: the platform has no version info, and the caller decides to
// fall back to no version comparison).
func AgentBinaryVersionHandler(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vp, ok := agentbin.VersionPath(dir, chi.URLParam(r, "arch"))
		if !ok {
			http.Error(w, "agent version info not found", http.StatusNotFound)
			return
		}
		data, err := os.ReadFile(vp)
		if err != nil {
			http.Error(w, "agent version info not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(data)
	}
}
