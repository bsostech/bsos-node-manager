package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"besu-manager/backend/internal/store"
)

// Prometheus HTTP service discovery (http_sd_configs). The platform serves the metrics scrape
// targets of our nodes from the nodes table + a node label (added to every series by SD
// relabeling). Nodes joining/being removed are picked up automatically, with no manual edits to
// prometheus.yml.

type sdTarget struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

// buildSDTargets converts our nodes into Prometheus SD targets. The target is always the platform
// itself (host), with the scrape path pointing at /agent-proxy/{nodeID}/metrics: nodes have no
// externally listening port, so metrics are fetched back over the WS tunnel the agent dials out.
// node/org labels are added to every series by SD relabeling. enrolled holds node ids that have
// ever obtained a certificate (semantics and the CA-reset boundary: see store.EnrolledNodeIDs).
func buildSDTargets(nodes []store.Node, enrolled map[string]bool, host, scheme string) []sdTarget {
	out := make([]sdTarget, 0, len(nodes))
	for _, n := range nodes {
		if !enrolled[n.ID] {
			// Not joined yet. Must not be included: a deployed node gets its inventory row before
			// installation, so including it would make the proxy return 503, up == 0, and two
			// minutes later fire a critical for a machine that is not even installed yet.
			continue
		}
		if n.QuarantinedAt != nil {
			// A quarantined node's handshake is rejected and it is not in the hub, so the proxy
			// returns 503 anyway. The real reason to exclude it is so NodeOffline and the
			// quarantine alert do not each fire for the same thing.
			continue
		}
		// Two targets per node: host metrics (agent) and Besu's native metrics (through the agent
		// tunnel). The source label separates them: "node offline" looks at host, "Besu not up"
		// looks at besu (internal/alerting/defaults.go).
		for _, t := range []struct{ source, route string }{{"host", AgentMetricsRoute}, {"besu", BesuMetricsRoute}} {
			out = append(out, sdTarget{
				Targets: []string{host},
				Labels: map[string]string{
					"node": n.ID, "org": n.Org, "source": t.source,
					"__metrics_path__": strings.Replace(t.route, "{nodeID}", n.ID, 1),
					"__scheme__":       scheme,
				},
			})
		}
	}
	return out
}

// SDTargetsHandler: GET /sd/targets. Protected by the SD_TOKEN bearer (disabled when unset,
// returning 404). Prometheus passes the token via http_sd_configs + authorization.credentials.
func SDTargetsHandler(st *store.Store, token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sdBearerOK(w, r, token) {
			return
		}
		nodes, err := st.ListNodes(r.Context())
		if err != nil {
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		enrolled, err := st.EnrolledNodeIDs(r.Context())
		if err != nil {
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(buildSDTargets(nodes, enrolled, r.Host, scheme))
	}
}
