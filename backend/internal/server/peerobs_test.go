package server

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"testing"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/auth"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/enode"
)

func pubOf(t *testing.T, seed string) (pub, addr string) {
	t.Helper()
	en, addr := mkPeer(t, seed, "10.0.0.1:30303")
	return strings.TrimPrefix(strings.Split(en, "@")[0], "enode://"), strings.ToLower(addr)
}

// Own / validator / other: own means the public key matches the nodes table, validator means the
// address derived from the public key is on-chain; names are keyed by that address.
func TestClassifyPeers(t *testing.T) {
	ownPub, _ := pubOf(t, "a1")
	valPub, valAddr := pubOf(t, "b2")
	otherPub, _ := pubOf(t, "c3")
	obs := []collector.ObservedPeer{
		{Pubkey: ownPub, Enode: "e1", RemoteAddress: "10.0.0.1:30303"},
		{Pubkey: valPub, Enode: "e2", RemoteAddress: "10.0.0.2:51234", Inbound: true},
		{Pubkey: otherPub, Enode: "e3"},
	}
	got := classifyPeers(obs, map[string]string{ownPub: "node-a1"}, map[string]bool{enode.NormalizeAddress(valAddr): true}, map[string]string{enode.NormalizeAddress(valAddr): "B 的驗證者"})
	if got[0].NodeId == nil || *got[0].NodeId != "node-a1" {
		t.Errorf("own: %+v", got[0])
	}
	if got[1].ValidatorAddress == nil || got[1].NodeId != nil || got[1].Address != valAddr || got[1].Name == nil || *got[1].Name != "B 的驗證者" || !got[1].Inbound {
		t.Errorf("validator: %+v", got[1])
	}
	if got[2].ValidatorAddress != nil || got[2].NodeId != nil || got[2].Name != nil || got[2].RemoteAddress != nil {
		t.Errorf("other: %+v", got[2])
	}
}

// The same peer with a new IP keeps the same name (names are bound to the address derived from the public key).
func TestPeerNameFollowsPubkey(t *testing.T) {
	pub, addr := pubOf(t, "b2")
	names := map[string]string{enode.NormalizeAddress(addr): "B 的驗證者"}
	for _, ra := range []string{"10.0.0.2:30303", "192.168.9.9:40001"} {
		got := classifyPeers([]collector.ObservedPeer{{Pubkey: pub, RemoteAddress: ra}}, nil, nil, names)
		if got[0].Name == nil || *got[0].Name != "B 的驗證者" {
			t.Fatalf("%s: %+v", ra, got[0])
		}
	}
}

// Connection count: the same node listed twice counts once; a validator no node of ours is
// connected to is 0; our own nodes' names are carried through as well.
func TestValidatorConnections(t *testing.T) {
	aPub, aAddr := pubOf(t, "a1")
	bPub, bAddr := pubOf(t, "b2")
	_, cAddr := pubOf(t, "c3")
	perNode := map[string][]collector.ObservedPeer{
		"node-a1": {{Pubkey: bPub}, {Pubkey: bPub}},
		"node-a2": {{Pubkey: bPub}, {Pubkey: aPub}},
	}
	nodeID := "node-a1"
	set := []api.ValidatorSetEntry{{Address: aAddr, NodeId: &nodeID}, {Address: strings.ToUpper(bAddr[:2]) + bAddr[2:]}, {Address: cAddr}}
	annotateValidators(set, connectedCounts(perNode), true, map[string]string{enode.NormalizeAddress(bAddr): "B", enode.NormalizeAddress(aAddr): "台北 1"})
	if *set[0].ConnectedNodes != 1 || set[0].Name == nil || *set[0].Name != "台北 1" {
		t.Errorf("own validator: %+v", set[0])
	}
	if *set[1].ConnectedNodes != 2 || set[1].Name == nil || *set[1].Name != "B" {
		t.Errorf("external validator: %+v", set[1])
	}
	if *set[2].ConnectedNodes != 0 || set[2].Name != nil {
		t.Errorf("unconnected validator: %+v", set[2])
	}
}

// Our org's node is also a validator: kind stays own, but validatorAddress is included.
func TestClassifyOwnValidator(t *testing.T) {
	pub, addr := pubOf(t, "a1")
	got := classifyPeers([]collector.ObservedPeer{{Pubkey: pub}}, map[string]string{pub: "node-a1"}, map[string]bool{enode.NormalizeAddress(addr): true}, nil)
	if got[0].NodeId == nil || got[0].Address != addr || got[0].ValidatorAddress == nil || *got[0].NodeId != "node-a1" {
		t.Fatalf("%+v", got[0])
	}
}

// No node of ours reported a peer list: connection count stays null (unknown), not 0.
func TestValidatorConnectionsUnknown(t *testing.T) {
	_, addr := pubOf(t, "b2")
	set := []api.ValidatorSetEntry{{Address: addr}}
	annotateValidators(set, connectedCounts(nil), false, nil)
	if set[0].ConnectedNodes != nil {
		t.Fatalf("want nil, got %d", *set[0].ConnectedNodes)
	}
}

// Fields returned depend on role: viewers and anonymous callers get only the allowlisted fields
// (address, direction, node id, name, validator address), operator / admin get everything; fields
// added to PeerInfo later do not leak past the allowlist.
func TestVisiblePeersByRole(t *testing.T) {
	keysOf := func(p api.PeerInfo) []string {
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}
	public := []string{"address", "inbound", "name", "nodeId", "validatorAddress"}
	all := []string{"address", "enode", "inbound", "name", "nodeId", "pubkey", "remoteAddress", "validatorAddress"}
	for _, tc := range []struct {
		role string // empty string = no logged-in user in the context
		want []string
	}{{"viewer", all}, {"", public}, {"operator", all}, {"admin", public}} {
		s, id, b := "x", "node-a1", true
		full := api.PeerInfo{Pubkey: &s, Address: s, Enode: &s, RemoteAddress: &s, Inbound: b, NodeId: &id, Name: &s, ValidatorAddress: &s}
		ctx := context.Background()
		if tc.role != "" {
			ctx = auth.WithPrincipal(ctx, &auth.Principal{Username: &s, Role: tc.role})
		}
		if got := keysOf(visiblePeers(ctx, []api.PeerInfo{full})[0]); !slices.Equal(got, tc.want) {
			t.Errorf("%q: keys %v, want %v", tc.role, got, tc.want)
		}
	}
}

// A peer name may be exactly nodeNameMax characters; one more is rejected.
func TestSetPeerNameLengthLimit(t *testing.T) {
	s, _ := accessTestServer(t)
	recordAudit(t)
	ctx := asAdmin(context.Background())
	_, addr := pubOf(t, "b2")
	set := func(name string) api.SetPeerNameResponseObject {
		t.Helper()
		r, err := s.SetPeerName(ctx, api.SetPeerNameRequestObject{Address: addr, Body: &api.PeerNameRequest{Name: name}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := set(strings.Repeat("名", nodeNameMax))
	if _, ok := r.(api.SetPeerName204Response); !ok {
		t.Errorf("剛好 %d 字應可設定,得 %#v", nodeNameMax, r)
	}
	if _, ok := set(strings.Repeat("名", nodeNameMax+1)).(api.SetPeerName400JSONResponse); !ok {
		t.Errorf("%d 字應 400", nodeNameMax+1)
	}
}
