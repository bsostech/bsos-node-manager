# Platform Server

The Platform Server is the control plane that each organization runs for its own nodes. It is a single Go binary
that serves the web UI and the Platform API, runs an embedded CA for the node agents, and reaches every Besu node
through a reverse tunnel that the node's agent dials out to the platform.

The server serves the web UI and the Platform API (`/api/v1`, defined in `contract/openapi.yaml`) on one origin. It
accepts agent connections on an mTLS listener (`:8443`) and issues, rotates and revokes agent certificates from its
embedded CA. Every 10 seconds it collects node state: host metrics from the agent, and chain state obtained by
calling the node's JSON-RPC through the tunnel. It reads time series from the organization's Prometheus, manages
Prometheus alert rules and Alertmanager notification settings, and signs the JWTs that Besu accepts on its JSON-RPC
endpoint.

## Build and run

Building requires Go at the version in `go.mod`. The development database runs in Docker.

```bash
cp .env.example .env    # set ORG_ID and CA_MASTER_KEY (openssl rand -hex 32)
make db-up              # PostgreSQL 16 in Docker (docker-compose.yml)
make run                # go run ./cmd/server
```

The schema is applied at startup. On first start the server creates the CA, the RPC signing key and a local `admin`
account. The admin password is `BOOTSTRAP_ADMIN_PASSWORD` (up.sh fills in a random one), or printed to the log once when that is unset, and must be changed at
first login.

The web UI is embedded with `go:embed` from `internal/webapp/dist`, which is empty in the repository. The container
build fills it (see `Dockerfile`). To serve the UI from a local build:

```bash
(cd ../frontend && npm ci && npm run build)
cp -R ../frontend/dist/. internal/webapp/dist/
```

For work on the UI itself, the Vite dev server with mock data is usually enough; see `frontend/`.

Other targets:

| Command | Description |
|---|---|
| `make build` | Builds `bin/server` |
| `make generate` | Regenerates `internal/api/api.gen.go` (and `enterprise/api/` when `contract/enterprise.yaml` exists) with `go tool oapi-codegen` |
| `make db-down` | Stops the development database |
| `go build -tags enterprise ./cmd/server` | Builds the Enterprise edition (requires `enterprise/`) |

Production deployments use the container image and the compose files under `deploy/platform/`.

## Tests

```bash
go test ./...
```

Tests that need PostgreSQL are skipped unless `TEST_DATABASE_URL` is set. It must point to a database on
`localhost` whose name contains `test`, for example `postgres://besu:besu@localhost:5432/besu_manager_test`.

## Configuration

Settings come from environment variables. A `.env` file in the working directory is loaded if present.

### Required

| Variable | Description |
|---|---|
| `ORG_ID` | Organization id, written into every certificate's O. Lowercase letters, digits, `.`, `-`, `_`. Cannot be changed after the CA is created. |
| `CA_MASTER_KEY` | 64 hex characters. Encrypts the CA private keys, the RPC signing key and stored secrets. Alternatively `CA_MASTER_KEY_FILE` with the same content; set only one. |

### Listeners and addresses

