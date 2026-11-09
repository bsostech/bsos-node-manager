// Package rpcops: the platform sends JSON-RPC directly to nodes. Requests travel through the agent's yamux
// reverse tunnel with X-Target: 127.0.0.1:<nodes.rpc_port> and are relayed unchanged to the node locally; the
// agent does not interpret the contents. Every operation file (peers / allowlist / vote / loglevel) follows
// "build JSON-RPC -> send through the tunnel -> read back and compare".
package rpcops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/auth"
	"besu-manager/backend/internal/store"
)

// Transport: relays a POST via the agent to a local port on the node (implemented by
// pki.Service.PostLoopback).
type Transport interface {
	PostLoopback(ctx context.Context, nodeID string, port int, body []byte, header http.Header) (status int, resp []byte, err error)
}

// NodeStore: looks up a node's RPC port (implemented by store.Store).
type NodeStore interface {
	GetNode(ctx context.Context, id string) (*store.Node, error)
}

// platformPermissions: the token permissions the platform uses for node RPC. Methods are listed one by one,
// no wildcards; any new call must be added here. If it is missing, Besu answers that entry with Unauthorized;
// it never silently succeeds.
var platformPermissions = []string{
	"eth:blockNumber", "eth:syncing", "eth:getBlockByNumber", "eth:chainId", "eth:getTransactionCount",
	"net:peerCount", "net:enode",
	"web3:clientVersion",
	"txpool:besuStatistics", "txpool:besuTransactions", "txpool:besuPendingTransactions",
	"admin:nodeInfo", "admin:peers", "admin:addPeer", "admin:removePeer", "admin:changeLogLevel",
	"perm:getAccountsAllowlist", "perm:addAccountsToAllowlist", "perm:removeAccountsFromAllowlist",
	"ibft:getValidatorsByBlockNumber", "ibft:getSignerMetrics", "ibft:getPendingVotes", "ibft:proposeValidatorVote", "ibft:discardValidatorVote",
	"qbft:getValidatorsByBlockNumber", "qbft:getSignerMetrics", "qbft:getPendingVotes", "qbft:proposeValidatorVote", "qbft:discardValidatorVote",
}

// Permits: whether the platform token is allowed to call this method (method is the RPC name, e.g.
// eth_blockNumber). For callers' tests to check their own method lists; for a method missing from the list
// Besu returns 401 for the whole batch.
func Permits(method string) bool {
	return slices.Contains(platformPermissions, strings.Replace(method, "_", ":", 1))
}

// Token lifetime and renewal threshold: this token travels with every request through the agent tunnel to the
// node, where the node host can see it; its permissions include peers, the allowlist and voting, and every
// node of this org accepts it. The lifetime is "how long a token stolen from a compromised node host can
// still reach other nodes after that host is isolated", so keep it short.
const (
	tokenTTL   = time.Hour
	tokenRenew = 20 * time.Minute
)

type cachedToken struct {
	token string
	exp   time.Time
}

type Client struct {
	tr     Transport
	signer *auth.RpcSigner // nil = no token (tests)
	nodes  NodeStore

	mu  sync.Mutex
	tok cachedToken // the token has no aud; every node of this org shares one
}

func New(tr Transport, signer *auth.RpcSigner, nodes NodeStore) *Client {
	return &Client{tr: tr, signer: signer, nodes: nodes}
}

// Call: a single JSON-RPC call.
type Call struct {
	Method string
	Params []any
}

// RPCError: a JSON-RPC error returned by the node.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// Besu returns -32604 Method not enabled for "namespace not enabled"; a non-existent method (e.g. ibft_ on a
// QBFT chain) returns -32601.
const (
	codeMethodNotFound   = -32601
	codeMethodNotEnabled = -32604
	// codeUnauthorized: Besu's UNAUTHORIZED. For a batch carrying a token that fails verification, Besu
	// returns HTTP 200 and puts this error in the individual entries (not necessarily all of them), not a
	// whole-batch 401 (verified on Besu 26.7; only a missing token gets 401).
	codeUnauthorized = -40100
)

// MethodUnavailable: the node has not enabled this namespace / this chain has no such method.
func MethodUnavailable(err error) bool {
	var re *RPCError
	return errors.As(err, &re) && (re.Code == codeMethodNotEnabled || re.Code == codeMethodNotFound)
}

// Result: the result of one entry in a batch; Raw is meaningless when Err is non-nil.
type Result struct {
	Raw json.RawMessage
	Err error
}

// Decode: decodes the result into out; returns this entry's error if it failed.
func (r Result) Decode(out any) error {
	if r.Err != nil {
		return r.Err
	}
	return json.Unmarshal(r.Raw, out)
}

// ErrNoResponse: the request got no HTTP response at all from the node (via the agent): agent not connected,
// tunnel timeout, could not send. Callers use it to tell "agent absent" from "agent present, but RPC returned
// an error" (the latter proves the tunnel works).
var ErrNoResponse = errors.New("node RPC did not respond")

// ErrUnauthorized: the node returned 401 (token rejected; the node does not hold this platform public key, or
// the clock skew is too large).
var ErrUnauthorized = errors.New("node RPC authentication failed (401)")

