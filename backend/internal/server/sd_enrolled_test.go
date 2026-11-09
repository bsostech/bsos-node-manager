package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"besu-manager/backend/internal/store"
)

// /sd/targets against the database: only nodes holding an unrevoked issuance record are handed to
// Prometheus, so an inventory row without a certificate, and a node deleted and recreated under the
// same id (its old certificates revoked), are not scraped.
func TestSDTargetsOnlyEnrolledNodes(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	for _, id := range []string{"node-a1", "node-a2", "node-a3"} {
		if err := s.store.UpsertOwnNode(ctx, id, "org-test", 8545, 9545, nil); err != nil {
			t.Fatal(err)
		}
	}
	notAfter := time.Now().Add(30 * 24 * time.Hour)
	for _, c := range []struct{ serial, node string }{{"s-a1", "node-a1"}, {"s-a3", "node-a3"}} {
		node := c.node
		if err := s.store.InsertIssuedCert(ctx, store.IssuedCert{Serial: c.serial, Subject: "CN=" + node, NodeID: &node,
			NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter, CertPEM: "pem"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.RevokeCert(ctx, "s-a3", "node deleted", "admin", notAfter); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/sd/targets", nil)
	req.Header.Set("Authorization", "Bearer sd-secret")
	rec := httptest.NewRecorder()
	SDTargetsHandler(s.store, "sd-secret")(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("應 200,得 %d", rec.Code)
	}
	var targets []sdTarget
	if err := json.Unmarshal(rec.Body.Bytes(), &targets); err != nil {
		t.Fatal(err)
	}
	nodes := map[string]int{}
	for _, tg := range targets {
		nodes[tg.Labels["node"]]++
	}
	if len(nodes) != 1 || nodes["node-a1"] != 2 {
		t.Fatalf("只有 node-a1 有未撤銷的簽發紀錄,應只給它的 host/besu 兩個目標: %v", nodes)
	}
}
