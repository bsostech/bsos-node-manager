package server

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"sync"
	"time"

	"besu-manager/backend/internal/pki"
	"besu-manager/backend/internal/store"

	"github.com/go-chi/chi/v5"
)

// Two routes for Prometheus to scrape a node's metrics (auth.isPublic lets them through by the
// /agent-proxy/ prefix; that lives in the auth package and cannot import these):
//   - AgentMetricsRoute: host metrics computed by the agent itself (bsos_node_cpu_percent etc.).
//   - BesuMetricsRoute: Besu's native metrics, forwarded as is through the agent tunnel with
//     X-Target; chain metrics are converted to bsos_node_* by Prometheus recording rules (see
//     deploy/platform/docker-compose/rules.yml).
const (
	AgentMetricsRoute = "/agent-proxy/{nodeID}/metrics"
	BesuMetricsRoute  = "/agent-proxy/{nodeID}/besu-metrics"
)

// agentScrapeTimeout is the platform-side limit. Prometheus's scrape_timeout is 10 seconds, and
// this must be shorter, fast enough that Prometheus gets a 502 instead of timing out itself.
const agentScrapeTimeout = 8 * time.Second

// AgentTransports returns the transport for sending HTTP requests to a node (the yamux session on
// that WS). It is an interface rather than *pki.Service so this can be tested against a fake
// transport.
type AgentTransports interface {
	AgentTransport(nodeID string) (http.RoundTripper, bool)
}

// NodeLookup looks up a node's metrics port (implemented by store.Store).
type NodeLookup interface {
	GetNode(ctx context.Context, id string) (*store.Node, error)
}

// AgentMetricsProxy: GET /agent-proxy/{nodeID}/metrics -> the agent's /host-metrics (Prometheus
// text format).
func AgentMetricsProxy(agents AgentTransports, token string) http.HandlerFunc {
	return metricsProxy(agents, token, agentScrapeTimeout, func(*http.Request, string) (string, string, error) {
		return pki.HostMetricsPath, "", nil
	})
}

// BesuMetricsProxy: GET /agent-proxy/{nodeID}/besu-metrics -> the node's local
// 127.0.0.1:<metrics_port>/metrics. The port is a column in the platform's nodes table; the agent
// only checks it is loopback and forwards.
func BesuMetricsProxy(agents AgentTransports, nodes NodeLookup, token string) http.HandlerFunc {
	return besuMetricsProxy(agents, nodes, token, agentScrapeTimeout)
}

func besuMetricsProxy(agents AgentTransports, nodes NodeLookup, token string, timeout time.Duration) http.HandlerFunc {
	// ports holds each node's last looked-up port (lastPort). It is reused while the DB is
	// temporarily unavailable, so a DB outage does not turn every node into up == 0. It is cleared
	// when the DB says the node does not exist; a node already removed from SD keeps an entry that
	// only a scrape with the same id can read.
	var ports sync.Map
	return metricsProxy(agents, token, timeout, func(r *http.Request, nodeID string) (string, string, error) {
		// The port lookup uses only 1/4 of the timeout: if the DB hangs (connects but does not
		// respond) there is still time to forward using the remembered port.
		ctx, cancel := context.WithTimeout(r.Context(), timeout/4)
		defer cancel()
		port, err := lookupMetricsPort(ctx, nodes, nodeID)
		switch {
		case err == nil:
			ports.Store(nodeID, lastPort{port: port})
		case errors.Is(err, errUnknownNode):
			ports.Delete(nodeID)
			return "", "", err
		default:
			v, ok := ports.Load(nodeID)
			if !ok {
				return "", "", err
			}
			last := v.(lastPort)
			if !last.stale { // logged once per node when reuse starts, no log spam however long the DB is down
				log.Printf("agent-proxy: look up node %s (reusing last port %d): %v", nodeID, last.port, err)
				ports.Store(nodeID, lastPort{port: last.port, stale: true})
			}
			port = last.port
		}
		// The port range is guaranteed by the DB CHECK (enforced on write), so it is not
		// re-validated here.
		return "/metrics", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil
	})
}

// lastPort returns the last looked-up port; stale = it is being reused now (DB lookup failed), used
// only to decide whether to log.
type lastPort struct {
	port  int
	stale bool
}

// lookupMetricsPort looks up a node's metrics port in the DB; a nonexistent node returns
// errUnknownNode.
func lookupMetricsPort(ctx context.Context, nodes NodeLookup, nodeID string) (int, error) {
	n, err := nodes.GetNode(ctx, nodeID)
	if err != nil {
		return 0, err
	}
	if n == nil {
		return 0, errUnknownNode
	}
	return n.MetricsPort, nil
}

var errUnknownNode = errors.New("unknown node")

// routeErrResponse: node not found = 404; DB timeout = 504; other DB errors = 502 (Prometheus
// records them all as up == 0). The response carries only a fixed message and never sends the raw
// DB error to the scraper; the raw text stays in the platform log.
func routeErrResponse(nodeID string, err error) (int, string) {
	switch {
	case errors.Is(err, errUnknownNode):
		return http.StatusNotFound, "unknown node"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "node lookup timed out"
	}
	log.Printf("agent-proxy: look up node %s: %v", nodeID, err)
	return http.StatusBadGateway, "node lookup failed"
}

// metricsProxy forwards Prometheus's scrape as is to the agent (or through the agent to the node's
// local endpoint). Protected by SD_TOKEN bearer (same as /sd/targets, same token, disabled when
// unset).
//
// It forwards as is instead of rebuilding the response: what comes back is a complete HTTP
// response, with Content-Type, status code and headers together. Prometheus fails the scrape
// outright on a missing or invalid Content-Type, so headers filled in by the platform would be a
// source of errors.
func metricsProxy(agents AgentTransports, token string, timeout time.Duration, route func(r *http.Request, nodeID string) (path, target string, err error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sdBearerOK(w, r, token) {
			return
		}
		// The timeout starts here and covers both the port lookup (DB) and the forward: even if the
		// DB hangs, respond before Prometheus's scrape_timeout.
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		r = r.WithContext(ctx)
		nodeID := chi.URLParam(r, "nodeID")
		path, target, err := route(r, nodeID)
		if err != nil {
			code, msg := routeErrResponse(nodeID, err)
			http.Error(w, msg, code)
			return
		}
		tr, online := agents.AgentTransport(nodeID)
		if !online {
			// Offline must return 503 here: letting RoundTrip fail would only give 502, while up ==
			// 0 should mean "cannot reach the node", not "the proxy is broken".
			http.Error(w, "agent offline", http.StatusServiceUnavailable)
			return
		}
		(&httputil.ReverseProxy{
			Transport: tr,
			ErrorLog:  log.Default(),
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(&url.URL{Scheme: "http", Host: pki.RequestHost})
				pr.Out.URL.Path = path // set explicitly, not reusing the inbound /agent-proxy/<id>/...
				pr.Out.URL.RawQuery = ""
				// ⚠️ Strip the authorization header: the inbound one is the SD_TOKEN Prometheus
				// brought, and the same token also works on /sd/targets, the platform's /metrics
				// and /alerts/webhook (injecting fake alerts). Forwarding it as is would hand the
				// platform's token to every agent.
				pr.Out.Header.Del("Authorization")
				if target != "" {
					pr.Out.Header.Set("X-Target", target)
				}
			},
		}).ServeHTTP(w, r)
	}
}
