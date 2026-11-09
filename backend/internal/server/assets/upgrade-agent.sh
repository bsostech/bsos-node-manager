#!/usr/bin/env bash
# BSOS Agent upgrade script (mTLS): uses the agent's existing identity to fetch the latest
# besu-agent binary from the platform -> compares versions -> only if different, swaps the file
# atomically + restarts; if the new version fails to come up it automatically swaps back to the old
# one. Requires root. Works for both systemd and docker installs (detected automatically from the
# files install.sh left behind).
#   curl -fsSL https://<platform>/upgrade-agent.sh -o upgrade-agent.sh
#   sudo bash upgrade-agent.sh [--platform <mTLS host:port>] [--state-dir /var/lib/besu-agent] [--zh]
#   (when both are omitted they are read from the agent config in /etc/besu-agent; the offline
#   install bundle installs this script as bsos-upgrade)
#
# Why mTLS (blocks man-in-the-middle):
#   The platform certificate chain is verified against the embedded CA root the agent trusts
#   (--cacert ca.pem) -> binds to the genuine platform; a man in the middle cannot obtain a
#   certificate issued by this private CA, so it cannot swap the binary. Public HTTP + SHA-256 only
#   protects against "transport corruption": a man in the middle can swap both the binary and the
#   hash, and the check still passes. SHA-256 is kept here as a second safeguard (and it goes over
#   the mTLS endpoint, which a man in the middle cannot tamper with anyway). Note: this does not
#   protect against the platform itself being compromised and agent-bin replaced; that needs offline
#   binary signing infrastructure (out of scope).
# Does not touch agent.env / compose settings and does not re-join (the certificate is in the state
# dir; swapping the binary does not affect identity).
set -euo pipefail

# ── Logging: colors only on a TTY, warn/err go to stderr, ERR trap reports where it failed ──
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
rc=0  # lets shellcheck know rc is defined inside the trap
trap 'rc=$?; [ $rc -ne 0 ] && printf "%s[ERR ]%s $ERR_AT: %s\n" "$c_red" "$c_rst" "$LINENO" "$rc" "$BASH_COMMAND" >&2' ERR

PLATFORM=""                       # platform mTLS address (host:port, the :8443 one)
STATE_DIR="/var/lib/besu-agent"   # agent certificate directory (matches install.sh's AGENT_STATE default)
STATE_GIVEN=0
while [ $# -gt 0 ]; do
  case "$1" in
    --platform)  PLATFORM="$2"; shift 2;;
    --state-dir) STATE_DIR="$2"; STATE_GIVEN=1; shift 2;;
    --zh)        shift;;
    *) die "$(t "unknown option: $1" "未知參數: $1")" 2;;
  esac
done

[ "$(id -u)" = "0" ] || die "$(t "upgrading needs root; run it with sudo." "升級需 root,請用 sudo。")"
# Without --platform / --state-dir, read the agent config install.sh wrote (once the offline bundle
# installs this as bsos-upgrade, just run it)
if [ -z "$PLATFORM" ]; then
  if [ -f /etc/besu-agent/agent.env ]; then
    PLATFORM="$(sed -n 's/^PLATFORM_ADDR=//p' /etc/besu-agent/agent.env)"
    s="$(sed -n 's/^AGENT_STATE_DIR=//p' /etc/besu-agent/agent.env)"; [ -z "$s" ] || [ "$STATE_GIVEN" = 1 ] || STATE_DIR="$s"
  elif [ -f /etc/besu-agent/docker-compose.yml ]; then
    PLATFORM="$(sed -n 's/^ *PLATFORM_ADDR: "\(.*\)"$/\1/p' /etc/besu-agent/docker-compose.yml)"
    s="$(sed -n 's/^ *AGENT_STATE_DIR: "\(.*\)"$/\1/p' /etc/besu-agent/docker-compose.yml)"; [ -z "$s" ] || [ "$STATE_GIVEN" = 1 ] || STATE_DIR="$s"
  fi
