#!/usr/bin/env bash
# BSOS Node Agent install script: installs only the agent, so a host **already running Besu** can join the platform.
#   It does not install Besu, touch any Besu file, or restart Besu; the platform does not care how Besu was
#     installed (package, docker, self-built).
#   The platform reaches 127.0.0.1:<RPC port> and <metrics port> through the agent; the ports are set in the
#     platform's node settings.
#   What Besu should enable (RPC / metrics / JWT / account allowlist): see the "Adjust Besu settings" step of the
#     platform's "Register node" page (/node-add).
# Usage: see usage() just below; --help prints it.
usage() {
  cat <<'USAGE'
Requires root.
  curl -fsSL https://<platform>/install.sh -o install.sh
  sudo bash install.sh --platform <host[:8443]> --token <join token> \
       [--runtime systemd|docker] [--platform-http https://<platform>] [--local] [--re-enroll] \
       [--allow-ports 8545,9545] [--disk-path /] [--state-dir /var/lib/besu-agent] [--log-sink <IP:port>]
  systemd (default) = the agent runs as a systemd service; docker = build a FROM scratch image locally and
    run it as a container.
  --local: use the offline bundle (downloaded from the platform's "Register node" page /node-add): the agent is taken
    from the script's directory and verified against SHA256SUMS, nothing is downloaded from the platform;
    the bundle's upgrade-agent.sh is also installed as bsos-upgrade (upgrades only connect to 8443).
  --re-enroll: renew the certificate when the node is already in the platform inventory but its certificate
    is invalid (keeps the node id and audit history; used by the platform's "Re-join"). --allow-ports,
    --log-sink, --state-dir and (systemd) --disk-path that are left out keep this host's current values;
    to drop one, delete its line from /etc/besu-agent/agent.env (or docker-compose.yml) first.
  --log-sink: the address where the agent receives Besu logs, default 127.0.0.1:5170; when Besu runs in a
    container on a bridge network, use docker0's IP.
  --dry-run: check the options and the host, test that the platform port answers, list what a real run
    would change, and exit without changing anything or using the join code.
  --zh: print messages in Chinese (default English).
  -h, --help: print this text.
USAGE
}
set -euo pipefail

# ── Logging: colors only on a TTY, warn/err go to stderr, the ERR trap reports where it failed (especially important under curl|bash) ──
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
# Message language: English by default, --zh switches to Chinese. Scanned before parsing so early errors follow it too.
ZH=0; for a in "$@"; do [ "$a" = --zh ] && ZH=1; done
t() { if [ "$ZH" = 1 ]; then printf '%s' "$2"; else printf '%s' "$1"; fi; }
ERR_AT="$(t 'failed at line %s (exit %s)' '第 %s 行失敗(exit %s)')"
rc=0  # tells shellcheck that rc inside the trap is defined
trap 'rc=$?; [ $rc -ne 0 ] && printf "%s[ERR ]%s $ERR_AT: %s\n" "$c_red" "$c_rst" "$LINENO" "$rc" "$BASH_COMMAND" >&2' ERR

PLATFORM=""; TOKEN=""; PLATFORM_HTTP=""
RUNTIME="systemd"
RE_ENROLL=0
LOCAL=0
DRY_RUN=0
ALLOW_PORTS=""; DISK_PATH=""; LOG_SINK=""
STATE_DIR="/var/lib/besu-agent"
GIVEN=""  # the options above that were passed, so a re-join can tell "left out" from "set to the default"
# INSTALL_ROOT prefixes every host path; it exists only so install_test.go can run the whole flow in a
# temporary directory. Leave it unset on real hosts.
ROOT="${INSTALL_ROOT:-}"
BIN="${ROOT}/usr/local/bin/besu-agent"
ETC="${ROOT}/etc/besu-agent"
UNIT="${ROOT}/etc/systemd/system/besu-agent.service"
SBIN="${ROOT}/usr/local/sbin"
CONTAINER_UID=10001   # the user the container runs as in docker mode; matches the Dockerfile USER, and the state directory is chowned to it

# Gets a flag's value; a missing value gives the user a readable error instead of set -u's "unbound
# variable".
need_val() { [ $# -ge 2 ] && [ -n "$2" ] || die "$(t "option $1 needs a value" "參數 $1 缺值")" 2; }

while [ $# -gt 0 ]; do
  case "$1" in
    --platform)      need_val "$@"; PLATFORM="$2"; shift 2;;       # agent mTLS host:port (join / WS)
    --token)         need_val "$@"; TOKEN="$2"; shift 2;;
    --platform-http) need_val "$@"; PLATFORM_HTTP="$2"; shift 2;;  # https base for fetching the agent binary
    --runtime)       need_val "$@"; RUNTIME="$2"; shift 2;;        # systemd (default) | docker
    --re-enroll)     RE_ENROLL=1; shift;;
    --local)         LOCAL=1; shift;;                              # use the offline bundle: the agent is taken from the script's directory
    --allow-ports)   need_val "$@"; ALLOW_PORTS="$2"; GIVEN="$GIVEN allow-ports"; shift 2;;   # restrict the platform's forwarding to these ports
    --disk-path)     need_val "$@"; DISK_PATH="$2"; GIVEN="$GIVEN disk-path"; shift 2;;       # path used to measure disk usage (systemd mode)
    --state-dir)     need_val "$@"; STATE_DIR="$2"; GIVEN="$GIVEN state-dir"; shift 2;;
    --log-sink)      need_val "$@"; LOG_SINK="$2"; GIVEN="$GIVEN log-sink"; shift 2;;         # IP:port that receives Besu logs (default 127.0.0.1:5170)
    --dry-run)       DRY_RUN=1; shift;;                            # check and list the changes, change nothing
    --zh)            shift;;                                       # print messages in Chinese (scanned above)
    -h|--help)       usage; exit 0;;
    *) die "$(t "unknown option: $1" "未知參數: $1")" 2;;
  esac
