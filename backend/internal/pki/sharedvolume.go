package pki

import (
	"os"
	"path/filepath"
)

// When the platform writes files into a volume shared with sibling containers, two things must be handled
// together: atomic replacement (a reader may be reading at any moment) and group permissions (the server
// writes as root, the Prometheus / Alertmanager containers read as nobody). This file is that toolkit; its
// current users are sd_token (in the `/sd-token` volume) and internal/alerting's alertmanager.yml.

// WriteAtomic: writes dir/name, replacing it via rename so readers never see a half-written file. mode is
// fixed regardless of umask; chown only when gid >= 0, so readers running as a different user can read it.
func WriteAtomic(dir, name string, data []byte, mode os.FileMode, gid int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dst := filepath.Join(dir, name)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	if gid >= 0 {
		if err := os.Chown(tmp, -1, gid); err != nil {
			return err
		}
	}
	return os.Rename(tmp, dst)
}

// WriteSDToken: writes the bearer Prometheus uses for /sd/targets and /agent-proxy to dir/sd_token. Written
// by the server (which holds the SD_TOKEN env), avoiding the permission trap of "host bind-mount file uid !=
// container nobody". Empty values are not written. gid >= 0 (cross-container read) -> 0640; otherwise 0600
// (same user reads and writes).
func WriteSDToken(dir, token string, gid int) error {
	if dir == "" || token == "" {
		return nil
	}
	mode := os.FileMode(0o600)
	if gid >= 0 {
		mode = 0o640
	}
	return WriteAtomic(dir, "sd_token", []byte(token), mode, gid)
}