// errUnauthorized: the 401 returned to callers, with a reason; errors.Is(err, ErrUnauthorized) still holds.
var errUnauthorized = apierr.Wrap(api.ErrorReasonNodeRpcUnauthorized, nil, ErrUnauthorized)

// Batch: sends a set of calls at once (one HTTP request); results come back in call order. error only means
// the whole batch failed (agent offline, node RPC not up, 401, undecodable response); per-entry errors are in
// Result.Err.
func (c *Client) Batch(ctx context.Context, nodeID string, calls []Call) ([]Result, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	reqs := make([]map[string]any, len(calls))
	for i, call := range calls {
		params := call.Params
		if params == nil {
			params = []any{}
		}
		reqs[i] = map[string]any{"jsonrpc": "2.0", "id": i, "method": call.Method, "params": params}
	}
	body, err := json.Marshal(reqs)
	if err != nil {
		return nil, err
	}
	status, resp, err := c.post(ctx, nodeID, body, true)
	if err != nil {
		return nil, apierr.Wrap(api.ErrorReasonNodeRpcNoResponse, map[string]string{"detail": err.Error()}, fmt.Errorf("%w: %w", ErrNoResponse, err))
	}
	if status == http.StatusUnauthorized {
		c.dropToken() // re-sign next time; may have just hit a node public key change
		return nil, errUnauthorized
	}
	if status != http.StatusOK {
		return nil, apierr.Wrap(api.ErrorReasonNodeRpcHttpStatus, map[string]string{"status": strconv.Itoa(status)}, fmt.Errorf("node RPC returned HTTP %d", status))
	}
	var items []struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(resp, &items); err != nil {
		return nil, apierr.Wrap(api.ErrorReasonNodeRpcBadResponse, map[string]string{"detail": err.Error()}, fmt.Errorf("node RPC: decode response: %w", err))
	}
	// Tokens signed by the platform are not scoped per method: any entry rejected means the node does not
	// accept this token (wrong public key or too much clock skew), equivalent to 401.
	for _, it := range items {
		if it.Error != nil && it.Error.Code == codeUnauthorized {
			c.dropToken()
			return nil, errUnauthorized
		}
	}
	out := make([]Result, len(calls))
	for i := range out {
		out[i].Err = apierr.Wrap(api.ErrorReasonNodeRpcMissingResult, map[string]string{"method": calls[i].Method}, fmt.Errorf("%s: node returned no result for this call", calls[i].Method))
	}
	for _, it := range items {
		if it.ID < 0 || it.ID >= len(calls) {
			continue
		}
		if it.Error != nil {
			out[it.ID] = Result{Err: apierr.Wrap(api.ErrorReasonNodeRpcError,
				map[string]string{"method": calls[it.ID].Method, "code": strconv.Itoa(it.Error.Code), "message": it.Error.Message},
				fmt.Errorf("%s: %w", calls[it.ID].Method, it.Error))}
		} else {
			out[it.ID] = Result{Raw: it.Result}
		}
	}
	return out, nil
}

// Call: a single call.
func (c *Client) Call(ctx context.Context, nodeID, method string, params []any, out any) error {
	res, err := c.Batch(ctx, nodeID, []Call{{method, params}})
	if err != nil {
		return err
	}
	if out == nil {
		return res[0].Err
	}
	return res[0].Decode(out)
}

// ProbeAuth: calls eth_blockNumber once without a token to see whether the node has RPC auth enabled: 401 =
// "enabled", 200 = "disabled", anything else (offline, node RPC not up) returns empty = unknown. Replaces the
// agent's probeAuth.
func (c *Client) ProbeAuth(ctx context.Context, nodeID string) string {
	status, _, err := c.post(ctx, nodeID, []byte(`{"jsonrpc":"2.0","id":0,"method":"eth_blockNumber","params":[]}`), false)
	if err != nil {
		return ""
	}
	switch status {
	case http.StatusUnauthorized:
		return "enabled"
	case http.StatusOK:
		return "disabled"
	}
	return ""
}

func (c *Client) post(ctx context.Context, nodeID string, body []byte, withToken bool) (int, []byte, error) {
	n, err := c.nodes.GetNode(ctx, nodeID)
	if err != nil {
		return 0, nil, err
	}
	if n == nil {
		return 0, nil, fmt.Errorf("node %s does not exist", nodeID)
	}
	h := http.Header{}
	if withToken {
		tok, err := c.token()
		if err != nil {
			return 0, nil, err
		}
		if tok != "" {
			h.Set("Authorization", "Bearer "+tok)
		}
	}
	return c.tr.PostLoopback(ctx, nodeID, n.RpcPort, body, h)
}

// token: the platform's own token (no aud, shared by all nodes); cached until only tokenRenew is left, then
// re-signed.
func (c *Client) token() (string, error) {
	if c.signer == nil {
		return "", nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tok.token != "" && time.Until(c.tok.exp) > tokenRenew {
		return c.tok.token, nil
	}
	tok, _, exp, err := c.signer.Sign("bsos-platform", platformPermissions, tokenTTL)
	if err != nil {
		return "", err
	}
	c.tok = cachedToken{tok, exp}
	return tok, nil
}

func (c *Client) dropToken() {
	c.mu.Lock()
	c.tok = cachedToken{}
	c.mu.Unlock()
}
