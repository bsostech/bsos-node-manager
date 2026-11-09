# Node Agent

The Node Agent is a small Go sidecar that runs next to each Besu node. It opens one outbound connection to the
platform, gives the platform a tunnel to the node's loopback interface, and reports host metrics. It does not write
Besu files, restart Besu or hold Besu's RPC credentials.

## How it works

The agent dials an mTLS WebSocket to the platform (`:8443`) and multiplexes it with yamux. It opens no externally
reachable port; its only listener is the local log intake (default `127.0.0.1:5170`).

Requests from the platform that carry `X-Target: 127.0.0.1:<port>` are forwarded unchanged to that local port. This
is how the platform calls Besu's JSON-RPC and metrics endpoints. Targets other than `127.0.0.1` or `::1` are
rejected with 403. Requests without `X-Target` are limited to the agent's own endpoints:

| Path | Description |
|---|---|
| `GET /host-metrics` | CPU, memory, disk and network of the host, sampled every 5 seconds. JSON when `Accept` includes `application/json`, otherwise Prometheus text format. |
| `GET /logs` | Recent Besu log lines from the in-memory buffer (about 2 MB) |
| `GET /logblocks` | Compressed log blocks from the on-disk spool, pulled by an Enterprise platform |
| `GET /agent-status?p2pPort=<port>` | Agent version, and whether a TCP connection to that port succeeds on `127.0.0.1` or any interface address |

Besu sends its logs to the intake through a log4j2 configuration that the platform generates. The spool under
`AGENT_STATE_DIR/logspool/` is written only after an Enterprise platform has pulled `/logblocks` once.

The control channel handles certificate rotation only. The platform sends `renew`, the agent replies with a CSR and
swaps in the new certificate without reconnecting.

## Build

```bash
make build          # bin/agent for the current platform
make build-linux    # static binaries bin/besu-agent-linux-amd64 and -arm64 (CGO_ENABLED=0)
go test ./...
```

`make build-linux` produces the file names that the install script downloads; point the platform's
`AGENT_BINARY_DIR` at `bin/`. Release builds set the version with `deploy/agent/build-agent.sh`, which overrides
`internal/version.Version` through `-ldflags`. `make generate` regenerates `internal/api/` from
`contract/openapi.yaml`.

Installation on node hosts is described in `deploy/agent/README.md`. The platform serves the install script at
`/install.sh`.

## Joining a platform

```bash
# 1. In the platform, open "Node registration & settings", "Register node", and create a join code for the node id.
# 2. Exchange the join code for a certificate. The root CA digest in the code pins the platform.
go run ./cmd/agent join --platform localhost:8443 --token <BSOS-1-...>
# 3. Start the agent. It exits if no certificate is present.
go run ./cmd/agent
```

If the certificate is no longer valid (host rebuilt, certificate revoked), create a new join code for the same node
id and run `join` with `--re-enroll`.

Subcommands:

| Command | Description |
|---|---|
| `join --platform <host:port> --token <token> [--state-dir <dir>] [--re-enroll]` | Enrolls the node and writes certificate material to the state directory |
| `version` | Prints the build version |
| `licenses` | Prints third-party license notices |

## Configuration

Settings come from environment variables. A `.env` file in the working directory is loaded if present.

| Variable | Default | Description |
|---|---|---|
| `PLATFORM_ADDR` | `localhost:8443` | Platform agent listener (`host:port`) that the agent dials. `join` takes `--platform` instead. |
| `AGENT_STATE_DIR` | `state` | Certificate material and log spool directory |
| `ALLOW_PORTS` | unset | Comma-separated ports allowed for `X-Target` forwarding and `/agent-status` probes. Unset allows all ports. An invalid value (not a 1–65535 port, or only commas) stops the agent from starting. Include the P2P port if the P2P probe is needed. |
| `DISK_PATH` | `/` | Path used for disk usage. In a container, point it at the mounted host root filesystem. |
| `LOG_SINK` | `127.0.0.1:5170` | Log intake address. When Besu runs in a container on a bridge network, use the bridge interface address. |
| `LOG_SPOOL_MAX` | `512MB` | Log spool size limit; the oldest blocks are dropped when it is full |

## Layout

| Path | Contents |
|---|---|
| `cmd/agent/` | Entry point and subcommands |
| `internal/api/` | Models generated from `contract/openapi.yaml` (do not edit) |
| `internal/join/` | Enrollment: digest pinning, CSR, writing certificate material |
| `internal/certstore/` | Loading and hot-swapping certificate material |
| `internal/wsclient/` | WebSocket and yamux: control channel, `X-Target` forwarding, local endpoints |
| `internal/hostmetrics/` | Host metric sampling and `/host-metrics` |
| `internal/logsink/` | Log intake and in-memory buffer |
| `internal/logspool/` | On-disk log spool and `/logblocks` |
| `internal/agentstatus/` | `/agent-status` |
| `internal/version/` | Build version |
