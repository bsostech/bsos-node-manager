package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/rpcops"
	"besu-manager/backend/internal/store"
	"besu-manager/backend/internal/store/storetest"
)

// ── fixture: a small BFT chain seen through our own nodes ──

// rlpStr / rlpList: minimal RLP encoding, enough to build IBFT2 / QBFT extraData.
func rlpStr(b []byte) []byte {
	if len(b) == 1 && b[0] < 0x80 {
		return b
	}
	return append(rlpHead(0x80, len(b)), b...)
}

func rlpList(items ...[]byte) []byte {
	var payload []byte
	for _, it := range items {
		payload = append(payload, it...)
	}
	return append(rlpHead(0xc0, len(payload)), payload...)
}

func rlpHead(base byte, n int) []byte {
	if n < 56 {
		return []byte{base + byte(n)}
	}
	var lb []byte
	for x := n; x > 0; x >>= 8 {
		lb = append([]byte{byte(x)}, lb...)
	}
	return append([]byte{base + 55 + byte(len(lb))}, lb...)
}

// bftExtra builds extraData: vanity, validator list, optional vote, round, no seals.
func bftExtra(t *testing.T, validators []string, vote *collector.BlockVote, round int) string {
	t.Helper()
	addr := func(a string) []byte {
		b, err := hex.DecodeString(strings.TrimPrefix(a, "0x"))
		if err != nil || len(b) != 20 {
			t.Fatalf("bad address %q", a)
		}
		return rlpStr(b)
	}
	var vals [][]byte
	for _, v := range validators {
		vals = append(vals, addr(v))
	}
	v := rlpList()
	if vote != nil {
		dir := byte(0)
		if vote.Add {
			dir = 0xff
		}
		v = rlpList(addr(vote.Target), rlpStr([]byte{dir}))
	}
	r := []byte{0, 0, 0, byte(round)}
	return "0x" + hex.EncodeToString(rlpList(rlpStr(make([]byte, 32)), rlpList(vals...), v, rlpStr(r), rlpList()))
}

// fakeChainRPC: every node not in down answers the collector's batch as a healthy node at height. Block n
// has validator set setAt(n) (sets: block -> set from that block on, sorted), proposer
// set[n % len(set)], carries votes[n] and has round rounds[n]. A validator in skip never produces:
// the next one in the set takes its blocks after one round change. Blocks are 2 s apart, the head
// one at base + 2*height. epoch = admin_nodeInfo readable (ADMIN on) with that epoch length.
type fakeChainRPC struct {
	t       *testing.T
	qbft    bool
	height  int64
	base    time.Time
	sets    map[int64][]string
	votes   map[int64]collector.BlockVote
	rounds  map[int64]int
	pending map[string]map[string]bool // node id -> *_getPendingVotes
	enodes  map[string]string          // node id -> net_enode
	peers   map[string][]string        // node id -> admin_peers enodes (latestBlock = height-3)
	down    map[string]bool
	epoch   int64
	skip    string
}

func (f *fakeChainRPC) setAt(n int64) []string {
	from := int64(-1)
	for k := range f.sets {
		if k <= n && k > from {
			from = k
		}
	}
	return f.sets[from]
}

func (f *fakeChainRPC) blockTime(n int64) time.Time {
	return f.base.Add(time.Duration(n) * 2 * time.Second)
}

// scheduled: whose turn block n is, before any round change.
func (f *fakeChainRPC) scheduled(n int64) string {
	set := f.setAt(n)
	return set[int(n)%len(set)]
}

func (f *fakeChainRPC) proposer(n int64) string {
	if p := f.scheduled(n); p != f.skip {
		return p
	}
	set := f.setAt(n)
	return set[(int(n)+1)%len(set)]
}

func (f *fakeChainRPC) round(n int64) int {
	if f.scheduled(n) == f.skip {
		return 1
	}
	return f.rounds[n]
}

func (f *fakeChainRPC) block(n int64) any {
	if n > f.height {
		return nil
	}
	var vote *collector.BlockVote
	if v, ok := f.votes[n]; ok {
		vote = &v
	}
	return map[string]any{
		"number": fmt.Sprintf("0x%x", n), "timestamp": fmt.Sprintf("0x%x", f.blockTime(n).Unix()), "miner": f.proposer(n),
		"extraData": bftExtra(f.t, f.setAt(n), vote, f.round(n)), "gasUsed": "0x0", "gasLimit": "0x1c9c380", "transactions": []string{},
	}
}

