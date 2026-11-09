package server

import (
	"context"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/store"
)

// /nodes/{id}/cert: the certificate shown is the most recently issued one (by issuance time, not
// by expiry), trusted only while unrevoked and unexpired, with issuer O=<org in lowercase>; a node
// with no issuance record is untrusted with no serial. Connection follows the collector: connected
// with lastSeen when the agent answered this round, lost otherwise. Unknown node gives 404.
func TestGetNodeCert(t *testing.T) {
	s, _ := accessTestServer(t)
	s.org = "Org-Test"
	ctx := context.Background()
	for _, id := range []string{"node-a1", "node-a2", "node-a3", "node-a4"} {
		if err := s.store.UpsertOwnNode(ctx, id, "org-test", 8545, 9545, nil); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	issue := func(serial, node string, notBefore, notAfter time.Time) {
		t.Helper()
		c := store.IssuedCert{Serial: serial, Subject: "CN=" + node, NodeID: &node, NotBefore: notBefore, NotAfter: notAfter, CertPEM: "pem"}
		if err := s.store.InsertIssuedCert(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	// a1: an older long-lived certificate and a newer short-lived one; the newer one counts
	issue("a1-old", "node-a1", now.Add(-48*time.Hour), now.Add(365*24*time.Hour))
	issue("a1-new", "node-a1", now.Add(-time.Hour), now.Add(30*24*time.Hour))
	issue("a2-revoked", "node-a2", now.Add(-time.Hour), now.Add(30*24*time.Hour))
	if err := s.store.RevokeCert(ctx, "a2-revoked", "test", "admin", now.Add(30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	issue("a4-expired", "node-a4", now.Add(-48*time.Hour), now.Add(-time.Hour))
	pollCollector(t, s, snapRPC{down: map[string]bool{"node-a2": true}}, "node-a1", "node-a2", "node-a3", "node-a4")

	get := func(id string) api.NodeCertStatus {
		t.Helper()
		r, err := s.GetNodeCert(ctx, api.GetNodeCertRequestObject{Id: id})
		if err != nil {
			t.Fatal(err)
		}
		v, ok := r.(api.GetNodeCert200JSONResponse)
		if !ok {
			t.Fatalf("%s: %#v", id, r)
		}
		return api.NodeCertStatus(v)
	}
	a1 := get("node-a1")
	if a1.Cert != api.Valid || a1.Serial == nil || *a1.Serial != "a1-new" || a1.NotAfter == nil || !a1.NotAfter.Equal(now.Add(30*24*time.Hour)) {
		t.Errorf("a1 取最新簽發的那張: %+v", a1)
	}
	if a1.Connection != api.NodeCertStatusConnectionConnected || a1.LastSeen == nil {
		t.Errorf("a1 這輪有回應應為 connected: %+v", a1)
	}
	if a2 := get("node-a2"); a2.Cert != api.Untrusted || a2.Serial == nil || *a2.Serial != "a2-revoked" || a2.Connection != api.NodeCertStatusConnectionLost || a2.LastSeen != nil {
		t.Errorf("a2 已撤銷、連不上: %+v", a2)
	}
	if a3 := get("node-a3"); a3.Cert != api.Untrusted || a3.Serial != nil || a3.NotAfter != nil {
		t.Errorf("a3 沒簽發紀錄: %+v", a3)
	}
	if a4 := get("node-a4"); a4.Cert != api.Untrusted || a4.Serial == nil {
		t.Errorf("a4 已過期: %+v", a4)
	}
	if r, err := s.GetNodeCert(ctx, api.GetNodeCertRequestObject{Id: "nope"}); err != nil {
		t.Fatal(err)
	} else if _, ok := r.(api.GetNodeCert404JSONResponse); !ok {
		t.Fatalf("不存在的節點應 404: %#v", r)
	}
}
