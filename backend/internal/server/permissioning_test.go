package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/pki"
	"besu-manager/backend/internal/rpcops"
	"besu-manager/backend/internal/store/storetest"
)

func TestNormalizePolicyAddresses(t *testing.T) {
	valid := "0xABCDEFabcdefABCDEFabcdefABCDEFabcdefABCD"
	addresses, err := normalizePolicyAddresses([]string{valid, "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd"})
	if err != nil || len(addresses) != 1 || addresses[0] != "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd" {
		t.Fatalf("canonical addresses: %v %v", addresses, err)
	}
	for _, bad := range []string{"", "0x1234", valid + "\n", "\"", " " + valid} {
		if _, err := normalizePolicyAddresses([]string{bad}); err == nil {
			t.Fatalf("accepted invalid address %q", bad)
		}
	}
	if addresses, err := normalizePolicyAddresses(nil); err != nil || addresses == nil || len(addresses) != 0 {
		t.Fatal("empty allowlist must remain valid")
	}
}

func onlineSnap(allow *[]string, e collector.AllowlistStatus) *collector.Snapshot {
	h := int64(1)
	return &collector.Snapshot{Ledger: &api.LedgerSummary{ChainHeight: &h}, Allowlist: allow, AllowlistStatus: e}
}

// GET: no allowlist file and PERM not enabled are both disabled; a differing list is drift; a
// readable list is returned for "import from node".
func TestCachedPolicyState(t *testing.T) {
	want := []string{"0xaaaa"}
	have := []string{"0xAAAA"}
	other := []string{"0xbbbb"}
	cases := []struct {
		name  string
		snap  *collector.Snapshot
		state api.PolicyApplyNodeResultState
		list  *[]string
	}{
		{"不在線", nil, api.PolicyApplyUnreachable, nil},
		{"RPC 打不到", &collector.Snapshot{}, api.PolicyApplyUnreachable, nil},
		{"一致", onlineSnap(&have, collector.AllowlistReadable), api.PolicyApplyInSync, &have},
		{"不一致", onlineSnap(&other, collector.AllowlistReadable), api.PolicyApplyDrift, &other},
		{"沒開白名單檔", onlineSnap(nil, collector.AllowlistFileDisabled), api.PolicyApplyDisabled, nil},
		{"沒開 PERM", onlineSnap(nil, collector.AllowlistPermMissing), api.PolicyApplyDisabled, nil},
		{"讀取失敗", onlineSnap(nil, collector.AllowlistReadFailed), api.PolicyApplyUnreachable, nil},
	}
	for _, tc := range cases {
		r := cachedPolicyState("n1", tc.snap, nil, want)
		if r.State != tc.state || r.CurrentAllowlist != tc.list {
			t.Errorf("%s: state=%s list=%v", tc.name, r.State, r.CurrentAllowlist)
		}
		if tc.state == api.PolicyApplyDisabled && (r.Reason == nil || r.Error == nil) {
			t.Errorf("%s: disabled 要說明是哪個設定", tc.name)
		}
		if tc.name == "讀取失敗" && (r.Reason == nil || *r.Reason != api.ErrorReasonAllowlistReadFailed) {
			t.Errorf("讀取失敗要帶 allowlist_read_failed: %+v", r)
		}
	}
}

// Right after a save the collector's round is older than the push: GET compares the list read back
// by the push, not the stale round. A round that started after the push wins again, so a real
// drift found later still shows.
func TestCachedPolicyStateAfterPush(t *testing.T) {
	want := []string{"0xaaaa"}
	old := []string{"0xbbbb"}
	t0 := time.Now()
	stale := onlineSnap(&old, collector.AllowlistReadable)
	stale.AsOf = t0
	pushed := &pushedAllowlist{list: []string{"0xAAAA"}, at: t0.Add(time.Second)}
	if r := cachedPolicyState("n1", stale, pushed, want); r.State != api.PolicyApplyInSync || r.CurrentAllowlist == nil || (*r.CurrentAllowlist)[0] != "0xAAAA" {
		t.Fatalf("round older than the push: %+v", r)
	}
	fresh := onlineSnap(&old, collector.AllowlistReadable)
	fresh.AsOf = t0.Add(2 * time.Second)
	if r := cachedPolicyState("n1", fresh, pushed, want); r.State != api.PolicyApplyDrift || (*r.CurrentAllowlist)[0] != "0xbbbb" {
		t.Fatalf("round newer than the push must show its own list: %+v", r)
	}
	if r := cachedPolicyState("n1", nil, pushed, want); r.State != api.PolicyApplyUnreachable {
		t.Fatalf("offline stays unreachable: %+v", r)
	}
}

