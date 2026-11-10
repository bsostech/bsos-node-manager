English | [繁體中文](README.zh-TW.md)

# Platform deployment

Each organization runs **one** platform: the server (which also serves the UI) and Postgres. The open-source edition is one organization per platform. Cross-org read-only access in the commercial edition needs two or more organizations, each with its own platform, viewing each other through access keys.

## How it runs

The platform runs **only in containers**. Docker Compose starts the whole stack (postgres, server, prometheus, alertmanager); see [`docker-compose/README.md`](docker-compose/README.md).

The server image is based on UBI 9 (Red Hat's freely redistributable base, same packages as RHEL 9) and built with cgo, so an HSM can be connected later. The host distribution does not matter; glibc comes with the image. The server serves the UI and terminates TLS itself (`backend/internal/webapp`), so no nginx is needed.

## Before you deploy

**What gets built.** `backend/Dockerfile` builds the server image from `backend/` and `frontend/` in several stages, embedding the frontend `dist/` and `install.sh` with `go:embed`. The host does not need Node or Go. The agent binary is **not** in the image. `up.sh` builds it on the first run, or run `deploy/agent/build-agent.sh` (it builds amd64 and arm64 in Docker). The output goes into `docker-compose/agent-bin/`, which is mounted read-only into the container. Without it, nodes get a 404 when the installer downloads the agent.

**Edition (Core / Enterprise).** `up.sh` builds Enterprise (image `bsos-node-manager-enterprise`) when `backend/enterprise/` exists, and Core (`bsos-node-manager`) otherwise. `--core` forces Core. A plain `docker compose up --build` builds Core; for Enterprise pass `EDITION=enterprise SERVER_IMAGE=bsos-node-manager-enterprise`.

**Network.** The platform and each node are on separate machines.

| From → to | Port | Purpose |
|---|---|---|
| Browser → platform | 443 (or 80 for testing; with automatic or manual certificates, 80 only redirects) | UI |
| Node → platform | **8443/tcp** | Agent join and the connection after it. Nodes dial out; the platform never connects to nodes |
| Platform ↔ platform | **8544/tcp** | Cross-org read-only access with access keys. Enterprise only, and only with `CROSS_ORG_ENABLED` |
| Node ↔ node | **30303/tcp+udp** | Besu P2P, including across organizations |
| Node host, local only | 8545 / 9545 | Besu RPC and metrics. Bind them to localhost; the agent reaches them over loopback |

**One process, one origin.** The frontend `dist/` is embedded in the server binary. The SPA, `/api/*`, `/install.sh`, `/upgrade-agent.sh` and `/besu-agent-linux-*` all sit on the same chi router, so there is no CORS, cookies stay simple, and no reverse proxy is needed.

**TLS, pick one** (see `backend/internal/webapp`):

1. Automatic HTTPS on a public domain (`--acme`). Uses `golang.org/x/crypto/acme/autocert` with TLS-ALPN-01; only 443 needs to be reachable. Certificates are requested and renewed automatically.
2. A certificate you supply, from a public CA or your company's internal CA (`--cert` / `--key`). The server reads the files itself and reloads them when their mtime changes.
3. Plain HTTP for testing (`--http`).

Autocert only works on the public internet, because Let's Encrypt must reach the host. On an internal network or with an internal CA, use `--cert` / `--key`.

**Behind a load balancer or nginx.** By default the platform does not trust `X-Forwarded-For` or `X-Real-IP` (anyone can set them), so the audit log and platform log record the proxy's IP. To record the real client, set `TRUSTED_PROXY_CIDR` in `server.env` to the proxy's IPs or ranges, comma-separated. `X-Forwarded-For` is then trusted only on connections from those addresses, and the platform takes the first address from the right that is not on the list. **List only the proxy's own addresses, never the clients' range.** If clients are also in 10.0.0.0/8 and you enter 10.0.0.0/8, any client connecting directly can forge its source with its own `X-Forwarded-For`. An invalid value stops the platform from starting (the log names the bad entry); it is never silently ignored. The proxy must **overwrite** the client's `X-Forwarded-For` rather than append to it, or at least make sure the rightmost entry is its own. The access log still records the connection address.

**Source IP allowlists (join tokens, access keys) use the connection address.** The platform terminates TLS on 8443 and 8544 itself, so no L7 proxy can sit in front of them and `X-Forwarded-For` is not read there. Port mapping in rootful Docker and Podman keeps the real source IP. **Rootless Podman with slirp4netns (the default on RHEL 8 and Podman before 5) rewrites every inbound source to `10.0.2.100`**, so any allowlist blocks everything; leave allowlists empty on such hosts. Podman 5's pasta keeps the remote source. Behind NAT or an L4 load balancer, the platform sees that device's address, so allowlist that.

**The sign-in cookie follows the TLS mode.** It is marked `Secure` in the HTTPS modes and not in `--http`. Set `COOKIE_SECURE` only when `--http` runs behind a TLS terminator.

**`CA_MASTER_KEY`: one per organization, never changed.** `up.sh` generates it and never overwrites an existing one.

**First account.** On first start the platform creates `admin`. Its password is `BOOTSTRAP_ADMIN_PASSWORD` in `server.env`, which `up.sh` fills with a random value on the first run (`grep BOOTSTRAP_ADMIN_PASSWORD server.env`). To choose it yourself, change it before the first start; it must meet the password rules or the platform will not start. The first login forces a password change.

Other people are added by the admin under "Accounts & permissions". Their temporary password is also shown only once and must be changed at first login. If `ACCOUNT_SMTP_*` is set in `server.env` (see `server.env.example`), the temporary password can be emailed to them instead, both when adding an account and when resetting a password. This mail server is used only for temporary passwords and is separate from alert email. It requires encryption by default: direct TLS on port 465, STARTTLS on other ports. Check what your server supports with `openssl s_client -starttls smtp -connect host:587` (drop `-starttls smtp` for port 465). For an unencrypted internal relay, set `ACCOUNT_SMTP_SECURITY=none` explicitly.

The admin only manages accounts. It cannot see monitoring or register nodes, so the first thing to do is create an "Operator" account and use it for registering nodes and day-to-day monitoring.

## CA: keep the root offline in production

If the database has no CA on first start, the platform signs its own root and intermediate and **stores the root private key, encrypted, in the database**. That is for development and trials only, and the server prints a warning at startup. For production, choose one of these **before the first start**:

```bash
cd deploy/platform/docker-compose
# Create a new CA; the root private key is written to root-ca.key, and the database keeps only the root certificate and the intermediate
./up.sh --org-id org-a.example --domain platform-a.example --cert ./fullchain.pem --key ./privkey.pem --offline-root
```

If you already have an enterprise PKI, use `--ca-import <dir>` instead. The directory holds `root.pem` plus `inter.pem` and `inter.key` signed by it; the root private key never comes in.

Move `root-ca.key` to offline storage right away and delete it from this host. You only need it to renew the intermediate. Run `docker compose exec server cactl show` to see the current state and expiry dates.

**Renewing the intermediate.** The intermediate is valid for 5 years. From 90 days before expiry, the server logs a warning every day and `cactl show` shows it too. When it expires, every agent has to re-join, so renew within the warning period. The root and its fingerprint stay the same, so agents do not re-join and external organizations' access keys stay valid.

- Root in the database (self-signed bootstrap): restarting the server renews it automatically. You can also run `docker compose exec server cactl rotate-intermediate` and then restart.
- Root offline:
  ```bash
  docker compose run --rm --user "$(id -u):$(id -g)" -v "$PWD:/out" --entrypoint cactl \
    server rotate-intermediate --csr-out /out/inter.csr
  # Give inter.csr to the root holder to sign as a CA (basicConstraints CA:TRUE, pathlen:0, keyCertSign) and get inter.pem back
  docker compose run --rm -v "$PWD:/in:ro" --entrypoint cactl \
    server rotate-intermediate --inter-cert /in/inter.pem
  docker compose restart server
  ```

Both keep the same intermediate private key, so agents are not affected. Add `--new-key` only if you **suspect the intermediate private key has leaked**. Certificates signed with the old key stop working at once, and **every node must re-join with a new join token**. With an offline root, add `--new-key` to both steps: the first step also writes the new private key (`--key-out`), and the second brings it back with `--inter-key`. Delete the key file after importing.

## Order

1. Deploy one platform per organization.
2. Each organization sets up its own Besu and genesis; the platform does not install Besu or create a genesis. Install the agent on each node ([`../agent/README.md`](../agent/README.md)) and check that it shows as online.
3. Cross-org access (Enterprise): the organization being viewed issues an access key under "Cross-org access settings → Access keys" and gives it to the other organization's admin, who pastes it under "Cross-org access settings → External orgs". Your nodes then appear in their "My nodes". Port 8544 is closed by default; see "Cross-org" in [`docker-compose/README.md`](docker-compose/README.md).
4. Check that blocks are being produced with `eth_blockNumber` and `net_peerCount`.
