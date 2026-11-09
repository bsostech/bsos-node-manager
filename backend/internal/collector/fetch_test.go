package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/enode"
	"besu-manager/backend/internal/rpcops"
)

// fakeRequester: the agent side. Host metrics return a fixed answer and record the path; /logs returns logs, /agent-status returns agent (empty string = 404).
type fakeRequester struct {
	status int
	body   string
	err    error
	logs   string
	agent  string

	gotPath   string
	agentPath string
	logsPath  string
}

func (f *fakeRequester) Request(_ context.Context, _, path string) (int, string, error) {
	if strings.HasPrefix(path, "/logs") {
		f.logsPath = path
		if f.err != nil {
			return 0, "", f.err
		}
		if f.logs == "" {
			return http.StatusNotFound, "404 page not found", nil
		}
		return http.StatusOK, f.logs, nil
	}
	if strings.HasPrefix(path, "/agent-status") {
		f.agentPath = path
		if f.agent == "" {
			return http.StatusNotFound, "404 page not found", nil
		}
		return http.StatusOK, f.agent, nil
	}
	f.gotPath = path
	return f.status, f.body, f.err
}

// fakeRPC: the node RPC side. answers returns a result per method; unlisted methods return method not found (like calling ibft_ on a QBFT chain).
type fakeRPC struct {
	answers map[string]any
	errs    map[string]*rpcops.RPCError // returns the given error per method (takes precedence over answers)
	err     error
	probes  int
	batches int
}

func (f *fakeRPC) Batch(_ context.Context, _ string, calls []rpcops.Call) ([]rpcops.Result, error) {
	f.batches++
	if f.err != nil {
		return nil, f.err
	}
	out := make([]rpcops.Result, len(calls))
	for i, c := range calls {
		if e, ok := f.errs[c.Method]; ok {
			out[i] = rpcops.Result{Err: e}
		} else if v, ok := f.answers[c.Method]; ok {
			b, _ := json.Marshal(v)
			out[i] = rpcops.Result{Raw: b}
		} else {
			out[i] = rpcops.Result{Err: &rpcops.RPCError{Code: -32601, Message: "Method not found"}}
		}
	}
	return out, nil
}

func (f *fakeRPC) ProbeAuth(context.Context, string) string { f.probes++; return "enabled" }

func qbftNode() *fakeRPC {
	return &fakeRPC{answers: map[string]any{
		"eth_blockNumber":                 "0x64",
		"eth_syncing":                     false,
		"net_peerCount":                   "0x3",
		"net_enode":                       "enode://" + pub64 + "@10.0.0.1:30303",
		"admin_nodeInfo":                  map[string]any{"enode": "enode://" + pub64 + "@10.0.0.1:30303", "protocols": map[string]any{"eth": map[string]any{"config": map[string]any{"qbft": map[string]any{"epochLength": 100}}}}},
		"qbft_getValidatorsByBlockNumber": []string{"0x1111111111111111111111111111111111111111"},
		"qbft_getPendingVotes":            map[string]bool{"0x2222222222222222222222222222222222222222": true},
		"perm_getAccountsAllowlist":       []string{"0xabc"},
		"eth_getBlockByNumber":            map[string]any{"timestamp": "0x10", "transactions": []string{}},
		"web3_clientVersion":              "besu/v25.4.1/linux-x86_64/openjdk-java-21",
		"eth_chainId":                     "0x539",
	}}
}

const pub64 = "98f47e4807289e219b5a7ed048d6eeefd35431f7793d9b8fc7777bb32fa0e849c9d7d4d3d33884db2a20c927b333f06b03c7781e3cb50a433b4e6d3f4d1fb9ec"

