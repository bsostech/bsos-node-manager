package pki

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// WriteSDToken should atomically write sd_token with matching contents; with gid < 0 it stays 0600 (same user
// reads and writes).
func TestWriteSDToken(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSDToken(dir, "tok-abc123", -1); err != nil {
		t.Fatalf("WriteSDToken: %v", err)
	}
	p := filepath.Join(dir, "sd_token")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(b) != "tok-abc123" {
		t.Fatalf("content = %q, want tok-abc123", b)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

// Empty dir / empty token writes nothing (no error when Prometheus is not deployed).
func TestWriteSDTokenSkipsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSDToken(dir, "", -1); err != nil {
		t.Fatalf("empty token should be no-op: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sd_token")); !os.IsNotExist(err) {
		t.Fatal("空 token 不應寫出檔案")
	}
	if err := WriteSDToken("", "tok", -1); err != nil {
		t.Fatalf("empty dir should be no-op: %v", err)
	}
}

// gid >= 0 must chown to that group (cross-container readers rely on it). No root needed: a file owner can
// chown to another group they belong to; skipped if there is no such group (only the primary group).
func TestWriteAtomicChownsGroup(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAtomic(dir, "f", []byte("x"), 0o640, -1); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "f"))
	if err != nil {
		t.Fatal(err)
	}
	orig := int(fi.Sys().(*syscall.Stat_t).Gid)
	groups, _ := os.Getgroups()
	gid := -1
	for _, g := range groups {
		if g != orig {
			gid = g
			break
		}
	}
	if gid < 0 {
		t.Skip("沒有主要群組以外的群組可 chown")
	}
	if err := WriteAtomic(dir, "f", []byte("x"), 0o640, gid); err != nil {
		t.Fatal(err)
	}
	if fi, err = os.Stat(filepath.Join(dir, "f")); err != nil {
		t.Fatal(err)
	}
	if got := int(fi.Sys().(*syscall.Stat_t).Gid); got != gid {
		t.Fatalf("gid = %d,want %d", got, gid)
	}
}
