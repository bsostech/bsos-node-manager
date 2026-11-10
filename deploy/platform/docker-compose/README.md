English | [繁體中文](README.zh-TW.md)

# Platform deployment with Docker Compose

One `docker compose up` starts the whole stack: **Postgres, the platform server, Prometheus and Alertmanager**. Images are built from source, so the target host needs only Docker, not Go or Node. The server serves the UI and terminates TLS itself (`backend/internal/webapp`); there is no nginx.

## Files

| File | Purpose |
|---|---|
| `up.sh` | One-step deploy: creates `server.env` (generating `CA_MASTER_KEY`), writes the TLS settings and installs certificates for the chosen mode, generates the database password into `.env`, then runs `docker compose up -d --build` |
| `docker-compose.yml` | The four services: postgres, server, prometheus, alertmanager |
| `server.env.example` | Template for the server container's `server.env` (`TLS_MODE`, `SITE_DOMAIN`, `ACME_EMAIL` and others) |
| `certs/` | `server.crt` / `server.key` for the manual-certificate mode, from a public CA or your company's CA. `up.sh --cert/--key` puts them here; they are bind-mounted into the server container and gitignored |
| `acme-cache` volume | Certificate cache for automatic HTTPS, kept across restarts and renewals. Do not wipe it with `docker compose down -v`, or the certificate is requested again |
| `agent-bin/` | Holds `besu-agent-linux-*`, mounted into the server container so node installs can download it |
| `prometheus.yml` | Prometheus scrape config. HTTP service discovery with a bearer token; the targets are the platform's `/agent-proxy/{nodeID}/metrics`, never the nodes directly. Mounted into the prometheus container |
| `rules.yml` | Prometheus recording rules that turn Besu's own metrics into `bsos_node_*`. Mounted into the prometheus container. Alert rules are not here |
| `rules-defaults.gen.yml` | A rendered copy of the default alert rules, used only by `promtool test rules rules_test.yml`. Not mounted |
| `prom-rules-config` volume | All alert rules (defaults plus the ones users add, all stored in the platform database). The platform renders them to `custom-rules.yml` in this volume. `prometheus.yml` reads `rules.d/*.yml` with a glob, so an empty new volume does not stop Prometheus from starting |
| `alertmanager.yml` | A **reference** Alertmanager config (basic, with only the platform receiver). The Compose stack does **not** mount it. At startup the platform renders the equivalent config from the "Alert notifications" page (with Email and Webhook receivers added if set) into the `alertmanager-config` volume; changing the settings rewrites it and triggers a reload. The file is kept for checking syntax locally with `promtool` / `amtool`, and as a starting point for anyone running their own Alertmanager |
| `SD_TOKEN` | The service discovery bearer token, stored only in `server.env` (`up.sh` generates it). At startup the server writes it to `sd_token` in the `sd-token` volume (mode 0640, gid 65534), and Prometheus reads `/sd-token/sd_token` |
| `POSTGRES_PASSWORD` | The database password, stored in `.env` in this directory (mode 600). `up.sh` generates a random one on its first run and never overwrites it. Without it every `docker compose` command (including `logs` and `down`) fails; other accounts need `sudo` to run compose commands. postgres is only on the internal `db` network, which only the server joins |

The image is built by `backend/Dockerfile` in several stages: first the frontend `frontend/dist`, which is embedded in the server binary with `go:embed`, then the Go build. The build context is the repository root, because it needs both `backend/` and `frontend/`.

## Before you start

- Target host: Docker with the Compose plugin. If it is missing, `up.sh --install-docker` installs the **latest** version with Docker's official script. It does not install anything by default.
- Firewall: open **80/443** for the UI (80 only redirects to 443) and **8443** for agent join. Open **8544** only for cross-org read-only access, which needs the Enterprise edition and `CROSS_ORG_ENABLED`, and only to the organizations that will view yours. Postgres and 9090 stay closed.
  - **Nothing needs to be opened toward the nodes.** Prometheus scrapes the platform's `/agent-proxy/{nodeID}/metrics`, and the platform forwards it to the node over the 8443 connection the agent dialed out. The platform never connects to a node.
- The agent binary goes in `agent-bin/`, built in Docker, so Go is not needed. If `agent-bin/` is empty, `up.sh` builds it (amd64 and arm64) itself; to rebuild later, run `../../agent/build-agent.sh`.

## Deploy (pick a TLS mode)

The server terminates TLS itself (`backend/internal/webapp`).

| Mode | Options | Use for |
|---|---|---|
| **Automatic** | `--acme --email <address>` | A public domain, with certificates requested and renewed automatically (`autocert`, TLS-ALPN-01; only 443 needs to be reachable) |
| **Manual certificate** | `--cert <file> --key <file>` | A certificate you obtained publicly, or one **signed by your company's CA** |
| **Plain HTTP** | `--http` (with `--host`) | Testing on an internal network or private line |

