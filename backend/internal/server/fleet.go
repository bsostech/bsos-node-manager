// GetFleetStatus returns the fleet source status, declared once by the overview status bar/legend
// (instead of red text on each chart). Two-state model: a node is either unreachable or absent from
// the result (ok is implicit and takes no space). It is derived from the Collector's own mTLS
// polling of each agent, which is the freshest source and works whether or not Prometheus is
// deployed.
package server

import (
	"context"
	"time"

	"besu-manager/backend/internal/api"
)

// fleetUnreachableAfter is how long an agent must stay unreachable before it is reported, so
// transient network jitter is not mistaken for an incident.
const fleetUnreachableAfter = 10 * time.Minute

func (s *Server) GetFleetStatus(ctx context.Context, _ api.GetFleetStatusRequestObject) (api.GetFleetStatusResponseObject, error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := make(api.GetFleetStatus200JSONResponse, 0)
	for _, n := range nodes {
		if st, ok := s.ownFleetStatus(n.ID, now); ok {
			out = append(out, st)
		}
	}
	return out, nil
}

func (s *Server) ownFleetStatus(nodeID string, now time.Time) (api.FleetNodeStatus, bool) {
	res, ok := s.col.Get(nodeID)
	// !EverSeen: never connected (unknown), so it cannot be "lost" yet; not reported.
	if !ok || !res.EverSeen || res.FailSince == nil {
		return api.FleetNodeStatus{}, false
	}
	if now.Sub(*res.FailSince) < fleetUnreachableAfter {
		return api.FleetNodeStatus{}, false
	}
	since := res.FailSince.UTC()
	reason := "node agent not responding"
	return api.FleetNodeStatus{NodeId: nodeID, Status: api.Unreachable, Since: since, Reason: &reason}, true
}
