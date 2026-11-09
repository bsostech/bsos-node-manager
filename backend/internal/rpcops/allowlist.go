package rpcops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
)

// The account allowlist goes through perm_* RPC and Besu writes the file back itself. Whether a node enforces
// the allowlist is a startup flag; the platform only detects it and does not turn it on over RPC.

// The two reasons the allowlist is unusable must be reported separately so users know which setting to change:
// ErrPermUnavailable = the PERM namespace is not enabled in RPC; ErrAllowlistDisabled = the namespace is enabled
// but the account allowlist file is not. Both "not enabled" errors carry their reason themselves (errors.Is
// compares the same value); ErrAllowlistPersist = Besu is reachable but cannot write the allowlist file (the file
// does not exist or the besu user lacks write permission); its reason is attached when PermErr wraps it.
var (
	ErrPermUnavailable   = apierr.Wrap(api.ErrorReasonAllowlistPermUnavailable, nil, errors.New("PERM namespace not enabled on the node (rpc-http-api)"))
	ErrAllowlistDisabled = apierr.Wrap(api.ErrorReasonAllowlistFileDisabled, nil, errors.New("account allowlist file not enabled on the node (permissions-accounts-config-file-enabled)"))
	ErrAllowlistPersist  = errors.New("node cannot write the allowlist file")
)

// Besu's response when the account allowlist is not enabled (RpcErrorType.ACCOUNT_ALLOWLIST_NOT_ENABLED; the
// code is the generic -32000, so only the message can be compared).
const msgAllowlistNotEnabled = "Account allowlist has not been enabled"

// Besu's response when it cannot write the allowlist file (RpcErrorType.ALLOWLIST_PERSIST_FAILURE); it is
// followed by ". Changes reverted", so compare the prefix.
const msgAllowlistPersist = "Unable to persist changes to allowlist configuration file"

// PermErr: "namespace not enabled" becomes ErrPermUnavailable, "allowlist file not enabled" becomes
// ErrAllowlistDisabled, and "cannot write the allowlist file" is wrapped as ErrAllowlistPersist (still
// wrapping *RPCError: reachable, the node refused), each with a reason; other errors are returned unchanged.
func PermErr(err error) error {
	var re *RPCError
	switch {
	case MethodUnavailable(err):
		return ErrPermUnavailable
	case errors.As(err, &re) && re.Message == msgAllowlistNotEnabled:
		return ErrAllowlistDisabled
	case errors.As(err, &re) && strings.HasPrefix(re.Message, msgAllowlistPersist):
		return apierr.Wrap(api.ErrorReasonAllowlistPersistFailed, nil, fmt.Errorf("%w: %w", ErrAllowlistPersist, re))
	}
	return err
}

// ReadAllowlist: the node's current account allowlist.
func (c *Client) ReadAllowlist(ctx context.Context, nodeID string) ([]string, error) {
	var have []string
	if err := c.Call(ctx, nodeID, "perm_getAccountsAllowlist", nil, &have); err != nil {
		return nil, PermErr(err)
	}
	if have == nil {
		have = []string{} // Besu returns null = empty list
	}
	return have, nil
}

// SyncAllowlist: changes the node's account allowlist to want (sending only the diff, since Besu rejects the
// whole call for duplicate adds / removing missing entries) and returns the list read back afterwards. A
// readback that differs from want is drift (err is nil); err only means unreachable, the node refused, or the
// allowlist is not enabled (see PermErr).
func (c *Client) SyncAllowlist(ctx context.Context, nodeID string, want []string) (have []string, err error) {
	have, err = c.ReadAllowlist(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	add, remove := AllowlistDiff(have, want)
	var calls []Call
	if len(add) > 0 {
		calls = append(calls, Call{"perm_addAccountsToAllowlist", []any{add}})
	}
	if len(remove) > 0 {
		calls = append(calls, Call{"perm_removeAccountsFromAllowlist", []any{remove}})
	}
	if len(calls) > 0 {
		res, err := c.Batch(ctx, nodeID, calls)
		if err != nil {
			return nil, err
		}
		for _, r := range res {
			if r.Err != nil {
				return nil, PermErr(r.Err)
			}
		}
		if have, err = c.ReadAllowlist(ctx, nodeID); err != nil {
			return nil, err
		}
	}
	return have, nil
}

// AllowlistDiff: compares case-insensitively; add uses want's spelling, remove uses the original string on
// the node (Besu's remove must match the string it stored).
func AllowlistDiff(have, want []string) (add, remove []string) {
	h, w := lowerSet(have), lowerSet(want)
	for _, a := range want {
		if !h[strings.ToLower(a)] {
			add = append(add, a)
			h[strings.ToLower(a)] = true // add only once when want has duplicates
		}
	}
	for _, a := range have {
		if !w[strings.ToLower(a)] {
			remove = append(remove, a)
		}
	}
	sort.Strings(add)
	sort.Strings(remove)
	return add, remove
}

// SameAccounts: whether two lists hold the same set of addresses (case-insensitive, ignoring order and duplicates).
func SameAccounts(a, b []string) bool {
	add, remove := AllowlistDiff(a, b)
	return len(add) == 0 && len(remove) == 0
}

func lowerSet(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[strings.ToLower(x)] = true
	}
	return m
}