```bash
cd deploy/platform/docker-compose

# A) Automatic HTTPS: autocert requests and renews from Let's Encrypt (TLS-ALPN-01, only 443 needed)
#    --domain must resolve to this host in public DNS. Add --staging to try the flow first without hitting rate limits.
./up.sh --org-id org-a.example --domain platform-a.example --acme --email ops@org-a.example

# B) HTTPS with your own certificate: certbot or purchased, or signed by your company's CA
./up.sh --org-id org-a.example --domain platform-a.example --cert ./fullchain.pem --key ./privkey.pem

# C) Plain HTTP for a quick test (when the internal network or private line is trusted)
./up.sh --org-id org-a.example --host <VM external IP> --http

# C') The UI and the agents use different addresses (e.g. the UI on the public IP, agent join limited to the internal network)
#     --agent-host sets the internal address used for PLATFORM_ADDR; without it, --host is used
./up.sh --org-id org-a.example --host <VM external IP> --agent-host <VM internal IP> --http
```

Which to choose:

- **Public domain**: `--acme`. Renewal is automatic, with no certbot cron and no extra proxy containers.
- **Internal network or company CA**: autocert does not work there, because Let's Encrypt must reach the host. Use `--cert/--key` with a `server.crt` from your company CA (its SAN must include the site's domain) and its private key. When it expires, replace the files following your company's process; the server notices the new mtime and reloads within seconds, no restart needed.
- **How autocert stores certificates**: in the `acme-cache` volume. They renew before expiry and are reloaded in the same process. **Do not wipe this volume with `down -v`**, or the certificate is requested again and may hit rate limits.

`up.sh` does four things:

1. Creates `server.env`, generating `CA_MASTER_KEY`. It **never overwrites** an existing one.
2. Installs certificates (manual mode) and writes `TLS_MODE`, `SITE_DOMAIN`, `ACME_EMAIL` and similar settings. These are not secrets and are overwritten whenever you pass a TLS option.
3. Builds the agent (amd64 and arm64) if `agent-bin/` is empty.
4. Generates a database password into `.env` if there is none yet (`POSTGRES_PASSWORD`; it **never overwrites** an existing one), then runs `docker compose up -d --build`.

Messages are in English; add `--zh` to print them in Chinese. To restart with the saved settings, run `./up.sh` with no options.

For a second organization's platform, run the same on its own host with, for example, `--org-id org-b.example --domain platform-b.example`.

**`--org-id` (organization id).** It is written into every certificate when the CA is created and **cannot be changed afterwards**; changing it means reinstalling and having every node re-join. Use lowercase letters, digits and `. - _`, starting and ending with a letter or digit. Your company domain (such as `example.com`) is a good choice. Enterprise requires it on the first deployment, and every organization in a consortium needs a different one. Core has no notion of organizations and generates `bsos-node-manager-<6 characters>` if you leave it out. If you plan to move from Core to Enterprise and join a consortium later, set `--org-id` on the first deployment; it becomes your organization id in the consortium.

## Keeping the master key out of the environment (optional)

When `CA_MASTER_KEY` is in `server.env`, `docker inspect` shows it in plain text. To mount it as a Docker secret instead:

```bash
grep '^CA_MASTER_KEY=' server.env | cut -d= -f2 | cut -d' ' -f1 > ca_master_key
sudo chown 10001 ca_master_key && chmod 400 ca_master_key   # the server container runs as uid 10001 and must be able to read it
sed -i '/^CA_MASTER_KEY=/d' server.env                       # setting both stops the server from starting
```

Then create `docker-compose.override.yml`. Compose applies it on top automatically, so `docker-compose.yml` stays unchanged. The 8544 `ports` entry for cross-org access goes in the same file (see "Cross-org").

```yaml
services:
  server:
    secrets: [ca_master_key]
    environment:
      CA_MASTER_KEY_FILE: /run/secrets/ca_master_key
secrets:
  ca_master_key:
    file: ./ca_master_key
```

Apply it with `docker compose up -d`. Like `server.env`, keep `ca_master_key` out of version control and out of the same backup as the database.

## First login

On first start the platform creates `admin`. `up.sh` generated its password on the first run:

```bash
grep BOOTSTRAP_ADMIN_PASSWORD server.env
```

The first login forces a password change. Do it before the platform is reachable by others. To choose the password yourself, see "First account" in [`../README.md`](../README.md).

The admin only manages accounts. After logging in, create an "Operator" account under "Accounts & permissions" and use it for registering nodes and monitoring.

## Monitoring (Prometheus)