// RPC reachable -> Besu layers built from RPC, host layer from host-metrics; consensus is decided by which method set returns validators.
func TestFetchBuildsFromRPC(t *testing.T) {
	req := &fakeRequester{status: http.StatusOK, body: `{"cpuPercent":12.5}`}
	rpc := qbftNode()
	c := New(nil, req, rpc)

	snap := c.fetch(context.Background(), "node-a", Result{}, true)
	if snap == nil || snap.Ledger == nil || *snap.Ledger.ChainHeight != 100 {
		t.Fatalf("鏈高度沒組出來:%+v", snap)
	}
	if req.gotPath != "/host-metrics" || snap.Hardware == nil || *snap.Hardware.CpuPercent != 12.5 {
		t.Fatalf("主機層:path=%s hw=%+v", req.gotPath, snap.Hardware)
	}
	if snap.Consensus == nil || *snap.Consensus.Algorithm != "qbft" || len(snap.PendingVotes) != 1 {
		t.Fatalf("共識:%+v", snap.Consensus)
	}
	if snap.EpochLength == nil || *snap.EpochLength != 100 {
		t.Fatalf("BFT 節點應讀到 epoch:%v", snap.EpochLength)
	}
	if *snap.P2p.PeerCount != 3 || snap.Allowlist == nil || (*snap.Allowlist)[0] != "0xabc" {
		t.Fatalf("p2p / allowlist:%+v %+v", snap.P2p, snap.Allowlist)
	}
	// qbftNode does not answer txpool_besuStatistics: TXPOOL is the only missing namespace.
	if len(snap.MissingAPIs) != 1 || snap.MissingAPIs[0] != "TXPOOL" {
		t.Fatalf("missing=%v", snap.MissingAPIs)
	}
	if snap.ClientVersion == nil || *snap.ClientVersion != "besu/v25.4.1/linux-x86_64/openjdk-java-21" {
		t.Fatalf("clientVersion=%v", snap.ClientVersion)
	}
	if snap.ChainID == nil || *snap.ChainID != 1337 {
		t.Fatalf("chainId=%v", snap.ChainID)
	}
	if snap.RpcAuth != "enabled" || rpc.batches != 2 {
		t.Fatalf("rpcAuth=%q batches=%d", snap.RpcAuth, rpc.batches)
	}
	// Non-probe rounds reuse the last result and do not probe again.
	again := c.fetch(context.Background(), "node-a", Result{Snap: snap}, false)
	if again.RpcAuth != "enabled" || rpc.probes != 1 {
		t.Fatalf("probes=%d", rpc.probes)
	}
	// The first round has no previous height, so no production distribution; it appears in the second round with the previous height
	rpc.answers["qbft_getSignerMetrics"] = []map[string]string{{"address": "0x1111111111111111111111111111111111111111", "proposedBlockCount": "0x5", "lastProposedBlockNumber": "0x64"}}
	first := c.fetch(context.Background(), "node-a", Result{}, false)
	if first.Consensus.ProposerMetrics != nil {
		t.Fatal("第一輪不該有出塊分布")
	}
	second := c.fetch(context.Background(), "node-a", Result{Snap: first}, false)
	if m := second.Consensus.ProposerMetrics; m == nil || len(*m) != 1 || (*m)[0].ProposedBlockCount != 5 || (*m)[0].LastProposedBlockNumber != 100 {
		t.Fatalf("第二輪應有出塊分布:%v", m)
	}
}

// Every method called each round must be in the platform token's permission list: miss one and Besu returns 401 for the whole batch, making the node RPC-unreachable.
func TestBatchMethodsPermitted(t *testing.T) {
	for _, c := range append(append([]rpcops.Call{}, firstBatch...), rpcops.Call{Method: "eth_getBlockByNumber"}) {
		if !rpcops.Permits(c.Method) {
			t.Errorf("%s 不在 rpcops.platformPermissions", c.Method)
		}
	}
}

// Agent present, RPC unreachable: only the host layer (the caller shows rpc_unreachable); no response and 401 must be distinguishable.
func TestFetchHostOnlyWhenRPCDown(t *testing.T) {
	for _, tc := range []struct {
		err          error
		unauthorized bool
	}{
		{errors.New("節點 RPC 回 HTTP 502"), false},
		{rpcops.ErrUnauthorized, true},
	} {
		c := New(nil, &fakeRequester{status: http.StatusOK, body: `{}`}, &fakeRPC{err: tc.err})
		snap := c.fetch(context.Background(), "node-a", Result{}, true)
		if snap == nil || snap.Hardware == nil || snap.Ledger != nil {
			t.Fatalf("應只有主機層:%+v", snap)
		}
		if snap.RpcUnauthorized != tc.unauthorized || snap.MissingAPIs != nil {
			t.Fatalf("%v: unauthorized=%v missing=%v", tc.err, snap.RpcUnauthorized, snap.MissingAPIs)
		}
	}
}

