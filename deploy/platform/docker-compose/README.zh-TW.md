[English](README.md) | 繁體中文

# 用 Docker Compose 部署平台

一個 `docker compose up` 就會起整套：**Postgres、平台 server、Prometheus、Alertmanager**。映像從原始碼建，目標主機只要有 Docker，不用裝 Go 或 Node。畫面和 TLS 都由 server 自己處理（`backend/internal/webapp`），沒有 nginx。

## 檔案

| 檔案 | 用途 |
|---|---|
| `up.sh` | 一次完成部署：建立 `server.env`（產生 `CA_MASTER_KEY`），依模式寫入 TLS 設定、放好憑證，在 `.env` 產生資料庫密碼，再跑 `docker compose up -d --build` |
| `docker-compose.yml` | 四個服務：postgres、server、prometheus、alertmanager |
| `server.env.example` | server 容器 `server.env` 的範本（`TLS_MODE`、`SITE_DOMAIN`、`ACME_EMAIL` 等） |
| `certs/` | 自備憑證模式的 `server.crt` / `server.key`，公網 CA 或公司內部 CA 簽的都可以。`up.sh --cert/--key` 會放進來，bind-mount 進 server 容器，不進版控 |
| `acme-cache` volume | 自動 HTTPS 的憑證快取，重啟和續期後都還在。不要用 `docker compose down -v` 清掉，否則會重新申請 |
| `agent-bin/` | 放 `besu-agent-linux-*`，掛進 server 容器，讓節點安裝時下載 |
| `prometheus.yml` | Prometheus 的抓取設定。用 HTTP 服務探索加 bearer token，抓的是平台的 `/agent-proxy/{nodeID}/metrics`，不直接連節點。掛進 prometheus 容器 |
| `rules.yml` | Prometheus recording rules，把 Besu 原生 metrics 換算成 `bsos_node_*`。掛進 prometheus 容器。告警規則不在這裡 |
| `rules-defaults.gen.yml` | 預設告警規則的渲染副本，只給 `promtool test rules rules_test.yml` 用，不掛進容器 |
| `prom-rules-config` volume | 全部的告警規則（預設的加上使用者新增的，都存在平台資料庫）。平台把它們渲染成 `custom-rules.yml` 寫進這個 volume。`prometheus.yml` 用 glob 讀 `rules.d/*.yml`，所以 volume 剛建立還是空的時，Prometheus 也能正常啟動 |
| `alertmanager.yml` | Alertmanager 設定的**參考範本**（基本版，只有 platform receiver）。Compose 部署**不**掛這個檔案。平台啟動時會依「告警通知」頁的設定，渲染出等價的設定（有設 Email、Webhook 就加上去），寫進 `alertmanager-config` volume；改設定就重寫並觸發重新載入。這個檔案留著是為了在本機用 `promtool` / `amtool` 檢查語法，也給自己管 Alertmanager 的人參考 |
| `SD_TOKEN` | 服務探索用的 bearer token，只存在 `server.env`（由 `up.sh` 產生）。server 啟動時把它寫進 `sd-token` volume 的 `sd_token`（權限 0640、gid 65534），Prometheus 讀 `/sd-token/sd_token` |
| `POSTGRES_PASSWORD` | 資料庫密碼，存在同目錄的 `.env`（權限 600）。`up.sh` 第一次執行時隨機產生，之後不覆蓋。沒有設定時，所有 `docker compose` 指令（包括 `logs`、`down`）都會報錯；其他帳號執行 compose 指令要用 `sudo`。postgres 只接在 `db` 內部網路，只有 server 連得到 |

映像由 `backend/Dockerfile` 以多階段建置：先建前端 `frontend/dist`，用 `go:embed` 嵌進 server 執行檔，再建 Go。build context 是 repo 根目錄，因為要同時讀到 `backend/` 和 `frontend/`。

## 開始前

