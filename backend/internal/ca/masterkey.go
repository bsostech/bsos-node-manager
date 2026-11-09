package ca

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// MasterKeyFromEnv reads either CA_MASTER_KEY (64 hex) or CA_MASTER_KEY_FILE (file with the same format, for a mounted docker secret).
// The file form keeps the master key out of the env in plaintext (env shows up in docker inspect and ps e). Shared by server and cactl.
func MasterKeyFromEnv() ([]byte, error) {
	val, file := os.Getenv("CA_MASTER_KEY"), os.Getenv("CA_MASTER_KEY_FILE")
	switch {
	case val != "" && file != "":
		return nil, fmt.Errorf("set only one of CA_MASTER_KEY and CA_MASTER_KEY_FILE")
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("CA_MASTER_KEY_FILE: %w", err)
		}
		val = strings.TrimSpace(string(b))
	case val == "":
		return nil, fmt.Errorf("CA_MASTER_KEY is not set (generate one with openssl rand -hex 32, or point CA_MASTER_KEY_FILE at a file)")
	}
	key, err := hex.DecodeString(val)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("CA master key must be 64 hex characters (openssl rand -hex 32)")
	}
	return key, nil
}
