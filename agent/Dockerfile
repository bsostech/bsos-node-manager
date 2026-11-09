# Node Agent container image.
# Build stage: static Linux binary (CGO disabled, no runtime dependencies).
FROM golang:1.26.10-alpine@sha256:a2012b4a1d1f9f7e9f44e0d39b805a901e856b1dd80b4e6ee4338db547050308 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/besu-agent ./cmd/agent

# Runtime stage: alpine (keeps shell/wget for on-site debugging; ca-certificates as a fallback).
# Alpine 3.20 is end-of-life. A comment cannot share the FROM line: Docker reads it as extra arguments and refuses to build.
FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache ca-certificates \
 && adduser -D -H -u 10001 besu-agent \
 # Cert material directory: must be writable by the non-root user (join writes agent.key/agent.crt, 0600).
 && mkdir -p /var/lib/besu-agent \
 && chown besu-agent:besu-agent /var/lib/besu-agent \
 && chmod 0700 /var/lib/besu-agent
COPY --from=build /out/besu-agent /usr/local/bin/besu-agent

# Notes:
# - Hardware metrics come from the host (not the container), so mount host /proc, /sys and rootfs and set
#   HOST_PROC / HOST_SYS / DISK_PATH (see deploy/agent/docker-compose.example.yml).
# - The platform forwards through this container to the node's local Besu (X-Target only accepts loopback),
#   so network_mode: host is required.
# - No container HEALTHCHECK: the agent does not listen externally (only the local log intake), and /healthz
#   is answered only over the WS to the platform, so it cannot be probed from inside the container. The real
#   liveness signal is "has the agent joined back to the platform" (visible on the platform side), which is
#   also the provisioning ack.
# Runs as non-root. The container binds no ports (all connections are dialed out by the agent), and host
# /proc and /sys are world-readable, so no extra capability is needed.
# Note: the mounted AGENT_STATE_DIR volume must be writable by uid 10001.
USER besu-agent

ENTRYPOINT ["besu-agent"]