// PUT: both PermErrs returned by SyncAllowlist are disabled; inconsistent read-back is drift; the
// read-back list is returned.
func TestSyncedPolicyState(t *testing.T) {
	want := []string{"0xaaaa"}
	reasonOf := func(r api.PolicyApplyNodeResult) api.ErrorReason {
		if r.Reason == nil {
			return ""
		}
		return *r.Reason
	}
	for err, reason := range map[error]api.ErrorReason{rpcops.ErrPermUnavailable: api.ErrorReasonAllowlistPermUnavailable, rpcops.ErrAllowlistDisabled: api.ErrorReasonAllowlistFileDisabled} {
		if r := syncedPolicyState("n1", nil, err, want); r.State != api.PolicyApplyDisabled || reasonOf(r) != reason || r.Error == nil || *r.Error != apierr.Message(reason, nil) {
			t.Errorf("%v: %+v", err, r)
		}
	}
	// agent not connected: the status is simply unreachable, no separate reason (even though the
	// error itself carries node_unreachable)
	offline := apierr.Wrap(api.ErrorReasonNodeUnreachable, map[string]string{"nodeId": "n1"}, fmt.Errorf("agent n1: %w", pki.ErrAgentOffline))
	if r := syncedPolicyState("n1", nil, offline, want); r.State != api.PolicyApplyUnreachable || r.Reason != nil || r.Error != nil {
		t.Errorf("offline: %+v", r)
	}
	// Besu returns a JSON-RPC error (reached it, it refused) = rejected: failing to write the file
	// has its own reason, others carry the raw text as node_rpc_error
	persist := rpcops.PermErr(fmt.Errorf("perm_addAccountsToAllowlist: %w", &rpcops.RPCError{Code: -32000, Message: "Unable to persist changes to allowlist configuration file. Changes reverted"}))
	if r := syncedPolicyState("n1", nil, persist, want); r.State != api.PolicyApplyRejected || reasonOf(r) != api.ErrorReasonAllowlistPersistFailed {
		t.Fatalf("persist: %+v", r)
	}
	other := apierr.Wrap(api.ErrorReasonNodeRpcError, map[string]string{"method": "perm_addAccountsToAllowlist", "code": "-32000", "message": "boom"}, fmt.Errorf("perm_addAccountsToAllowlist: %w", &rpcops.RPCError{Code: -32000, Message: "boom"}))
	if r := syncedPolicyState("n1", nil, other, want); r.State != api.PolicyApplyRejected || reasonOf(r) != api.ErrorReasonNodeRpcError || (*r.Params)["message"] != "boom" {
		t.Fatalf("besu error: %+v", r)
	}
	// other errors (unreachable): use the reason carried by the underlying error if any, otherwise
	// operation_failed + raw text
	noResp := apierr.Wrap(api.ErrorReasonNodeRpcNoResponse, map[string]string{"detail": "connection refused"}, fmt.Errorf("%w: connection refused", rpcops.ErrNoResponse))
	if r := syncedPolicyState("n1", nil, noResp, want); r.State != api.PolicyApplyUnreachable || reasonOf(r) != api.ErrorReasonNodeRpcNoResponse || (*r.Params)["detail"] != "connection refused" {
		t.Errorf("no response: %+v", r)
	}
	if r := syncedPolicyState("n1", nil, errors.New("connection refused"), want); r.State != api.PolicyApplyUnreachable || reasonOf(r) != api.ErrorReasonOperationFailed || (*r.Params)["detail"] != "connection refused" {
		t.Errorf("unreachable: %+v", r)
	}
	if r := syncedPolicyState("n1", []string{"0xbbbb"}, nil, want); r.State != api.PolicyApplyDrift || r.CurrentAllowlist == nil || (*r.CurrentAllowlist)[0] != "0xbbbb" {
		t.Errorf("drift: %+v", r)
	}
	if r := syncedPolicyState("n1", []string{"0xAAAA"}, nil, want); r.State != api.PolicyApplyInSync {
		t.Errorf("in sync: %+v", r)
	}
}