func (f *fakeChainRPC) Batch(_ context.Context, nodeID string, calls []rpcops.Call) ([]rpcops.Result, error) {
	if f.down[nodeID] {
		return nil, rpcops.ErrNoResponse
	}
	ns, other := "ibft", "qbft"
	if f.qbft {
		ns, other = "qbft", "ibft"
	}
	out := make([]rpcops.Result, len(calls))
	for i, c := range calls {
		var v any
		switch c.Method {
		case "eth_blockNumber":
			v = fmt.Sprintf("0x%x", f.height)
		case "eth_syncing":
			v = false
		case "eth_chainId":
			v = "0x539"
		case "net_enode":
			v = f.enodes[nodeID]
		case ns + "_getValidatorsByBlockNumber":
			v = f.setAt(f.height)
		case ns + "_getPendingVotes":
			p := f.pending[nodeID]
			if p == nil {
				p = map[string]bool{}
			}
			v = p
		case "admin_nodeInfo":
			if f.epoch == 0 {
				out[i] = rpcops.Result{Err: &rpcops.RPCError{Code: -32604, Message: "Method not enabled"}}
				continue
			}
			key := "ibft2"
			if f.qbft {
				key = "qbft"
			}
			v = map[string]any{"protocols": map[string]any{"eth": map[string]any{"config": map[string]any{key: map[string]any{"epochLength": f.epoch, "blockPeriodSeconds": 2}}}}}
		case "admin_peers":
			list := []map[string]any{}
			for _, en := range f.peers[nodeID] {
				list = append(list, map[string]any{"enode": en, "protocols": map[string]any{"eth": map[string]any{"latestBlock": f.height - 3}}})
			}
			v = list
		case "eth_getBlockByNumber":
			tag, _ := c.Params[0].(string)
			n := f.height
			if tag != "latest" {
				fmt.Sscanf(tag, "0x%x", &n)
			}
			v = f.block(n)
		case other + "_getValidatorsByBlockNumber", other + "_getPendingVotes":
			out[i] = rpcops.Result{Err: &rpcops.RPCError{Code: -32601, Message: "Method not found"}}
			continue
		default:
			out[i] = rpcops.Result{Err: &rpcops.RPCError{Code: -32601, Message: "Method not found"}}
			continue
		}
		raw, _ := json.Marshal(v)
		out[i] = rpcops.Result{Raw: raw}
	}
	return out, nil
}

func (*fakeChainRPC) ProbeAuth(context.Context, string) string { return "disabled" }

// voteOps: records every vote RPC the platform sends; fail makes that node's sends fail.
// PendingVotes answers from the same map the collector reads.
type voteOps struct {
	NodeOps
	calls   []string
	fail    map[string]error
	pending map[string]map[string]bool
}

func (o *voteOps) ProposeVote(_ context.Context, nodeID, consensus, address string, add bool) error {
	o.calls = append(o.calls, fmt.Sprintf("%s %s propose %s %v", nodeID, consensus, address, add))
	return o.fail[nodeID]
}

func (o *voteOps) DiscardVote(_ context.Context, nodeID, consensus, address string) error {
	o.calls = append(o.calls, fmt.Sprintf("%s %s discard %s", nodeID, consensus, address))
	return o.fail[nodeID]
}

func (o *voteOps) PendingVotes(_ context.Context, nodeID, _ string) (map[string]bool, error) {
	if p, ok := o.pending[nodeID]; ok {
		return p, nil
	}
	return nil, errors.New("unreachable")
}

// chainFixture: a chain with validators to vote on. Our org runs node-a1 and node-a2 (validators v1, v2) and node-a3 (not
// a validator); another org runs validator vb. x is a candidate address from outside the set.
type chainFixture struct {
	s              *Server
	rpc            *fakeChainRPC
	ops            *voteOps
	v1, v2, a3, vb string
	en1, en2, enb  string
	x              string
	set            []string // v1, v2, vb sorted
}