done

# The platform's re-join command carries none of these options, and the configuration is rewritten from
# them below: an option left out keeps the value this host's agent runs with now, not the default.
if [ "$RE_ENROLL" = 1 ]; then
  prev() {
    if [ -f "${ETC}/agent.env" ]; then sed -n "s/^$1=//p" "${ETC}/agent.env"
    elif [ -f "${ETC}/docker-compose.yml" ]; then sed -n "s/^ *$1: \"\(.*\)\"$/\1/p" "${ETC}/docker-compose.yml"; fi
  }
  given() { case " $GIVEN " in *" $1 "*) return 0;; *) return 1;; esac; }
  given allow-ports || ALLOW_PORTS="$(prev ALLOW_PORTS)"
  given log-sink    || LOG_SINK="$(prev LOG_SINK)"
  s="$(prev AGENT_STATE_DIR)"; given state-dir || [ -z "$s" ] || STATE_DIR="$s"
  # Docker mode always measures the mounted host root, so only a systemd agent's own setting carries over.
  if ! given disk-path && [ "$RUNTIME" = systemd ] && [ -f "${ETC}/agent.env" ]; then DISK_PATH="$(prev DISK_PATH)"; fi
fi

# Argument checks come before the root check: someone with a typo should not be asked for sudo
# before seeing the real error.
[ -n "$PLATFORM" ] || die "$(t "missing required option --platform" "缺少必填參數 --platform")" 2
[ -n "$TOKEN" ]    || die "$(t "missing required option --token" "缺少必填參數 --token")" 2
# The agent port is 8443 unless stated; without a port the agent would dial 443 and the hint below would
# point at the wrong port.
case "$PLATFORM" in *:*) ;; *) PLATFORM="$PLATFORM:8443";; esac
case "$RUNTIME" in systemd|docker) ;; *) die "$(t "--runtime accepts only systemd or docker (got '$RUNTIME')" "--runtime 只接受 systemd 或 docker(收到 '$RUNTIME')")" 2;; esac
if [ -n "$ALLOW_PORTS" ]; then
  printf '%s' "$ALLOW_PORTS" | grep -Eq '^[0-9]+(,[0-9]+)*$' || die "$(t "--allow-ports takes comma-separated ports, e.g. 8545,9545" "--allow-ports 格式是逗號分隔的埠號,例如 8545,9545")" 2
  for p in ${ALLOW_PORTS//,/ }; do
    [ "$p" -ge 1 ] && [ "$p" -le 65535 ] || die "$(t "--allow-ports: port $p is not in 1-65535" "--allow-ports 的埠 $p 不在 1–65535")" 2
  done
fi
STATE_DIR="${STATE_DIR%/}"
case "$STATE_DIR" in /*) ;; *) die "$(t "--state-dir must be an absolute path" "--state-dir 要絕對路徑")" 2;; esac
# Only checks the shape; "must be loopback or the IP of a local bridge interface" is checked by the
# agent at startup (it can see the interfaces).
if [ -n "$LOG_SINK" ]; then
  printf '%s' "$LOG_SINK" | grep -Eq '^[0-9]{1,3}(\.[0-9]{1,3}){3}:[0-9]{1,5}$' \
    || die "$(t "--log-sink takes IPv4:port, e.g. 172.17.0.1:5170" "--log-sink 格式是 IPv4:埠,例如 172.17.0.1:5170")" 2
fi
if [ -n "$DISK_PATH" ] && [ "$RUNTIME" = "docker" ]; then
  # Inside the container it measures the mounted host rootfs, with the path fixed by the script;
  # letting the user change it would only measure the container's own overlay layer.
  die "$(t "--disk-path applies only to systemd mode; docker mode always measures the host root" "--disk-path 只用在 systemd 模式;docker 模式固定量宿主根目錄")" 2
fi

# Which endpoint join uses: /pki/v1/join by default (new nodes only); --re-enroll uses
# /pki/v1/re-enroll. An empty string means no flag, so it is deliberately expanded unquoted here.
JOIN_MODE_ARG=""
[ "$RE_ENROLL" = "0" ] || JOIN_MODE_ARG="--re-enroll"

# A dry run changes nothing, so it does not need root
[ "$DRY_RUN" = 1 ] || [ "$(id -u)" = "0" ] || die "$(t "installing needs root; run it with sudo." "安裝需 root,請用 sudo。")"
[ -n "$PLATFORM_HTTP" ] || PLATFORM_HTTP="https://${PLATFORM%%:*}"

case "$(uname -m)" in
  x86_64|amd64) GOARCH="amd64";;
  aarch64|arm64) GOARCH="arm64";;
  *) die "$(t "unsupported architecture: $(uname -m)" "不支援的架構: $(uname -m)")";;
esac

# The directory of the unpacked offline bundle (where install.sh is). Computed only with --local:
# curl | bash has no BASH_SOURCE and would abort under set -u
SRC_DIR=""
[ "$LOCAL" = "0" ] || SRC_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

step "$(t "Preflight checks (runtime=${RUNTIME})" "前置檢查(runtime=${RUNTIME})")"
[ "$LOCAL" = "1" ] || command -v curl >/dev/null || die "$(t "curl is required" "需要 curl")" 2
command -v sha256sum >/dev/null || die "$(t "sha256sum (coreutils) is required" "需要 sha256sum(coreutils)")" 2
if [ "$RUNTIME" = "systemd" ]; then
  command -v systemctl >/dev/null || die "$(t "systemd mode needs systemd (systemctl)" "systemd 模式需要 systemd(systemctl)")" 2
else
  command -v docker >/dev/null || die "$(t "docker mode needs docker" "docker 模式需要 docker")" 2
  docker compose version >/dev/null 2>&1 || die "$(t "docker mode needs the docker compose v2 plugin" "docker 模式需要 docker compose v2 外掛")" 2
fi
ok "$(t "Prerequisites met" "前置條件齊備")"

if [ "$DRY_RUN" = 1 ]; then
  step "$(t "Dry run: reach the platform" "試跑:連平台")"
  # Only opens a TCP connection, so the join code is not used; the real run's join still checks the certificate.
  if timeout 5 bash -c ": </dev/tcp/${PLATFORM%:*}/${PLATFORM##*:}" 2>/dev/null; then
    ok "$(t "${PLATFORM} accepts connections" "${PLATFORM} 連得到")"
  else
    warn "$(t "cannot reach ${PLATFORM}; check the address and that port ${PLATFORM##*:} is open between this host and the platform" "連不到 ${PLATFORM};檢查位址，以及這台主機到平台的 ${PLATFORM##*:} 埠有沒有開")"
  fi
  step "$(t "Dry run: what a real run would change" "試跑:實際執行會做的變更")"
  if [ "$LOCAL" = 1 ]; then
    log "$(t "take besu-agent-linux-${GOARCH} from ${SRC_DIR}, check it against SHA256SUMS, install it as ${BIN}" "從 ${SRC_DIR} 取 besu-agent-linux-${GOARCH},對 SHA256SUMS 校驗後裝成 ${BIN}")"
  else
    log "$(t "download ${PLATFORM_HTTP}/besu-agent-linux-${GOARCH}, check its SHA-256, install it as ${BIN}" "下載 ${PLATFORM_HTTP}/besu-agent-linux-${GOARCH},校驗 SHA-256 後裝成 ${BIN}")"
  fi
  if [ "$RE_ENROLL" = 1 ]; then
    log "$(t "stop the agent running on this host, then re-enroll with ${PLATFORM}; the new certificate goes to ${STATE_DIR}" "停掉這台主機上執行中的 agent,再向 ${PLATFORM} 重新接入;新憑證寫到 ${STATE_DIR}")"
  else
    log "$(t "join ${PLATFORM} with the join code; the certificate goes to ${STATE_DIR}" "用接入碼向 ${PLATFORM} 接入;憑證寫到 ${STATE_DIR}")"
  fi
  if [ "$RUNTIME" = systemd ]; then
    log "$(t "create the system user besu-agent if missing, write ${ETC}/agent.env and ${UNIT}, enable and start besu-agent.service" "沒有就建立系統帳號 besu-agent,寫入 ${ETC}/agent.env 與 ${UNIT},啟用並啟動 besu-agent.service")"
  else
    log "$(t "build the local image besu-agent:<version>, write ${ETC}/docker-compose.yml, start the besu-agent container" "在本機建 besu-agent:<版本> 映像，寫入 ${ETC}/docker-compose.yml,啟動 besu-agent 容器")"
  fi
  log "$(t "install ${SBIN}/bsos-uninstall and ${SBIN}/bsos-upgrade" "裝上 ${SBIN}/bsos-uninstall 與 ${SBIN}/bsos-upgrade")"
  ok "$(t "Dry run finished: nothing was changed and the join code was not used." "試跑完成：沒有改動任何東西，接入碼也沒有用掉。")"
  exit 0
fi

# Nothing on the host changes until the join succeeds: the binary and the certificate go to .new
# paths first, and a running agent is only stopped (never removed) for a re-enroll. If anything fails
# before the join completes, the EXIT trap removes the .new files and starts the stopped agent again,
# leaving the host as it was. After a successful join the old certificate is already revoked and the
# join code used, so there is nothing to go back to; the trap then says how to finish by hand.
STAGE="${STATE_DIR}.new"
JOINED=0; STARTED=0; STOPPED=""; NEW_IMAGE=""; ctx=""
on_exit() {
  rc=$?
  [ "$rc" -ne 0 ] || return 0
  [ -z "$ctx" ] || rm -rf "$ctx"
  if [ "$JOINED" = 0 ]; then
    rm -rf "${BIN}.new" "$STAGE"
    [ -z "$NEW_IMAGE" ] || docker image rm "$NEW_IMAGE" >/dev/null 2>&1 || true
    case "$STOPPED" in
      systemd) systemctl start besu-agent || true;;
      docker)  docker compose -f "${ETC}/docker-compose.yml" start || true;;
    esac
    warn "$(t "Nothing was changed on this host." "這台主機沒有任何變更。")"
  elif [ "$STARTED" = 0 ]; then
    warn "$(t "The join succeeded and the certificate is in ${STATE_DIR}; the join code is used up, so do not run install.sh again. Fix the error above, then start the agent: systemctl restart besu-agent, or docker compose -f ${ETC}/docker-compose.yml up -d" "接入已成功,憑證在 ${STATE_DIR};接入碼已用掉,不要重跑 install.sh。修好上面的錯誤後啟動 agent:systemctl restart besu-agent,或 docker compose -f ${ETC}/docker-compose.yml up -d")"
  else
    warn "$(t "The agent is joined and running; only installing bsos-uninstall / bsos-upgrade into ${SBIN} failed." "agent 已接入並在執行;只有把 bsos-uninstall / bsos-upgrade 裝到 ${SBIN} 失敗。")"
  fi
}
trap on_exit EXIT
# A signal (Ctrl-C, a dropped SSH session) would run the EXIT trap with status 0 and skip the cleanup;
# turn it into a failing exit instead.
trap 'exit 130' INT HUP TERM
rm -rf "${BIN}.new" "$STAGE"  # leftovers of an interrupted earlier run

# ── agent binary ─────────────────────────────────────────────────────────────
# Download to .new; it is moved into place only after the join succeeds (a later mv, not an
# overwrite, also avoids text file busy when the agent is running). Compare the SHA-256 the platform provides
# before swapping: this runs as a long-lived service, so an interrupted or tampered download must
# not be silently installed. Docker mode also installs it on the host: join and the upgrade script's
# version comparison use this copy, and the image just wraps it.
if [ "$LOCAL" = "1" ]; then
  # Offline bundle: the checksum comes from the bundle's SHA256SUMS (verifying the whole bundle is
  # up to the user before unpacking, see the command on the onboarding page)
  step "$(t "Take the agent binary from the bundle (linux/${GOARCH})" "取用安裝包裡的 agent binary(linux/${GOARCH})")"
  [ -f "${SRC_DIR}/besu-agent-linux-${GOARCH}" ] || die "$(t "the bundle has no ${GOARCH} agent (${SRC_DIR}/besu-agent-linux-${GOARCH})" "安裝包裡沒有 ${GOARCH} 的 agent(${SRC_DIR}/besu-agent-linux-${GOARCH})")" 3
  [ -f "${SRC_DIR}/SHA256SUMS" ] || die "$(t "the bundle has no SHA256SUMS" "安裝包裡沒有 SHA256SUMS")" 3
  install -m 0600 "${SRC_DIR}/besu-agent-linux-${GOARCH}" "${BIN}.new"
  expected="$(awk -v f="besu-agent-linux-${GOARCH}" '$2 == f {print $1}' "${SRC_DIR}/SHA256SUMS")"
  [ -n "$expected" ] || die "$(t "SHA256SUMS has no entry for besu-agent-linux-${GOARCH}" "SHA256SUMS 裡沒有 besu-agent-linux-${GOARCH}")" 3
else
  step "$(t "Download the agent binary (linux/${GOARCH})" "下載 agent binary(linux/${GOARCH})")"
  curl -fsSL "${PLATFORM_HTTP}/besu-agent-linux-${GOARCH}" -o "${BIN}.new"
  expected="$(curl -fsSL "${PLATFORM_HTTP}/besu-agent-linux-${GOARCH}.sha256" | awk '{print $1}')"
  [ -n "$expected" ] || die "$(t "could not get the agent binary's SHA-256 (${PLATFORM_HTTP}/besu-agent-linux-${GOARCH}.sha256)" "抓不到 agent binary 的 SHA-256 校驗值(${PLATFORM_HTTP}/besu-agent-linux-${GOARCH}.sha256)")" 3
fi
actual="$(sha256sum "${BIN}.new" | awk '{print $1}')"
if [ "$expected" != "$actual" ]; then
  rm -f "${BIN}.new"
  die "$(t "agent binary checksum mismatch (expected ${expected}, got ${actual}); the download may be broken or tampered with. Install aborted" "agent binary 完整性校驗失敗(期望 ${expected},實際 ${actual})——下載可能中斷或遭竄改,已中止安裝")" 3
fi
chmod 0755 "${BIN}.new"
AGENT_VERSION="$(timeout 5 "${BIN}.new" version 2>/dev/null | head -1 | tr -d '[:space:]' || true)"
# The version string is used as a docker tag: tags do not accept + (semver build metadata), so it is
# replaced with _.
IMAGE_TAG="besu-agent:$(printf '%s' "${AGENT_VERSION:-local-${actual:0:12}}" | tr '+' '_')"
ok "besu-agent ${AGENT_VERSION:-$(t '<unknown version>' '<未知版本>')} $(t 'verified' '校驗通過')"

# Docker mode: build the image before the join, so the step most likely to fail cannot leave a
# joined node without a runnable agent. A tag that already existed (same version reinstalled) is
# kept on failure, since a running container may use it.
if [ "$RUNTIME" = "docker" ]; then
  docker image inspect "$IMAGE_TAG" >/dev/null 2>&1 || NEW_IMAGE="$IMAGE_TAG"
  ctx="$(mktemp -d)"
  install -m 0755 "${BIN}.new" "${ctx}/besu-agent"
  printf 'FROM scratch\nCOPY besu-agent /besu-agent\nUSER %s\nENTRYPOINT ["/besu-agent"]\n' "$CONTAINER_UID" > "${ctx}/Dockerfile"
  docker build -q -t "$IMAGE_TAG" "$ctx" >/dev/null
  rm -rf "$ctx"; ctx=""
fi

# Re-enroll: the platform refuses while this node is still connected, so stop the local agent (either
# runtime). Only stop it: if the join fails, the EXIT trap starts it again.
if [ "$RE_ENROLL" = "1" ]; then
  if systemctl is-active --quiet besu-agent 2>/dev/null; then
    systemctl stop besu-agent; STOPPED=systemd
  elif [ -f "${ETC}/docker-compose.yml" ] && [ "$(docker inspect -f '{{.State.Running}}' besu-agent 2>/dev/null)" = true ]; then
    docker compose -f "${ETC}/docker-compose.yml" stop; STOPPED=docker
  fi
  [ -z "$STOPPED" ] || log "$(t "Stopped the running agent for the re-enroll; it starts again if the join fails" "已停下執行中的 agent 以便重新接入;接入失敗會再啟動")"
fi

# ── join: exchange the token for a certificate into the staging directory ──────────────────────────
# A sibling of the state directory, so the move below stays on one filesystem and keeps the default
# SELinux label of the parent directory.
step "$(t "Join the platform (${PLATFORM})" "join 平台(${PLATFORM})")"
[ -f "${STATE_DIR}/agent.crt" ] && [ "$RE_ENROLL" = "0" ] && warn "$(t "${STATE_DIR} already has a certificate; it is replaced if the join succeeds" "${STATE_DIR} 已有憑證,接入成功後會被取代")"
join_once() {
  # shellcheck disable=SC2086
  "${BIN}.new" join $JOIN_MODE_ARG --platform "$PLATFORM" --token "$TOKEN" --state-dir "$STAGE"
}
# The agent's own output is English and names the staging directory, so only a failure shows it, after
# a sentence in the chosen language; on success the lines below say where things ended up.
join_failed() {
  local why
  # Matched on the agent's and the platform's English messages; install_test.go pins each case.
  case "$join_out" in
    *'malformed token'*|*'encoding/hex'*)
               why="$(t "the join code is not complete; copy it again from the platform" "接入碼不完整;請從平台重新複製")" ;;
    *'(403)'*) why="$(t "this host's address is not in the join code's allowed source IPs" "這台主機的位址不在接入碼允許的來源 IP 裡")" ;;
    *'digest mismatch'*)
               why="$(t "the join code was made by a different platform than ${PLATFORM}, or something on the network is intercepting the connection" "接入碼不是 ${PLATFORM} 這個平台產生的,或網路上有東西攔截了連線")" ;;
    *'(401)'*) why="$(t "the join code is wrong, already used, expired, or replaced by a newer one; generate a new one on the platform" "接入碼錯誤、已用過、已過期,或已被新的取代;請在平台重新產生")" ;;
    *'still connected'*)
               why="$(t "the platform still sees this node connected; stop the agent that is running for it (also on another host or in the other runtime) and try again" "平台看到這個節點還連著;請停掉正在跑的 agent(也可能在別台主機或用另一種方式在跑)後再試")" ;;
    *'already exists'*)
               why="$(t "a node with this id is already registered; to replace it, unenroll it on the platform first, or use Re-join" "這個節點 id 已經登錄;要換掉它,先在平台解除接入,或改用重新接入")" ;;
    *'(404)'*) why="$(t "this node is not registered on the platform; use the install command from Register node instead of Re-join" "平台上沒有這個節點;請改用「登錄節點」產生的安裝指令,不要用重新接入")" ;;
    *'no such host'*)
               why="$(t "the platform address ${PLATFORM} cannot be resolved; check the host name" "平台位址 ${PLATFORM} 查不到;請確認主機名稱")" ;;
    *'deadline exceeded'*|*'connection refused'*|*'no route to host'*|*'network is unreachable'*|*'i/o timeout'*)
               why="$(t "cannot reach the platform at ${PLATFORM}; check the address and that port 8443 is open" "連不到平台 ${PLATFORM};請確認位址,以及 8443 埠有開")" ;;
    *'verify platform certificate'*|*'not a platform'*|*'no peer certificate'*|*'tls:'*|*'TLS handshake'*|*'HTTP response to HTTPS client'*)
               why="$(t "${PLATFORM} did not answer as this platform's agent port; check the address and that the port is 8443" "${PLATFORM} 不是平台給 agent 連的埠;請確認位址,以及埠號是 8443")" ;;
    *)         why="" ;;
  esac
  [ -z "$why" ] || warn "$why"
  printf '%s\n' "$join_out" | sed 's/^[0-9/]* [0-9:]* //' | sed "s/^/  $(t "details" "詳細"): /" >&2
  die "$(t "join failed" "接入失敗")"
}
if ! join_out="$(join_once 2>&1)"; then
  # The platform drops a connection from its list as soon as the TCP connection closes, so a 409 right
  # after stopping the agent is rare; one retry covers it.
  if [ -n "$STOPPED" ] && printf '%s' "$join_out" | grep -q '(409)'; then
    sleep 5
    join_out="$(join_once 2>&1)" || join_failed
  else
    join_failed
  fi
fi
JOINED=1
VALID_UNTIL="$(printf '%s' "$join_out" | sed -n 's/.*certificate valid until \([^,]*\),.*/\1/p')"

# From here on the join is done. Move the new certificate into place (keeping anything else in the
# state directory, such as logspool/), then the binary.
install -d -m 0700 "$STATE_DIR"
for f in "$STAGE"/*; do mv -f "$f" "$STATE_DIR/"; done
rm -rf "$STAGE"
mv -f "${BIN}.new" "$BIN"
ok "$(t "Certificate written to ${STATE_DIR}${VALID_UNTIL:+ (valid until ${VALID_UNTIL}; renewed automatically while online)}; agent installed at ${BIN}" "憑證已寫入 ${STATE_DIR}${VALID_UNTIL:+(有效到 ${VALID_UNTIL},在線時自動換發)};agent 裝到 ${BIN}")"

# Rerunning with the other runtime: tear the old one down now that it is no longer needed (two agents
# would both connect, and the upgrade script tells the mode from whether the compose file exists).
if [ "$RUNTIME" = "systemd" ] && [ -f "${ETC}/docker-compose.yml" ]; then
  warn "$(t "Removing the earlier docker install" "移除先前的 docker 安裝")"
  docker compose -f "${ETC}/docker-compose.yml" down --rmi all || true
  rm -rf "${ETC}/docker-compose.yml" "${ETC}/image"
elif [ "$RUNTIME" = "docker" ] && [ -f "$UNIT" ]; then
  warn "$(t "Removing the earlier systemd install" "移除先前的 systemd 安裝")"
  systemctl disable --now besu-agent || true
  rm -f "$UNIT" "${ETC}/agent.env"
  systemctl daemon-reload
fi

install -d -m 0755 "$ETC"

if [ "$RUNTIME" = "systemd" ]; then
  step "$(t "systemd service" "systemd 服務")"
  id besu-agent >/dev/null 2>&1 || useradd --system --home "$STATE_DIR" --shell /usr/sbin/nologin besu-agent
  chown -R besu-agent:besu-agent "$STATE_DIR"
  {
    echo "PLATFORM_ADDR=${PLATFORM}"
    echo "AGENT_STATE_DIR=${STATE_DIR}"
    [ -z "$ALLOW_PORTS" ] || echo "ALLOW_PORTS=${ALLOW_PORTS}"
    [ -z "$DISK_PATH" ]   || echo "DISK_PATH=${DISK_PATH}"
    [ -z "$LOG_SINK" ]    || echo "LOG_SINK=${LOG_SINK}"
  } > "${ETC}/agent.env"
  chmod 0644 "${ETC}/agent.env"
  # The agent only needs to dial one outbound connection, read /proc and write its own state
  # directory. Everything else is locked down.
  install -d -m 0755 "$(dirname "$UNIT")"
  cat > "$UNIT" <<UNIT
[Unit]
Description=BSOS Node Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=besu-agent
Group=besu-agent
EnvironmentFile=${ETC}/agent.env
WorkingDirectory=${STATE_DIR}
ExecStart=${BIN}
Restart=always
RestartSec=5
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=${STATE_DIR}
ProtectHome=true
PrivateTmp=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable besu-agent >/dev/null
  systemctl restart besu-agent
  sleep 1
  systemctl is-active --quiet besu-agent || die "$(t "besu-agent is not active; check journalctl -u besu-agent" "besu-agent 未 active,請查 journalctl -u besu-agent")"
  ok "$(t "besu-agent.service started" "besu-agent.service 已啟動")"
else
  step "$(t "Docker image and container" "docker 映像與容器")"
  chown -R "${CONTAINER_UID}:${CONTAINER_UID}" "$STATE_DIR"
  # Built locally FROM scratch: contains only the same SHA-256-verified binary, so no image registry
  # needs to be trusted and it installs offline; the agent is a static binary and its trust anchor
  # is the pinned platform CA, so scratch is enough.
  # The image was built before the join; keep its context here for upgrade-agent.sh's rebuilds.
  install -d -m 0755 "${ETC}/image"
  install -m 0755 "$BIN" "${ETC}/image/besu-agent"
  cat > "${ETC}/image/Dockerfile" <<DOCKERFILE
FROM scratch
COPY besu-agent /besu-agent
USER ${CONTAINER_UID}
ENTRYPOINT ["/besu-agent"]
DOCKERFILE
  # gopsutil honors HOST_PROC / HOST_SYS (the same approach as node_exporter); host networking makes
  # 127.0.0.1:<port> the host's Besu, and the host's /proc, /sys and rootfs are mounted read-only so
  # host metrics describe the host rather than the container.
  cat > "${ETC}/docker-compose.yml" <<COMPOSE
services:
  besu-agent:
    image: ${IMAGE_TAG}
    container_name: besu-agent
    restart: unless-stopped
    network_mode: host
    # Docker's SELinux support would confine it away from the host's /, /proc, /sys and the state directory;
    # relabeling / with :z is not an option, so run it unconfined, like a systemd agent.
    security_opt: ["label=disable"]
    volumes:
      - ${STATE_DIR}:${STATE_DIR}
      - /proc:/host/proc:ro
      - /sys:/host/sys:ro
      - /:/rootfs:ro
    environment:
      PLATFORM_ADDR: "${PLATFORM}"
      AGENT_STATE_DIR: "${STATE_DIR}"
      HOST_PROC: /host/proc
      HOST_SYS: /host/sys
      DISK_PATH: /rootfs
COMPOSE
  [ -z "$ALLOW_PORTS" ] || echo "      ALLOW_PORTS: \"${ALLOW_PORTS}\"" >> "${ETC}/docker-compose.yml"
  [ -z "$LOG_SINK" ]    || echo "      LOG_SINK: \"${LOG_SINK}\"" >> "${ETC}/docker-compose.yml"
  docker compose -f "${ETC}/docker-compose.yml" up -d --force-recreate
  ok "$(t "besu-agent container started (${IMAGE_TAG})" "besu-agent 容器已啟動(${IMAGE_TAG})")"
fi

STARTED=1

# ── Uninstall: removes only the agent, leaves Besu alone; the state directory is kept (so it can be reinstalled with the same certificate) ──
install -d -m 0755 "$SBIN"
cat > "${SBIN}/bsos-uninstall" <<'UNINSTALL'
#!/usr/bin/env bash
# Removes the BSOS Node Agent. Does not touch Besu. The certificate directory is kept; add --purge
# to delete it too.
set -euo pipefail
# English by default; --zh prints Chinese.
PURGE=0; ZH=0
for a in "$@"; do case "$a" in --purge) PURGE=1;; --zh) ZH=1;; esac; done
t() { if [ "$ZH" = 1 ]; then printf '%s' "$2"; else printf '%s' "$1"; fi; }
[ "$(id -u)" = "0" ] || { t "needs root" "需 root" >&2; echo >&2; exit 1; }
STATE_DIR="$(sed -n 's/^AGENT_STATE_DIR=//p' /etc/besu-agent/agent.env 2>/dev/null || true)"
if [ -f /etc/besu-agent/docker-compose.yml ]; then
  STATE_DIR="$(sed -n 's/^ *AGENT_STATE_DIR: "\(.*\)"$/\1/p' /etc/besu-agent/docker-compose.yml)"
  docker compose -f /etc/besu-agent/docker-compose.yml down --rmi all || true
fi
if [ -f /etc/systemd/system/besu-agent.service ]; then
  systemctl disable --now besu-agent || true
  rm -f /etc/systemd/system/besu-agent.service
  systemctl daemon-reload
fi
rm -rf /etc/besu-agent /usr/local/bin/besu-agent /usr/local/bin/besu-agent.prev
id besu-agent >/dev/null 2>&1 && userdel besu-agent || true
rm -f /usr/local/sbin/bsos-uninstall /usr/local/sbin/bsos-upgrade
if [ "$PURGE" = "1" ] && [ -n "$STATE_DIR" ]; then
  rm -rf "$STATE_DIR"; t "Agent removed; certificate directory $STATE_DIR deleted" "agent 已移除,憑證目錄 $STATE_DIR 已刪除"; echo
else
  t "Agent removed; certificate directory ${STATE_DIR:-<unknown>} kept" "agent 已移除,憑證目錄 ${STATE_DIR:-<未知>} 保留"; echo
fi
UNINSTALL
chmod 0755 "${SBIN}/bsos-uninstall"

# ── Upgrade command: the offline bundle includes upgrade-agent.sh, installed as bsos-upgrade (platform address and certificate directory are read from the agent config; only connects to 8443) ──
# Copy first, then verify the copy (same as the agent binary): if you verify and then copy, a swap
# in between would go unnoticed.
if [ "$LOCAL" = "1" ] && [ -f "${SRC_DIR}/upgrade-agent.sh" ]; then
  install -m 0600 "${SRC_DIR}/upgrade-agent.sh" "${SBIN}/bsos-upgrade.new"
  want="$(awk '$2 == "upgrade-agent.sh" {print $1}' "${SRC_DIR}/SHA256SUMS")"
  if [ -n "$want" ] && [ "$want" = "$(sha256sum "${SBIN}/bsos-upgrade.new" | awk '{print $1}')" ]; then
    chmod 0755 "${SBIN}/bsos-upgrade.new"
    mv "${SBIN}/bsos-upgrade.new" "${SBIN}/bsos-upgrade"
  else
    rm -f "${SBIN}/bsos-upgrade.new"
    warn "$(t "upgrade-agent.sh does not match SHA256SUMS; bsos-upgrade not installed" "upgrade-agent.sh 與 SHA256SUMS 不符,未安裝 bsos-upgrade")"
  fi
fi

echo
ok "$(t "Done. This node should show as online on the platform." "完成。平台上這個節點應顯示在線。")"
log "$(t "If chain state or charts are missing, check that Besu's RPC and metrics are enabled and the ports match the node's settings on the platform." "看不到鏈狀態或圖表時,檢查 Besu 的 RPC / metrics 有沒有開、埠是否與平台節點設定一致。")"
[ ! -x "${SBIN}/bsos-upgrade" ] || log "$(t "Upgrade: sudo /usr/local/sbin/bsos-upgrade" "升級:sudo /usr/local/sbin/bsos-upgrade")"
# Full paths: sudo's secure_path on RHEL does not include /usr/local/sbin
log "$(t "Uninstall: sudo /usr/local/sbin/bsos-uninstall (add --purge to delete the certificates too)" "解除安裝:sudo /usr/local/sbin/bsos-uninstall(加 --purge 連憑證一起刪)")"
