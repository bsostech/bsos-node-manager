package apierr

import (
	"errors"
	"fmt"
	"testing"

	"besu-manager/backend/internal/api"
)

// Every reason in the contract enum needs an English template, otherwise the message is an empty string.
func TestEveryReasonHasTemplate(t *testing.T) {
	doc, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range doc.Components.Schemas["Error"].Value.Properties["reason"].Value.Enum {
		if templates[api.ErrorReason(v.(string))] == "" {
			t.Errorf("reason %q 沒有英文句型", v)
		}
	}
}

func TestNew(t *testing.T) {
	e := New(api.ErrorCodeInvalid, api.ErrorReasonFieldRequired, map[string]string{"field": "name"})
	if e.Message != "name is required" || *e.Reason != api.ErrorReasonFieldRequired || (*e.Params)["field"] != "name" {
		t.Fatalf("%+v", e)
	}
	if e := New(api.ErrorCodeNotFound, api.ErrorReasonUserNotFound, nil); e.Params != nil {
		t.Fatal("沒有 params 不該帶空物件")
	}
}

// The wrapping layer attaches only when the inner error has no reason: wrapping "節點 RPC 沒有回應" (node RPC did not respond) around "節點連不上" (node unreachable) yields node unreachable.
// Error() is the full Chinese string (for logs), and errors.Is works as before.
func TestWrapKeepsRootReason(t *testing.T) {
	offline := errors.New("未連線")
	inner := Wrap(api.ErrorReasonNodeUnreachable, map[string]string{"nodeId": "node-a1"}, fmt.Errorf("agent node-a1: %w", offline))
	noResp := errors.New("節點 RPC 沒有回應")
	outer := Wrap(api.ErrorReasonNodeRpcNoResponse, map[string]string{"detail": inner.Error()}, fmt.Errorf("%w: %w", noResp, inner))
	r, p, ok := ReasonOf(outer)
	if !ok || r != api.ErrorReasonNodeUnreachable || p["nodeId"] != "node-a1" {
		t.Fatalf("要取到最根本的 reason: %s %v", r, p)
	}
	if outer.Error() != "節點 RPC 沒有回應: agent node-a1: 未連線" || !errors.Is(outer, offline) || !errors.Is(outer, noResp) {
		t.Fatalf("Error() 與 errors.Is 要照舊: %q", outer.Error())
	}
	plain := Wrap(api.ErrorReasonNodeRpcNoResponse, map[string]string{"detail": "x"}, errors.New("x"))
	if r, _, _ := ReasonOf(plain); r != api.ErrorReasonNodeRpcNoResponse {
		t.Fatalf("內層沒有 reason 時掛外層的: %s", r)
	}
	if _, _, ok := ReasonOf(errors.New("y")); ok {
		t.Fatal("沒有 reason 的錯誤不該取到")
	}
}
