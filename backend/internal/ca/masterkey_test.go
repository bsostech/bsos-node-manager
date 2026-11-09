package ca

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMasterKeyFromEnv(t *testing.T) {
	const hexKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	file := filepath.Join(t.TempDir(), "master")
	if err := os.WriteFile(file, []byte(hexKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, val, file, wantErr string
	}{
		{"env", hexKey, "", ""},
		{"檔案(去尾端換行)", "", file, ""},
		{"兩個都設", hexKey, file, "set only one"},
		{"都沒設", "", "", "is not set"},
		{"長度不對", "abcd", "", "64 hex"},
		{"檔案不存在", "", file + ".nope", "CA_MASTER_KEY_FILE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CA_MASTER_KEY", tc.val)
			t.Setenv("CA_MASTER_KEY_FILE", tc.file)
			key, err := MasterKeyFromEnv()
			if tc.wantErr == "" {
				if err != nil || len(key) != 32 || key[31] != 0x1f {
					t.Fatalf("應成功:%v %x", err, key)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("應回含 %q 的錯誤,得 %v", tc.wantErr, err)
			}
		})
	}
}
