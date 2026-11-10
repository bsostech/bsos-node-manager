#!/usr/bin/env bash
# Platform side: build the agent's Linux binary into docker-compose's agent-bin/.
# The node install.sh and the "Upgrade Agent" command both fetch it from the platform; an empty agent-bin means 404.
# Builds with Docker, so the platform does not need Go. Can be run from this directory or any path.
#   ./build-agent.sh                # amd64 + arm64 (default; both from one checkout, so their versions match)
#   ./build-agent.sh --arch amd64   # one architecture only
#   ./build-agent.sh --out <dir>    # custom output (e.g. a custom AGENT_BINARY_DIR)
#   ./build-agent.sh --zh           # print messages in Chinese (default English)
set -euo pipefail

# Message language: English by default, --zh switches to Chinese. Scanned before parsing so early errors follow it too.
ZH=0; for a in "$@"; do [ "$a" = --zh ] && ZH=1; done
t() { if [ "$ZH" = 1 ]; then printf '%s' "$2"; else printf '%s' "$1"; fi; }

# ── Logging: color only on a TTY, warn/err go to stderr, ERR trap reports where it failed ──
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  c_rst=$'\033[0m'; c_red=$'\033[0;31m'; c_yel=$'\033[0;33m'; c_grn=$'\033[0;32m'; c_blu=$'\033[0;36m'; c_dim=$'\033[2m'
else c_rst=; c_red=; c_yel=; c_grn=; c_blu=; c_dim=; fi
_now() { date +%H:%M:%S; }
log()  { printf '%s%s%s %s[INFO]%s %s\n' "$c_dim" "$(_now)" "$c_rst" "$c_blu" "$c_rst" "$*"; }
warn() { printf '%s%s%s %s[WARN]%s %s\n' "$c_dim" "$(_now)" "$c_rst" "$c_yel" "$c_rst" "$*" >&2; }
ok()   { printf '%s%s%s %s[ OK ]%s %s\n' "$c_dim" "$(_now)" "$c_rst" "$c_grn" "$c_rst" "$*"; }
die()  { printf '%s%s%s %s[ERR ]%s %s\n' "$c_dim" "$(_now)" "$c_rst" "$c_red" "$c_rst" "$1" >&2; exit "${2:-1}"; }
_step=0
step() { _step=$((_step+1)); printf '\n%s▸ %d. %s%s\n' "$c_blu" "$_step" "$*" "$c_rst"; }
ERR_AT="$(t 'failed at line %s (exit %s)' '第 %s 行失敗(exit %s)')"
trap 'rc=$?; [ $rc -ne 0 ] && printf "%s[ERR ]%s $ERR_AT: %s\n" "$c_red" "$c_rst" "$LINENO" "$rc" "$BASH_COMMAND" >&2' ERR

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
AGENT_DIR="$ROOT/agent"
OUT="$ROOT/deploy/platform/docker-compose/agent-bin"
ARCHES="amd64 arm64"
VERSION=""

while [ $# -gt 0 ]; do
  case "$1" in
    --arch)
      case "$2" in
        both) ARCHES="amd64 arm64";;
        amd64|arm64) ARCHES="$2";;
        *) die "$(t "unknown arch: $2 (amd64|arm64|both)" "未知 arch: $2(amd64|arm64|both)")" 2;;
      esac; shift 2;;
    --out) OUT="$2"; shift 2;;
    --version) VERSION="$2"; shift 2;;       # override the injected version string (default: git describe)
    --zh) shift;;
    *) die "$(t "unknown option: $1" "未知參數: $1")" 2;;
  esac
done

command -v docker >/dev/null || die "$(t "docker is required." "需要 docker。")"
# Right after up.sh --install-docker the current shell is not in the docker group yet: fall back to sudo, as up.sh does.
DOCKER="docker"
if ! docker info >/dev/null 2>&1; then
  sudo docker info >/dev/null 2>&1 || die "$(t "cannot talk to the Docker daemon; is it running? (systemctl status docker)" "連不到 Docker daemon,它有在跑嗎?(systemctl status docker)")"
  DOCKER="sudo docker"
fi
[ -d "$AGENT_DIR/cmd/agent" ] || die "$(t "agent source not found: $AGENT_DIR" "找不到 agent 原始碼: $AGENT_DIR")"
mkdir -p "$OUT"

# The version string is computed outside the script (on the host) and passed in: the Docker build context mounts only agent/, there is no .git in the container,
# so git describe cannot run there. Without --version it is computed from the repo with git describe, falling back to dev.
if [ -z "$VERSION" ]; then
  VERSION="$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || true)"
  [ -n "$VERSION" ] || VERSION="dev"
fi
log "$(t "Version: $VERSION" "注入版本: $VERSION")"

# ldflags override agent/internal/version.Version (module path per agent/go.mod: besu-manager/agent).
LDFLAGS="-s -w -X besu-manager/agent/internal/version.Version=${VERSION}"

for arch in $ARCHES; do
  step "$(t "Build besu-agent-linux-${arch} (in Docker, no Go needed; version ${VERSION})" "build besu-agent-linux-${arch}(Docker,免裝 Go;版本 ${VERSION})")"
  $DOCKER run --rm -v "$AGENT_DIR":/src:z -w /src golang:1.26.10-alpine@sha256:a2012b4a1d1f9f7e9f44e0d39b805a901e856b1dd80b4e6ee4338db547050308 \
    sh -c "CGO_ENABLED=0 GOOS=linux GOARCH=${arch} go build -trimpath -ldflags='${LDFLAGS}' -o bin/besu-agent-linux-${arch} ./cmd/agent"
  cp "$AGENT_DIR/bin/besu-agent-linux-${arch}" "$OUT/"
  # Sidecar .version file: the platform's version endpoint reads it (it cannot run a cross-arch binary to get the version). One per arch, same content.
  printf '%s\n' "$VERSION" > "$OUT/besu-agent-linux-${arch}.version"
  log "→ $OUT/besu-agent-linux-${arch}(+ .version=${VERSION})"
done

echo
ok "$(t "Done. agent-bin is bind-mounted into the server container, so it takes effect now; no server restart needed." "完成。agent-bin 是 bind-mount 進 server 容器,放好即生效,不用重啟 server。")"
log "$(t "Check: curl -sI https://<platform>/besu-agent-linux-amd64 | head -1   # expect 200" "驗證:curl -sI https://<平台>/besu-agent-linux-amd64 | head -1   # 期望 200")"