fi
[ -n "$PLATFORM" ] || die "$(t "missing --platform <platform mTLS host:port>" "缺少 --platform <平台 mTLS host:port>")" 2

CERT="${STATE_DIR}/agent.crt"; KEY="${STATE_DIR}/agent.key"; CA="${STATE_DIR}/ca.pem"
for f in "$CERT" "$KEY" "$CA"; do
  [ -r "$f" ] || die "$(t "cannot read certificate material $f (does --state-dir point to where the agent is installed?)" "讀不到憑證材料 $f(--state-dir 指到 agent 裝的位置了嗎?)")" 2
done

case "$(uname -m)" in
  x86_64|amd64) ARCH="amd64";;
  aarch64|arm64) ARCH="arm64";;
  *) die "$(t "unsupported architecture: $(uname -m)" "不支援的架構: $(uname -m)")";;
esac

BIN=/usr/local/bin/besu-agent
PLATFORM_HOST="${PLATFORM%%:*}"
PLATFORM_PORT="${PLATFORM##*:}"; [ "$PLATFORM_PORT" = "$PLATFORM" ] && PLATFORM_PORT=8443

# The client certificate chain curl sends = leaf (agent.crt) + intermediate (inside ca.pem);
# platform mTLS needs the full chain to the root to verify the client. --cert agent.crt alone sends
# only the leaf, the platform cannot build the chain and the handshake fails, so merge them into a
# temp file first.
CLIENT_CHAIN="$(mktemp)"
trap 'rm -f "$CLIENT_CHAIN" "${BIN}.new"' EXIT
PREV="${BIN}.prev"   # the old version before the swap: swapped back if the new one fails to start; kept even after a successful upgrade for a manual rollback later
cat "$CERT" "$CA" > "$CLIENT_CHAIN"

# The platform leaf certificate's SAN is <org>.platform.internal (ca.PlatformServerName), where org
# = the O in the agent certificate's Subject. curl connects using this name (--connect-to routes it
# to the real address), so verification is standard TLS: chain to ca.pem, host name matched against
# the SAN.
# The installed agent reads it, so the host needs no openssl (RHEL does not ship it by default). An
# agent built before the org subcommand would start as a daemon instead, so bound it with timeout
# and fall back to openssl when present.
ORG="$(timeout 5 "$BIN" org --state-dir "$STATE_DIR" 2>/dev/null || true)"
if [ -z "$ORG" ] && command -v openssl >/dev/null; then
  ORG="$(openssl x509 -in "$CERT" -noout -subject -nameopt RFC2253 | sed -n 's/^subject=//; s/.*\(^\|,\)O=\([^,]*\).*/\2/p')"
fi
[ -n "$ORG" ] || die "$(t "cannot read the organization (O) from the agent certificate" "讀不到 agent 憑證的單位(O)")" 2
SERVER_NAME="${ORG}.platform.internal"

# curl_mtls <url-path> [extra curl args...]: calls the platform mTLS endpoint. $1 = path, the rest
# is passed to curl as is (e.g. -o).
curl_mtls() {
  local path="$1"; shift
  curl -fsSL --cert "$CLIENT_CHAIN" --key "$KEY" --cacert "$CA" \
    --connect-to "${SERVER_NAME}:${PLATFORM_PORT}:${PLATFORM_HOST}:${PLATFORM_PORT}" \
    "$@" "https://${SERVER_NAME}:${PLATFORM_PORT}${path}"
}

step "$(t "Check the latest version on the platform (mTLS)" "查平台最新版本(mTLS)")"
REMOTE_VER="$(curl_mtls "/pki/v1/agent-binary/${ARCH}/version" | tr -d '[:space:]')" \
  || die "$(t "could not get the platform version (mTLS connection or certificate failed, or the platform has no .version)" "抓不到平台版本(mTLS 連線/憑證失敗,或平台未提供 .version)")" 3