- 目標主機：Docker 和 Compose 外掛。沒有的話，`up.sh --install-docker` 會用 Docker 官方腳本裝**最新版**。預設不會裝任何東西。
- 防火牆：開 **80/443** 給畫面用（80 只轉址到 443），開 **8443** 給 agent 接入。**8544** 只在跨單位唯讀時才開，需要商業版和 `CROSS_ORG_ENABLED`，而且只對要看你們的單位開。Postgres 和 9090 不對外。
  - **不需要對節點開任何東西。**Prometheus 抓的是平台的 `/agent-proxy/{nodeID}/metrics`，平台再透過 agent 撥出的 8443 連線轉給節點。平台不會主動連節點。
- agent 執行檔放在 `agent-bin/`，在 Docker 裡建，不用裝 Go。`agent-bin/` 是空的話，`up.sh` 會自己建 amd64 版。有 arm64 節點就另外跑：

```bash
../../agent/build-agent.sh --arch both   # amd64 + arm64
```

## 部署（選一種 TLS 模式）

TLS 由 server 自己處理（`backend/internal/webapp`）。

| 模式 | 參數 | 適合 |
|---|---|---|
| **自動** | `--acme --email <信箱>` | 公網網域，憑證自動申請與續期（`autocert`，TLS-ALPN-01，只需要 443 對外可達） |
| **自備憑證** | `--cert <檔案> --key <檔案>` | 公網取得的憑證，或**公司內部 CA 簽**的憑證 |
| **純 HTTP** | `--http`（加 `--host`） | 內網或專線上測試 |

```bash
cd deploy/platform/docker-compose

# A) 自動 HTTPS：autocert 向 Let's Encrypt 申請並續期（TLS-ALPN-01，只需要 443）
#    --domain 要在公網 DNS 解析到這台主機。想先試流程、避開 rate limit，就加 --staging。
./up.sh --org-id org-a.example --domain platform-a.example --acme --email ops@org-a.example

# B) 自備憑證的 HTTPS：certbot 或購買的，或公司內部 CA 簽的
./up.sh --org-id org-a.example --domain platform-a.example --cert ./fullchain.pem --key ./privkey.pem

# C) 純 HTTP 快速測試（內網或專線本身可信時）
./up.sh --org-id org-a.example --host <VM 對外 IP> --http

# C') 畫面和 agent 用不同位址（例如畫面走公網 IP，agent 接入限制在內網）
#     --agent-host 指定 PLATFORM_ADDR 用的內網位址；不給就用 --host
./up.sh --org-id org-a.example --host <VM 對外 IP> --agent-host <VM 內網 IP> --http
```

怎麼選：

- **公網網域**：用 `--acme`。自動續期，不用排 certbot cron，也不用多起代理容器。
- **純內網或公司內部 CA**：autocert 用不了，因為 Let's Encrypt 要連得到主機。改用 `--cert/--key`，請公司 CA 簽 `server.crt`（SAN 要包含站台網域）並準備私鑰。到期時照公司的流程換檔，server 發現 mtime 變了，幾秒內就會重新讀取，不用重啟。
- **autocert 的憑證存在哪**：`acme-cache` volume。到期前自動續，同一個程式裡直接重新載入。**不要用 `down -v` 清掉這個 volume**，否則會重新申請，可能撞到 rate limit。

`up.sh` 做四件事：

1. 建立 `server.env`，產生 `CA_MASTER_KEY`。已經有的**不會覆蓋**。
2. 放好憑證（自備憑證模式），寫入 `TLS_MODE`、`SITE_DOMAIN`、`ACME_EMAIL` 等設定。這些不是密鑰，每次帶 TLS 參數都會覆寫。
3. `agent-bin/` 是空的就建 amd64 的 agent。
4. `.env` 還沒有資料庫密碼時產生一組（`POSTGRES_PASSWORD`，已經有的**不會覆蓋**），再執行 `docker compose up -d --build`。

