package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/store"
)

// The join token list is newest first; a pending token past its expiry reads as expired while used
// and revoked keep their stored status; node binding and source allowlist are returned, the secret
// hash is not.
func TestListJoinTokens(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	node, name := "node-a1", "台北一號"
	for i, tk := range []store.JoinToken{
		{ID: "jt-used", Status: "used", NodeID: node, NodeName: &name, ExpiresAt: now.Add(time.Hour)},
		{ID: "jt-revoked", Status: "revoked", NodeID: "n-jt-revoked", ExpiresAt: now.Add(time.Hour)},
		{ID: "jt-expired", Status: "pending", NodeID: "n-jt-expired", ExpiresAt: now.Add(-time.Minute)},
		{ID: "jt-pending", Status: "pending", NodeID: "n-jt-pending", AllowedSources: []string{"10.0.0.0/24"}, ExpiresAt: now.Add(time.Hour)},
	} {
		tk.SecretHash = "secret-hash-" + tk.ID
		tk.CreatedAt = now.Add(time.Duration(i-10) * time.Minute)
		tk.RpcPort, tk.MetricsPort = 8545, 9545
		if err := s.store.CreateJoinToken(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.ListJoinTokens(asAdmin(ctx), api.ListJoinTokensRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	list := r.(api.ListJoinTokens200JSONResponse)
	want := []struct {
		id     string
		status api.JoinTokenStatus
	}{{"jt-pending", api.JoinTokenStatusPending}, {"jt-expired", api.JoinTokenStatusExpired}, {"jt-revoked", api.JoinTokenStatusRevoked}, {"jt-used", api.JoinTokenStatusUsed}}
	if len(list) != len(want) {
		t.Fatalf("筆數 %d: %+v", len(list), list)
	}
	for i, w := range want {
		if list[i].Id != w.id || list[i].Status != w.status {
			t.Errorf("第 %d 筆應為 %s/%s,得 %s/%s", i, w.id, w.status, list[i].Id, list[i].Status)
		}
	}
	if p := list[0]; len(p.AllowedSources) != 1 || p.AllowedSources[0] != "10.0.0.0/24" || p.NodeId != "n-jt-pending" {
		t.Errorf("pending: %+v", p)
	}
	if u := list[3]; u.NodeId != node || u.NodeName == nil || *u.NodeName != name || !u.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Errorf("used: %+v", u)
	}
	if e := list[1]; e.AllowedSources == nil {
		t.Error("沒有來源限制應回空陣列不是 null")
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "secret-hash-") {
		t.Fatalf("回應不該帶 token 雜湊: %s", raw)
	}
}
