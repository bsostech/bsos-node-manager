package store

import "context"

// PeerName: the name a user gave a node, keyed by address (0x + lowercase). The peers page derives the
// address from the public key and the validators page uses the address directly, so both show the same name.
type PeerName struct {
	Address string
	Name    string
}

func (s *Store) SetPeerName(ctx context.Context, n PeerName) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO peer_names (address, name) VALUES ($1, $2)
		ON CONFLICT (address) DO UPDATE SET name = EXCLUDED.name, updated_at = now()`, n.Address, n.Name)
	return err
}

func (s *Store) DeletePeerName(ctx context.Context, address string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM peer_names WHERE address = $1`, address)
	return err
}

func (s *Store) ListPeerNames(ctx context.Context) ([]PeerName, error) {
	rows, err := s.pool.Query(ctx, `SELECT address, name FROM peer_names`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PeerName
	for rows.Next() {
		var n PeerName
		if err := rows.Scan(&n.Address, &n.Name); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
