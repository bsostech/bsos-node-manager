package store

import (
	"context"
	"errors"
	"testing"
)

func TestSavePermissioningPolicyAdvancesRevision(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	for want := int64(1); want <= 2; want++ {
		rev, err := s.SavePermissioningPolicy(ctx, SharedPolicy, []string{"0xabc"}, want-1)
		if err != nil || rev != want {
			t.Fatalf("revision=%d want %d err=%v", rev, want, err)
		}
	}
	all, err := s.ListPermissioningPolicies(ctx)
	if p := all[SharedPolicy]; err != nil || len(p.AccountsAllowlist) != 1 || p.Revision != 2 {
		t.Fatalf("policy=%+v err=%v", p, err)
	}
}

// Saving with an old revision returns ErrPolicyStale, leaving both the list and the revision unchanged (when
// two people edit at once, the later save must not overwrite the earlier one).
func TestSavePermissioningPolicyRejectsStaleRevision(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	if _, err := s.SavePermissioningPolicy(ctx, SharedPolicy, []string{"0xaaa"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SavePermissioningPolicy(ctx, SharedPolicy, []string{"0xbbb"}, 0); !errors.Is(err, ErrPolicyStale) {
		t.Fatalf("stale save err=%v, want ErrPolicyStale", err)
	}
	all, err := s.ListPermissioningPolicies(ctx)
	if p := all[SharedPolicy]; err != nil || p.Revision != 1 || len(p.AccountsAllowlist) != 1 || p.AccountsAllowlist[0] != "0xaaa" {
		t.Fatalf("policy changed by stale save: %+v err=%v", p, err)
	}
}

// Custom list: created with revision 0; a second person also creating with 0 gets stale; after deletion,
// saving with the old revision is stale (no silent recreation), and only 0 recreates it.
func TestNodePermissioningPolicyLifecycle(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	if rev, err := s.SavePermissioningPolicy(ctx, "n1", []string{"0xaaa"}, 0); err != nil || rev != 1 {
		t.Fatalf("create rev=%d err=%v", rev, err)
	}
	if _, err := s.SavePermissioningPolicy(ctx, "n1", []string{"0xbbb"}, 0); !errors.Is(err, ErrPolicyStale) {
		t.Fatalf("second create err=%v, want ErrPolicyStale", err)
	}
	all, _ := s.ListPermissioningPolicies(ctx)
	if p, ok := all["n1"]; !ok || p.AccountsAllowlist[0] != "0xaaa" || all[SharedPolicy].Revision != 0 {
		t.Fatalf("after create: %+v", all)
	}
	if ok, err := s.DeletePermissioningPolicy(ctx, "n1"); !ok || err != nil {
		t.Fatalf("delete ok=%v err=%v", ok, err)
	}
	if ok, _ := s.DeletePermissioningPolicy(ctx, "n1"); ok {
		t.Fatal("second delete reported a row")
	}
	if _, err := s.SavePermissioningPolicy(ctx, "n1", []string{"0xccc"}, 1); !errors.Is(err, ErrPolicyStale) {
		t.Fatalf("save after delete err=%v, want ErrPolicyStale", err)
	}
	if _, err := s.DeletePermissioningPolicy(ctx, SharedPolicy); err == nil {
		t.Fatal("shared list was deletable")
	}
}
