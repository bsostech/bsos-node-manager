[English](README.md) | 繁體中文

# agent 部署

節點 agent 跑在 Besu 旁邊。它對平台開一條只通到本機 loopback 的反向隧道，並回報主機指標，本身不認識 Besu。平台透過它呼叫本機 Besu 的 JSON-RPC 和 metrics，用哪些埠是平台上節點設定的欄位。

**安裝腳本只裝 agent。**Besu 由各單位用自己的方式裝（套件、Docker、Ansible 都可以），平台不裝、不設定，也不重啟 Besu。Besu 要開哪些東西，看「節點登錄與設定 → 登錄節點」（`/node-add`）的「調整 Besu 設定」那一步：沒開 RPC，節點會在線但看不到鏈的狀態；沒開 metrics，就沒有時序圖表。

## 檔案

| 檔案 | 用途 |
|---|---|
| `build-agent.sh` | **在平台主機跑**：用 Docker 建 agent 執行檔，放進 Compose 的 `agent-bin/`。安裝和升級都從那裡下載 |
| `docker-compose.example.yml` / `agent.env.example` / `besu-agent.service` | 不用安裝腳本、手動安裝時的範本 |

安裝與升級腳本在 `backend/internal/server/assets/`，由平台以 `GET /install.sh`、`GET /upgrade-agent.sh` 提供，這裡不放副本。

## 前置條件

- Linux、systemd、root 權限。docker 模式另外需要 Docker 和 Compose v2 外掛。
- 這台主機上的 Besu 已經在跑，或之後會跑。agent 可以先接上，Besu 起來之前平台會顯示「RPC 未接上」。
- 已在平台「節點登錄與設定 → 登錄節點」建好接入碼。建立時填節點 id、RPC 埠和 metrics 埠。
- 防火牆：agent 只往平台的 8443 撥出，**不需要開任何入站埠**。另外會開一個只收本機連線的日誌入口（預設 `127.0.0.1:5170`）。

### SELinux（RHEL 等 enforcing 的主機）

已在 RHEL 10.2（SELinux enforcing）測過兩種 Docker 設定：

- **Docker 預設（沒開 SELinux 支援）**：容器不受 SELinux 限制，不用處理。
- **Docker 開了 SELinux 支援**（`/etc/docker/daemon.json` 有 `"selinux-enabled": true`）：容器跑在 `container_t`，讀不到主機上一般標籤的檔案。
  - docker 模式的 agent：`install.sh` 寫出的 compose 檔已經帶 `security_opt: ["label=disable"]`，跟 systemd 模式一樣以不受限的方式執行。agent 要讀主機的 `/`、`/proc` 和 `/sys`，不能用 `:z` 把主機根目錄重新貼標籤。手動安裝的話，`docker-compose.example.yml` 也有同一行。
  - 平台：`deploy/platform/docker-compose/docker-compose.yml` 的主機掛載（`agent-bin`、`certs`、`prometheus.yml`、`rules.yml`）帶 `,z`，Docker 會把它們標成容器可讀；沒開 SELinux 支援時 Docker 會忽略這個選項。手動換了 `certs/` 裡的憑證後，重跑 `up.sh`（或 `docker compose up -d --force-recreate`），讓新檔案也重新貼標籤。
- **systemd 模式**：agent 執行檔的標籤是 `bin_t`，以不受限的方式執行，不用處理。

改了 Docker 的 SELinux 設定後，既有的容器要重建（`docker compose up -d --force-recreate`）才會套上新標籤。

## 安裝

平台的「登錄節點」會組好指令，長這樣：

```bash
curl -fsSL https://<platform>/install.sh -o install.sh
sudo bash install.sh --platform <platform>:8443 --token <接入碼> \
  [--runtime systemd|docker] [--platform-http https://<platform>] \
  [--allow-ports 8545,9545] [--disk-path /] [--state-dir /var/lib/besu-agent] [--re-enroll] [--log-sink <IP:埠>]
```

| 參數 | 說明 |
|---|---|
| `--platform` | 平台的 mTLS 位址，接入和之後的連線都用它；只寫主機時預設 `:8443` |
| `--token` | 接入碼；節點 id 和埠都綁在接入碼上 |
| `--runtime` | `systemd`（預設）或 `docker` |
| `--platform-http` | 下載 agent 執行檔的 HTTPS 位址，預設 `https://<--platform 的主機>` |
| `--allow-ports` | 選填。平台透過 agent 只能連到這幾個埠；不設就是 loopback 全開。值不合法（不在 1–65535 的埠，或只有逗號）時 agent 會拒絕啟動 |
| `--disk-path` | 選填。量磁碟使用率的路徑（只用在 systemd 模式） |
| `--state-dir` | 選填。憑證目錄，預設 `/var/lib/besu-agent` |
| `--local` | 用離線安裝包：agent 從腳本所在的目錄取，用 `SHA256SUMS` 校驗，不向平台下載；也會裝上 `bsos-upgrade` |
| `--re-enroll` | 節點已經在平台上，但憑證失效了（節點頁的「重新接入」會產生這條指令） |
| `--log-sink` | 選填。agent 收 Besu 日誌的 IP:埠，預設 `127.0.0.1:5170`。Besu 跑在 bridge 網路的容器裡時，改成 docker0 的 IP |
| `--zh` | 訊息用中文印出 |

平台給的指令會先用平台提供的 SHA-256 校驗 `install.sh`，通過才執行。

腳本會做這些事：下載 agent 並比對 SHA-256，用 `besu-agent join` 換到憑證，然後啟動服務。