func newChainFixture(t *testing.T, qbft bool) *chainFixture {
	t.Helper()
	s, _ := accessTestServer(t)
	ctx := context.Background()
	en1, v1 := mkPeer(t, "a1", "10.0.0.1:30303")
	en2, v2 := mkPeer(t, "a2", "10.0.0.2:30303")
	en3, a3 := mkPeer(t, "a3", "10.0.0.3:30303")
	enb, vb := mkPeer(t, "b1", "10.0.1.1:30303")
	_, x := mkPeer(t, "c1", "10.0.2.1:30303")
	name := "north-1"
	for id, ident := range map[string][2]string{"node-a1": {v1, en1}, "node-a2": {v2, en2}, "node-a3": {a3, en3}} {
		var nm *string
		if id == "node-a1" {
			nm = &name
		}
		if err := s.store.UpsertOwnNode(ctx, id, "org-test", 8545, 9545, nm); err != nil {
			t.Fatal(err)
		}
		if err := s.store.SetNodeIdentity(ctx, id, ident[0], ident[1]); err != nil {
			t.Fatal(err)
		}
	}
	set := []string{v1, v2, vb}
	sort.Strings(set)
	pending := map[string]map[string]bool{}
	g := &chainFixture{s: s, v1: v1, v2: v2, a3: a3, vb: vb, en1: en1, en2: en2, enb: enb, x: x, set: set}
	g.rpc = &fakeChainRPC{t: t, qbft: qbft, height: 12, sets: map[int64][]string{0: set}, votes: map[int64]collector.BlockVote{}, rounds: map[int64]int{},
		pending: pending, enodes: map[string]string{"node-a1": en1, "node-a2": en2, "node-a3": en3},
		peers: map[string][]string{"node-a1": {en2, enb}, "node-a2": {en1, enb}}, down: map[string]bool{}, epoch: 30000}
	g.ops = &voteOps{fail: map[string]error{}, pending: pending}
	s.ops = g.ops
	s.voteTouched = map[string]time.Time{}
	return g
}

// poll runs collector rounds over the fixture until the block cache holds the whole chain (or, with
// every node down, until all three have a result).
func (g *chainFixture) poll(t *testing.T) {
	t.Helper()
	g.rpc.base = time.Now().Add(-time.Duration(g.rpc.height) * 2 * time.Second).Truncate(time.Second)
	g.s.col = collector.New(g.s.store, downRequester{}, g.rpc)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { g.s.col.Run(ctx, 20*time.Millisecond); close(done) }()
	defer func() { cancel(); <-done }()
	allDown := len(g.rpc.down) == 3
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		_, ok1 := g.s.col.Get("node-a1")
		_, ok3 := g.s.col.Get("node-a3")
		if allDown && ok1 && ok3 {
			return
		}
		if v := g.s.col.Chain(); !allDown && int64(len(v.Blocks)) == g.rpc.height && (g.rpc.epoch == 0 || v.VotesComplete()) {
			return
		}
	}
	t.Fatalf("collector did not cache the fixture chain in time: %+v", g.s.col.Chain())
}

// turnOf: the first block from 1 on whose proposer is v.
func (g *chainFixture) turnOf(v string) int64 {
	for n := int64(1); ; n++ {
		if g.rpc.proposer(n) == v {
			return n
		}
	}
}

// ── votes ──