// Missing namespaces: missing only when both IBFT and QBFT are unavailable; PERM enabled without an account allowlist file is not a missing namespace and is recorded separately in AllowlistStatus.
func TestMissingAPIs(t *testing.T) {
	rpc := qbftNode()
	delete(rpc.answers, "admin_nodeInfo")
	delete(rpc.answers, "qbft_getValidatorsByBlockNumber")
	rpc.answers["txpool_besuStatistics"] = map[string]int{"localCount": 0, "remoteCount": 0}
	c := New(nil, &fakeRequester{status: http.StatusOK, body: `{}`}, rpc)
	snap := c.fetch(context.Background(), "node-a", Result{}, true)
	got := strings.Join(snap.MissingAPIs, ",")
	if got != "ADMIN,IBFT/QBFT" {
		t.Fatalf("missing=%s", got)
	}
	res := make([]rpcops.Result, len(firstBatch))
	res[iAllowlist] = rpcops.Result{Err: &rpcops.RPCError{Code: -32000, Message: "Account allowlist has not been enabled"}}
	if m := missingAPIs(res); len(m) != 0 {
		t.Fatalf("白名單檔沒啟用不算缺 namespace:%v", m)
	}
	res[iAllowlist] = rpcops.Result{Err: &rpcops.RPCError{Code: -32601, Message: "Method not found"}}
	if m := missingAPIs(res); len(m) != 1 || m[0] != "PERM" {
		t.Fatalf("PERM 方法不存在應算缺 PERM:%v", m)
	}
}

// Log ingest status: record connected as the agent reports it; a failed /logs request (here 404) is recorded as nil = unknown.
func TestFetchLogsConnected(t *testing.T) {
	c := New(nil, &fakeRequester{status: http.StatusOK, body: `{}`, logs: `{"epoch":1,"connected":true,"lines":[],"next":0,"skipped":0}`}, qbftNode())
	if snap := c.fetch(context.Background(), "node-a", Result{}, true); snap.LogsConnected == nil || !*snap.LogsConnected {
		t.Fatalf("connected 應為 true:%v", snap.LogsConnected)
	}
	c = New(nil, &fakeRequester{status: http.StatusOK, body: `{}`}, qbftNode())
	if snap := c.fetch(context.Background(), "node-a", Result{}, true); snap.LogsConnected != nil {
		t.Fatalf("/logs 失敗應為 nil:%v", *snap.LogsConnected)
	}
}

// Host metrics unavailable (timeout, agent just started and not sampled yet) but RPC responds: the tunnel works and the agent is online, so it must not be judged unreachable.
func TestFetchHostFailsButRPCAnswers(t *testing.T) {
	hostDown := &fakeRequester{status: http.StatusServiceUnavailable, body: "尚未完成首次取樣"}
	c := New(nil, hostDown, &fakeRPC{err: rpcops.ErrUnauthorized})
	snap := c.fetch(context.Background(), "node-a", Result{}, true)
	if snap == nil || !snap.RpcUnauthorized || snap.Ledger != nil {
		t.Fatalf("401 應保留 snapshot 並標被拒:%+v", snap)
	}
	c = New(nil, hostDown, qbftNode())
	if snap := c.fetch(context.Background(), "node-a", Result{}, true); snap == nil || snap.Ledger == nil {
		t.Fatalf("RPC 正常時應有帳本層:%+v", snap)
	}
}

// Both unavailable -> nil, and the caller treats the node as offline.
func TestFetchOfflineNodeReturnsNil(t *testing.T) {
	c := New(nil, &fakeRequester{err: errors.New("agent node-a 未連線")}, &fakeRPC{err: fmt.Errorf("%w: agent node-a 未連線", rpcops.ErrNoResponse)})
	if snap := c.fetch(context.Background(), "node-a", Result{}, true); snap != nil {
		t.Fatal("拉不到應回 nil")
	}
}

