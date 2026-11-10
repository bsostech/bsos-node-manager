#!/usr/bin/env bash
# One-shot Docker Compose deployment for the platform. Run it in this directory (requires docker + the compose plugin).
#
# Usage (pick one of three TLS modes: public automatic / manual certificate from a public or internal CA / plain HTTP).
# TLS is terminated by the Go server itself (backend/internal/webapp, see deploy/platform/README.md):
#   # Public "automatic" HTTPS: autocert (requests/renews from Let's Encrypt via TLS-ALPN-01; only needs 443 reachable from outside)
#   #   Requires --domain to resolve to this host in public DNS. Not usable on a purely internal network (use --cert/--key below).
#   ./up.sh --org-id org-a.example --domain platform-a.example --acme --email ops@org-a.example
#   ./up.sh --org-id org-a.example --domain platform-a.example --acme --email ops@org-a.example --staging  # test the flow first, avoiding rate limits
#   # HTTPS with a "manual" certificate: public certbot/purchased, or signed by an internal company CA (only the certificate source differs)
#   ./up.sh --org-id org-a.example --domain platform-a.example --cert ./fullchain.pem --key ./privkey.pem
#   # Plain HTTP quick test (the internal network/leased line is trusted itself)
#   ./up.sh --org-id org-a.example --host 203.0.113.10 --http
#   # Agent address differs from the browser address (e.g. the platform UI uses the public external IP,
#   # but agent join is restricted to the internal network by the firewall design and only knows the internal IP)
#   #   --agent-host defaults to --host when omitted (same address; single internal-network setups need not pass it)
#   ./up.sh --org-id org-a.example --host 203.0.113.10 --agent-host 10.0.0.5 --http
#   ./up.sh                                                  # restart: no arguments needed, reuses the existing settings (reads TLS_MODE from server.env)
#   # Edition: builds Enterprise (image bsos-node-manager-enterprise) if backend/enterprise/ exists, otherwise (open source) Core;
#   # --core forces a Core build
#   ./up.sh --core
#   # Check the options and the host and list what would change, without changing anything
#   ./up.sh --org-id org-a.example --domain platform-a.example --acme --email ops@org-a.example --dry-run
#   # Messages are in English; add --zh to any run to print them in Chinese
#   # Version: taken from git (git describe), or from VERSION in a source download; --version v1.2.3 overrides it
#   # Production: add --offline-root on the first run; the CA root private key stays out of the DB and is written to root-ca.key for you to keep offline
#   ./up.sh --org-id org-a.example --domain platform-a.example --cert ./fullchain.pem --key ./privkey.pem --offline-root
#   # Or import an enterprise PKI (directory containing root.pem, inter.pem, inter.key)
#   ./up.sh --org-id org-a.example --domain platform-a.example --cert ./fullchain.pem --key ./privkey.pem --ca-import ./ca
#
# What it does: [--install-docker installs the latest docker only if missing] (1) generate server.env (CA_MASTER_KEY generated, idempotent, never overwritten)
#        (2) install certificates per mode (manual mode) + write TLS_MODE etc. into server.env (3) build the agent into agent-bin if it is
#        empty or built from another version
#        (4) generate the database password into .env if there is none (never overwritten), then docker compose up -d --build.
set -euo pipefail

# Message language: English by default, --zh switches to Chinese. Scanned before parsing so early errors follow it too.
ZH=0; for a in "$@"; do [ "$a" = --zh ] && ZH=1; done
t() { if [ "$ZH" = 1 ]; then printf '%s' "$2"; else printf '%s' "$1"; fi; }

# ── Logging: color only on a TTY (off for pipes/CI), warn/err go to stderr, ERR trap reports where it failed ──
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

HERE="$(cd "$(dirname "$0")" && pwd)"; cd "$HERE"

