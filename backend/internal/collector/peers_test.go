package collector

import (
	"strings"
	"testing"

	"besu-manager/backend/internal/rpcops"
)

// admin_peers: the public key comes from the enode (lowercased), malformed enodes are skipped; nil when ADMIN is not enabled.
func TestObservedPeers(t *testing.T) {
	pub := strings.Repeat("AB", 64)
	raw := `[{"enode":"enode://` + pub + `@10.0.0.2:30303","network":{"remoteAddress":"10.0.0.2:51234","inbound":true}},
	         {"enode":"not-an-enode","network":{"remoteAddress":"x","inbound":false}}]`
	got := observedPeers(rpcops.Result{Raw: []byte(raw)})
	if len(got) != 1 || got[0].Pubkey != strings.ToLower(pub) || got[0].RemoteAddress != "10.0.0.2:51234" || !got[0].Inbound {
		t.Fatalf("got %+v", got)
	}
	if got[0].LatestBlock != nil {
		t.Fatalf("沒有 protocols.eth 應為 nil:%v", *got[0].LatestBlock)
	}
	// Peer chain head: latestBlock; 0 right after connecting means not estimated yet, treated as unknown
	two := `[{"enode":"enode://` + pub + `@10.0.0.2:30303","protocols":{"eth":{"latestBlock":500,"version":71}}},
	         {"enode":"enode://` + strings.Repeat("cd", 64) + `@10.0.0.3:30303","protocols":{"eth":{"latestBlock":0}}}]`
	got = observedPeers(rpcops.Result{Raw: []byte(two)})
	if len(got) != 2 || got[0].LatestBlock == nil || *got[0].LatestBlock != 500 || got[1].LatestBlock != nil {
		t.Fatalf("latestBlock %+v", got)
	}
	if observedPeers(rpcops.Result{Err: &rpcops.RPCError{Code: -32601, Message: "Method not found"}}) != nil {
		t.Fatal("ADMIN disabled should be nil")
	}
}