// Effective list: with the toggle on it is shared union custom, off it is custom only; duplicates
// and case are normalized.
func TestEffectiveAllowlist(t *testing.T) {
	a, b, c := "0x"+strings.Repeat("a", 40), "0x"+strings.Repeat("b", 40), "0x"+strings.Repeat("c", 40)
	if got := effectiveAllowlist([]string{a, b}, true, []string{"0x" + strings.Repeat("B", 40), c}); fmt.Sprint(got) != fmt.Sprint([]string{a, b, c}) {
		t.Errorf("開著: %v", got)
	}
	if got := effectiveAllowlist([]string{a}, false, []string{c}); fmt.Sprint(got) != fmt.Sprint([]string{c}) {
		t.Errorf("關著: %v", got)
	}
	if got := effectiveAllowlist(nil, false, nil); got == nil || len(got) != 0 {
		t.Errorf("都沒有要是空名單: %v", got)
	}
	if got := without([]string{a, b}, []string{b}); fmt.Sprint(got) != fmt.Sprint([]string{a}) {
		t.Errorf("without: %v", got)
	}
}

// allowOps: records which list SyncAllowlist pushed to which node; the fail node returns an error,
// the drift node reads back a different list, the rest read back the pushed list.
type allowOps struct {
	NodeOps
	mu    sync.Mutex
	calls map[string][]string
	fail  map[string]error
	drift map[string]bool
	live  map[string][]string // what ReadAllowlist reads; unlisted nodes have an empty list
}

func (o *allowOps) ReadAllowlist(_ context.Context, nodeID string) ([]string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string{}, o.live[nodeID]...), nil
}

func (o *allowOps) SyncAllowlist(_ context.Context, nodeID string, want []string) ([]string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls[nodeID] = want
	if err := o.fail[nodeID]; err != nil {
		return nil, err
	}
	if o.drift[nodeID] {
		return []string{}, nil
	}
	return want, nil
}

// downRequester: host metrics are never available (tests only the RPC layer).
type downRequester struct{}

func (downRequester) Request(context.Context, string, string) (int, string, error) {
	return 0, "", errors.New("no host metrics")
}

// allowRPC: every node returns height and allowlist (nodes not in have return an empty list); the
// down node is entirely unreachable.
type allowRPC struct {
	down string
	have map[string]string // node id -> JSON of perm_getAccountsAllowlist
}

func (f allowRPC) Batch(_ context.Context, nodeID string, calls []rpcops.Call) ([]rpcops.Result, error) {
	if nodeID == f.down {
		return nil, rpcops.ErrNoResponse
	}
	out := make([]rpcops.Result, len(calls))
	for i, c := range calls {
		switch c.Method {
		case "eth_blockNumber":
			out[i] = rpcops.Result{Raw: []byte(`"0x10"`)}
		case "perm_getAccountsAllowlist":
			raw := `[]`
			if h, ok := f.have[nodeID]; ok {
				raw = h
			}
			out[i] = rpcops.Result{Raw: []byte(raw)}
		default:
			out[i] = rpcops.Result{Err: &rpcops.RPCError{Code: -32601, Message: "Method not found"}}
		}
	}
	return out, nil
}

func (allowRPC) ProbeAuth(context.Context, string) string { return "disabled" }

