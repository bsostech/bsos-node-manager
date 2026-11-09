package server

import (
	"context"
	"testing"
	"time"

	"besu-manager/backend/internal/api"
)

// The fleet status bar flags a node only after it has been unreachable for fleetUnreachableAfter:
// a healthy node, a node never seen, and a node that just dropped are all left out.
func TestGetFleetStatus(t *testing.T) {
	g := newChainFixture(t, true)
	g.rpc.down["node-a3"] = true // never seen
	g.poll(t)
	fleet := func() api.GetFleetStatus200JSONResponse {
		t.Helper()
		res, err := g.s.GetFleetStatus(context.Background(), api.GetFleetStatusRequestObject{})
		if err != nil {
			t.Fatal(err)
		}
		return res.(api.GetFleetStatus200JSONResponse)
	}
	if got := fleet(); len(got) != 0 {
		t.Fatalf("healthy and never-seen nodes reported: %+v", got)
	}

	// node-a2 drops: one more round on the same collector records the start of the outage.
	g.rpc.down = map[string]bool{"node-a2": true, "node-a3": true}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { g.s.col.Run(ctx, time.Hour); close(done) }()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if r, _ := g.s.col.Get("node-a2"); r.FailSince != nil {
			break
		}
	}
	cancel()
	<-done
	if r, _ := g.s.col.Get("node-a2"); !r.EverSeen || r.FailSince == nil {
		t.Fatalf("fixture: node-a2 should be seen and now failing: %+v", r)
	}
	if got := fleet(); len(got) != 0 {
		t.Fatalf("node down for seconds reported: %+v", got)
	}

	// Past the threshold the dropped node is reported as unreachable from when it dropped; the
	// never-seen node and the healthy one still are not.
	r, _ := g.s.col.Get("node-a2")
	later := time.Now().Add(fleetUnreachableAfter + time.Minute)
	st, ok := g.s.ownFleetStatus("node-a2", later)
	if !ok || st.NodeId != "node-a2" || st.Status != api.Unreachable || !st.Since.Equal(r.FailSince.UTC()) || st.Reason == nil || *st.Reason == "" {
		t.Fatalf("node down past the threshold: %+v, %v", st, ok)
	}
	for _, id := range []string{"node-a1", "node-a3"} {
		if st, ok := g.s.ownFleetStatus(id, later); ok {
			t.Fatalf("%s reported: %+v", id, st)
		}
	}
}
