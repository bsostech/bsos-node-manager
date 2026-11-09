package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"besu-manager/backend/internal/store"

	"github.com/go-chi/chi/v5"
)

type fakeAgents struct {
	tr   http.RoundTripper
	node string // only this id counts as online
}

func (f fakeAgents) AgentTransport(nodeID string) (http.RoundTripper, bool) {
	if nodeID != f.node {
		return nil, false
	}
	return f.tr, true
}

type recordingTripper struct {
	got *http.Request
}

func (rt *recordingTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := r.Context().Err(); err != nil { // a real transport cannot forward once the request's deadline has passed
		return nil, err
	}
	rt.got = r
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain; version=0.0.4"}},
		Body:       io.NopCloser(strings.NewReader("besu_up 1\n")),
	}, nil
}

func serveProxy(agents AgentTransports, token, path, auth string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Get("/agent-proxy/{nodeID}/metrics", AgentMetricsProxy(agents, token))
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// Forwarding must keep the agent's original response (status code + Content-Type): Prometheus fails
// the scrape outright on a missing or invalid Content-Type, so headers filled in by the platform
// would be a source of errors.
func TestAgentMetricsProxyForwardsResponse(t *testing.T) {
	rt := &recordingTripper{}
	rec := serveProxy(fakeAgents{tr: rt, node: "node-a1"}, "secret", "/agent-proxy/node-a1/metrics", "Bearer secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("狀態碼 = %d, 期望 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; version=0.0.4" {
		t.Errorf("Content-Type 應原樣帶過來, 得 %q", ct)
	}
	if rec.Body.String() != "besu_up 1\n" {
		t.Errorf("body = %q", rec.Body.String())
	}
	if rt.got.URL.Path != "/host-metrics" || rt.got.Header.Get("X-Target") != "" {
		t.Errorf("轉給 agent 的路徑 = %q X-Target=%q, 期望 /host-metrics 且不帶 X-Target", rt.got.URL.Path, rt.got.Header.Get("X-Target"))
	}
}

// Security: the inbound bearer is the platform's own SD_TOKEN (the same token also works on
// /sd/targets, the platform's /metrics and /alerts/webhook); forwarding it as is would hand it to
// every agent.
func TestAgentMetricsProxyStripsAuthorization(t *testing.T) {
	rt := &recordingTripper{}
	serveProxy(fakeAgents{tr: rt, node: "node-a1"}, "secret", "/agent-proxy/node-a1/metrics", "Bearer secret")
	if got := rt.got.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization 不該轉給 agent, 得 %q", got)
	}
}

// Offline must return 503: letting RoundTrip fail would only give 502, while up == 0 should mean
// "cannot reach the node".
func TestAgentMetricsProxyOfflineIs503(t *testing.T) {
	rec := serveProxy(fakeAgents{node: "node-a1"}, "secret", "/agent-proxy/node-zz/metrics", "Bearer secret")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("離線節點應 503, 得 %d", rec.Code)
	}
}

func TestAgentMetricsProxyTokenGate(t *testing.T) {
	agents := fakeAgents{tr: &recordingTripper{}, node: "node-a1"}
	if rec := serveProxy(agents, "", "/agent-proxy/node-a1/metrics", "Bearer secret"); rec.Code != http.StatusNotFound {
		t.Errorf("未設 token 應 404, 得 %d", rec.Code)
	}
	if rec := serveProxy(agents, "secret", "/agent-proxy/node-a1/metrics", "Bearer wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("錯 token 應 401, 得 %d", rec.Code)
	}
}

type fakeNodes map[string]*store.Node

func (f fakeNodes) GetNode(_ context.Context, id string) (*store.Node, error) { return f[id], nil }

// Besu native metrics: forwarded through the agent with X-Target to the node's local metrics port
// (port taken from the nodes table); nonexistent nodes are not forwarded.
func TestBesuMetricsProxyTargetsMetricsPort(t *testing.T) {
	rt := &recordingTripper{}
	nodes := fakeNodes{
		"node-a": {ID: "node-a", MetricsPort: 9600},
	}
	r := chi.NewRouter()
	r.Get(BesuMetricsRoute, BesuMetricsProxy(fakeAgents{tr: rt, node: "node-a"}, nodes, "tok"))
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	rec := get("/agent-proxy/node-a/besu-metrics")
	if rec.Code != http.StatusOK || rt.got == nil || rt.got.URL.Path != "/metrics" || rt.got.Header.Get("X-Target") != "127.0.0.1:9600" {
		t.Fatalf("code=%d path=%q X-Target=%q", rec.Code, rt.got.URL.Path, rt.got.Header.Get("X-Target"))
	}
	if rec = get("/agent-proxy/node-x/besu-metrics"); rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的節點應 404,得 %d", rec.Code)
	}
}

