package rpcops

import "context"

// SetLogLevel: admin_changeLogLevel, effective immediately and not persisted (a Besu restart returns to the
// logging in config.toml). level is validated by the caller (contract enum); Besu accepts OFF / FATAL /
// ERROR / WARN / INFO / DEBUG / TRACE / ALL.
func (c *Client) SetLogLevel(ctx context.Context, nodeID, level string) error {
	return c.Call(ctx, nodeID, "admin_changeLogLevel", []any{level}, nil)
}