訊息預設是英文，加 `--zh` 改用中文。要用已存的設定重啟，直接跑 `./up.sh`，不用帶參數。

第二個單位的平台，在它自己的主機上同樣跑一次，例如改成 `--org-id org-b.example --domain platform-b.example`。

**`--org-id`（單位 id）**：建立 CA 時會寫進每一張憑證，**建好後不能改**；要改只能重裝，所有節點也要重新接入。只能用小寫英數字和 `. - _`，開頭和結尾必須是英數字，建議用公司網域（例如 `example.com`）。商業版第一次部署一定要填，聯盟裡每個單位都不能重複。開源版沒有單位的概念，不填就自動產生 `bsos-node-manager-<6 碼>`。如果之後打算從開源版升級到商業版加入聯盟，第一次部署就先填好 `--org-id`，它會成為你在聯盟裡的單位 id。

## master key 不放在環境變數（選用）

`CA_MASTER_KEY` 寫在 `server.env` 時，`docker inspect` 看得到明文。改用 Docker secret 掛檔案：

```bash
grep '^CA_MASTER_KEY=' server.env | cut -d= -f2 | cut -d' ' -f1 > ca_master_key
sudo chown 10001 ca_master_key && chmod 400 ca_master_key   # server 容器以 uid 10001 執行，要讀得到
sed -i '/^CA_MASTER_KEY=/d' server.env                       # 兩個都設的話 server 會拒絕啟動
```

再建立 `docker-compose.override.yml`。Compose 會自動疊上去，不用改 `docker-compose.yml`。要開跨單位時，8544 的 `ports` 也寫在同一個檔案（見「跨單位」）。

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

用 `docker compose up -d` 套用。`ca_master_key` 和 `server.env` 一樣，不要進版控，也不要跟資料庫放在同一份備份裡。

## 第一次登入

平台第一次啟動時會建立 `admin`，密碼是 `up.sh` 第一次執行時產生的：

```bash
grep BOOTSTRAP_ADMIN_PASSWORD server.env
```

第一次登入會強制改密碼，請在平台開放給其他人之前完成。要自己指定密碼，見 [`../README.zh-TW.md`](../README.zh-TW.md) 的「第一個帳號」。

admin 只管帳號。登入後先在「帳號權限管理」建一個「維運人員」帳號，登錄節點和監控都用它。

## 監控（Prometheus）

`up.sh` 起的服務裡已經有 `prometheus`，**一開始就會收集真實的時序資料**。server 沒設 `PROMETHEUS_URL` 時，圖表 API 回 503，不會畫假資料。

- **自動探索**：Prometheus 帶 `SD_TOKEN` 從 `server:8080/sd/targets` 取得本單位的節點清單。節點接入或移除會自動跟上，不用改設定。
- **抓取路徑**：Prometheus 不連節點，一律抓平台的 `/agent-proxy/{nodeID}/metrics`，由平台透過 agent 撥出的 8443 連線轉給節點。抓取和探索共用同一個 `SD_TOKEN`，不需要另外的抓取憑證。
- **保留期間**：Prometheus 保留 15 天（`docker-compose.yml` 的 `--storage.tsdb.retention.time`）。圖表只顯示 24 小時，多留的資料用來查問題。

Prometheus 只連平台，在同一個 Docker 網路裡，有 bearer token 保護。節點上沒有任何能被冒充的監聽埠，所以在封閉網路裡用 ARP 或 DNS 欺騙注入假資料的手法行不通。平台把抓取請求轉給 agent 之前，會拿掉 `Authorization` 標頭：那個 token 也能存取 `/sd/targets` 和 `/alerts/webhook`，不該交給每一台節點。

確認方式：`docker compose logs -f prometheus`，或開 `http://<VM>:9090`（預設不對外，要暫時發布埠或用 SSH 轉埠），看 Targets 是不是 `UP`。

## 跨單位

