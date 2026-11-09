package collector

import (
	"errors"
	"testing"

	"besu-manager/backend/internal/store"
)

// Use the new list when the DB is readable.
func TestPollTargetsUsesFreshList(t *testing.T) {
	got := pollTargets([]store.Node{{ID: "node-a"}, {ID: "node-b"}}, nil, []string{"node-old"})
	if len(got) != 2 || got[0] != "node-a" || got[1] != "node-b" {
		t.Fatalf("got %v", got)
	}
}

// Keep last round's list when the DB is unreadable.
//
// This is a regression test, not defensive code: the read timeouts on both ends assume "the platform sends a request at least every 5 seconds", and whether one can be sent
// depends on getting the node list here. If the round were simply abandoned, a Postgres hiccup longer than a minute would fire every agent's
// read timeout at once: the whole fleet disconnects, then backs off and reconnects together.
func TestPollTargetsFallsBackWhenDBFails(t *testing.T) {
	last := []string{"node-a", "node-b"}
	got := pollTargets(nil, errors.New("db down"), last)
	if len(got) != 2 || got[0] != "node-a" {
		t.Fatalf("DB 失敗時應沿用上一輪清單,got %v", got)
	}
}

// When the nodes really are all deleted the list must become empty too; it must not keep the old one because "an empty list looks like a failure".
func TestPollTargetsEmptyListIsRespected(t *testing.T) {
	if got := pollTargets(nil, nil, []string{"node-a"}); len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}