[ -n "$REMOTE_VER" ] || die "$(t "the platform returned an empty version" "平台回傳空版本")" 3
# Local state: timeout as a backstop in case the binary hangs instead of printing its version;
# empty result -> treated as needing an upgrade.
LOCAL_VER="$(timeout 5 "$BIN" version 2>/dev/null | head -1 | tr -d '[:space:]' || true)"
log "$(t "Local version: ${LOCAL_VER:-<unknown>} / platform version: ${REMOTE_VER}" "本機版本: ${LOCAL_VER:-<未知>} / 平台版本: ${REMOTE_VER}")"

if [ -n "$LOCAL_VER" ] && [ "$LOCAL_VER" = "$REMOTE_VER" ]; then
  echo
  ok "$(t "Already on the latest ${REMOTE_VER}; nothing to do." "已是最新 ${REMOTE_VER},無需換檔。")"
  exit 0
fi

step "$(t "Download the latest agent binary (${ARCH}, mTLS)" "抓最新 agent binary(${ARCH},mTLS)")"
curl_mtls "/pki/v1/agent-binary/${ARCH}" -o "${BIN}.new" \
  || die "$(t "downloading the agent binary failed" "下載 agent binary 失敗")" 3
log "$(t "Downloaded → ${BIN}.new" "下載完成 → ${BIN}.new")"

step "$(t "Checksum (SHA-256)" "完整性校驗(SHA-256,雙保險)")"
EXPECTED_SHA="$(curl_mtls "/pki/v1/agent-binary/${ARCH}.sha256" | awk '{print $1}')"
ACTUAL_SHA="$(sha256sum "${BIN}.new" | awk '{print $1}')"
[ -n "$EXPECTED_SHA" ] || die "$(t "could not get the agent binary's SHA-256" "抓不到 agent binary 的 SHA-256 校驗值")" 3
if [ "$EXPECTED_SHA" != "$ACTUAL_SHA" ]; then
  rm -f "${BIN}.new"
  die "$(t "agent binary checksum mismatch (expected ${EXPECTED_SHA}, got ${ACTUAL_SHA}); the download may be broken or tampered with. Upgrade aborted" "agent binary 完整性校驗失敗(期望 ${EXPECTED_SHA},實際 ${ACTUAL_SHA})——下載可能中斷或遭竄改,已中止升級")" 3
fi
chmod 0755 "${BIN}.new"
# Run the new file once first: a wrong architecture or a broken file is caught here, before the old
# version is touched
NEW_VER="$(timeout 5 "${BIN}.new" version 2>/dev/null | head -1 | tr -d '[:space:]' || true)"
[ "$NEW_VER" = "$REMOTE_VER" ] || die "$(t "the new binary does not run or reports the wrong version (printed '${NEW_VER:-<none>}', the platform says ${REMOTE_VER}); aborted, the old version is untouched" "新檔執行不起來或版本不符(印出「${NEW_VER:-<無>}」,平台說是 ${REMOTE_VER}),已中止,舊版沒動")" 3

WATCH_SECS=20
# stays_up: after the restart, watch for WATCH_SECS seconds; it counts as up only if it did not stop
# and was not automatically restarted in the meantime (systemd's Restart=always / docker's
# unless-stopped keep relaunching an agent that dies right after starting, so looking only at the
# last moment would misjudge). agent_up / agent_restarts are defined below per install type.
stays_up() {
  local before
  before="$(agent_restarts)"
  for _ in $(seq 1 "$WATCH_SECS"); do
    sleep 1
    agent_up || return 1
    [ "$(agent_restarts)" = "$before" ] || return 1
  done
}
# put_back: swap the old file back. Copy then mv, rather than overwriting a file that may still be
# executing (text file busy).
put_back() { cp -p "$PREV" "${BIN}.new" && mv "${BIN}.new" "$BIN"; }

step "$(t "Swap the binary atomically (mv, avoiding 'text file busy'); the old one is kept at ${PREV}" "原子換檔(mv,避免執行中換檔的 'text file busy');舊版留在 ${PREV}")"
[ ! -f "$BIN" ] || cp -p "$BIN" "$PREV"
mv "${BIN}.new" "$BIN"