// Proposing goes out once to each of our validator nodes (never to our non-validator node or
// another org's validator), with the consensus type the chain reports and the address lowercased;
// one node failing is reported per node and does not fail the request. Audit: an entry naming the
// target nodes before sending, then one with each node's result.
func TestProposeValidatorVoteFansOut(t *testing.T) {
	for _, c := range []struct {
		qbft      bool
		consensus string
	}{{true, "qbft"}, {false, "ibft2"}} {
		t.Run(c.consensus, func(t *testing.T) {
			g := newChainFixture(t, c.qbft)
			audited := recordAudit(t)
			g.poll(t)
			g.ops.fail["node-a1"] = errors.New("agent offline") // the first target fails; the later one must still be sent to
			ctx := asAdmin(context.Background())

			res, err := g.s.ProposeValidatorVote(ctx, api.ProposeValidatorVoteRequestObject{Body: &api.ProposeValidatorVoteJSONRequestBody{Address: "0x" + strings.ToUpper(g.x[2:]), Add: true}})
			if err != nil {
				t.Fatal(err)
			}
			ok, isOK := res.(api.ProposeValidatorVote200JSONResponse)
			if !isOK {
				t.Fatalf("want 200, got %T %+v", res, res)
			}
			want := []string{"node-a1 " + c.consensus + " propose " + g.x + " true", "node-a2 " + c.consensus + " propose " + g.x + " true"}
			if !slices.Equal(g.ops.calls, want) {
				t.Fatalf("RPC calls:\n got %v\nwant %v", g.ops.calls, want)
			}
			r := ok.Results
			if len(r) != 2 || r[0].NodeId != "node-a1" || r[0].Ok || r[1].NodeId != "node-a2" || !r[1].Ok ||
				r[0].Reason == nil || *r[0].Reason != api.ErrorReasonOperationFailed || (*r[0].Params)["detail"] != "agent offline" {
				t.Fatalf("per-node results: %+v", r)
			}
			if _, touched := g.s.voteTouched[g.x]; !touched {
				t.Fatalf("voteTouched not marked for %s: %v", g.x, g.s.voteTouched)
			}
			entries := audited("validator_vote_proposed")
			if len(entries) != 2 {
				t.Fatalf("want 2 audit entries, got %+v", entries)
			}
			if e := entries[0]; e["result"] != nil || e["address"] != g.x || e["add"] != true || !slices.Equal(e["nodes"].([]string), []string{"node-a1", "node-a2"}) {
				t.Fatalf("intent entry: %+v", e)
			}
			if e := entries[1]; e["result"] != "done" || e["address"] != g.x {
				t.Fatalf("result entry: %+v", e)
			}
			if nr := entries[1]["nodes"].([]api.ValidatorVoteNodeResult); len(nr) != 2 || nr[0].NodeId != "node-a1" || nr[0].Ok || nr[1].NodeId != "node-a2" || !nr[1].Ok {
				t.Fatalf("audited per-node results: %+v", nr)
			}
		})
	}
}

