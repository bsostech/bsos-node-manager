package rpcops

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/auth"
	"besu-manager/backend/internal/store"
)

// fakeNode: a fake node RPC. Answers by method and records the calls it received; responses are deliberately
// in reverse order to verify Batch maps them back by id.
type fakeNode struct {
	allow  []string
	peers  []string
	self   string
	calls  []string
	status int
	body   []byte // non-nil: return this body as-is (HTTP 200)
}

func (f *fakeNode) GetNode(_ context.Context, id string) (*store.Node, error) {
	return &store.Node{ID: id, RpcPort: 8545}, nil
}

func (f *fakeNode) PostLoopback(_ context.Context, _ string, port int, body []byte, _ http.Header) (int, []byte, error) {
	if port != 8545 {
		return 0, nil, errors.New("wrong port")
	}
	if f.status != 0 {
		return f.status, nil, nil
	}
	if f.body != nil {
		return 200, f.body, nil
	}
	var reqs []struct {
		ID     int             `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &reqs); err != nil {
		return 400, nil, nil
	}
	var out []map[string]any
	for i := len(reqs) - 1; i >= 0; i-- {
		r := reqs[i]
		f.calls = append(f.calls, r.Method)
		var params []json.RawMessage
		_ = json.Unmarshal(r.Params, &params)
		item := map[string]any{"jsonrpc": "2.0", "id": r.ID}
		switch r.Method {
		case "perm_getAccountsAllowlist":
			item["result"] = f.allow
		case "perm_addAccountsToAllowlist":
			var xs []string
			_ = json.Unmarshal(params[0], &xs)
			f.allow = append(f.allow, xs...)
			item["result"] = "Success"
		case "perm_removeAccountsFromAllowlist":
			var xs []string
			_ = json.Unmarshal(params[0], &xs)
			var keep []string
			for _, a := range f.allow {
				drop := false
				for _, x := range xs {
					drop = drop || a == x
				}
				if !drop {
					keep = append(keep, a)
				}
			}
			f.allow = keep
			item["result"] = "Success"
		case "admin_addPeer", "admin_removePeer":
			item["result"] = true
		case "admin_nodeInfo":
			item["result"] = map[string]any{"enode": f.self}
		case "admin_peers":
			var ps []map[string]any
			for _, p := range f.peers {
				ps = append(ps, map[string]any{"enode": p})
			}
			item["result"] = ps
		default:
			item["error"] = map[string]any{"code": -32604, "message": "Method not enabled"}
		}
		out = append(out, item)
	}
	b, _ := json.Marshal(out)
	return 200, b, nil
}

func TestSyncAllowlist(t *testing.T) {
	f := &fakeNode{allow: []string{"0xAAAA", "0xbbbb"}}
	c := New(f, nil, f)
	have, err := c.SyncAllowlist(context.Background(), "n1", []string{"0xaaaa", "0xcccc"})
	if err != nil || !SameAccounts(have, []string{"0xaaaa", "0xcccc"}) {
		t.Fatalf("have=%v err=%v", have, err)
	}
	if want := []string{"0xAAAA", "0xcccc"}; !reflect.DeepEqual(f.allow, want) {
		t.Fatalf("node allowlist = %v, want %v", f.allow, want)
	}
	// Already in sync: read once, send no add / remove.
	f.calls = nil
	if have, err := c.SyncAllowlist(context.Background(), "n1", []string{"0xCCCC", "0xaaaa"}); err != nil || !SameAccounts(have, []string{"0xCCCC", "0xaaaa"}) {
		t.Fatalf("second sync have=%v err=%v", have, err)
	}
	if !reflect.DeepEqual(f.calls, []string{"perm_getAccountsAllowlist"}) {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestPermUnavailable(t *testing.T) {
	f := &fakeNode{}
	c := New(&noPerm{f}, nil, f)
	if _, err := c.SyncAllowlist(context.Background(), "n1", nil); !errors.Is(err, ErrAllowlistDisabled) {
		t.Fatalf("err = %v, want ErrAllowlistDisabled", err)
	}
}

// PERM namespace not enabled (method not found) and the allowlist file not enabled are two different things,
// each with its own error; other errors are passed through unchanged.
func TestPermErrSplit(t *testing.T) {
	other := &RPCError{Code: -32000, Message: "boom"}
	persist := &RPCError{Code: -32000, Message: msgAllowlistPersist + ". Changes reverted"}
	for _, c := range []struct {
		in, want error
		reason   api.ErrorReason
	}{
		{&RPCError{Code: codeMethodNotFound, Message: "Method not found"}, ErrPermUnavailable, api.ErrorReasonAllowlistPermUnavailable},
		{&RPCError{Code: codeMethodNotEnabled, Message: "Method not enabled"}, ErrPermUnavailable, api.ErrorReasonAllowlistPermUnavailable},
		{&RPCError{Code: -32000, Message: msgAllowlistNotEnabled}, ErrAllowlistDisabled, api.ErrorReasonAllowlistFileDisabled},
		{persist, ErrAllowlistPersist, api.ErrorReasonAllowlistPersistFailed},
		{other, other, ""},
	} {
		got := PermErr(c.in)
		if !errors.Is(got, c.want) {
			t.Errorf("PermErr(%v) = %v, want %v", c.in, got, c.want)
		}
		if r, _, _ := apierr.ReasonOf(got); r != c.reason {
			t.Errorf("PermErr(%v) reason %q, want %q", c.in, r, c.reason)
		}
	}
	// Persist failures still wrap *RPCError (reachable, the node refused); Error() is still the sentinel
	// sentence followed by the original text
	if got := PermErr(persist); !errors.As(got, new(*RPCError)) || got.Error() != "node cannot write the allowlist file: rpc error -32000: "+persist.Message {
		t.Errorf("persist: %q", got.Error())
	}
}

type noPerm struct{ *fakeNode }

func (n *noPerm) PostLoopback(ctx context.Context, id string, port int, body []byte, h http.Header) (int, []byte, error) {
	return 200, []byte(`[{"jsonrpc":"2.0","id":0,"error":{"code":-32000,"message":"Account allowlist has not been enabled"}}]`), nil
}

func TestSyncPeers(t *testing.T) {
	self := "enode://aa@10.0.0.1:30303"
	f := &fakeNode{self: self, peers: []string{"enode://BB@10.0.0.9:51234"}} // inbound: different host:port, different case
	c := New(f, nil, f)
	want := []string{self, "enode://bb@10.0.0.2:30303", "enode://cc@10.0.0.3:30303"}
	ps, err := c.SyncPeers(context.Background(), "n1", want, PeerDiff([]string{"enode://dd@10.0.0.4:30303", "enode://bb@x:1"}, want))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ps.Missing, []string{"enode://cc@10.0.0.3:30303"}) || len(ps.Failed) != 0 {
		t.Fatalf("sync = %+v", ps)
	}
	removes := 0
	for _, m := range f.calls {
		if m == "admin_removePeer" {
			removes++
		}
	}
	if removes != 1 {
		t.Fatalf("removePeer calls = %d, want 1 (only dd)", removes)
	}
}

func TestBatchUnauthorized(t *testing.T) {
	f := &fakeNode{status: 401}
	c := New(f, nil, f)
	if _, err := c.Batch(context.Background(), "n1", []Call{{"eth_blockNumber", nil}}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
	if got := c.ProbeAuth(context.Background(), "n1"); got != "enabled" {
		t.Fatalf("probe = %q", got)
	}
	// A batch whose token fails verification: Besu returns 200 and puts -40100 in the individual entries (not
	// necessarily all of them). It must be treated as 401, not "method unreachable".
	f = &fakeNode{body: []byte(`[{"jsonrpc":"2.0","id":0,"error":{"code":-40100,"message":"Unauthorized"}},{"jsonrpc":"2.0","id":1,"error":{"code":-32604,"message":"Method not enabled"}}]`)}
	c = New(f, nil, f)
	if _, err := c.Batch(context.Background(), "n1", []Call{{"eth_blockNumber", nil}, {"net_peerCount", nil}}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("batch 帶 -40100: err = %v", err)
	}
}

// Transport failures (agent not connected, tunnel timeout) must be distinguishable from "the node returned an
// error": the collector uses this to decide whether the agent is online.
func TestBatchNoResponse(t *testing.T) {
	f := &fakeNode{}
	c := New(offlineTransport{}, nil, f)
	if _, err := c.Batch(context.Background(), "n1", []Call{{"eth_blockNumber", nil}}); !errors.Is(err, ErrNoResponse) {
		t.Fatalf("err = %v", err)
	}
	f = &fakeNode{status: 401}
	if _, err := New(f, nil, f).Batch(context.Background(), "n1", []Call{{"eth_blockNumber", nil}}); errors.Is(err, ErrNoResponse) {
		t.Fatal("401 是節點回的錯,不是沒回應")
	}
}

// The node returns an id outside this batch's range: skip that entry without panicking; the unmatched entry
// is still reported missing.
func TestBatchIgnoresOutOfRangeID(t *testing.T) {
	f := &fakeNode{body: []byte(`[{"jsonrpc":"2.0","id":1,"result":"0x1"}]`)}
	res, err := New(f, nil, f).Batch(context.Background(), "n1", []Call{{"eth_blockNumber", nil}})
	if err != nil || len(res) != 1 || res[0].Err == nil {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// removePeer fails: it must be listed in Failed, and that enode must also be listed in Unremoved (to be sent
// again next round).
func TestSyncPeersReportsUnremoved(t *testing.T) {
	node := rpcFunc(func(m string, _ []json.RawMessage) any {
		switch m {
		case "admin_addPeer":
			return true
		case "admin_removePeer":
			return &RPCError{Code: -32000, Message: "boom"}
		case "admin_nodeInfo":
			return map[string]any{"enode": "enode://aa@10.0.0.1:30303"}
		case "admin_peers":
			return []any{}
		}
		return &RPCError{Code: -32601, Message: "Method not found"}
	})
	drop := "enode://dd@10.0.0.4:30303"
	ps, err := New(node, nil, node).SyncPeers(context.Background(), "n1", []string{"enode://bb@10.0.0.2:30303"}, []string{drop})
	if err != nil {
		t.Fatal(err)
	}
	if len(ps.Failed) != 1 || !reflect.DeepEqual(ps.Unremoved, []string{drop}) {
		t.Fatalf("sync = %+v", ps)
	}
}

// headerTransport: records the headers of the last request and hands the response to fakeNode.
type headerTransport struct {
	*fakeNode
	got http.Header
}

func (h *headerTransport) PostLoopback(ctx context.Context, id string, port int, body []byte, hd http.Header) (int, []byte, error) {
	h.got = hd
	return h.fakeNode.PostLoopback(ctx, id, port, body, hd)
}

// With a signer, every request must carry a Bearer token.
func TestBatchSendsBearerToken(t *testing.T) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := auth.NewRpcSigner(k)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeNode{}
	tr := &headerTransport{fakeNode: f}
	if _, err := New(tr, signer, f).Batch(context.Background(), "n1", []Call{{"admin_peers", nil}}); err != nil {
		t.Fatal(err)
	}
	if got := tr.got.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") || len(got) <= len("Bearer ") {
		t.Fatalf("Authorization = %q", got)
	}
}

type offlineTransport struct{}

func (offlineTransport) PostLoopback(context.Context, string, int, []byte, http.Header) (int, []byte, error) {
	return 0, nil, errors.New("agent n1: 未連線")
}