The stack started by `up.sh` includes the `prometheus` service and **collects real time series out of the box**. If the server has no `PROMETHEUS_URL`, the chart API returns 503 instead of drawing fake data.

- **Discovery.** Prometheus gets this organization's node list from `server:8080/sd/targets` with `SD_TOKEN`. Nodes that join or are removed are picked up automatically; there is no config to edit.
- **Scrape path.** Prometheus never connects to nodes. It always scrapes the platform's `/agent-proxy/{nodeID}/metrics`, and the platform forwards the request to the node over the 8443 connection the agent dialed out. Scraping and discovery share the same `SD_TOKEN`; there is no scraper certificate.
- **Retention.** Prometheus keeps 15 days (`--storage.tsdb.retention.time` in `docker-compose.yml`). The charts show only 24 hours; the rest is there for investigating problems.

Prometheus talks only to the platform, on the same Docker network, protected by the bearer token. Nodes have nothing listening that could be impersonated, so ARP or DNS spoofing inside a closed network cannot inject fake metrics. Before forwarding a scrape to an agent, the platform strips the `Authorization` header: that token also works for `/sd/targets` and `/alerts/webhook` and must not be handed to every node.

To check: `docker compose logs -f prometheus`, or open `http://<VM>:9090` (not exposed by default; publish it temporarily or use an SSH tunnel) and see whether the targets are `UP`.

## Cross-org

Cross-org read-only access uses access keys. The organization being viewed issues a key under "Cross-org access settings → Access keys", entering an 8544 address the other side can reach, and gives the key to the other organization's admin. They paste it under "Cross-org access settings → External orgs" on their platform, and your nodes appear in their "My nodes". Port 8544 must be open to them.

8544 is off by default: the server does not listen, Compose does not publish it, and issuing a key is refused. To turn it on, set both:

```bash
echo 'CROSS_ORG_ENABLED=true' >> server.env
```

```yaml
# docker-compose.override.yml (if it already has the CA master key secret, add this under the same server entry)
services:
  server:
    ports: ["${CROSS_ORG_PORT:-8544}:8544"]
```

Apply with `docker compose up -d`. One without the other does not work: without the variable the server does not listen; without `ports` nothing outside can reach it. The startup log says `cross-org listener on …` or `cross-org listener disabled`.

## Connecting nodes

Each organization sets up its own Besu; the platform does not install it. Once a node is running, create a join token under "Node registration & settings" and run `install.sh` on the node host with the command shown on screen ([`../../agent/README.md`](../../agent/README.md)).

## Day-to-day commands

```bash
docker compose ps                 # status
docker compose logs -f server     # server log
docker compose up -d --build      # rebuild after changing code (only once up.sh has run; the edition comes from EDITION in .env, written by up.sh)
docker compose down               # stop, keeping the volumes (pgdata, acme-cache and the others); certs/ is a host folder and is never touched
docker compose down -v            # stop and delete all data, see below
```

The first time, and after wiping volumes, always use `./up.sh`: it creates `server.env` before starting anything.

## Common mistakes

1. Plain HTTP (`--http`) with `COOKIE_SECURE=true` set by hand: login keeps returning 401. Remove the line; the cookie follows the TLS mode by itself.
2. Changing `CA_MASTER_KEY`, or sharing one between two organizations: the CA can no longer be decrypted. `up.sh` never overwrites an existing `server.env`; do not change the key by hand.
   The same goes for `POSTGRES_PASSWORD` in `.env`: postgres applies the password only when its data volume is first created, so after a change the server can no longer connect to the database.
3. `docker compose down -v` deletes the CA, the database and the ACME certificate cache together. It resets the organization's identity: the server bootstraps a new root and intermediate with the same `CA_MASTER_KEY`, every node that had joined must re-join, and in ACME mode the certificate is requested again. After that, run `./up.sh --org-id … --domain/--host …` again.

## Troubleshooting

| Error in the Prometheus log | Cause | Fix |
|---|---|---|
| `Get ".../sd/targets": ... 404 Not Found` | `SD_TOKEN` is empty on the server (a mismatched token gives 401, not 404) | Set `SD_TOKEN` in `server.env` (for example `openssl rand -hex 24`), then run `docker compose up -d server prometheus` |
| `/sd-token/sd_token: permission denied` | The server wrote the file as 0600, and Prometheus (`nobody`, gid 65534) cannot read it | `docker-compose.yml` gives the server `SD_TOKEN_GID=65534` so it writes 0640 with that group. Check the setting is there and rebuild: `docker compose up -d --build server`. The file is rewritten at startup |

**`up.sh` says `missing --org-id`.** If `server.env` already exists, run it with no options to restart; the org id and site address are reused. `--org-id` and `--domain` or `--host` are needed only when `server.env` is first created.