// A vote in the direction the set already has is rejected with its reason and sends nothing;
// with no validator node of ours, or no node reporting the consensus type, voting is blocked.
func TestProposeValidatorVoteRejected(t *testing.T) {
	g := newChainFixture(t, true)
	audited := recordAudit(t)
	g.poll(t)
	ctx := asAdmin(context.Background())
	propose := func(addr string, add bool) api.ProposeValidatorVoteResponseObject {
		t.Helper()
		res, err := g.s.ProposeValidatorVote(ctx, api.ProposeValidatorVoteRequestObject{Body: &api.ProposeValidatorVoteJSONRequestBody{Address: addr, Add: add}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	for _, c := range []struct {
		addr   string
		add    bool
		reason api.ErrorReason
	}{
		{g.vb, true, api.ErrorReasonAlreadyValidator},
		{g.x, false, api.ErrorReasonNotValidator},
	} {
		res, ok := propose(c.addr, c.add).(api.ProposeValidatorVote409JSONResponse)
		if !ok || res.Reason == nil || *res.Reason != c.reason {
			t.Fatalf("%s add=%v: want 409 %s, got %+v", c.addr, c.add, c.reason, res)
		}
	}
	if len(g.ops.calls) != 0 || len(audited("validator_vote_proposed")) != 0 {
		t.Fatalf("rejected vote must send and audit nothing: calls %v", g.ops.calls)
	}

	// The set no longer holds any of our validators: nobody to send to.
	g.rpc.sets = map[int64][]string{0: {g.vb}}
	g.poll(t)
	if res, ok := propose(g.x, true).(api.ProposeValidatorVote409JSONResponse); !ok || *res.Reason != api.ErrorReasonNoValidatorNodes {
		t.Fatalf("want 409 no_validator_nodes, got %+v", res)
	}
	// Every node down: the consensus type is unknown.
	g.rpc.down = map[string]bool{"node-a1": true, "node-a2": true, "node-a3": true}
	g.poll(t)
	if res, ok := propose(g.x, true).(api.ProposeValidatorVote409JSONResponse); !ok || *res.Reason != api.ErrorReasonVoteNoConsensus {
		t.Fatalf("want 409 vote_no_consensus, got %+v", res)
	}
	if len(g.ops.calls) != 0 {
		t.Fatalf("blocked vote sent RPC: %v", g.ops.calls)
	}
}

// The intent entry cannot be written: the request fails and no node receives a vote.
func TestProposeValidatorVoteAuditFailClosed(t *testing.T) {
	g := newChainFixture(t, true)
	g.poll(t)
	events := storetest.FailAuditForTest(t)
	_, err := g.s.ProposeValidatorVote(asAdmin(context.Background()), api.ProposeValidatorVoteRequestObject{Body: &api.ProposeValidatorVoteJSONRequestBody{Address: g.x, Add: true}})
	if !errors.Is(err, errAudit) {
		t.Fatalf("want errAudit, got %v", err)
	}
	if len(g.ops.calls) != 0 || !slices.Equal(*events, []string{"validator_vote_proposed"}) {
		t.Fatalf("calls %v, audit attempts %v", g.ops.calls, *events)
	}
}

// Withdrawing: a node whose vote is already in a block casts the opposite vote to cancel it; a node
// whose vote is only pending just drops it. The intent entry records which nodes cancel.
func TestDiscardValidatorVote(t *testing.T) {
	g := newChainFixture(t, true)
	audited := recordAudit(t)
	g.rpc.votes[g.turnOf(g.v1)] = collector.BlockVote{Target: g.x, Add: true}
	g.rpc.pending["node-a2"] = map[string]bool{g.x: true}
	g.poll(t)

	res, err := g.s.DiscardValidatorVote(asAdmin(context.Background()), api.DiscardValidatorVoteRequestObject{Address: "0x" + strings.ToUpper(g.x[2:])})
	if err != nil {
		t.Fatal(err)
	}
	ok, isOK := res.(api.DiscardValidatorVote200JSONResponse)
	if !isOK || len(ok.Results) != 2 || !ok.Results[0].Ok || !ok.Results[1].Ok {
		t.Fatalf("want 200 with two ok results, got %+v", res)
	}
	want := []string{"node-a1 qbft propose " + g.x + " false", "node-a2 qbft discard " + g.x}
	if !slices.Equal(g.ops.calls, want) {
		t.Fatalf("RPC calls:\n got %v\nwant %v", g.ops.calls, want)
	}
	entries := audited("validator_vote_discarded")
	if len(entries) != 2 || entries[0]["address"] != g.x || fmt.Sprint(entries[0]["cancel"]) != "map[node-a1:true]" || entries[1]["result"] != "done" {
		t.Fatalf("audit: %+v", entries)
	}

	// No validator node of ours left in the set: 409 and nothing sent.
	g.ops.calls = nil
	g.rpc.sets = map[int64][]string{0: {g.vb}}
	g.rpc.votes = map[int64]collector.BlockVote{}
	g.poll(t)
	res, err = g.s.DiscardValidatorVote(asAdmin(context.Background()), api.DiscardValidatorVoteRequestObject{Address: g.x})
	if c, ok := res.(api.DiscardValidatorVote409JSONResponse); err != nil || !ok || *c.Reason != api.ErrorReasonNoValidatorNodes || len(g.ops.calls) != 0 {
		t.Fatalf("want 409 no_validator_nodes and no RPC, got %+v err=%v calls=%v", res, err, g.ops.calls)
	}
}

// In-progress votes: the on-chain vote of v1 is counted at its block, v2's vote is still only
// pending on node-a2; the tally is known because the epoch is.
func TestListValidatorVotes(t *testing.T) {
	g := newChainFixture(t, false)
	blk := g.turnOf(g.v1)
	g.rpc.votes[blk] = collector.BlockVote{Target: g.x, Add: true}
	g.rpc.pending["node-a2"] = map[string]bool{g.x: true}
	g.poll(t)

	res, err := g.s.ListValidatorVotes(context.Background(), api.ListValidatorVotesRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	got := res.(api.ListValidatorVotes200JSONResponse)
	if got.ValidatorCount != 3 || got.Needed != 2 || !slices.Equal(got.OwnValidators, []string{"node-a1", "node-a2"}) || !got.TallyKnown {
		t.Fatalf("header: %+v", got)
	}
	if len(got.Votes) != 1 || got.Votes[0].Address != g.x || !got.Votes[0].Add || len(got.Votes[0].Voters) != 2 {
		t.Fatalf("votes: %+v", got.Votes)
	}
	byAddr := map[string]api.ValidatorVoter{}
	for _, v := range got.Votes[0].Voters {
		byAddr[v.Address] = v
	}
	if v := byAddr[g.v1]; v.CountedAt == nil || *v.CountedAt != blk || v.NodeId == nil || *v.NodeId != "node-a1" || v.Pending == nil || *v.Pending {
		t.Fatalf("v1 counted on chain: %+v", v)
	}
	if v := byAddr[g.v2]; v.CountedAt != nil || v.Pending == nil || !*v.Pending || *v.NodeId != "node-a2" {
		t.Fatalf("v2 pending only: %+v", v)
	}

	// ADMIN off: the epoch is unknown, so the tally is not trustworthy.
	g.rpc.epoch = 0
	g.poll(t)
	res, _ = g.s.ListValidatorVotes(context.Background(), api.ListValidatorVotesRequestObject{})
	if res.(api.ListValidatorVotes200JSONResponse).TallyKnown {
		t.Fatal("tallyKnown should be false when the epoch is unknown")
	}
}

// Validator set page: our validators carry org and node id, another org's has neither; connection
// counts come from our nodes' admin_peers; names overlay. vb never produces, so every turn it had
// is a miss and it is not producing; the others are. Turns match the chain block for block.
func TestListValidatorSet(t *testing.T) {
	g := newChainFixture(t, true)
	g.rpc.skip = g.vb
	if err := g.s.store.SetPeerName(context.Background(), store.PeerName{Address: g.vb, Name: "south-1"}); err != nil {
		t.Fatal(err)
	}
	g.poll(t)
	res, err := g.s.ListValidatorSet(context.Background(), api.ListValidatorSetRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	set := res.(api.ListValidatorSet200JSONResponse)
	if len(set) != 3 {
		t.Fatalf("want 3 validators, got %+v", set)
	}
	// Expected turns from the fixture: block n is scheduled's turn; if skipped, it is a miss for
	// scheduled and a proposal by the next validator.
	wantTurns := map[string]string{}
	for n := int64(1); n <= g.rpc.height; n++ {
		if s := g.rpc.scheduled(n); s == g.rpc.skip {
			wantTurns[s] += fmt.Sprintf(" %d:-", n)
		}
		wantTurns[g.rpc.proposer(n)] += fmt.Sprintf(" %d:+", n)
	}
	for _, v := range set {
		var turns string
		for _, tn := range v.Turns {
			mark := "-"
			if tn.Proposed {
				mark = "+"
				if tn.At == nil || !tn.At.Equal(g.rpc.blockTime(tn.Block)) {
					t.Fatalf("%s: proposal at block %d has time %v", v.Address, tn.Block, tn.At)
				}
			}
			turns += fmt.Sprintf(" %d:%s", tn.Block, mark)
		}
		if turns != wantTurns[v.Address] {
			t.Fatalf("%s turns:\n got%s\nwant%s", v.Address, turns, wantTurns[v.Address])
		}
		if v.ConnectedNodes == nil || v.Producing == nil || *v.Producing != (v.Address != g.vb) {
			t.Fatalf("%s: connected/producing: %+v", v.Address, v)
		}
		switch v.Address {
		case g.v1:
			if *v.NodeId != "node-a1" || *v.OrgId != "org-test" || *v.Name != "north-1" || *v.ConnectedNodes != 1 {
				t.Fatalf("v1: %+v", v)
			}
		case g.v2:
			if *v.NodeId != "node-a2" || v.Name != nil || *v.ConnectedNodes != 1 {
				t.Fatalf("v2: %+v", v)
			}
		case g.vb:
			if v.NodeId != nil || v.OrgId != nil || *v.Name != "south-1" || *v.ConnectedNodes != 2 {
				t.Fatalf("other org's validator: %+v", v)
			}
		default:
			t.Fatalf("unexpected validator %s", v.Address)
		}
	}
}
