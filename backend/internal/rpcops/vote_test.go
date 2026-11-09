package rpcops

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// recorder: a fake node that records "method params" for every call and answers with result
// (or *RPCError). rpcFunc asks its function twice per request, so each RPC is recorded twice;
// compare against twice(want) so a real duplicate RPC still shows up as an extra pair.
func recorder(calls *[]string, result func(method string) any) rpcFunc {
	return func(method string, params []json.RawMessage) any {
		raw, _ := json.Marshal(params)
		*calls = append(*calls, method+" "+string(raw))
		return result(method)
	}
}

func twice(want []string) []string {
	var out []string
	for _, w := range want {
		out = append(out, w, w)
	}
	return out
}

// Votes go to the namespace of the chain's consensus: qbft for QBFT, ibft for IBFT 2.0 and for an
// unknown (empty) consensus. Each method must be in the platform token's permissions, or Besu
// rejects the whole batch with 401.
func TestVoteNamespace(t *testing.T) {
	const addr = "0xab00000000000000000000000000000000000001"
	for _, c := range []struct{ consensus, ns string }{{"qbft", "qbft"}, {"ibft2", "ibft"}, {"", "ibft"}} {
		var calls []string
		cl := New(recorder(&calls, func(m string) any {
			if strings.HasSuffix(m, "_getPendingVotes") {
				return map[string]bool{addr: true}
			}
			return true
		}), nil, rpcFunc(nil))
		ctx := context.Background()
		if err := cl.ProposeVote(ctx, "n1", c.consensus, addr, false); err != nil {
			t.Fatal(err)
		}
		if err := cl.DiscardVote(ctx, "n1", c.consensus, addr); err != nil {
			t.Fatal(err)
		}
		pending, err := cl.PendingVotes(ctx, "n1", c.consensus)
		if err != nil || !reflect.DeepEqual(pending, map[string]bool{addr: true}) {
			t.Fatalf("%q pending = %v, %v", c.consensus, pending, err)
		}
		want := []string{
			c.ns + `_proposeValidatorVote ["` + addr + `",false]`,
			c.ns + `_discardValidatorVote ["` + addr + `"]`,
			c.ns + `_getPendingVotes []`,
		}
		if !reflect.DeepEqual(calls, twice(want)) {
			t.Fatalf("%q calls:\n got %v\nwant %v", c.consensus, calls, want)
		}
		for _, m := range []string{"_proposeValidatorVote", "_discardValidatorVote", "_getPendingVotes"} {
			if !Permits(c.ns + m) {
				t.Errorf("platform token does not permit %s%s", c.ns, m)
			}
		}
	}
}

// A node answering false to a proposal is an error; a JSON-RPC error (e.g. voting not enabled in
// QBFT contract mode) comes back as *RPCError from all three calls.
func TestVoteErrors(t *testing.T) {
	var calls []string
	cl := New(recorder(&calls, func(string) any { return false }), nil, rpcFunc(nil))
	if err := cl.ProposeVote(context.Background(), "n1", "qbft", "0xab", true); err == nil || !strings.Contains(err.Error(), "qbft_proposeValidatorVote") {
		t.Fatalf("false answer: err = %v", err)
	}

	notEnabled := &RPCError{Code: -32604, Message: "Method not enabled"}
	cl = New(recorder(&calls, func(string) any { return notEnabled }), nil, rpcFunc(nil))
	ctx := context.Background()
	var rpcErr *RPCError
	if err := cl.ProposeVote(ctx, "n1", "qbft", "0xab", true); !errors.As(err, &rpcErr) {
		t.Fatalf("propose: err = %v", err)
	}
	if err := cl.DiscardVote(ctx, "n1", "qbft", "0xab"); !errors.As(err, &rpcErr) {
		t.Fatalf("discard: err = %v", err)
	}
	if _, err := cl.PendingVotes(ctx, "n1", "qbft"); !errors.As(err, &rpcErr) {
		t.Fatalf("pending: err = %v", err)
	}
}

// Log level goes out as admin_changeLogLevel with the level as its only parameter; a node error is
// returned as is.
func TestSetLogLevel(t *testing.T) {
	var calls []string
	cl := New(recorder(&calls, func(string) any { return nil }), nil, rpcFunc(nil))
	if err := cl.SetLogLevel(context.Background(), "n1", "DEBUG"); err != nil {
		t.Fatal(err)
	}
	if want := twice([]string{`admin_changeLogLevel ["DEBUG"]`}); !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v", calls)
	}
	if !Permits("admin_changeLogLevel") {
		t.Error("platform token does not permit admin_changeLogLevel")
	}
	cl = New(recorder(&calls, func(string) any { return &RPCError{Code: -32602, Message: "Invalid params"} }), nil, rpcFunc(nil))
	var rpcErr *RPCError
	if err := cl.SetLogLevel(context.Background(), "n1", "LOUD"); !errors.As(err, &rpcErr) || rpcErr.Code != -32602 {
		t.Fatalf("err = %v", err)
	}
}