type slowNodes struct{}

func (slowNodes) GetNode(ctx context.Context, _ string) (*store.Node, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// The port lookup's DB call is also within the timeout: when the DB hangs, the platform must return
// 504 before Prometheus's scrape_timeout instead of hanging along with it.
func TestBesuMetricsProxyTimeoutCoversLookup(t *testing.T) {
	r := chi.NewRouter()
	r.Get(BesuMetricsRoute, besuMetricsProxy(fakeAgents{tr: &recordingTripper{}, node: "node-a"}, slowNodes{}, "tok", 50*time.Millisecond))
	req := httptest.NewRequest(http.MethodGet, "/agent-proxy/node-a/besu-metrics", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	start := time.Now()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusGatewayTimeout || time.Since(start) > time.Second {
		t.Fatalf("code=%d took=%s", rec.Code, time.Since(start))
	}
}

// flakyNodes fails DB queries while down (simulating a DB outage).
type flakyNodes struct {
	nodes fakeNodes
	down  *bool
}

func (f flakyNodes) GetNode(ctx context.Context, id string) (*store.Node, error) {
	if *f.down {
		return nil, errors.New("connection refused")
	}
	return f.nodes.GetNode(ctx, id)
}

// During a DB outage the last looked-up port is reused so scrapes of all nodes do not fail; nodes
// never looked up still get 502; after a node is deleted (DB says it does not exist) the remembered
// port is cleared too.
func TestBesuMetricsProxyKeepsPortWhileDBDown(t *testing.T) {
	rt := &recordingTripper{}
	down := false
	nodes := flakyNodes{nodes: fakeNodes{"node-a": {ID: "node-a", MetricsPort: 9600}}, down: &down}
	r := chi.NewRouter()
	r.Get(BesuMetricsRoute, BesuMetricsProxy(fakeAgents{tr: rt, node: "node-a"}, nodes, "tok"))
	get := func(id string) int {
		req := httptest.NewRequest(http.MethodGet, "/agent-proxy/"+id+"/besu-metrics", nil)
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := get("node-a"); c != http.StatusOK {
		t.Fatalf("DB 正常時 = %d", c)
	}
	down = true
	rt.got = nil
	if c := get("node-a"); c != http.StatusOK || rt.got.Header.Get("X-Target") != "127.0.0.1:9600" {
		t.Fatalf("DB 斷線應沿用上次的埠,code=%d", c)
	}
	if c := get("node-b"); c != http.StatusBadGateway {
		t.Fatalf("沒查到過的節點應 502,得 %d", c)
	}
	down = false
	delete(nodes.nodes, "node-a")
	if c := get("node-a"); c != http.StatusNotFound {
		t.Fatalf("刪掉的節點應 404,得 %d", c)
	}
	down = true
	if c := get("node-a"); c != http.StatusBadGateway {
		t.Fatalf("刪掉後不該再沿用舊埠,得 %d", c)
	}
}

// hungNodes answers normally at first, then after hang waits until timeout like a stuck DB.
type hungNodes struct {
	nodes fakeNodes
	hang  *bool
}

func (h hungNodes) GetNode(ctx context.Context, id string) (*store.Node, error) {
	if *h.hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return h.nodes.GetNode(ctx, id)
}

// When the DB hangs (rather than refusing connections), the port lookup uses only part of the
// timeout and the rest is used to forward with the remembered port.
func TestBesuMetricsProxyHungDBUsesKeptPort(t *testing.T) {
	rt := &recordingTripper{}
	hang := false
	nodes := hungNodes{nodes: fakeNodes{"node-a": {ID: "node-a", MetricsPort: 9600}}, hang: &hang}
	r := chi.NewRouter()
	r.Get(BesuMetricsRoute, besuMetricsProxy(fakeAgents{tr: rt, node: "node-a"}, nodes, "tok", 200*time.Millisecond))
	get := func() int {
		req := httptest.NewRequest(http.MethodGet, "/agent-proxy/node-a/besu-metrics", nil)
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := get(); c != http.StatusOK {
		t.Fatalf("DB 正常時 = %d", c)
	}
	hang = true
	rt.got = nil
	if c := get(); c != http.StatusOK || rt.got == nil || rt.got.Header.Get("X-Target") != "127.0.0.1:9600" {
		t.Fatalf("DB 卡住應沿用上次的埠,code=%d", c)
	}
}
