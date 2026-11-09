English | [繁體中文](README.zh-TW.md)

# Agent deployment

The node agent runs next to Besu. It opens a reverse tunnel to the platform that only reaches loopback, and reports host metrics. It knows nothing about Besu itself. Through it, the platform calls the local Besu JSON-RPC and metrics; the ports are fields in the node's settings on the platform.

**The install script installs only the agent.** Each organization installs Besu its own way (packages, Docker, Ansible). The platform does not install, configure or restart Besu. What Besu needs to enable is listed in the "Adjust Besu settings" step of "Node registration & settings → Register node" (`/node-add`). Without RPC the node is online but shows no chain state; without metrics there are no time-series charts.

## Files

| File | Purpose |
|---|---|
| `build-agent.sh` | **Runs on the platform host.** Builds the agent binary in Docker and puts it in the Compose `agent-bin/` folder. Installs and upgrades download it from there |
| `docker-compose.example.yml` / `agent.env.example` / `besu-agent.service` | Templates for installing by hand without the install script |

The install and upgrade scripts live in `backend/internal/server/assets/`. The platform serves them at `GET /install.sh` and `GET /upgrade-agent.sh`; there are no copies here.

## Requirements

- Linux with systemd, and root. Docker mode also needs Docker and the Compose v2 plugin.
- Besu running on this host, or about to be. The agent can be connected first; the platform shows "RPC unreachable" until Besu is up.
- A join token created on the platform under "Node registration & settings → Register node". The node id, RPC port and metrics port are entered when creating it.
- Firewall: the agent only dials out to the platform on 8443, **no inbound ports**. It also opens a log receiver that accepts local connections only (`127.0.0.1:5170` by default).

### SELinux (RHEL and other enforcing hosts)

Tested on RHEL 10.2 with SELinux enforcing, with both Docker settings:

- **Docker default (SELinux support off).** Containers are not confined by SELinux. Nothing to do.
- **Docker with SELinux support** (`"selinux-enabled": true` in `/etc/docker/daemon.json`). Containers run as `container_t` and cannot read host files with ordinary labels:
  - Docker-mode agent: the compose file written by `install.sh` has `security_opt: ["label=disable"]`, so it runs unconfined, like the systemd mode. The agent reads the host's `/`, `/proc` and `/sys`, and relabeling the host root with `:z` is not an option. For a manual install, `docker-compose.example.yml` has the same line.
  - Platform: the host mounts in `deploy/platform/docker-compose/docker-compose.yml` (`agent-bin`, `certs`, `prometheus.yml`, `rules.yml`) carry `,z`, so Docker labels them readable by the container. Docker ignores this when SELinux support is off. After replacing certificates in `certs/` by hand, run `up.sh` again (or `docker compose up -d --force-recreate`) so the new files are relabeled too.
- **systemd mode.** The agent binary is labeled `bin_t` and runs unconfined. Nothing to do.

After changing Docker's SELinux setting, recreate existing containers (`docker compose up -d --force-recreate`) to apply the new labels.

## Install

"Register node" on the platform builds the command. It looks like this:

```bash
curl -fsSL https://<platform>/install.sh -o install.sh
sudo bash install.sh --platform <platform>:8443 --token <join token> \
  [--runtime systemd|docker] [--platform-http https://<platform>] \
  [--allow-ports 8545,9545] [--disk-path /] [--state-dir /var/lib/besu-agent] [--re-enroll] [--log-sink <IP:port>]
```

| Option | Meaning |
|---|---|
| `--platform` | Platform mTLS address, for join and the connection after it; a host without a port means `:8443` |
| `--token` | Join token; the node id and ports are bound to it |
| `--runtime` | `systemd` (default) or `docker` |
| `--platform-http` | HTTPS base for downloading the agent binary. Default `https://<host from --platform>` |
| `--allow-ports` | Optional. Limits what the platform can reach through the agent to these ports. Unset means any loopback port. An invalid value (a port outside 1–65535, or only commas) stops the agent from starting |
| `--disk-path` | Optional. Path used to measure disk usage (systemd mode only) |
| `--state-dir` | Optional. Certificate directory, default `/var/lib/besu-agent` |
| `--local` | Use the offline bundle: take the agent from the script's directory and verify it against `SHA256SUMS` instead of downloading it. Also installs `bsos-upgrade` |
| `--re-enroll` | The node is already on the platform but its certificate is no longer valid ("Re-join" on the node page generates this command) |
| `--log-sink` | Optional. IP:port where the agent receives Besu logs, default `127.0.0.1:5170`. If Besu runs in a container on a bridge network, use the docker0 IP |
| `--zh` | Print messages in Chinese |

The command from the platform checks `install.sh` against a SHA-256 from the platform before running it.

What the script does: download the agent and check its SHA-256, run `besu-agent join` to get a certificate, then start the service.