// Shared list union each node's custom additions: saving shared pushes to nodes with the toggle on
// (each gets its own effective list); custom additions drop entries already in shared; after
// turning the toggle off, saving shared does not push to it; turning it on drops duplicates; a
// single node's write failure does not save; clear, reapply.
func TestNodeAllowlistModel(t *testing.T) {
	s, _ := accessTestServer(t)
	audited := recordAudit(t)
	ctx := asAdmin(context.Background())
	for _, id := range []string{"node-a1", "node-a2", "node-a3"} {
		if err := s.store.UpsertOwnNode(ctx, id, "org-test", 8545, 9545, nil); err != nil {
			t.Fatal(err)
		}
	}
	s.col = collector.New(s.store, downRequester{}, allowRPC{})
	a, b, c := "0x"+strings.Repeat("a", 40), "0x"+strings.Repeat("b", 40), "0x"+strings.Repeat("c", 40)
	o := &allowOps{calls: map[string][]string{}, fail: map[string]error{}, drift: map[string]bool{}}
	s.ops = o
	reset := func() { o.calls = map[string][]string{} }
	custom := func() map[string]api.NodeAllowlist {
		g, err := s.GetPermissioningPolicy(ctx, api.GetPermissioningPolicyRequestObject{})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]api.NodeAllowlist{}
		for _, c := range *g.(api.GetPermissioningPolicy200JSONResponse).Custom {
			out[c.NodeId] = c
		}
		return out
	}
	setNode := func(id string, rev int64, list ...string) api.SetNodeAllowlistResponseObject {
		r, err := s.SetNodeAllowlist(ctx, api.SetNodeAllowlistRequestObject{Id: id, Body: &api.PermissioningPolicy{Revision: rev, AccountsAllowlist: list}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	setShared := func(rev int64, list ...string) api.SetPermissioningPolicy200JSONResponse {
		r, err := s.SetPermissioningPolicy(ctx, api.SetPermissioningPolicyRequestObject{Body: &api.PermissioningPolicy{Revision: rev, AccountsAllowlist: list}})
		res, ok := r.(api.SetPermissioningPolicy200JSONResponse)
		if err != nil || !ok {
			t.Fatalf("存共用: %#v %v", r, err)
		}
		return res
	}
	toggle := func(id string, on bool) api.SetNodeAllowlistShared200JSONResponse {
		r, err := s.SetNodeAllowlistShared(ctx, api.SetNodeAllowlistSharedRequestObject{Id: id, Body: &api.NodeAllowlistSharedRequest{UseShared: on}})
		res, ok := r.(api.SetNodeAllowlistShared200JSONResponse)
		if err != nil || !ok {
			t.Fatalf("切換 %s: %#v %v", id, r, err)
		}
		return res
	}
	str := fmt.Sprint

	// this node's own entries can be added even while the shared list is empty
	if res, ok := setNode("node-a2", 0, b).(api.SetNodeAllowlist200JSONResponse); !ok || res.PolicyRevision != 1 || str(o.calls["node-a2"]) != str([]string{b}) {
		t.Fatalf("自行加入: %#v calls=%v", res, o.calls)
	}
	if d := audited("permissioning_node_set"); len(d) == 0 || d[0]["created"] != true {
		t.Fatalf("稽核: %v", d)
	}
	// save shared: all three toggles on, a2 gets shared union its own
	reset()
	if res := setShared(0, a); len(res.Nodes) != 3 || str(o.calls["node-a2"]) != str([]string{a, b}) || str(o.calls["node-a1"]) != str([]string{a}) {
		t.Fatalf("存共用: %#v calls=%v", res, o.calls)
	}
	// a successful push keeps what it read back for GET until the next collection round
	if v, ok := s.pushedAllowlists.Load("node-a2"); !ok || str(v.(pushedAllowlist).list) != str([]string{a, b}) {
		t.Fatalf("push read-back not kept: %v %v", v, ok)
	}
	// with the toggle on, custom entries already in shared are not stored separately
	if res, ok := setNode("node-a2", 1, a, b).(api.SetNodeAllowlist200JSONResponse); !ok || res.PolicyRevision != 2 || str(custom()["node-a2"].AccountsAllowlist) != str([]string{b}) {
		t.Fatalf("剔除共用有的: %#v %v", res, custom())
	}
	// adding only entries already in shared = no custom addresses: no row created, revision 0
	if res, ok := setNode("node-a1", 0, a).(api.SetNodeAllowlist200JSONResponse); !ok || res.PolicyRevision != 0 {
		t.Fatalf("剔完為空: %#v", res)
	}
	if _, ok := custom()["node-a1"]; ok {
		t.Fatal("剔完為空不該建列")
	}

	// turn a2 off: only its own remain; later shared saves do not push to it
	reset()
	if res := toggle("node-a2", false); str(o.calls["node-a2"]) != str([]string{b}) || res.PolicyRevision != 2 {
		t.Fatalf("關掉: %#v calls=%v", res, o.calls)
	}
	if c := custom()["node-a2"]; c.UseShared {
		t.Fatalf("GET 要帶開關: %#v", c)
	}
	reset()
	if res := setShared(1, a, c); len(res.Nodes) != 2 || o.calls["node-a2"] != nil {
		t.Fatalf("存共用不推關著的: %#v calls=%v", res, o.calls)
	}
	// with the toggle off, custom additions are stored as-is (not deduplicated)
	if res, ok := setNode("node-a2", 2, a, b).(api.SetNodeAllowlist200JSONResponse); !ok || res.PolicyRevision != 3 || str(custom()["node-a2"].AccountsAllowlist) != str([]string{a, b}) {
		t.Fatalf("關著照存: %#v %v", res, custom())
	}
	// turn on: drop entries already in shared, push shared union the rest
	reset()
	if res := toggle("node-a2", true); res.PolicyRevision != 4 || str(o.calls["node-a2"]) != str([]string{a, b, c}) || str(custom()["node-a2"].AccountsAllowlist) != str([]string{b}) {
		t.Fatalf("打開: %#v calls=%v custom=%v", res, o.calls, custom())
	}
	if d := audited("permissioning_node_shared_set"); len(d) == 0 || d[0]["use_shared"] != false {
		t.Fatalf("開關稽核: %v", d)
	}
	// a node with no custom addresses turned off: GET must still list it
	toggle("node-a1", false)
	if c, ok := custom()["node-a1"]; !ok || c.Revision != 0 || c.UseShared || len(c.AccountsAllowlist) != 0 {
		t.Fatalf("關著沒有地址: %#v", custom())
	}

	// a single node's write failure does not save: rejected -> 502, inconsistent read-back -> 502, offline -> 409
	o.fail["node-a3"] = rpcops.PermErr(&rpcops.RPCError{Code: -32000, Message: "Unable to persist changes to allowlist configuration file. Changes reverted"})
	if r, ok := setNode("node-a3", 0, b).(api.SetNodeAllowlist502JSONResponse); !ok || !hasReason(api.Error(r), api.ErrorReasonAllowlistPersistFailed) {
		t.Fatalf("拒絕: %#v", r)
	}
	o.fail["node-a3"] = &rpcops.RPCError{Code: -32000, Message: "boom"} // no reason from the underlying error: operation_failed + raw text
	if r, ok := setNode("node-a3", 0, b).(api.SetNodeAllowlist502JSONResponse); !ok || !hasReason(api.Error(r), api.ErrorReasonOperationFailed) || !strings.Contains((*r.Params)["detail"], "boom") {
		t.Fatalf("拒絕(其他): %#v", r)
	}
	o.fail["node-a3"] = rpcops.ErrAllowlistDisabled
	if r, ok := setNode("node-a3", 0, b).(api.SetNodeAllowlist409JSONResponse); !ok || !hasReason(api.Error(r), api.ErrorReasonAllowlistDisabled) || (*r.Params)["nodeId"] != "node-a3" {
		t.Fatalf("沒啟用: %#v", r)
	}
	o.fail["node-a3"] = pki.ErrAgentOffline
	if r, ok := setNode("node-a3", 0, b).(api.SetNodeAllowlist409JSONResponse); !ok || !hasReason(api.Error(r), api.ErrorReasonNodeUnreachable) || (*r.Params)["nodeId"] != "node-a3" {
		t.Fatalf("離線: %#v", r)
	}
	if r, err := s.SyncNodeAllowlist(ctx, api.SyncNodeAllowlistRequestObject{Id: "node-a3"}); err != nil {
		t.Fatal(err)
	} else if _, ok := r.(api.SyncNodeAllowlist409JSONResponse); !ok {
		t.Fatalf("重新套用離線: %#v", r)
	}
	delete(o.fail, "node-a3")
	o.drift["node-a3"] = true
	if r, ok := setNode("node-a3", 0, b).(api.SetNodeAllowlist502JSONResponse); !ok || !hasReason(api.Error(r), api.ErrorReasonAllowlistMismatch) || (*r.Params)["nodeId"] != "node-a3" {
		t.Fatalf("讀回不一致: %#v", r)
	}
	if r, err := s.SetNodeAllowlistShared(ctx, api.SetNodeAllowlistSharedRequestObject{Id: "node-a3", Body: &api.NodeAllowlistSharedRequest{UseShared: true}}); err != nil {
		t.Fatal(err)
	} else if _, ok := r.(api.SetNodeAllowlistShared502JSONResponse); !ok {
		t.Fatalf("切換讀回不一致: %#v", r)
	}
	delete(o.drift, "node-a3")
	if c, ok := custom()["node-a3"]; ok {
		t.Fatalf("失敗不該存: %#v", c)
	}
	var failed []map[string]any
	for _, d := range audited("permissioning_node_set") {
		if d["result"] == "failed" {
			failed = append(failed, d)
		}
	}
	if len(failed) != 5 {
		t.Fatalf("失敗要記稽核 5 筆,拿到 %d", len(failed))
	}
	// audit stores the code; the node id goes in reason_params
	if p, _ := failed[3]["reason_params"].(map[string]string); failed[3]["reason"] != string(api.ErrorReasonNodeUnreachable) || p["nodeId"] != "node-a3" {
		t.Fatalf("離線的稽核 %+v", failed[3])
	}

	// reapply: push the effective list, return the shared list's version
	reset()
	if r, err := s.SyncNodeAllowlist(ctx, api.SyncNodeAllowlistRequestObject{Id: "node-a2"}); err != nil {
		t.Fatal(err)
	} else if res, ok := r.(api.SyncNodeAllowlist200JSONResponse); !ok || res.PolicyRevision != 2 || str(o.calls["node-a2"]) != str([]string{a, b, c}) {
		t.Fatalf("重新套用: %#v calls=%v", r, o.calls)
	}
	// clear: push the shared list, toggle unchanged; clearing again is 404
	reset()
	if r, err := s.DeleteNodeAllowlist(ctx, api.DeleteNodeAllowlistRequestObject{Id: "node-a2"}); err != nil {
		t.Fatal(err)
	} else if res, ok := r.(api.DeleteNodeAllowlist200JSONResponse); !ok || res.PolicyRevision != 2 || str(o.calls["node-a2"]) != str([]string{a, c}) {
		t.Fatalf("清空: %#v calls=%v", r, o.calls)
	}
	if d := audited("permissioning_node_removed"); len(d) == 0 || str(d[0]["removed"]) != str([]string{b}) {
		t.Fatalf("清空稽核: %v", d)
	}
	if r, _ := s.DeleteNodeAllowlist(ctx, api.DeleteNodeAllowlistRequestObject{Id: "node-a2"}); r == nil {
		t.Fatal("nil")
	} else if _, ok := r.(api.DeleteNodeAllowlist404JSONResponse); !ok {
		t.Fatalf("再清應 404: %#v", r)
	}
	// the shared list can be saved empty
	if res := setShared(2); res.PolicyRevision != 3 {
		t.Fatalf("存空共用: %#v", res)
	}
	// stale version is 409
	if r := setNode("node-a2", 5, b); r == nil {
		t.Fatal("nil")
	} else if _, ok := r.(api.SetNodeAllowlist409JSONResponse); !ok {
		t.Fatalf("舊版本應 409: %#v", r)
	}

	// read-only fields (nodes, custom) have no effect if sent: request validation does not reject
	// them (the contract says "ignored on PUT") and the handler does not read them
	before := custom()
	reset()
	junk := "0x" + strings.Repeat("e", 40)
	r, err := s.SetPermissioningPolicy(ctx, api.SetPermissioningPolicyRequestObject{Body: &api.PermissioningPolicy{Revision: 3, AccountsAllowlist: []string{a, c},
		Custom: &[]api.NodeAllowlist{{NodeId: "node-a1", AccountsAllowlist: []string{junk}, UseShared: true, Revision: 9}},
		Nodes:  &[]api.PolicyApplyNodeResult{{NodeId: "node-a9", State: api.PolicyApplyInSync}}}})
	if _, ok := r.(api.SetPermissioningPolicy200JSONResponse); err != nil || !ok {
		t.Fatalf("帶唯讀欄位存共用: %#v %v", r, err)
	}
	if str(custom()) != str(before) || strings.Contains(str(o.calls), junk) || o.calls["node-a9"] != nil {
		t.Fatalf("唯讀欄位不該被存或推: custom=%v calls=%v", custom(), o.calls)
	}
	// single node (PUT /nodes/{id}/allowlist uses the same schema): read-only fields sent, only
	// accountsAllowlist is stored
	reset()
	nr, err := s.SetNodeAllowlist(ctx, api.SetNodeAllowlistRequestObject{Id: "node-a1", Body: &api.PermissioningPolicy{Revision: custom()["node-a1"].Revision, AccountsAllowlist: []string{b},
		Custom: &[]api.NodeAllowlist{{NodeId: "node-a2", AccountsAllowlist: []string{junk}}}}})
	if _, ok := nr.(api.SetNodeAllowlist200JSONResponse); err != nil || !ok {
		t.Fatalf("單台帶唯讀欄位: %#v %v", nr, err)
	}
	if got := custom(); str(got["node-a1"].AccountsAllowlist) != str([]string{b}) || str(got["node-a2"]) != str(before["node-a2"]) || strings.Contains(str(o.calls), junk) {
		t.Fatalf("單台唯讀欄位不該被存或推: custom=%v calls=%v", got, o.calls)
	}
}

// Address names: stored lowercase and trimmed; name is required and capped at nodeNameMax (the cap
// itself is allowed); gone after delete; deleting an invalid address still returns 204.
func TestAccountNames(t *testing.T) {
	s, _ := accessTestServer(t)
	audited := recordAudit(t)
	ctx := asAdmin(context.Background())
	addr := "0x" + strings.Repeat("A", 40)
	set := func(a, name string) api.SetAccountNameResponseObject {
		r, err := s.SetAccountName(ctx, api.SetAccountNameRequestObject{Address: a, Body: &api.PeerNameRequest{Name: name}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if _, ok := set(addr, strings.Repeat("名", nodeNameMax)).(api.SetAccountName204Response); !ok {
		t.Errorf("剛好 %d 字應可設定", nodeNameMax)
	}
	if _, ok := set(addr, "  客戶 A  ").(api.SetAccountName204Response); !ok {
		t.Fatal("設定失敗")
	}
	for _, bad := range []struct{ a, n string }{{"0x1234", "x"}, {addr, "  "}, {addr, strings.Repeat("名", nodeNameMax+1)}} {
		if _, ok := set(bad.a, bad.n).(api.SetAccountName400JSONResponse); !ok {
			t.Errorf("%q %q 應 400", bad.a, bad.n)
		}
	}
	got, _ := s.ListAccountNames(ctx, api.ListAccountNamesRequestObject{})
	if m := got.(api.ListAccountNames200JSONResponse); len(m) != 1 || m[strings.ToLower(addr)] != "客戶 A" {
		t.Fatalf("清單: %v", m)
	}
	if r, _ := s.DeleteAccountName(ctx, api.DeleteAccountNameRequestObject{Address: "nope"}); r == nil {
		t.Fatal("nil")
	} else if e, ok := r.(api.DeleteAccountName400JSONResponse); !ok || e.Reason == nil || *e.Reason != api.ErrorReasonFieldFormat {
		t.Fatalf("不合法的地址應 400 field_format: %#v", r)
	}
	if _, err := s.DeleteAccountName(ctx, api.DeleteAccountNameRequestObject{Address: addr}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListAccountNames(ctx, api.ListAccountNamesRequestObject{})
	if m := got.(api.ListAccountNames200JSONResponse); len(m) != 0 {
		t.Fatalf("刪除後: %v", m)
	}
	if len(audited("account_name_set")) != 2 || len(audited("account_name_deleted")) != 1 {
		t.Fatalf("稽核: set=%v deleted=%v", audited("account_name_set"), audited("account_name_deleted"))
	}
}

// Audit cannot be written: a single node's write is neither pushed to the node nor stored.
func TestNodeAllowlistAuditFailClosed(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := asAdmin(context.Background())
	if err := s.store.UpsertOwnNode(ctx, "n1", "org-test", 8545, 9545, nil); err != nil {
		t.Fatal(err)
	}
	o := &allowOps{calls: map[string][]string{}}
	s.ops = o
	storetest.FailAuditForTest(t)
	b := "0x" + strings.Repeat("b", 40)
	if _, err := s.SetNodeAllowlist(ctx, api.SetNodeAllowlistRequestObject{Id: "n1", Body: &api.PermissioningPolicy{AccountsAllowlist: []string{b}}}); !errors.Is(err, errAudit) {
		t.Fatalf("自行加入: %v", err)
	}
	if _, err := s.SetNodeAllowlistShared(ctx, api.SetNodeAllowlistSharedRequestObject{Id: "n1", Body: &api.NodeAllowlistSharedRequest{UseShared: false}}); !errors.Is(err, errAudit) {
		t.Fatalf("切換: %v", err)
	}
	if len(o.calls) != 0 {
		t.Fatalf("稽核失敗不該推: %v", o.calls)
	}
	if n, _ := s.store.GetNode(ctx, "n1"); n == nil || !n.AllowlistShared {
		t.Fatalf("開關不該變: %+v", n)
	}
	if p, _ := s.store.ListPermissioningPolicies(ctx); len(p) != 1 {
		t.Fatalf("不該建列: %v", p)
	}
}

// Newly joined node: push the effective list once only if its allowlist is empty; leave nodes that
// already have addresses alone; if unreadable or the cache predates joining, wait for the next
// round; once pushed, never push again.
func TestInitialAllowlist(t *testing.T) {
	s, pool := accessTestServer(t)
	audited := recordAudit(t)
	ctx := asAdmin(context.Background())
	for _, id := range []string{"node-a1", "node-a2", "node-a3", "node-a4", "node-a5"} {
		if err := s.store.UpsertOwnNode(ctx, id, "org-test", 8545, 9545, nil); err != nil {
			t.Fatal(err)
		}
	}
	a, c := "0x"+strings.Repeat("a", 40), "0x"+strings.Repeat("c", 40)
	o := &allowOps{calls: map[string][]string{}, fail: map[string]error{}, drift: map[string]bool{}}
	s.ops = o
	if _, err := s.SetPermissioningPolicy(ctx, api.SetPermissioningPolicyRequestObject{Body: &api.PermissioningPolicy{AccountsAllowlist: []string{a}}}); err != nil {
		t.Fatal(err)
	}
	o.calls = map[string][]string{}
	// a1 empty allowlist, a2 already has other addresses, a3 unreachable
	s.col = collector.New(s.store, downRequester{}, allowRPC{down: "node-a3", have: map[string]string{"node-a2": `["` + c + `"]`}})
	pollCtx, cancel := context.WithCancel(context.Background())
	go s.col.Run(pollCtx, time.Hour)
	for i := 0; i < 100; i++ {
		_, ok1 := s.col.Get("node-a1")
		_, ok2 := s.col.Get("node-a2")
		_, ok3 := s.col.Get("node-a3")
		_, ok4 := s.col.Get("node-a4")
		_, ok5 := s.col.Get("node-a5")
		if ok1 && ok2 && ok3 && ok4 && ok5 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	// a5's cache is empty, but a live read just before pushing finds addresses on the node (added
	// after the cache): do not push, clear the flag
	o.live = map[string][]string{"node-a5": {c}}
	// a4's cache predates the join time (e.g. collector still holds the old node after delete and
	// rejoin): not trustworthy, wait for the next round
	if _, err := pool.Exec(ctx, `UPDATE nodes SET allowlist_initial_since = now() + interval '1 hour' WHERE id = 'node-a4'`); err != nil {
		t.Fatal(err)
	}

	if err := s.pushInitialAllowlists(ctx); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(o.calls) != fmt.Sprint(map[string][]string{"node-a1": {a}}) {
		t.Fatalf("只該推 a1 的實際名單: %v", o.calls)
	}
	if d := audited("permissioning_node_initial"); len(d) == 0 || d[len(d)-1]["node_id"] != "node-a1" {
		t.Fatalf("稽核: %v", d)
	}
	pending := func(id string) bool {
		n, err := s.store.GetNode(ctx, id)
		if err != nil || n == nil {
			t.Fatal(err)
		}
		return n.AllowlistInitialSince != nil
	}
	if pending("node-a1") || pending("node-a2") || !pending("node-a3") || !pending("node-a4") || pending("node-a5") {
		t.Fatalf("旗標:a1=%v a2=%v a3=%v a4=%v a5=%v,應只剩 a3、a4", pending("node-a1"), pending("node-a2"), pending("node-a3"), pending("node-a4"), pending("node-a5"))
	}
	o.calls = map[string][]string{}
	if err := s.pushInitialAllowlists(ctx); err != nil || len(o.calls) != 0 {
		t.Fatalf("推過的不該再推: %v %v", o.calls, err)
	}
}

func hasReason(e api.Error, want api.ErrorReason) bool { return e.Reason != nil && *e.Reason == want }