// Reasons the allowlist is unreadable are stored as an enum: PERM not enabled and allowlist file not enabled both count as disabled; AllowlistEnabled is false only when the allowlist file is not enabled.
func TestAllowlistStatus(t *testing.T) {
	no := false
	yes := true
	cases := []struct {
		name     string
		mutate   func(*fakeRPC)
		want     AllowlistStatus
		disabled error
		enabled  *bool
	}{
		{"讀得到", func(*fakeRPC) {}, AllowlistReadable, nil, &yes},
		{"PERM 沒開", func(f *fakeRPC) { delete(f.answers, "perm_getAccountsAllowlist") }, AllowlistPermMissing, rpcops.ErrPermUnavailable, nil},
		{"白名單檔沒啟用", func(f *fakeRPC) {
			f.errs = map[string]*rpcops.RPCError{"perm_getAccountsAllowlist": {Code: -32000, Message: "Account allowlist has not been enabled"}}
		}, AllowlistFileDisabled, rpcops.ErrAllowlistDisabled, &no},
		{"其他錯誤", func(f *fakeRPC) {
			f.errs = map[string]*rpcops.RPCError{"perm_getAccountsAllowlist": {Code: -32000, Message: "boom"}}
		}, AllowlistReadFailed, nil, nil},
	}
	for _, tc := range cases {
		rpc := qbftNode()
		tc.mutate(rpc)
		snap := New(nil, &fakeRequester{status: http.StatusOK, body: `{}`}, rpc).fetch(context.Background(), "node-a", Result{}, true)
		if got := snap.AllowlistStatus.DisabledErr(); snap.AllowlistStatus != tc.want || (got == nil) != (tc.disabled == nil) || !errors.Is(got, tc.disabled) {
			t.Errorf("%s: AllowlistStatus=%v want %v", tc.name, snap.AllowlistStatus, tc.want)
		}
		if got := snap.AllowlistEnabled(); (got == nil) != (tc.enabled == nil) || (got != nil && *got != *tc.enabled) {
			t.Errorf("%s: AllowlistEnabled=%v want %v", tc.name, got, tc.enabled)
		}
	}
}

// Node identity comes from net_enode: even without ADMIN the enode is available and the node can tell whether it is a validator; epoch length is only in admin_nodeInfo and is nil when unreadable.
func TestIdentityFromNetEnodeWithoutAdmin(t *testing.T) {
	rpc := qbftNode()
	delete(rpc.answers, "admin_nodeInfo")
	self, err := enode.Address("enode://" + pub64 + "@10.0.0.1:30303")
	if err != nil {
		t.Fatal(err)
	}
	rpc.answers["qbft_getValidatorsByBlockNumber"] = []string{"0x" + strings.ToUpper(self[2:])}
	snap := New(nil, &fakeRequester{status: http.StatusOK, body: `{}`}, rpc).fetch(context.Background(), "node-a", Result{}, true)
	if snap.P2p.Enode == nil || *snap.P2p.Enode != "enode://"+pub64+"@10.0.0.1:30303" {
		t.Fatalf("enode 應取自 net_enode:%v", snap.P2p.Enode)
	}
	if snap.Consensus.IsValidator == nil || !*snap.Consensus.IsValidator {
		t.Fatalf("由 net_enode 推出的地址應認得出自己是驗證者:%v", snap.Consensus.IsValidator)
	}
	if snap.EpochLength != nil {
		t.Fatalf("沒有 ADMIN 不該猜 epoch 長度:%d", *snap.EpochLength)
	}
}

// Epoch length: use the chain config value if set, otherwise Besu's default; block period: present only if set (the milliseconds variant wins); admin_nodeInfo unreadable or not a BFT chain = both unknown.
func TestBftConfig(t *testing.T) {
	ok := func(v any) rpcops.Result { b, _ := json.Marshal(v); return rpcops.Result{Raw: b} }
	cfg := func(algo string, o map[string]any) rpcops.Result {
		return ok(map[string]any{"protocols": map[string]any{"eth": map[string]any{"config": map[string]any{algo: o}}}})
	}
	withEpoch := cfg("ibft2", map[string]any{"epochLength": 500})
	withPeriod := cfg("qbft", map[string]any{"epochLength": 500, "blockPeriodSeconds": 2})
	withMillis := cfg("qbft", map[string]any{"blockPeriodSeconds": 1, "xBlockPeriodMilliSeconds": 500})
	noEpoch := ok(map[string]any{"enode": "enode://x@1.2.3.4:30303"})
	disabled := rpcops.Result{Err: &rpcops.RPCError{Code: -32604, Message: "Method not enabled"}}
	for _, tc := range []struct {
		r      rpcops.Result
		bft    bool
		epoch  int64   // 0 = nil
		period float64 // 0 = nil
	}{
		{withEpoch, true, 500, 0}, {withPeriod, true, 500, 2}, {withMillis, true, defaultEpochLength, 0.5},
		{noEpoch, true, defaultEpochLength, 0}, {disabled, true, 0, 0}, {withEpoch, false, 0, 0},
	} {
		e, p := bftConfig(tc.r, tc.bft)
		if (e == nil) != (tc.epoch == 0) || (e != nil && *e != tc.epoch) {
			t.Errorf("bft=%v epoch want %d got %v", tc.bft, tc.epoch, e)
		}
		if (p == nil) != (tc.period == 0) || (p != nil && *p != tc.period) {
			t.Errorf("bft=%v period want %g got %v", tc.bft, tc.period, p)
		}
	}
}

