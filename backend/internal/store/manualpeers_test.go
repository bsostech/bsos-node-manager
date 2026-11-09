package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// One entry per public key; rejected once the cap is reached; can be re-added after deletion.
func TestManualPeers(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	p := ManualPeer{Pubkey: strings.Repeat("ab", 64), Enode: "enode://x@10.0.0.9:30303", CreatedAt: time.Now()}
	if added, full, err := s.AddManualPeer(ctx, p, 1); err != nil || !added || full {
		t.Fatalf("add: %v %v %v", added, full, err)
	}
	if added, full, err := s.AddManualPeer(ctx, p, 2); err != nil || added || full {
		t.Fatalf("dup: added=%v full=%v err=%v", added, full, err)
	}
	q := p
	q.Pubkey = strings.Repeat("cd", 64)
	if added, full, err := s.AddManualPeer(ctx, q, 1); err != nil || added || !full {
		t.Fatalf("limit: added=%v full=%v err=%v", added, full, err)
	}
	if ok, err := s.DeleteManualPeer(ctx, p.Pubkey); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, _ := s.DeleteManualPeer(ctx, p.Pubkey); ok {
		t.Fatal("deleted twice")
	}
	if list, _ := s.ListManualPeers(ctx); len(list) != 0 {
		t.Fatalf("list %+v", list)
	}
}

// Concurrent adds: the list is locked before counting, so the cap is never exceeded. The shared list and each
// node's list have their own lock.
func TestPeerListCapUnderConcurrency(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	const limit, writers = 5, 20
	var wg sync.WaitGroup
	for i := range writers {
		pub := fmt.Sprintf("%0128x", i+1)
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, _, err := s.AddManualPeer(ctx, ManualPeer{Pubkey: pub, Enode: "enode://" + pub + "@10.0.0.9:30303", CreatedAt: time.Now()}, limit); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, _, err := s.AddNodePeer(ctx, NodePeer{NodeID: "node-a1", Pubkey: pub, Enode: "enode://" + pub + "@10.0.0.9:30303", CreatedAt: time.Now()}, limit); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var shared, own int
	if err := s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM manual_peers), (SELECT count(*) FROM node_peers WHERE node_id = 'node-a1')`).Scan(&shared, &own); err != nil {
		t.Fatal(err)
	}
	if shared != limit || own != limit {
		t.Fatalf("共用清單 %d 筆、node-a1 %d 筆,上限 %d", shared, own, limit)
	}
}