- **systemd**：建立 `besu-agent` 系統帳號，unit 只能寫自己的憑證目錄（`ProtectSystem=strict`、`NoNewPrivileges`）。
- **docker**：用 `FROM scratch` 的 Dockerfile 在本機把同一支執行檔包成映像，不連任何 registry，離線也能建。compose 檔在 `/etc/besu-agent/docker-compose.yml`。容器用 host 網路才連得到本機 Besu 的 `127.0.0.1`，並以唯讀方式掛主機的 `/proc`、`/sys` 和根目錄，讓指標是主機的，不是容器的。

跑完後節點在平台上顯示在線。解除安裝：`sudo /usr/local/sbin/bsos-uninstall`。它只移除 agent，憑證目錄會保留；加 `--purge` 一起刪掉。

### 離線安裝包

節點主機連不到平台的 HTTPS，或安裝前要先審查檔案時，在登錄頁選「離線安裝包」。下載 `besu-agent-<版本>-linux.tar.gz`（裡面有 install.sh、upgrade-agent.sh、x86_64 和 ARM64 的 agent、`SHA256SUMS`），自己傳到節點主機，照頁面上的指令校驗整包、解壓，再執行 `sudo bash install.sh --local --platform <host:8443> --token <接入碼>`。裝好之後 agent 仍然要連得到平台的 8443。

## 日誌（選用）

Besu 用平台產生的 log4j2 設定把日誌送給 agent，節點頁的「日誌」分頁就能看到最近的日誌。步驟在登錄頁和日誌分頁上都有：下載設定檔，Besu 加上 `LOG4J_CONFIGURATION_FILE=<設定檔>`，重啟 Besu。Besu 跑在 bridge 網路的容器裡時，agent 加 `--log-sink <docker0 的 IP>:5170`，設定檔也送到同一個位址。不設定就沒有日誌功能，其他功能不受影響。

## 升級

節點的「Agent 與接入」分頁上，「升級 Agent」卡片會給指令：

```bash
curl -fsSL https://<platform>/upgrade-agent.sh -o upgrade-agent.sh
sudo bash upgrade-agent.sh
```

平台位址和憑證目錄會從 `/etc/besu-agent` 的 agent 設定讀取，不用帶參數。

它用 agent 自己的憑證走 mTLS 下載新版，比對 SHA-256，先執行一次新檔確認版本，再換檔。模式會自動判斷：systemd 重啟服務；docker 重建映像和容器。之後觀察 20 秒，新版中途停掉或被自動重啟，就換回舊版。結束碼 4 表示已經換回舊版、節點照常運作；5 表示換不回去，要人工處理。

- 舊檔留在 `/usr/local/bin/besu-agent.prev`。docker 模式在升級成功後會刪掉舊映像，之後要手動退回就得用 `.prev` 重建。
- systemd 235 以前（例如 RHEL 7）讀不到重啟次數，只剩「有沒有在跑」這項檢查。
- 只連 8443，而且用的是 agent 的憑證，所以 agent 沒連上時也能跑。
- 平台升級後如果要求較新的 agent 協定，舊 agent 會被拒絕連線（節點顯示「Agent 版本過舊」），每台主機都要跑一次這支腳本。
- 平台上放的版本比節點舊也照樣換。平台的 `agent-bin/` 放的就是這個平台要用的版本，所以平台退版時 agent 也跟著退。
- 用離線安裝包裝的節點已經有 `bsos-upgrade`（同一支腳本，平台位址和憑證目錄從 agent 設定讀取），直接執行 `sudo /usr/local/sbin/bsos-upgrade`。

## 手動安裝

```bash
install -m755 besu-agent-linux-amd64 /usr/local/bin/besu-agent
useradd --system --home /var/lib/besu-agent --shell /usr/sbin/nologin besu-agent
install -d -m 0700 /var/lib/besu-agent /etc/besu-agent
besu-agent join --platform <host:port> --token <t> --state-dir /var/lib/besu-agent
chown -R besu-agent:besu-agent /var/lib/besu-agent
cp agent.env.example /etc/besu-agent/agent.env    # 改成實際的值
cp besu-agent.service /etc/systemd/system/ && systemctl enable --now besu-agent
```

docker 的手動安裝照 [`docker-compose.example.yml`](docker-compose.example.yml)。

## 環境變數

| 變數 | 預設 | 說明 |
|---|---|---|
| `PLATFORM_ADDR` | `localhost:8443` | 平台的 mTLS 位址 |
| `AGENT_STATE_DIR` | `state` | 憑證目錄 |
| `ALLOW_PORTS` | 空 | 選填。平台透過 agent 能連的埠；空的就是 loopback 全開。值不合法（不在 1–65535 的埠，或只有逗號）時 agent 會拒絕啟動 |
| `DISK_PATH` | `/` | 選填。量磁碟使用率的路徑；在容器裡指向 `/rootfs` |
| `LOG_SINK` | `127.0.0.1:5170` | 選填。收 Besu 日誌的位址。Besu 跑在 bridge 網路的容器裡時，改成 docker0 的 IP |
| `LOG_SPOOL_MAX` | `512MB` | 選填。日誌暫存的上限（`AGENT_STATE_DIR/logspool/`），商業版平台來拉過日誌才會寫入。滿了就丟掉最舊的區塊 |

## 協定相容

agent 和平台之間用 yamux，新舊版本的協定兩個方向都不相容，所以升級平台時也要用 `upgrade-agent.sh` 升級 agent。連線速度至少要約 90 kbit/s，再慢的話 session 會被自己的 keepalive 關掉。