// net_enode undecodable: identity stays empty and the error is recorded; the collector logs once per failure stretch and resets once readable.
func TestEnodeWarnOnce(t *testing.T) {
	rpc := qbftNode()
	rpc.answers["net_enode"] = "not-an-enode"
	c := New(nil, &fakeRequester{status: http.StatusOK, body: `{}`}, rpc)
	snap := c.fetch(context.Background(), "node-a", Result{}, true)
	if snap.EnodeErr == nil || snap.P2p.Enode != nil {
		t.Fatalf("解不開應記錯誤、不填 enode:%v %v", snap.EnodeErr, snap.P2p.Enode)
	}
	c.warnEnode("node-a", snap)
	if !c.enodeWarned["node-a"] {
		t.Fatal("應記一次")
	}
	rpc.answers["net_enode"] = "enode://" + pub64 + "@10.0.0.1:30303"
	c.warnEnode("node-a", c.fetch(context.Background(), "node-a", Result{}, true))
	if c.enodeWarned["node-a"] {
		t.Fatal("讀到了應重置")
	}
}

// The production distribution uses the range from the previous round's height back 1000 blocks (end exclusive); without a previous height no range is sent and Besu's default applies.
func TestBatchForSignerRange(t *testing.T) {
	b := batchFor(5000)
	if p := b[iQbftSigners].Params; len(p) != 2 || p[0] != "0xfa0" || p[1] != "0x1388" || b[iIbftSigners].Params[0] != "0xfa0" {
		t.Fatalf("params %v", p)
	}
	if p := batchFor(300)[iQbftSigners].Params; p[0] != "0x1" {
		t.Fatalf("低於 1000 塊從 1 起(跳過創世塊): %v", p)
	}
	if batchFor(0)[iQbftSigners].Params != nil || firstBatch[iQbftSigners].Params != nil {
		t.Fatal("沒有上一輪高度不帶範圍,也不能改到 firstBatch")
	}
}

// The two values for txpool alerts: the sequenced pool carries a cap, layered (-1) does not; the full listing yields the oldest entry, an empty pool reads a listing but has no time; nothing is recorded without TXPOOL.
func TestFetchTxpoolLimitAndOldest(t *testing.T) {
	rpc := qbftNode()
	rpc.answers["txpool_besuStatistics"] = map[string]int{"localCount": 1, "remoteCount": 2, "maxSize": 4096}
	rpc.answers["txpool_besuTransactions"] = []map[string]any{
		{"hash": "0x1", "addedToPoolAt": "2026-10-29T05:00:10Z"},
		{"hash": "0x2", "addedToPoolAt": "2026-10-29T05:00:03.5Z"},
	}
	c := New(nil, &fakeRequester{status: http.StatusOK, body: `{}`}, rpc)
	snap := c.fetch(context.Background(), "node-a", Result{}, true)
	if *snap.Ledger.TxpoolPending != 3 || snap.TxpoolMax == nil || *snap.TxpoolMax != 4096 || !snap.TxpoolListed || snap.TxpoolOldest == nil || !snap.TxpoolOldest.Equal(time.Date(2026, 10, 29, 5, 0, 3, 5e8, time.UTC)) {
		t.Fatalf("max=%v listed=%v oldest=%v", snap.TxpoolMax, snap.TxpoolListed, snap.TxpoolOldest)
	}
	rpc.answers["txpool_besuStatistics"] = map[string]int{"localCount": 0, "remoteCount": 0, "maxSize": -1}
	rpc.answers["txpool_besuTransactions"] = []map[string]any{}
	snap = c.fetch(context.Background(), "node-a", Result{}, true)
	if snap.TxpoolMax != nil || !snap.TxpoolListed || snap.TxpoolOldest != nil {
		t.Fatalf("layered 空池:max=%v listed=%v oldest=%v", snap.TxpoolMax, snap.TxpoolListed, snap.TxpoolOldest)
	}
	// pool too large, listing not fetched: oldest age unknown
	rpc.answers["txpool_besuStatistics"] = map[string]int{"localCount": txpoolListMax + 1, "remoteCount": 0, "maxSize": -1}
	if snap = c.fetch(context.Background(), "node-a", Result{}, true); snap.TxpoolListed || snap.TxpoolOldest != nil {
		t.Fatal("超過上限不該拉清單")
	}
	delete(rpc.answers, "txpool_besuStatistics")
	delete(rpc.answers, "txpool_besuTransactions")
	snap = c.fetch(context.Background(), "node-a", Result{}, true)
	if snap.TxpoolListed || snap.TxpoolOldest != nil || snap.TxpoolMax != nil {
		t.Fatal("沒開 TXPOOL 不該記")
	}
}

