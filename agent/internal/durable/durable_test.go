package durable

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAndSyncDir(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := WriteFile(p, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(p, []byte("two")); err != nil { // overwrites, does not append
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "two" {
		t.Fatalf("content = %q, want two", b)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
	if err := SyncDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(filepath.Join(dir, "missing", "f"), nil); err == nil {
		t.Fatal("writing into a missing directory should fail")
	}
}
