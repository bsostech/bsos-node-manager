package server

import (
	"context"
	"testing"

	"besu-manager/backend/internal/api"
)

// Renaming a node answers 204 and the new name is what the node reads back afterwards.
func TestSetNodeNameStores(t *testing.T) {
	s, _ := accessTestServer(t)
	recordAudit(t)
	ctx := asAdmin(context.Background())
	if err := s.store.UpsertOwnNode(ctx, "node-a1", "org-test", 8545, 9545, nil); err != nil {
		t.Fatal(err)
	}
	name := "台北 1"
	r, err := s.SetNodeName(ctx, api.SetNodeNameRequestObject{Id: "node-a1", Body: &api.SetNodeNameJSONRequestBody{Name: &name}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(api.SetNodeName204Response); !ok {
		t.Fatalf("改名應 204,得 %#v", r)
	}
	if n, err := s.store.GetNode(ctx, "node-a1"); err != nil || n == nil || n.Name == nil || *n.Name != name {
		t.Fatalf("名稱應已寫入:%+v %v", n, err)
	}
}