- **systemd**: creates a `besu-agent` system user. The unit can write only to its own certificate directory (`ProtectSystem=strict`, `NoNewPrivileges`).
- **docker**: builds the same binary into a local image with a `FROM scratch` Dockerfile. It pulls from no registry, so it builds offline. The compose file is `/etc/besu-agent/docker-compose.yml`. The container uses host networking to reach Besu at `127.0.0.1`, and mounts the host's `/proc`, `/sys` and root filesystem read-only so the metrics are the host's, not the container's.

When it finishes, the node shows as online. To uninstall: `sudo /usr/local/sbin/bsos-uninstall`. It removes only the agent and keeps the certificate directory; add `--purge` to delete that too.

### Offline bundle

If the node host cannot reach the platform over HTTPS, or the files must be reviewed before installing, choose "Offline bundle" on the register page. Download `besu-agent-<version>-linux.tar.gz` (install.sh, upgrade-agent.sh, the agent for x86_64 and ARM64, and `SHA256SUMS`), copy it to the node host, and follow the commands on the page: verify the bundle, unpack it, then `sudo bash install.sh --local --platform <host:8443> --token <join token>`. The agent still needs to reach the platform on 8443 afterwards.

## Logs (optional)

Besu sends its logs to the agent using a log4j2 config generated by the platform, and the "Logs" tab on the node page shows the recent ones. The steps are on the register page and on the Logs tab: download the config, add `LOG4J_CONFIGURATION_FILE=<config file>` to Besu, restart Besu. If Besu runs in a container on a bridge network, start the agent with `--log-sink <docker0 IP>:5170` and point the config at the same address. Without this there are no logs on the platform; nothing else is affected.

## Upgrade

The "Upgrade Agent" card on the node's "Agent & enrollment" tab gives the command:

```bash
curl -fsSL https://<platform>/upgrade-agent.sh -o upgrade-agent.sh
sudo bash upgrade-agent.sh
```

It reads the platform address and certificate directory from the agent config in `/etc/besu-agent`, so no options are needed.

It uses the agent's own certificate over mTLS to download the new version, checks the SHA-256, runs the new binary once to confirm its version, then swaps the file. It detects the mode: systemd restarts the service; Docker rebuilds the image and recreates the container. It then watches for 20 seconds. If the new version stops or is restarted automatically, it switches back. Exit code 4 means it switched back and the node runs as before; 5 means switching back failed and someone has to fix it by hand.

- The old binary stays at `/usr/local/bin/besu-agent.prev`. Docker mode deletes the old image after a successful upgrade, so going back by hand means rebuilding from `.prev`.
- systemd older than 235 (RHEL 7, for example) does not report restart counts, so only the "is it running" check applies.
- It only connects to 8443, and it uses the agent's certificate, so it works even when the agent is not connected.
- If a platform upgrade requires a newer agent protocol, the platform refuses old agents (the node shows "Agent too old"). Run this script on every host.
- It installs whatever the platform has, even an older version. The platform's `agent-bin/` holds the version that platform wants, so rolling back the platform rolls back the agents too.
- Nodes installed from the offline bundle already have `bsos-upgrade` (the same script, reading the platform address and certificate directory from the agent config): `sudo /usr/local/sbin/bsos-upgrade`.

## Install by hand

```bash
install -m755 besu-agent-linux-amd64 /usr/local/bin/besu-agent
useradd --system --home /var/lib/besu-agent --shell /usr/sbin/nologin besu-agent
install -d -m 0700 /var/lib/besu-agent /etc/besu-agent
besu-agent join --platform <host:port> --token <t> --state-dir /var/lib/besu-agent
chown -R besu-agent:besu-agent /var/lib/besu-agent
cp agent.env.example /etc/besu-agent/agent.env    # edit to real values
cp besu-agent.service /etc/systemd/system/ && systemctl enable --now besu-agent
```

For Docker, follow [`docker-compose.example.yml`](docker-compose.example.yml).

## Environment variables

| Variable | Default | Meaning |
|---|---|---|
| `PLATFORM_ADDR` | `localhost:8443` | Platform mTLS address |
| `AGENT_STATE_DIR` | `state` | Certificate directory |
| `ALLOW_PORTS` | empty | Optional. Ports the platform may reach through the agent. Empty means any loopback port. An invalid value (a port outside 1–65535, or only commas) stops the agent from starting |
| `DISK_PATH` | `/` | Optional. Path used to measure disk usage; `/rootfs` inside a container |
| `LOG_SINK` | `127.0.0.1:5170` | Optional. Address that receives Besu logs. If Besu runs in a container on a bridge network, use the docker0 IP |
| `LOG_SPOOL_MAX` | `512MB` | Optional. Size limit of the log spool (`AGENT_STATE_DIR/logspool/`), written only after an Enterprise platform has fetched logs. When full, the oldest blocks are dropped |

## Protocol compatibility

The agent and platform talk yamux. Old and new protocol versions are not compatible in either direction, so when you upgrade the platform, upgrade the agents with `upgrade-agent.sh` too. The link needs at least about 90 kbit/s; slower than that, the session's own keepalive closes it.
