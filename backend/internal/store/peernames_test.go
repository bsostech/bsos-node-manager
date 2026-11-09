package store

import (
	"context"
	"strings"
	"testing"
)

// Names are keyed by address: setting it again renames rather than adding a new row.
func TestPeerNames(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	addr := "0x" + strings.Repeat("ab", 20)
	for _, name := range []string{"舊名", "新名"} {
		if err := s.SetPeerName(ctx, PeerName{Address: addr, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListPeerNames(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "新名" || list[0].Address != addr {
		t.Fatalf("list %+v err %v", list, err)
	}
	if err := s.DeletePeerName(ctx, addr); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListPeerNames(ctx); len(list) != 0 {
		t.Fatalf("after delete: %+v", list)
	}
}