| Variable | Default | Description |
|---|---|---|
| `DATABASE_URL` | `postgres://besu:besu@localhost:5432/besu_manager` | PostgreSQL connection string |
| `TLS_MODE` | `http` | Website TLS: `http`, `acme` (Let's Encrypt) or `manual` |
| `LISTEN_ADDR` | `:80` | Website and Platform API in `http` mode; `acme` and `manual` always listen on `:443` |
| `REDIRECT_ADDR` | `:80` | HTTP to HTTPS redirect in `acme` and `manual` mode |
| `SITE_DOMAIN`, `ACME_EMAIL`, `ACME_CACHE_DIR`, `ACME_STAGING` | | `acme` mode settings |
| `SITE_CERT_FILE`, `SITE_KEY_FILE` | | `manual` mode certificate and key |
| `COOKIE_SECURE` | derived from `TLS_MODE` | Override for the session cookie's `Secure` flag, for `http` mode behind a TLS terminator |
| `TRUSTED_PROXY_CIDR` | | Proxies whose `X-Forwarded-For` is trusted for the audit source IP |
| `INTERNAL_ADDR` | `:8080` | Plain-HTTP listener for Prometheus, Alertmanager and the healthcheck only (`/healthz`, `/metrics`, `/sd/targets`, metrics proxies, `/alerts/webhook`). Not published. |
| `AGENT_API_ADDR` | `:8443` | Agent mTLS listener |
| `PLATFORM_ADDR` | `localhost:8443` | Agent listener address as seen by nodes; written into the install command |
| `PUBLIC_API_BASE` | | HTTP base URL nodes use to reach the platform, when it differs from the browser's origin |
| `AGENT_BINARY_DIR` | | Directory holding the `besu-agent-linux-{arch}` binaries served to nodes |
| `LEAF_TTL` | `72h` | Agent certificate lifetime. Values under 24h log a warning and are meant only for rotation demos. |
| `RPC_KEY_BACKEND` | `db` | Where the RPC signing key lives; this build provides `db` only |
| `BOOTSTRAP_ADMIN_PASSWORD` | generated | Initial `admin` password |

For local development, `.env.example` moves `LISTEN_ADDR` to `:8080` and `INTERNAL_ADDR` to `:8081`.

### Monitoring and alerting

| Variable | Description |
|---|---|
| `PROMETHEUS_URL` | Prometheus query endpoint. When unset, the series API returns 503 and the alert and availability views are empty. |
| `SD_TOKEN` | Bearer token that Prometheus and Alertmanager use on the internal routes. When unset, the token-protected routes are disabled. |
| `SD_TOKEN_DIR`, `SD_TOKEN_GID` | Directory and group for writing `sd_token` for the Prometheus container |
| `PROM_RULES_DIR`, `PROM_RELOAD_URL` | Where to write alert rule files and how to reload Prometheus. Both are required for rule management. |
| `ALERTMANAGER_CONFIG_DIR`, `ALERTMANAGER_CONFIG_GID`, `ALERTMANAGER_RELOAD_URL` | Where to write `alertmanager.yml` and how to reload it. Both the directory and the URL are required for notification settings. |
| `ACCOUNT_SMTP_HOST`, `_PORT`, `_USERNAME`, `_PASSWORD`, `_PASSWORD_FILE`, `_FROM`, `_SECURITY`, `_CA_FILE` | Mail server for temporary-password emails. Unset disables the emails; invalid values stop startup. |

## Layout

| Path | Contents |
|---|---|
| `cmd/server/` | Server entry point; `edition_core.go` / `edition_enterprise.go` select the edition by build tag |
| `cmd/cactl/` | CA material CLI |
| `internal/api/` | Generated from `contract/openapi.yaml` (`api.gen.go`, do not edit) |
| `internal/server/` | Platform API handlers, join tokens, install scripts (`assets/`), Prometheus service discovery and metrics proxies |
| `internal/collector/` | Periodic node snapshots (host metrics from the agent, chain state over JSON-RPC) |
| `internal/rpcops/` | JSON-RPC calls to nodes through the agent tunnel |
| `internal/pki/` | CA runtime: join API, agent mTLS listener, WebSocket control channel, rotation and deny list |
| `internal/ca/` | CA primitives: issuance, key encryption, `CA_MASTER_KEY` loading |
| `internal/auth/` | Sessions, local accounts, roles and permissions, RPC JWT signing |
| `internal/alerting/` | Alert rules, notification channels, account mail |
| `internal/prom/` | Prometheus query client |
| `internal/store/` | PostgreSQL access; `schema.sql` is applied at startup |
| `internal/webapp/` | Embedded web UI and website TLS (`http` / `acme` / `manual`) |
| `internal/enode/` | enode URL parsing |
| `internal/agentbin/` | Agent binary paths and checksums |
| `internal/apierr/` | Error responses with a machine-readable reason |
| `internal/reqinfo/` | Request source IP and user agent for auditing |
| `enterprise/` | Enterprise edition only (not in the open-source edition) |

## Embedded CA

The server signs agent certificates with a two-tier CA (root and intermediate) stored in PostgreSQL, with the private
keys encrypted by `CA_MASTER_KEY`. By default the first start generates a self-signed root and keeps its encrypted
private key in the database. To keep the root key out of the system, run `cactl` before the first start of the
server:

| Command | Description |
|---|---|
| `cactl init-offline [--root-key-out root-ca.key]` | Generates a new CA and writes the root private key to a file; the database keeps only the intermediate key |
| `cactl import --root-cert root.pem --inter-cert inter.pem --inter-key inter.key` | Uses an existing PKI; the platform holds only an intermediate under it |
| `cactl show` | Shows root and intermediate status, expiry and fingerprint |
| `cactl rotate-intermediate` | Renews the intermediate. With an offline root, `--csr-out` produces a CSR and `--inter-cert` imports the signed certificate. `--new-key` replaces the key and requires every node to re-join. |

`cactl` reads `DATABASE_URL` and `CA_MASTER_KEY` in the same way as the server. `init-offline` and `import` refuse to
run when CA material already exists. Restart the server after `rotate-intermediate`.

An agent certificate goes through three stages. To join, `POST /api/v1/join-tokens` returns a token of the form
`BSOS-1-sha256:<root CA digest>-<secret>`, valid for 15 minutes, 1 hour (default) or 24 hours; the database stores
only a SHA-256 hash of the secret. The agent pins the platform's root by the digest, sends a CSR and receives a leaf
certificate. Rotation starts when a leaf has less than a third of its lifetime left: the platform asks the agent over
the tunnel for a new CSR, and the agent swaps certificates without dropping the connection. Offboarding a node in the UI,
or re-enrolling it with a join token, adds its certificates to the deny list. The server checks the list every 2
seconds, so the agent is disconnected within a few seconds.

## RPC signing key

On first start the server also generates an RS256 key pair, stored in the `rpc_signing_key` table with the private
key encrypted by `CA_MASTER_KEY`. The platform signs the JWTs it sends to Besu's JSON-RPC with this key. The public
key is available at `GET /api/v1/platform/rpc-public-key` (JSON) and `/api/v1/platform/rpc-public-key.pem`; node
operators configure it with Besu's `--rpc-http-authentication-jwt-public-key-file`.

Back up `CA_MASTER_KEY` together with the database. Without it the key cannot be decrypted. There is no rotation
workflow: deleting the row and restarting generates a new pair, after which every token signed with the old key is
rejected and every node must install the new public key and restart Besu.

The signer is `RpcSigner` in `internal/auth/rpcjwt.go`. An HSM or Vault backend replaces the `crypto.Signer` it
wraps.