ORG_ID=""; DOMAIN=""; HOST=""; AGENT_HOST=""; HTTP=false; INSTALL_DOCKER=false; CERT=""; KEY=""
ACME=false; EMAIL=""; STAGING=false; FORCE_CORE=false; OFFLINE_ROOT=false; CA_IMPORT=""; DRY_RUN=false; ORG_GENERATED=false; VERSION_ARG=""
NEED_DOCKER=false  # dry run on a host without docker: the real run would install it first
while [ $# -gt 0 ]; do
  case "$1" in
    --org-id) ORG_ID="$2"; shift 2;;  # org id (written into every certificate's O, cannot change once the CA exists); required for Enterprise, auto-generated for Core if omitted
    --domain) DOMAIN="$2"; shift 2;;
    --host)   HOST="$2"; shift 2;;       # browser/UI public address
    --agent-host) AGENT_HOST="$2"; shift 2;;  # used for PLATFORM_ADDR; defaults to --host when omitted (see the default below)
    --http)   HTTP=true; shift;;         # plain HTTP (server listens only on :80)
    --cert)   CERT="$2"; shift 2;;       # HTTPS manual certificate (fullchain; signed by a public CA or an internal company CA)
    --key)    KEY="$2"; shift 2;;        # HTTPS private key
    --acme)   ACME=true; shift;;         # public automatic HTTPS (autocert/TLS-ALPN-01; needs public DNS + 443 reachable from outside)
    --email)  EMAIL="$2"; shift 2;;      # Let's Encrypt account / expiry notice email (required with --acme)
    --staging) STAGING=true; shift;;     # test with the LE staging CA first (avoids production rate limits; browsers do not trust the certificate)
    --install-docker) INSTALL_DOCKER=true; shift;;  # install the latest docker automatically if missing (off by default)
    --core)   FORCE_CORE=true; shift;;   # force a Core build (default: Enterprise if the Enterprise code is present)
    --offline-root) OFFLINE_ROOT=true; shift;;  # first deployment: the CA root private key is written to root-ca.key for you to take away, not stored in the DB
    --ca-import) CA_IMPORT="$2"; shift 2;;      # first deployment: import an enterprise PKI (directory containing root.pem, inter.pem, inter.key; the root private key is not imported)
    --dry-run) DRY_RUN=true; shift;;     # check the options, the ports and DNS, list what would change, change nothing
    --version) VERSION_ARG="$2"; shift 2;;  # version built into the server and the agent (default: see deploy/version.sh)
    --zh)     shift;;                    # print messages in Chinese (scanned above)
    *) die "$(t "unknown option: $1" "未知參數: $1")" 2;;
  esac
done
if $OFFLINE_ROOT && [ -n "$CA_IMPORT" ]; then die "$(t "use only one of --offline-root and --ca-import" "--offline-root 與 --ca-import 只能擇一")" 2; fi
# Edition: build Enterprise if the Enterprise code (backend/enterprise/) exists; the open-source edition lacks this directory and builds Core.
# The edition must be known first to tell whether --org-id is required.
# Every secret up.sh generates (CA key, tokens, passwords) comes from openssl; a missing one must stop the run, not write empty values.
command -v openssl >/dev/null || die "$(t "openssl is required (install it with your package manager, e.g. dnf install openssl)" "需要 openssl(用套件管理員安裝,例如 dnf install openssl)")" 2
EDITION="core"; SERVER_IMAGE="bsos-node-manager"
if ! $FORCE_CORE && [ -d "$HERE/../../../backend/enterprise" ]; then EDITION="enterprise"; SERVER_IMAGE="bsos-node-manager-enterprise"; fi
# Version of this checkout, built into both the server image and the agent so the two always match.
# shellcheck source=../../version.sh
. "$HERE/../../version.sh"
resolve_version "$HERE/../../.." "$VERSION_ARG"
# The agent in agent-bin/ is rebuilt when it is missing or was built from another version. A checkout with local changes keeps
# the same version string (…-dirty, or dev), so after editing agent code rebuild it with deploy/agent/build-agent.sh.
AGENT_STALE=false
for f in agent-bin/besu-agent-linux-amd64 agent-bin/besu-agent-linux-arm64; do
  [ -f "$f" ] && [ "$(cat "$f.version" 2>/dev/null)" = "$VERSION" ] || AGENT_STALE=true
