package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// The platform's node RPC signing key (single row). The private key is AES-GCM encrypted (auth/secret.go);
// the public key is kept in plaintext so GET /platform/rpc-public-key can return it directly.

// GetRpcSigningKey: returns (nil, "", nil) when not generated yet.
func (s *Store) GetRpcSigningKey(ctx context.Context) ([]byte, string, error) {
	var keyEnc []byte
	var pub string
	err := s.pool.QueryRow(ctx, `SELECT private_key_enc, public_key_pem FROM rpc_signing_key WHERE id = 1`).Scan(&keyEnc, &pub)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	return keyEnc, pub, nil
}

// SaveRpcSigningKey: writes only when none exists; if two platform instances start at once only one succeeds,
// and the other reads the same key on its next Load.
func (s *Store) SaveRpcSigningKey(ctx context.Context, keyEnc []byte, publicPEM, algorithm string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO rpc_signing_key (id, private_key_enc, public_key_pem, algorithm, created_at)
		VALUES (1, $1, $2, $3, now())
		ON CONFLICT (id) DO NOTHING`, keyEnc, publicPEM, algorithm)
	return err
}