跨單位唯讀用存取金鑰。被看的一方在「跨單位存取設定 → 存取金鑰」發一把金鑰，8544 位址填對方連得到的那個，再把金鑰交給對方 admin。對方貼到他們平台的「跨單位存取設定 → 外部單位」，就會在「我的節點」看到你的節點。8544 要對對方開放。

8544 預設是關的：server 不聽、Compose 不發布，發金鑰也會被拒絕。要打開，兩個地方都要設：

```bash
echo 'CROSS_ORG_ENABLED=true' >> server.env
```

```yaml
# docker-compose.override.yml（如果已經有 CA master key 的 secret，加在同一個 server 底下）
services:
  server:
    ports: ["${CROSS_ORG_PORT:-8544}:8544"]
```

用 `docker compose up -d` 套用。只設一邊不會通：沒設環境變數，server 不聽；沒加 `ports`，外面連不到。啟動日誌會印 `cross-org listener on …` 或 `cross-org listener disabled`。

## 接入節點

Besu 由各單位自己架，平台不裝。節點起來之後，在「節點登錄與設定」產生接入碼，照畫面上的指令在節點主機跑 `install.sh`（見 [`../../agent/README.zh-TW.md`](../../agent/README.zh-TW.md)）。

## 常用指令

```bash
docker compose ps                 # 狀態
docker compose logs -f server     # server 日誌
docker compose up -d --build      # 改程式後重建（up.sh 跑過一次之後才能用；版本沿用 up.sh 寫進 .env 的 EDITION）
docker compose down               # 停止，保留 volume（pgdata、acme-cache 等）；certs/ 是主機資料夾，不受影響
docker compose down -v            # 停止並刪除所有資料，見下方
```

第一次啟動、或清過 volume 之後，一律用 `./up.sh`：它會先建好 `server.env` 再啟動。

## 常見錯誤

1. 只建了 amd64，卻有 arm64 節點：那些節點下載 agent 時會拿到 404。跑 `build-agent.sh --arch both`。
2. 純 HTTP（`--http`）卻手動設了 `COOKIE_SECURE=true`：登入一直回 401。把那行拿掉，cookie 會自己跟著 TLS 模式。
3. 換了 `CA_MASTER_KEY`，或兩個單位共用一把：CA 會解不開。`up.sh` 不會覆蓋已存在的 `server.env`，不要自己手動換。
   `.env` 的 `POSTGRES_PASSWORD` 也一樣：postgres 只在資料 volume 第一次建立時套用密碼，換掉之後 server 就連不上資料庫。
4. `docker compose down -v` 會把 CA、資料庫和 ACME 憑證快取一起刪掉，等於重置整個單位的身分：server 會用同一把 `CA_MASTER_KEY` 產生新的 root 和 intermediate，所有已接入的節點都要重新接入，ACME 模式也會重新申請憑證。之後要再跑一次 `./up.sh --org-id … --domain/--host …`。

## 排錯

| Prometheus 日誌裡的錯誤 | 原因 | 處理 |
|---|---|---|
| `Get ".../sd/targets": ... 404 Not Found` | server 端的 `SD_TOKEN` 是空的（token 對不上會是 401，不是 404） | 在 `server.env` 設好 `SD_TOKEN`（例如 `openssl rand -hex 24`），再跑 `docker compose up -d server prometheus` |
| `/sd-token/sd_token: permission denied` | server 寫出的檔案是 0600，Prometheus（`nobody`，gid 65534）讀不到 | `docker-compose.yml` 有給 server 設 `SD_TOKEN_GID=65534`，讓它寫成 0640 並設好群組。確認這個設定還在，再重建：`docker compose up -d --build server`，啟動時會重寫檔案 |

**`up.sh` 顯示 `missing --org-id`**：如果 `server.env` 已經存在，不帶參數直接跑就能重啟，單位 id 和站台位址會沿用。只有第一次建立 `server.env` 時才需要 `--org-id`，以及 `--domain` 或 `--host`。