done
# --org-id is needed only when server.env is first created; if it exists (restart/idempotent rerun), ORG_ID from server.env is reused.
# The org id is written into every certificate when the CA is created and cannot change later. Core has no concept of orgs, so if omitted one is generated and written to server.env;
# if you plan to upgrade to Enterprise, pass --org-id on the first run (it is the org other organizations see when they read your nodes).
if [ ! -f server.env ]; then
  if [ -z "$ORG_ID" ]; then
    [ "$EDITION" = core ] || die "$(t "missing --org-id (required on the first Enterprise deployment): use your company domain, e.g. example.com; it cannot be changed later" "缺少 --org-id(Enterprise 首次部署必填):請填公司網域,例如 example.com;建好後不能改")" 2
    ORG_ID="bsos-node-manager-$(openssl rand -hex 3)"; ORG_GENERATED=true
  fi
  # Same rule as server's ca.CheckOrgID; reject early instead of waiting for server to refuse to start after the build
  if [[ ! "$ORG_ID" =~ ^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$ ]] || [[ "$ORG_ID" == *..* ]]; then
    die "$(t "--org-id \"$ORG_ID\" is invalid: lowercase letters, digits, . - _ only, starting and ending with a letter or digit (e.g. example.com)" "--org-id \"$ORG_ID\" 不合法:只能用小寫英數與 . - _,頭尾須為英數(例如 example.com)")" 2
  fi
fi

# Docker: only when missing, check --install-docker. Right after install the current shell may not have the docker group yet -> later docker commands fall back to sudo automatically.
if ! command -v docker >/dev/null; then
  $INSTALL_DOCKER || die "$(t "docker is required (or add --install-docker to install the latest version)." "需要 docker(或加 --install-docker 自動裝最新版)。")"
  if $DRY_RUN; then NEED_DOCKER=true; else
  step "$(t "Install Docker (latest, get.docker.com)" "安裝 Docker(最新版,get.docker.com)")"
  SUDO=""; [ "$(id -u)" = 0 ] || SUDO="sudo"
  curl -fsSL https://get.docker.com | $SUDO sh
  $SUDO systemctl enable --now docker 2>/dev/null || true
  [ "$(id -u)" = 0 ] || $SUDO usermod -aG docker "$USER" 2>/dev/null || true
  ok "$(t "Docker installed" "Docker 已安裝")"
  fi
fi
DOCKER="docker"
if ! $NEED_DOCKER; then docker info >/dev/null 2>&1 || DOCKER="sudo docker"; fi  # not in the group/needs privileges -> sudo

# TLS mode derivation: acme (public automatic) | http (plain HTTP) | https (manual certificate) | no arguments (restart, reuse the existing TLS_MODE in server.env).
# All three are terminated by the Go server itself (backend/internal/webapp); the CLI maps to TLS_MODE=acme/http/manual.
MODE=""
if $ACME; then
  MODE="acme"
  [ -n "$DOMAIN" ] || die "$(t "--acme needs --domain (a domain that resolves in public DNS)" "--acme 需要 --domain(公網可解析的網域)")" 2
  [ -n "$EMAIL" ]  || die "$(t "--acme needs --email (Let's Encrypt account and expiry notices)" "--acme 需要 --email(Let's Encrypt 帳號 / 到期通知)")" 2
  [ -n "$HOST" ] || HOST="$DOMAIN"
elif $HTTP; then
  MODE="http"
  [ -n "$HOST" ] || die "$(t "plain HTTP mode needs --host (reachable from browsers; use --agent-host if agents must use another address)" "純 HTTP 模式請給 --host(瀏覽器要連得到;agent 另有防火牆限制時用 --agent-host 分開指定)")" 2