COMPOSE=/etc/besu-agent/docker-compose.yml
if [ -f "$COMPOSE" ]; then
  # docker mode: rebuild the local image with the new binary, point compose at the new tag, recreate
  # the container. The layout (image/ directory, compose's image: line, tag rules) is decided by
  # install.sh; change both together.
  NEW_TAG="besu-agent:$(printf '%s' "$REMOTE_VER" | tr '+' '_')"
  OLD_TAG="$(sed -n 's/^ *image: *//p' "$COMPOSE" | head -1)"
  LOGS="docker logs besu-agent"
  agent_up() { [ "$(docker inspect -f '{{.State.Running}}' besu-agent 2>/dev/null)" = true ]; }
  agent_restarts() { docker inspect -f '{{.RestartCount}}' besu-agent 2>/dev/null; }
  run_tag() { sed -i "s|^\( *image: *\).*|\1$1|" "$COMPOSE"; docker compose -f "$COMPOSE" up -d --force-recreate; }
  # Swap back: point compose back at the old tag (the old image is deleted only after a successful
  # upgrade); also put the old file back in the build context so the next rebuild does not pick up
  # the broken version
  restore_old() {
    [ -n "$OLD_TAG" ] && [ "$OLD_TAG" != "$NEW_TAG" ] || return 1
    [ ! -f "$PREV" ] || { put_back && install -m 0755 "$BIN" /etc/besu-agent/image/besu-agent; }
    run_tag "$OLD_TAG"
    docker image rm "$NEW_TAG" >/dev/null 2>&1 || true
  }
  step "$(t "Rebuild the image and recreate the container" "重 build 映像並重建容器")"
  install -m 0755 "$BIN" /etc/besu-agent/image/besu-agent
  docker build -q -t "$NEW_TAG" /etc/besu-agent/image >/dev/null
  run_tag "$NEW_TAG"
else
  LOGS="journalctl -u besu-agent"
  agent_up() { systemctl is-active --quiet besu-agent; }
  # No --value: systemd before 230 does not have it; before 235 there is not even NRestarts, both
  # sides read an empty string and only agent_up is left as a guard
  agent_restarts() { systemctl show -p NRestarts besu-agent 2>/dev/null; }
  restore_old() { [ -f "$PREV" ] || return 1; put_back && systemctl restart besu-agent; }
  step "restart besu-agent"
  systemctl restart besu-agent
fi

step "$(t "Check the new version stays up (${WATCH_SECS} s)" "確認新版起來(看 ${WATCH_SECS} 秒)")"
if ! stays_up; then
  # 4 = the new version did not come up and we are back on the old one (node runs as usual); 5 =
  # could not go back, needs a human
  warn "$(t "The new version ${REMOTE_VER} did not stay up; switching back to the old one (new version log: ${LOGS})" "新版 ${REMOTE_VER} 沒有穩定跑起來,換回舊版(新版的 log:${LOGS})")"
  restore_old || die "$(t "no old version to switch back to; fix it by hand (log: ${LOGS})" "沒有舊版可以換回,請手動處理(log:${LOGS})")" 5
  stays_up || die "$(t "the old version did not come up either; fix it by hand (log: ${LOGS})" "換回舊版也沒起來,請手動處理(log:${LOGS})")" 5
  die "$(t "the new version ${REMOTE_VER} does not start; switched back to ${LOCAL_VER:-the old version}" "新版 ${REMOTE_VER} 起不來,已換回 ${LOCAL_VER:-舊版}")" 4
fi
# docker: the old tag is no longer needed, remove it; failing to remove it (another container still
# uses it) is not a failure.
if [ -f "$COMPOSE" ] && [ -n "$OLD_TAG" ] && [ "$OLD_TAG" != "$NEW_TAG" ]; then
  docker image rm "$OLD_TAG" >/dev/null 2>&1 || true
fi
echo
ok "$(t "Agent upgraded ${LOCAL_VER:-<unknown>} → ${REMOTE_VER} and restarted." "agent 已升級 ${LOCAL_VER:-<未知>} → ${REMOTE_VER} 並重啟。")"
