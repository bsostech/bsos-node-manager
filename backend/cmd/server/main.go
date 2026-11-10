// Platform Server entry point: serves the Platform API for the frontend.
package main

import (
	"context"
	"crypto"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"besu-manager/backend/internal/alerting"
	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/auth"
	"besu-manager/backend/internal/ca"
	"besu-manager/backend/internal/collector"
	"besu-manager/backend/internal/pki"
	"besu-manager/backend/internal/prom"
	"besu-manager/backend/internal/reqinfo"
	"besu-manager/backend/internal/rpcops"
	"besu-manager/backend/internal/server"
	"besu-manager/backend/internal/store"
	"besu-manager/backend/internal/version"
	"besu-manager/backend/internal/webapp"
)

const (
	collectInterval = 10 * time.Second // lower frequency to reduce node RPC load; the frontend's smallest refresh step (frontend/src/lib/refreshInterval.ts) matches this value, change both together
)

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// getenvInt reads an integer env var, returning def when unset or unparsable.
func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// wiring holds the Core objects handed to wireEnterprise (unused in Core, see edition_core.go).
type wiring struct {
	router       chi.Router
	pool         *pgxpool.Pool
	store        *store.Store
	auth         *auth.Service
	master       []byte
	server       *server.Server
	pki          *pki.Service
	platformAddr string
	rules        *alerting.RulesService
	stop         context.Context // cancelled on SIGTERM / Ctrl-C
	listeners    *sync.WaitGroup // listeners to wait for before exiting
}