elif [ -n "$DOMAIN" ] || [ -n "$CERT" ] || [ -n "$KEY" ]; then
  MODE="https"
  [ -n "$DOMAIN" ] || die "$(t "HTTPS mode needs --domain (this site's public domain)" "HTTPS 模式請給 --domain(本站對外網域)")" 2
  [ -n "$CERT" ] && [ -n "$KEY" ] || die "$(t "own-certificate mode needs --cert and --key (bought, from certbot, or signed by your internal CA; for automatic renewal on the internet use --acme)" "HTTPS 手動憑證模式請給 --cert 與 --key(公網 certbot/購買,或公司內部 CA 簽;公網想自動續期改 --acme)")" 2
  [ -f "$CERT" ] || die "$(t "certificate not found: $CERT" "找不到憑證: $CERT")" 2
  [ -f "$KEY" ]  || die "$(t "private key not found: $KEY" "找不到私鑰: $KEY")" 2
  [ -n "$HOST" ] || HOST="$DOMAIN"
elif [ -f server.env ] && grep -q '^TLS_MODE=' server.env; then
  log "$(t 'Keeping the existing mode (restart, no TLS options needed):' '沿用既有部署模式(重啟,免帶 TLS 參數):') $(grep '^TLS_MODE=' server.env | cut -d= -f2-)"
else
  die "$(t "the first deployment needs a TLS mode: --acme (+ --email), --domain + --cert + --key, or --http (+ --host)" "首次部署請指定 TLS 模式:--acme(+--email)、--domain+--cert+--key、或 --http(+--host)")" 2
fi

# The TLS_MODE variables are not secrets and may be overwritten whenever a TLS flag is passed (unlike CA_MASTER_KEY/SD_TOKEN, which are generated only the first time).
TLS_MODE_VAL="$MODE"; [ "$MODE" = https ] && TLS_MODE_VAL="manual"
ACME_STAGING_VAL="false"; $STAGING && ACME_STAGING_VAL="true"
# agent join (PLATFORM_ADDR) defaults to --host; only split internal deployments (e.g. a cloud VM
# where the browser uses the external IP but the agent firewall is limited to the internal network) need a separate --agent-host.
[ -n "$AGENT_HOST" ] || AGENT_HOST="$HOST"

step "$(t "Preflight checks" "前置檢查")"
# Published ports: the same variables docker-compose.yml reads, from the environment or .env.
port_of() { local v="${!1:-}"; [ -n "$v" ] || v="$(sed -n "s/^$1=//p" .env 2>/dev/null | tail -n1)"; printf '%s' "${v:-$2}"; }
PORTS="$(port_of WEB_PORT 80) $(port_of WEB_TLS_PORT 443) $(port_of AGENT_PORT 8443)"
if ! $NEED_DOCKER && [ -n "$($DOCKER compose ps --status running -q server 2>/dev/null)" ]; then
  log "$(t "The platform is already running; its own ports ($PORTS) are not checked" "平台已在執行;它自己用的埠($PORTS)不檢查")"
elif command -v ss >/dev/null; then
  # ss sees every listening socket without root; only the owning program needs root (-p), so ask for it
  # but do not rely on it.
  busy=""
  for p in $PORTS; do
    held="$(ss -Hltnp "sport = :$p" 2>/dev/null || true)"
    [ -z "$held" ] || busy="${busy}${busy:+$'\n'}  :$p  ${held//$'\n'/ | }"
  done
  [ -z "$busy" ] || die "$(t "these ports are already in use; stop what holds them, or move the platform with WEB_PORT / WEB_TLS_PORT / AGENT_PORT in .env (run as root to see which program holds each):" "這些埠已被佔用;停掉佔用的程式，或在 .env 用 WEB_PORT / WEB_TLS_PORT / AGENT_PORT 改埠(用 root 執行才看得到是哪個程式):")"$'\n'"$busy"
  ok "$(t "Ports $PORTS are free" "埠 $PORTS 都沒被佔用")"
else
  warn "$(t "ss (iproute2) is not installed; ports $PORTS are not checked" "沒有 ss(iproute2),不檢查埠 $PORTS")"
fi
# ACME needs public DNS pointing at this host. No address means the certificate request will fail; an address
# that is not on this host is only a warning, since a NAT or port forward in front is common.
if [ "$MODE" = acme ] && command -v getent >/dev/null; then
  ips="$( { getent ahosts "$DOMAIN" 2>/dev/null || true; } | awk '{print $1}' | sort -u | tr '\n' ' ')"
  ips="${ips% }"
  [ -n "$ips" ] || die "$(t "--domain $DOMAIN does not resolve; ACME needs a public DNS record pointing at this host" "--domain $DOMAIN 解析不到;ACME 需要公網 DNS 指到這台主機")" 2
  local_ips=" $(hostname -I 2>/dev/null || true) "
  match=false; for ip in $ips; do [[ "$local_ips" == *" $ip "* ]] && match=true; done
  if $match; then ok "$(t "$DOMAIN resolves to this host ($ips)" "$DOMAIN 解析到這台主機($ips)")"
  else warn "$(t "$DOMAIN resolves to $ips, which is not an address of this host; fine behind NAT or a port forward, otherwise fix DNS before the certificate request fails" "$DOMAIN 解析到 $ips,不是這台主機的位址;前面有 NAT 或轉埠就沒關係，否則先修 DNS,不然憑證會申請失敗")"; fi
fi

if $DRY_RUN; then
  step "$(t "Dry run: what a real run would change" "試跑:實際執行會做的變更")"
  ! $NEED_DOCKER || log "$(t "Install the latest docker from get.docker.com" "從 get.docker.com 安裝最新版 docker")"
  log "$(t "Edition ${EDITION}: build the image ${SERVER_IMAGE} from this checkout" "版本 ${EDITION}:從這份原始碼建 ${SERVER_IMAGE} 映像")"
  log "$(t "Version ${VERSION}${RELEASE_DATE:+ (released $RELEASE_DATE)}, written to .env" "版號 ${VERSION}${RELEASE_DATE:+(發布於 $RELEASE_DATE)},寫入 .env")"
  grep -qE '^POSTGRES_PASSWORD=.' .env 2>/dev/null || log "$(t "Generate a random database password into .env (chmod 600)" "產生隨機的資料庫密碼寫進 .env(chmod 600)")"
  if [ ! -f server.env ]; then
    org_shown="$ORG_ID"; ! $ORG_GENERATED || org_shown="bsos-node-manager-<random>"
    log "$(t "Create server.env (chmod 600): ORG_ID=${org_shown}, PLATFORM_ADDR=${AGENT_HOST}:8443, TLS_MODE=${TLS_MODE_VAL}, new CA_MASTER_KEY / SD_TOKEN / admin password" "建立 server.env(chmod 600):ORG_ID=${org_shown}、PLATFORM_ADDR=${AGENT_HOST}:8443、TLS_MODE=${TLS_MODE_VAL},新產 CA_MASTER_KEY / SD_TOKEN / admin 密碼")"
  elif [ -n "$MODE" ]; then
    log "$(t "Keep server.env and its keys; set TLS_MODE=${TLS_MODE_VAL} and the matching site settings" "保留 server.env 與其中的金鑰;改寫 TLS_MODE=${TLS_MODE_VAL} 與對應的站台設定")"
  else
    log "$(t "Keep server.env unchanged" "server.env 不變")"
  fi
  [ "$MODE" != https ] || log "$(t "Copy $CERT and $KEY to certs/server.crt and certs/server.key (owner uid 10001, needs sudo)" "把 $CERT 與 $KEY 複製成 certs/server.crt、certs/server.key(擁有者 uid 10001,需要 sudo)")"
  { [ -f server.env ] && grep -qE '^TZ=' server.env; } || log "$(t "Add the host time zone to server.env as TZ" "把主機時區寫進 server.env 的 TZ")"
  if $AGENT_STALE; then log "$(t "Build the agent ${VERSION} (x86_64 and aarch64) into agent-bin/" "建 agent ${VERSION}(x86_64 與 aarch64)到 agent-bin/")"
  else log "$(t "Reuse the agent ${VERSION} in agent-bin/" "沿用 agent-bin/ 裡的 agent ${VERSION}")"; fi
  $OFFLINE_ROOT && log "$(t "Create the CA with the root private key written to root-ca.key (only if the database has no CA yet)" "建立 CA,root 私鑰寫到 root-ca.key(只在 DB 還沒有 CA 時)")"
  [ -z "$CA_IMPORT" ] || log "$(t "Import the CA from $CA_IMPORT (only if the database has no CA yet)" "從 $CA_IMPORT 匯入 CA(只在 DB 還沒有 CA 時)")"
  log "$(t "docker compose up -d --build: start or replace the containers, publishing ports $PORTS" "docker compose up -d --build:啟動或替換容器，對外開 $PORTS")"
  ok "$(t "Dry run finished: nothing was changed." "試跑完成：沒有改動任何東西。")"
  exit 0
fi

if [ "$MODE" = https ]; then
  step "$(t "Own certificate" "手動憑證")"
  mkdir -p certs
  install -m644 "$CERT" certs/server.crt
  # The server container runs as uid 10001, so the key must belong to it; changing the owner needs root.
  SUDO=""; [ "$(id -u)" = 0 ] || SUDO="sudo"
  $SUDO install -m600 -o 10001 "$KEY" certs/server.key
  log "$(t "Installed the certificate → certs/server.crt (644) / server.key (600, owner uid 10001)" "裝上憑證 → certs/server.crt(644)/ server.key(600,擁有者 uid 10001)")"
fi


# Append KEY=VALUE to server.env. A hand-edited file may lack a final newline; add one first so the new line
# does not run onto the last one.
append_env() {
  if [ -s server.env ] && [ -n "$(tail -c1 server.env)" ]; then printf '\n' >> server.env; fi
  printf '%s=%s\n' "$1" "$2" >> server.env
}
step "server.env"
# Public HTTP base (used by nodes to download the installer and fetch the log settings and RPC public key): scheme from the TLS mode, host from DOMAIN/HOST.
API_SCHEME="https"; [ "$MODE" = http ] && API_SCHEME="http"
API_BASE="${API_SCHEME}://${DOMAIN:-$HOST}"
if [ ! -f server.env ]; then
  CA_KEY="$(openssl rand -hex 32)"
  SD_TOK="$(openssl rand -hex 24)"
  # The server prints a generated admin password only once, in a log that a later up.sh run discards with the container; keep it here instead.
  # No 'a' in the alphabet, so it can never contain the username "admin" (the password rules reject that).
  ADMIN_PW="$(openssl rand -base64 48 | tr -dc 'B-HJ-NP-Zb-km-z2-9' | cut -c1-16)"
  sed -e "s|^ORG_ID=.*|ORG_ID=${ORG_ID}|" \
      -e "s|^CA_MASTER_KEY=.*|CA_MASTER_KEY=${CA_KEY}|" \
      -e "s|^PLATFORM_ADDR=.*|PLATFORM_ADDR=${AGENT_HOST}:8443|" \
      -e "s|^PUBLIC_API_BASE=.*|PUBLIC_API_BASE=${API_BASE}|" \
      -e "s|^# BOOTSTRAP_ADMIN_PASSWORD=.*|BOOTSTRAP_ADMIN_PASSWORD=${ADMIN_PW}|" \
      -e "s|^SD_TOKEN=.*|SD_TOKEN=${SD_TOK}|" \
      -e "s|^TLS_MODE=.*|TLS_MODE=${TLS_MODE_VAL}|" \
      -e "s|^SITE_DOMAIN=.*|SITE_DOMAIN=${DOMAIN}|" \
      -e "s|^ACME_EMAIL=.*|ACME_EMAIL=${EMAIL}|" \
      -e "s|^ACME_STAGING=.*|ACME_STAGING=${ACME_STAGING_VAL}|" \
      -e "s|platform-a.example|${DOMAIN:-$HOST}|g" \
      server.env.example > server.env
  chmod 600 server.env
  log "$(t "Generated server.env (CA_MASTER_KEY / SD_TOKEN / BOOTSTRAP_ADMIN_PASSWORD created, chmod 600)" "生成 server.env(CA_MASTER_KEY / SD_TOKEN / BOOTSTRAP_ADMIN_PASSWORD 已自動產,chmod 600)")"
else
  log "$(t "server.env exists; not overwritten (keeping CA_MASTER_KEY / SD_TOKEN)" "已存在,不覆蓋(保留既有 CA_MASTER_KEY / SD_TOKEN)")"
  # TLS variables can be overwritten every time (updated only when the matching flag is given; on a no-argument restart MODE is empty and the existing values are kept).
  if [ -n "$MODE" ]; then
    sed -i.bak \
      -e "s|^TLS_MODE=.*|TLS_MODE=${TLS_MODE_VAL}|" \
      -e "s|^SITE_DOMAIN=.*|SITE_DOMAIN=${DOMAIN}|" \
      -e "s|^ACME_EMAIL=.*|ACME_EMAIL=${EMAIL}|" \
      -e "s|^ACME_STAGING=.*|ACME_STAGING=${ACME_STAGING_VAL}|" \
      -e "s|^PUBLIC_API_BASE=.*|PUBLIC_API_BASE=${API_BASE}|" \
      server.env && rm -f server.env.bak
    log "$(t "TLS_MODE=${TLS_MODE_VAL} written to server.env" "TLS_MODE=${TLS_MODE_VAL} 已寫入 server.env")"
  fi
fi

# Time zone for alert notifications: the server image has no /etc/localtime, so without TZ the platform's "host zone" would always be UTC.
# Pass the host's IANA name in; an existing TZ line (set by hand) is left alone.
if ! grep -qE '^TZ=' server.env; then
  HOST_TZ="$(timedatectl show -p Timezone --value 2>/dev/null || true)"
  if [ -z "$HOST_TZ" ]; then
    HOST_TZ="$(readlink /etc/localtime 2>/dev/null | sed -n 's|.*zoneinfo/||p' || true)"
  fi
  if [ -z "$HOST_TZ" ] && [ -r /etc/timezone ]; then
    HOST_TZ="$(head -n1 /etc/timezone | tr -d '[:space:]')"
  fi
  if [ -n "$HOST_TZ" ]; then
    append_env TZ "$HOST_TZ"
    log "$(t "TZ=${HOST_TZ} written to server.env (alert notifications default to the host time zone)" "TZ=${HOST_TZ} 已寫入 server.env(告警通知預設用主機時區)")"
  else
    warn "$(t "Could not detect the host time zone; alert notifications default to UTC. To set one, add an IANA name such as TZ=Asia/Taipei to server.env" "偵測不到主機時區,告警通知預設用 UTC;要指定請在 server.env 加 TZ=Asia/Taipei 之類的 IANA 名稱")"
  fi
fi

step "$(t "Check agent-bin" "agent-bin 檢查")"
# Node installs download the agent from here, so it is built (x86_64 and aarch64) when missing or from another version;
# a build that already matches is left alone, so reruns stay fast.
if $AGENT_STALE; then
  log "$(t "Building the agent ${VERSION} (x86_64 and aarch64) into agent-bin/" "建 agent ${VERSION}(x86_64 與 aarch64)到 agent-bin/")"
  ZH_ARG=""; [ "$ZH" = 1 ] && ZH_ARG="--zh"
  "$HERE/../../agent/build-agent.sh" --version "$VERSION" $ZH_ARG
fi
log "OK: $(ls agent-bin/besu-agent-linux-* | tr '\n' ' ')"

step "docker compose up"
# Write the edition and version (determined at the top) into .env, which compose reads automatically: a later manual docker compose up --build uses the same edition instead of silently reverting to Core.
# .env also holds the database password, generated once and never rewritten: postgres applies it only when its data volume is first
# created, so a new value would no longer match the existing database. Hex keeps it safe inside DATABASE_URL.
touch .env
(umask 077; grep -vE '^(EDITION|SERVER_IMAGE|BUILD_VERSION|BUILD_RELEASE_DATE)=' .env > .env.tmp) || true
if ! grep -qE '^POSTGRES_PASSWORD=.' .env.tmp; then
  printf 'POSTGRES_PASSWORD=%s\n' "$(openssl rand -hex 24)" >> .env.tmp
  log "$(t "Database password generated and written to .env" "資料庫密碼已產生並寫入 .env")"
fi
printf 'EDITION=%s\nSERVER_IMAGE=%s\nBUILD_VERSION=%s\nBUILD_RELEASE_DATE=%s\n' "$EDITION" "$SERVER_IMAGE" "$VERSION" "$RELEASE_DATE" >> .env.tmp && chmod 600 .env.tmp && mv .env.tmp .env
log "$(t "Edition: ${EDITION} (image ${SERVER_IMAGE}), version ${VERSION}, written to .env" "版本:${EDITION}(映像 ${SERVER_IMAGE}),版號 ${VERSION},已寫入 .env")"
# On first start server self-signs a CA and stores the root private key in the DB; for an offline root or an imported enterprise PKI, the CA must be created before that.
if $OFFLINE_ROOT || [ -n "$CA_IMPORT" ]; then
  $DOCKER compose build server
  # Store in a variable before comparing: piping straight into grep -q under pipefail lets cactl's SIGPIPE invert the check
  ca_state="$($DOCKER compose run --rm --entrypoint cactl server show)" || die "$(t "cactl show failed (see the messages above)" "cactl show 失敗(看上面的訊息)")"
  if [[ "$ca_state" != *"no CA material yet"* ]]; then
    warn "$(t "The database already has a CA; skipping --offline-root / --ca-import (they only apply on the first deployment; current state: docker compose exec server cactl show)" "DB 已有 CA,略過 --offline-root / --ca-import(只在首次部署有作用;目前狀態:docker compose exec server cactl show)")"
  elif $OFFLINE_ROOT; then
    [ -e root-ca.key ] && die "$(t "root-ca.key already exists; stopping so an existing root private key is not overwritten" "root-ca.key 已存在,為免覆蓋既有 root 私鑰而中止")"
    $DOCKER compose run --rm --user "$(id -u):$(id -g)" -v "$HERE:/out" --entrypoint cactl server \
      init-offline --root-key-out /out/root-ca.key || die "$(t "cactl init-offline failed (see the messages above)" "cactl init-offline 失敗(看上面的訊息)")"
    warn "$(t "The root private key is in $HERE/root-ca.key: move it to offline storage now and delete it from this host. It is needed only to replace the intermediate." "root 私鑰在 $HERE/root-ca.key:立刻移到離線保管處並從這台刪掉,只有換 intermediate 時才用得到。")"
  else
    CA_IMPORT="$(cd "$CA_IMPORT" && pwd)" || die "$(t "--ca-import directory not found" "找不到 --ca-import 目錄")"
    $DOCKER compose run --rm -v "$CA_IMPORT:/in:ro" --entrypoint cactl server \
      import --root-cert /in/root.pem --inter-cert /in/inter.pem --inter-key /in/inter.key \
      || die "$(t "cactl import failed (see the messages above)" "cactl import 失敗(看上面的訊息)")"
    warn "$(t "Import done; delete $CA_IMPORT/inter.key (it is now stored encrypted in the database)." "匯入完成後請刪掉 $CA_IMPORT/inter.key(已加密存進 DB)。")"
  fi
fi
$DOCKER compose up -d --build --remove-orphans

echo
ok "$(t "Up. The initial password for admin is in server.env: grep BOOTSTRAP_ADMIN_PASSWORD server.env. The first sign-in requires a password change; do it soon." "起來了。預設管理員 admin 的初始密碼在 server.env:grep BOOTSTRAP_ADMIN_PASSWORD server.env —— 首次登入會強制改密碼,請盡快完成。")"
case "$MODE" in
  acme)  log "$(t "Site: https://${DOMAIN} (the server requests the Let's Encrypt certificate on the first connection, which may take a few seconds)" "站台:https://${DOMAIN}(server 首次向 Let's Encrypt 申請憑證,第一次連線可能要等幾秒)")"
         $STAGING && warn "$(t "Staging mode: the certificate is signed by the LE staging CA and browsers will not trust it. Once the flow works, run again without --staging." "staging 模式:憑證由 LE staging CA 簽,瀏覽器會跳不信任——驗完流程改不帶 --staging 重跑。")"
         log "$(t "If it hangs: docker compose logs -f server (check that --domain resolves to this host and 443 is reachable from outside)." "若卡住:docker compose logs -f server(確認 --domain 的 DNS 已指到本機、443 對外可達)。")" ;;
  https) log "$(t "Site: https://${DOMAIN}" "站台:https://${DOMAIN}")" ;;
  http)  log "$(t "Site: http://${HOST} (plain HTTP, testing only)" "站台:http://${HOST}(純 HTTP 測試)")" ;;
  *)     log "$(t "Site: existing mode (TLS_MODE in server.env)" "站台:沿用既有部署模式(server.env 的 TLS_MODE)")" ;;
esac
log "$(t "Next: sign in as admin, change the password, create an operator account, then add nodes under Node registration & settings." "下一步:用 admin 登入改密碼、建維運人員帳號,再到「節點登錄與設定」接入節點(見 README)。")"
