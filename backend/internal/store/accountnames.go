package store

import "context"

// Names for authorized transaction addresses: the platform's own address book, keyed by address (0x +
// lowercase), never written to nodes.

func (s *Store) SetAccountName(ctx context.Context, address, name string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO account_names (address, name) VALUES ($1, $2)
		ON CONFLICT (address) DO UPDATE SET name = EXCLUDED.name, updated_at = now()`, address, name)
	return err
}

func (s *Store) DeleteAccountName(ctx context.Context, address string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM account_names WHERE address = $1`, address)
	return err
}

// ListAccountNames: address -> name.
func (s *Store) ListAccountNames(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT address, name FROM account_names`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var a, n string
		if err := rows.Scan(&a, &n); err != nil {
			return nil, err
		}
		out[a] = n
	}
	return out, rows.Err()
}