func main() {
	_ = godotenv.Load()
	log.Printf("bsos-node-manager server %s (%s edition)", version.Version, edition)
	// SIGTERM (docker stop, systemctl stop) and Ctrl-C: stop accepting connections, let requests in flight finish,
	// then exit 0 instead of being killed mid-request.
	stop, cancelStop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancelStop()
	var listeners sync.WaitGroup

	dbURL := getenv("DATABASE_URL", "postgres://besu:besu@localhost:5432/besu_manager")
	orgID := os.Getenv("ORG_ID")
	if err := ca.CheckOrgID(orgID); err != nil {
		log.Fatal(err)
	}
	platformAddr := getenv("PLATFORM_ADDR", "localhost:8443") // mTLS address agents dial when joining

	// TLS mode of the website (frontend + Platform API, same-origin, served directly with no web server in front; see internal/webapp).
	// The three options map one-to-one to --acme / --cert+--key / --http in up.sh/setup.sh.
	webappCfg := webapp.Config{
		Mode:     webapp.TLSMode(getenv("TLS_MODE", "http")),
		Domain:   getenv("SITE_DOMAIN", ""),
		Email:    getenv("ACME_EMAIL", ""),
		CacheDir: getenv("ACME_CACHE_DIR", "/var/lib/bsos-node-manager/acme-cache"),
		Staging:  getenv("ACME_STAGING", "") == "true",
		CertFile: getenv("SITE_CERT_FILE", ""),
		KeyFile:  getenv("SITE_KEY_FILE", ""),
	}
	if webappCfg.Mode == webapp.ModeHTTP {
		webappCfg.HTTPAddr = getenv("LISTEN_ADDR", ":80")
	} else {
		webappCfg.Addr = ":443"                             // the TLS listen port must not be misconfigurable; acme/manual always use 443 and ignore LISTEN_ADDR
		webappCfg.HTTPAddr = getenv("REDIRECT_ADDR", ":80") // only redirects 80->443 (user convenience)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("connect postgres: %v", err)
	}
	st := store.New(pool)
	if err := st.Migrate(ctx); err != nil {
		log.Fatalf("apply schema: %v", err)
	}
	masterBytes, err := ca.MasterKeyFromEnv()
	if err != nil {
		log.Fatal(err)
	}

	// Wire Enterprise tables, the audit writer and default rules right away, so later startup steps (CA bootstrap etc.) get audited.
	if err := prepareEnterprise(ctx, pool, masterBytes); err != nil {
		log.Fatalf("prepare enterprise: %v", err)
	}

	// AuthN/RBAC: session + bootstrap admin + local accounts. SSO (LDAP/OIDC) is Enterprise, wired by wireEnterprise.
	// The cookie's Secure flag is derived from the TLS mode instead of a separate switch that could contradict it:
	// when the site runs HTTPS (acme/manual) the session cookie must be Secure, while in plain-HTTP mode the
	// browser would never send a Secure cookie back. A standalone switch defaulting to false would be an insecure
	// default whenever someone forgot to set it.
	// COOKIE_SECURE stays as an explicit override; its only legitimate use is TLS_MODE=http behind a TLS terminator.
	cookieSecure := webappCfg.Mode != webapp.ModeHTTP
	if v := getenv("COOKIE_SECURE", ""); v != "" {
		cookieSecure = v == "true"
	}
	authSvc := auth.New(st, orgID, cookieSecure)
	if err := authSvc.Bootstrap(ctx); err != nil {
		log.Fatalf("auth bootstrap: %v", err)
	}
	leafTTL, err := time.ParseDuration(getenv("LEAF_TTL", "72h"))
	if err != nil {
		log.Fatalf("LEAF_TTL: %v", err)
	}
	if leafTTL < 24*time.Hour {
		log.Printf("warning: LEAF_TTL=%s is below the supported range (24h-7d), for rotation demos only", leafTTL)
	}
	pkiSvc, err := pki.Load(ctx, st, masterBytes, orgID, leafTTL)
	if err != nil {
		log.Fatalf("pki: %v", err)
	}
	// Signing key for node RPC tokens (pluggable backend): defaults to db, which generates a key pair on first start
	// and stores the private key in the DB encrypted with CA_MASTER_KEY, backed up like the CA; rotating the key =
	// every node redeploys the public key + restarts. Node operators place the public key next to Besu.
	// Backends available in this build
	rpcKeyBackends := map[string]auth.RpcKeyBackend{
		"db": func(ctx context.Context) (crypto.Signer, error) { return auth.LoadOrCreateRpcKey(ctx, st, masterBytes) },
	}
	rpcKeyBackend := getenv("RPC_KEY_BACKEND", "db")
	rpcSigner, err := auth.OpenRpcSigner(ctx, rpcKeyBackend, rpcKeyBackends)
	if err != nil {
		log.Fatalf("rpc signing key: %v", err)
	}
	log.Printf("rpc auth: signing backend %s, platform public key digest sha256=%s (GET /api/v1/platform/rpc-public-key)", rpcKeyBackend, rpcSigner.Digest())
	go pkiSvc.RunRotation(context.Background(), 30*time.Second)
	go pkiSvc.RunDenyWatch(context.Background(), 2*time.Second) // revocation takes effect in <5s
	// SD_TOKEN_DIR is where sd_token is written for Prometheus and Alertmanager; SD_TOKEN_GID is their containers' gid.
	sdTokenDir, sdTokenGID := getenv("SD_TOKEN_DIR", ""), getenvInt("SD_TOKEN_GID", -1)
	// The server writes the SD bearer into a shared volume (0640+gid) and Prometheus reads /sd-token/sd_token;
	// this avoids the permission trap where a host bind-mounted file's uid != the container's nobody (non-swarm compose file secrets keep host permissions).
	if err := pki.WriteSDToken(sdTokenDir, getenv("SD_TOKEN", ""), sdTokenGID); err != nil {
		log.Printf("write sd_token: %v", err)
	}
	trustedProxies, err := reqinfo.ParseCIDRs(os.Getenv("TRUSTED_PROXY_CIDR"))
	if err != nil {
		log.Fatalf("TRUSTED_PROXY_CIDR: %v", err)
	}
	// Internal CA for SMTP STARTTLS/TLS, shared by alert mail and password mail. Unreadable refuses to start,
	// checked before any listener opens so a bad file exits without having served anything.
	smtpCA, err := alerting.SMTPCAFromEnv()
	if err != nil {
		log.Fatalf("invalid SMTP_CA_FILE: %v", err)
	}

	agentAPIAddr := getenv("AGENT_API_ADDR", ":8443")
	// AGENT_BINARY_DIR: the public HTTP endpoint (first install) and the mTLS endpoint (upgrade) share the same directory setting.
	agentBinaryDir := os.Getenv("AGENT_BINARY_DIR")
	listeners.Add(1)
	go func() {
		defer listeners.Done()
		// agents connect to 8443 directly (mTLS, no L7 proxy), so the source IP is always the connection address
		// The read deadlines do not reach the agent's WebSocket: net/http clears them when the connection is upgraded.
		srv := webapp.NewServer(agentAPIAddr, reqinfo.Middleware(nil)(pkiSvc.Router(agentBinaryDir)))
		srv.TLSConfig = pkiSvc.ServerTLSConfig()
		log.Printf("agent mTLS listener on %s (org=%s, root digest=%s)",
			agentAPIAddr, pkiSvc.CA.Org, pkiSvc.CA.RootDigest()[:12])
		// certificates come from TLSConfig
		if err := webapp.ServeUntil(stop, srv, func() error { return srv.ListenAndServeTLS("", "") }); err != nil {
			log.Fatal(err)
		}
	}()

	// The platform issues JSON-RPC to nodes directly, relayed over the agent-dialed WS reverse tunnel to the node's local RPC port.
	ops := rpcops.New(pkiSvc, rpcSigner, st)
	col := collector.New(st, pkiSvc, ops) // host metrics come from the agent; the platform queries the Besu layers over RPC itself
	go col.Run(context.Background(), collectInterval)
	go col.RunHead(context.Background()) // the chain page's head updates every 2s (asks only the source node for its latest block)

	r := chi.NewRouter()
	// No RealIP: by default nothing proxies in front, so X-Forwarded-For / X-Real-IP / True-Client-IP are whatever the
	// request says, and trusting them lets the logged source IP be forged. For deployments behind a proxy see deploy/platform/README.md.
	r.Use(middleware.RequestID, middleware.Logger, middleware.Recoverer)
	// Audit source IP: X-Forwarded-For is trusted only when the connection comes from TRUSTED_PROXY_CIDR (see reqinfo). Must run before auth so permission-denied records also carry the IP.
	r.Use(reqinfo.Middleware(trustedProxies))
	r.Use(authSvc.Middleware) // identity + login (public routes: see auth.isPublic); role permissions are checked after the route matches (auth.RequireScopes)

	// Routes for the internal-only plain HTTP listener (see internalAddr at the end of the file): only the few our own
	// Prometheus / Alertmanager / healthcheck call, so login and admin APIs never appear in cleartext on the docker
	// network. Each handler is built once and shared by both (BesuMetricsProxy is stateful).
	internal := chi.NewRouter()
	internal.Use(middleware.RequestID, middleware.Logger, middleware.Recoverer)
	sdToken := getenv("SD_TOKEN", "")
	both := func(method, path string, h http.HandlerFunc) {
		r.Method(method, path, h)
		internal.Method(method, path, h)
	}

	// Plain-text version of the platform RPC signing public key (the JSON version is in the contract): install.sh saves it with curl -o.
	r.Get("/api/v1/platform/rpc-public-key.pem", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		_, _ = w.Write([]byte(rpcSigner.PublicKeyPEM()))
	})

	// Besu logging config (log4j2.xml; the join page passes sink parameters according to how Besu runs)
	r.Get("/api/v1/platform/besu-log4j2.xml", server.BesuLog4jHandler())

	// node install script + agent binary (public; fetched by the curl command the frontend builds, token passed as a parameter)
	r.Get("/install.sh", server.InstallScriptHandler())
	r.Get("/upgrade-agent.sh", server.UpgradeScriptHandler())
	// offline install bundle (requires login; downloaded via the join page's "離線安裝包" (offline bundle) button, see getAgentBundle in the contract)
	r.With(authSvc.Require(auth.PermMonitorView)).Get("/api/v1/platform/agent-bundle.tar.gz", server.AgentBundleHandler(agentBinaryDir))
	r.Get("/besu-agent-linux-{arch}", server.AgentBinaryHandler(agentBinaryDir))
	// HEAD too: the manual checks the download with curl -sI (http.ServeFile answers HEAD without a body)
	r.Head("/besu-agent-linux-{arch}", server.AgentBinaryHandler(agentBinaryDir))
	r.Get("/besu-agent-linux-{arch}.sha256", server.AgentBinaryChecksumHandler(agentBinaryDir))
	// Public version endpoint (the version is not secret; a first install can query it without identity, upgrades use the mTLS one): reads the sibling .version file.
	r.Get("/besu-agent-linux-{arch}.version", server.AgentBinaryVersionHandler(agentBinaryDir))

	// Pure liveness check: no auth, no redirect, used by the docker-compose healthcheck (see the comment on
	// server.healthcheck in that file). In acme/manual mode :80 only 301s to the external domain; a healthcheck
	// that followed it would hang on outbound connectivity/cert validation and never pass. This one is on the internal-only :8080, independent of TLS_MODE.
	both(http.MethodGet, "/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	// Prometheus HTTP service discovery (protected by the SD_TOKEN bearer; disabled when unset)
	both(http.MethodGet, "/sd/targets", server.SDTargetsHandler(st, sdToken))
	// Node metrics reverse proxy: Prometheus calls this and the platform forwards to the node over the agent-dialed WS (same SD_TOKEN)
	both(http.MethodGet, server.AgentMetricsRoute, server.AgentMetricsProxy(pkiSvc, sdToken))
	both(http.MethodGet, server.BesuMetricsRoute, server.BesuMetricsProxy(pkiSvc, st, sdToken))
	// Alertmanager webhook ingestion (the platform receiver in alertmanager.yml calls this; protected by the same SD_TOKEN bearer)
	both(http.MethodPost, "/alerts/webhook", server.AlertWebhookHandler(st, sdToken))

	// PROMETHEUS_URL set -> monitoring charts query real time series; unset -> the time-series API returns 503 (alerts and availability are unavailable too).
	promClient := prom.New(getenv("PROMETHEUS_URL", ""))
	if promClient != nil {
		// the availability bar's lag threshold comes from the "節點落後" (node lagging) rule and chain stability from the "出塊變慢 / 嚴重變慢" (block production slow / severely slow) multipliers, so pages use the same criteria as alerts
		promClient.RuleThreshold = func(ctx context.Context, preset string) (float64, bool) {
			r, err := st.GetAlertRuleByPreset(ctx, preset)
			if err != nil || r == nil {
				return 0, false
			}
			return r.Threshold, true
		}
		promClient.SetAlertmanager(getenv("ALERTMANAGER_RELOAD_URL", "")) // whether alerts update in real time also depends on Alertmanager
		go promClient.RunBesuUp(context.Background())                     // the capability list's metrics status reads this cache
		go promClient.RunHistory(context.Background())                    // node list 24h availability, node detail disk days left, chain 24h stability
	}

	// Alert notification settings: if either ALERTMANAGER_CONFIG_DIR or RELOAD_URL is empty
	// the feature is disabled (e.g. deployments that manage Alertmanager themselves). Apply once at boot (even with no settings
	// the base alertmanager.yml with only the platform receiver must be written, or Alertmanager will not start).
	alertSvc := alerting.New(st, masterBytes,
		getenv("ALERTMANAGER_CONFIG_DIR", ""), getenvInt("ALERTMANAGER_CONFIG_GID", -1),
		getenv("ALERTMANAGER_RELOAD_URL", ""))
	alertSvc.ShowOrg = edition == "enterprise"
	alertSvc.SMTPCA = smtpCA
	if err := alertSvc.WriteSMTPCA(); err != nil {
		log.Fatalf("copy SMTP_CA_FILE for Alertmanager: %v", err)
	}
	if err := alertSvc.LoadAndApply(ctx); err != nil {
		log.Printf("warning: apply alert notification settings (Alertmanager config may not have been written): %v", err)
	}

	// Alert rules (defaults + user-added): if either PROM_RULES_DIR or PROM_RELOAD_URL is empty the platform writes no rule files (Prometheus is self-managed).
	rulesSvc := alerting.NewRulesService(st, getenv("PROM_RULES_DIR", ""), getenv("PROM_RELOAD_URL", ""))
	alertSvc.Rules = rulesSvc // a notification language change rewrites the rules file too
	if err := rulesSvc.SeedDefaults(ctx); err != nil {
		log.Printf("warning: write default alert rules: %v", err)
	}
	if err := rulesSvc.LoadAndApply(ctx); err != nil {
		log.Printf("warning: apply alert rules (custom-rules.yml may not have been written): %v", err)
	}

	// Mail server for temporary-password emails: read only from env, separate from the alerting one. Invalid settings refuse to start; reachability is only logged.
	accountMail, err := alerting.AccountMailFromEnv(smtpCA)
	if err != nil {
		log.Fatalf("invalid password email settings (ACCOUNT_SMTP_*): %v", err)
	}
	srv := server.New(server.Deps{
		Store:          st,
		Collector:      col,
		Auth:           authSvc,
		Org:            orgID,
		PlatformAddr:   platformAddr,
		PublicAPIBase:  os.Getenv("PUBLIC_API_BASE"),
		RootDigest:     pkiSvc.CA.RootDigest(),
		AgentBinaryDir: agentBinaryDir,
		Prom:           promClient,
		Ops:            ops,
		AlertSvc:       alertSvc,
		RulesSvc:       rulesSvc,
		RpcSigner:      rpcSigner,
		AccountMail:    accountMail,
		AgentOutdated:  pkiSvc.AgentOutdatedAt,
	})
	if accountMail != nil {
		go func() {
			if err := alerting.CheckMail(context.Background(), *accountMail); err != nil {
				log.Printf("warning: password email server %s:%d (%s) is unreachable or rejected authentication; sending temporary passwords will fail: %v", accountMail.Host, accountMail.Port, accountMail.Security, err)
				return
			}
			log.Printf("password email server %s:%d (%s) connected and authenticated", accountMail.Host, accountMail.Port, accountMail.Security)
		}()
	}
	// Platform's own metrics (bsos_platform_*: cert expiry, node isolation; bsos_chain_*: chain-level figures for chain-wide alert rules)
	both(http.MethodGet, "/metrics", server.PlatformMetricsHandler(srv, sdToken))
	go srv.RunPeerPush(context.Background(), 30*time.Second)         // each round re-sends the full user-entered peer list via admin_addPeer (the shared list for nodes whose toggle is on, plus each node's own added peers)
	go srv.RunVoteCleanup(context.Background(), 30*time.Second)      // withdraw validator votes that already took effect but remain on nodes
	go srv.RunInitialAllowlist(context.Background(), 10*time.Second) // push the account allowlist once to newly joined nodes
	go srv.RunAlertReconcile(context.Background(), time.Minute)      // close alerts that will never receive a "resolved"
	strictOpts := api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  server.StrictRequestError,
		ResponseErrorHandlerFunc: server.StrictResponseError,
	}
	// Each operation's permissions are declared as security scopes in the contract; generated code puts them in ctx and RequireScopes enforces them
	// Validate requests against the contract (required, enums, formats); listed before RequireScopes = inner layer, so permissions are checked first
	spec, err := api.GetSpec()
	if err != nil {
		log.Fatalf("contract: %v", err)
	}
	h := api.HandlerWithOptions(api.NewStrictHandlerWithOptions(srv, nil, strictOpts), api.ChiServerOptions{
		BaseURL: "/api/v1", BaseRouter: r, ErrorHandlerFunc: server.ParamError,
		Middlewares: []api.MiddlewareFunc{server.ValidateRequests(spec, "/api/v1"), authSvc.RequireScopes(server.Scopes)},
	})

	// Enterprise wires its own tables, SSO, cross-org (8544), role table and routes here; in Core this is a no-op.
	if err := wireEnterprise(ctx, wiring{router: r, pool: pool, store: st, auth: authSvc, master: masterBytes,
		server: srv, pki: pkiSvc, platformAddr: platformAddr, rules: rulesSvc, stop: stop, listeners: &listeners}); err != nil {
		log.Fatalf("enterprise: %v", err)
	}

	// Unregistered routes land here: the frontend SPA served same-origin (embedded in the binary); paths under /api/ get a 404 JSON.
	r.NotFound(webapp.Handler().ServeHTTP)
	r.MethodNotAllowed(webapp.MethodNotAllowed(func(method, path string) bool {
		return r.Match(chi.NewRouteContext(), method, path)
	}))

	// Lets the auth middleware tell exactly whether a path is really registered (see the auth.Service.routeMatch comment).
	// Must be wired after all routes are registered, or routes not yet mounted would be misjudged as nonexistent.
	authSvc.SetRouteMatch(func(method, path string) bool {
		return r.Match(chi.NewRouteContext(), method, path)
	})

	// Internal-only plain HTTP listener (port not published, no firewall rule) for Prometheus, Alertmanager and the
	// healthcheck on the same network. It is independent of the public site's TLS_MODE and must stay reachable even
	// when acme/manual mode (serving only on 443) is chosen. Only the internal routes are mounted (see both above); everything else is 404.
	internalAddr := getenv("INTERNAL_ADDR", ":8080")
	listeners.Add(1)
	go func() {
		defer listeners.Done()
		log.Printf("internal listener on %s (internal calls on the same network only: Prometheus, Alertmanager, healthcheck; not exposed)", internalAddr)
		// nosemgrep: go.lang.security.audit.net.use-tls.use-tls -- internal-only listener: no published port, no firewall rule, only routes used by Prometheus / Alertmanager / healthcheck
		srv := webapp.NewServer(internalAddr, internal)
		if err := webapp.ServeUntil(stop, srv, srv.ListenAndServe); err != nil {
			log.Fatal(err)
		}
	}()

	log.Printf("platform server (org=%s, edition=%s) mode=%s listening on %s", orgID, edition, webappCfg.Mode, webappCfg.Addr+webappCfg.HTTPAddr)
	if err := webapp.Serve(stop, webappCfg, h); err != nil {
		log.Fatal(err)
	}
	listeners.Wait()
	// Only now: requests that were finishing above may still relay through an agent's tunnel. Shutdown does
	// not touch upgraded (WebSocket) connections, so close them ourselves.
	pkiSvc.CloseAgents()
	log.Printf("platform server stopped")
}