// Log relay: limit is sent only when specified; lines from the agent are relayed as is, null becomes an empty array.
func TestLogsForwardsLimitAndLines(t *testing.T) {
	req := &fakeRequester{logs: `{"epoch":1,"connected":true,"lines":[{"seq":1,"text":"a"},{"seq":2,"text":"b"}],"next":3,"skipped":0}`}
	c := New(nil, req, nil)
	l, err := c.Logs(context.Background(), "node-a", nil, 5)
	if err != nil || req.logsPath != "/logs?limit=5" || len(l.Lines) != 2 {
		t.Fatalf("path=%s lines=%d err=%v", req.logsPath, len(l.Lines), err)
	}
	if _, err := c.Logs(context.Background(), "node-a", nil, 0); err != nil || req.logsPath != "/logs?" {
		t.Fatalf("limit 0 不該帶:%s %v", req.logsPath, err)
	}
	req.logs = `{"epoch":1,"connected":true,"lines":null,"next":0,"skipped":0}`
	if l, err := c.Logs(context.Background(), "node-a", nil, 0); err != nil || l.Lines == nil || len(l.Lines) != 0 {
		t.Fatalf("null 應為空陣列:%v %v", l.Lines, err)
	}
}

// eth_syncing: an object = syncing, behind by highest - current; false = synced.
func TestSyncStatus(t *testing.T) {
	if s := syncStatus(rpcops.Result{Raw: []byte(`{"currentBlock":"0x5","highestBlock":"0xa"}`)}); !s.syncing || s.blocksBehind != 5 {
		t.Fatalf("同步中:%+v", s)
	}
	if s := syncStatus(rpcops.Result{Raw: []byte(`false`)}); s.syncing || s.blocksBehind != 0 {
		t.Fatalf("已同步:%+v", s)
	}
}

// Exactly txpoolListMax transactions in the pool: still within the cap, so the listing is fetched.
func TestPoolOldestAtLimit(t *testing.T) {
	rpc := &fakeRPC{answers: map[string]any{"txpool_besuTransactions": []map[string]any{{"hash": "0x1", "addedToPoolAt": "2026-10-29T05:00:10Z"}}}}
	oldest, listed := poolOldest(context.Background(), rpc, "node-a", txpoolListMax)
	if !listed || oldest == nil || rpc.batches != 1 {
		t.Fatalf("listed=%v oldest=%v batches=%d", listed, oldest, rpc.batches)
	}
}

// blockTimesRPC: returns timestamps by block number, 2 transactions per block.
type blockTimesRPC map[int64]int64

func (f blockTimesRPC) Batch(_ context.Context, _ string, calls []rpcops.Call) ([]rpcops.Result, error) {
	out := make([]rpcops.Result, len(calls))
	for i, c := range calls {
		var n int64
		fmt.Sscanf(c.Params[0].(string), "0x%x", &n)
		b, _ := json.Marshal(map[string]any{"timestamp": fmt.Sprintf("0x%x", f[n]), "transactions": []string{"0x01", "0x02"}})
		out[i] = rpcops.Result{Raw: b}
	}
	return out, nil
}

func (blockTimesRPC) ProbeAuth(context.Context, string) string { return "enabled" }

// Block time = time difference between the latest block and sampleWindow blocks back / sampleWindow; tps uses the latest block's tx count; a zero time difference emits nothing (otherwise tps is +Inf).
func TestBlockTiming(t *testing.T) {
	var l api.LedgerSummary
	blockTiming(context.Background(), blockTimesRPC{100: 1000, 90: 980, 1: 0}, "node-a", 100, &l)
	if l.Tps == nil || *l.Tps != 1 {
		t.Fatalf("tps=%v", l.Tps)
	}
	l = api.LedgerSummary{}
	blockTiming(context.Background(), blockTimesRPC{100: 1000, 90: 1000}, "node-a", 100, &l)
	if l.Tps != nil {
		t.Fatalf("時間差 0:tps=%v", l.Tps)
	}
}
