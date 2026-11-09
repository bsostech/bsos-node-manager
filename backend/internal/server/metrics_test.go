package server

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/store"
)

// Platform gauges read live from the DB on each scrape: the latest certificate's expiry (unix
// seconds) and a quarantined node's flag both come out.
func TestPlatformCollectorCertAndQuarantine(t *testing.T) {
	s, _ := accessTestServer(t)
	ctx := context.Background()
	if err := s.store.UpsertOwnNode(ctx, "node-a1", "org-test", 8545, 9545, nil); err != nil {
		t.Fatal(err)
	}
	id := "node-a1"
	notAfter := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	if err := s.store.InsertIssuedCert(ctx, store.IssuedCert{Serial: "s1", Subject: "CN=node-a1", NodeID: &id, NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter, CertPEM: "pem"}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.MarkNodeQuarantined(ctx, id); err != nil {
		t.Fatal(err)
	}
	s.col = collector.New(s.store, downRequester{}, allowRPC{})
	ch := make(chan prometheus.Metric, 32)
	(&platformCollector{srv: s}).Collect(ch)
	close(ch)
	got := map[string]float64{}
	for m := range ch {
		var d dto.Metric
		if err := m.Write(&d); err != nil {
			t.Fatal(err)
		}
		if slices.ContainsFunc(d.Label, func(l *dto.LabelPair) bool { return l.GetName() == "node_id" && l.GetValue() == id }) {
			got[m.Desc().String()] = d.GetGauge().GetValue()
		}
	}
	if v, ok := got[certExpiryDesc.String()]; !ok || v != float64(notAfter.Unix()) {
		t.Errorf("憑證到期 = %v(有=%v),want %d", v, ok, notAfter.Unix())
	}
	if v, ok := got[nodeQuarantinedDesc.String()]; !ok || v != 1 {
		t.Errorf("隔離 = %v(有=%v)", v, ok)
	}
}
