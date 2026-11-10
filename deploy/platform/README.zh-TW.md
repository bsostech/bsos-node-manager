[English](README.md) | 繁體中文

# 平台部署

每個單位跑**一套**平台：server（同時提供畫面）加 Postgres。開源版一個單位一套；商業版的跨單位唯讀需要兩個以上的單位，各自一套，用存取金鑰互相查看。

## 執行方式

平台**只以容器部署**。Docker Compose 一次起整套（postgres、server、prometheus、alertmanager），見 [`docker-compose/README.zh-TW.md`](docker-compose/README.zh-TW.md)。

server 映像以 UBI 9 為基底（Red Hat 可免費散布的映像，套件與 RHEL 9 相同），並開啟 cgo，以後可以直接接 HSM。主機用哪個發行版都可以，glibc 跟著映像走。畫面和 TLS 都由 server 自己處理（`backend/internal/webapp`），不需要 nginx。

## 部署前

**建置內容**：`backend/Dockerfile` 以多階段建置，從 `backend/` 和 `frontend/` 建出 server 映像，前端 `dist/` 和 `install.sh` 用 `go:embed` 嵌進去，主機不用裝 Node 或 Go。agent 執行檔**不在映像裡**，`up.sh` 第一次執行時會建好，也可以自己跑 `deploy/agent/build-agent.sh`（在 Docker 裡編 amd64 與 arm64）。產出放在 `docker-compose/agent-bin/`，以唯讀方式掛進容器。沒建的話，節點安裝時下載 agent 會拿到 404。

**版本（開源版／商業版）**：`up.sh` 發現 `backend/enterprise/` 就建商業版（映像 `bsos-node-manager-enterprise`），沒有就建開源版（`bsos-node-manager`）；加 `--core` 強制建開源版。直接 `docker compose up --build` 預設建開源版，要建商業版就帶 `EDITION=enterprise SERVER_IMAGE=bsos-node-manager-enterprise`。

**網路**：平台和每個節點各在不同機器上。

| 來源 → 目的 | 埠 | 用途 |
|---|---|---|
| 瀏覽器 → 平台 | 443（測試可用 80；自動或自備憑證模式下 80 只做轉址） | 開啟畫面 |
| 節點 → 平台 | **8443/tcp** | agent 接入和之後的連線。由節點主動撥出，平台不會連節點 |
| 平台 ↔ 平台 | **8544/tcp** | 跨單位唯讀（存取金鑰）。只有商業版、而且設了 `CROSS_ORG_ENABLED` 才需要 |
| 節點 ↔ 節點 | **30303/tcp+udp** | Besu P2P，包含跨單位 |
| 節點主機本機 | 8545 / 9545 | Besu 的 RPC 和 metrics。綁 localhost 就好，agent 從 loopback 連 |

**同一個程式、同一個來源**：前端 `dist/` 嵌在 server 執行檔裡。畫面、`/api/*`、`/install.sh`、`/upgrade-agent.sh`、`/besu-agent-linux-*` 都在同一個 chi router 上，所以沒有 CORS 問題，cookie 單純，也不需要反向代理。

**TLS 三選一**（見 `backend/internal/webapp`）：

1. 公網網域自動 HTTPS（`--acme`）：用 `golang.org/x/crypto/acme/autocert`，走 TLS-ALPN-01，只需要 443 對外可達，憑證自動申請與續期。
2. 自備憑證，公網 CA 或公司內部 CA 簽的都可以（`--cert` / `--key`）：server 自己讀檔，檔案 mtime 變了就重新載入。
3. 純 HTTP，只用於測試（`--http`）。

autocert 只能用在公網，因為 Let's Encrypt 要連得到主機。純內網或用內部 CA 時，請用 `--cert` / `--key`。

**前面有負載平衡器或 nginx 時**：平台預設不信任 `X-Forwarded-For` 和 `X-Real-IP`（誰都能填），所以稽核和平台日誌記到的來源是轉送機的 IP。要記到真正的來源，在 `server.env` 設 `TRUSTED_PROXY_CIDR`，填轉送機的 IP 或網段，用逗號分隔。之後只有來自這些位址的連線才採信 `X-Forwarded-For`，並從右往左取第一個不在清單裡的位址。**只填轉送機自己的位址，不要填用戶端的網段。**例如用戶端也在 10.0.0.0/8，你卻填了 10.0.0.0/8，任何直連的用戶端都能自己填 `X-Forwarded-For` 偽造來源。格式寫錯時平台會拒絕啟動（日誌會指出是哪一段），不會當作沒設。轉送機要**覆寫**用戶端帶來的 `X-Forwarded-For`，不是附加在後面，至少要保證最右邊那段是它自己填的。平台的存取日誌仍然記連線位址。

**來源 IP 白名單（接入碼、存取金鑰）看的是連線位址**：8443 和 8544 由平台自己終結 TLS，前面不能放 L7 轉送，也不看 `X-Forwarded-For`。rootful Docker 和 Podman 的埠映射會保留真實來源 IP。**rootless Podman 用 slirp4netns 時（RHEL 8、Podman 5 以前的預設），所有入站來源都會變成 `10.0.2.100`**，填了白名單就等於全擋，這種主機請留空。Podman 5 的 pasta 會保留遠端來源。前面有 NAT 或 L4 負載平衡器時，平台看到的是那台設備的位址，白名單要填它。

**登入 cookie 跟著 TLS 模式**：HTTPS 模式加 `Secure`，`--http` 不加。只有 `--http` 前面另有 TLS 終端時才需要設 `COOKIE_SECURE`。

**`CA_MASTER_KEY` 每個單位一把，設了就不要換**：`up.sh` 會自動產生，而且不會覆蓋已經存在的。

**第一個帳號**：平台第一次啟動時建立 `admin`，密碼是 `server.env` 的 `BOOTSTRAP_ADMIN_PASSWORD`，`up.sh` 第一次執行時會填一組隨機值（`grep BOOTSTRAP_ADMIN_PASSWORD server.env`）。要自己指定，就在第一次啟動前改掉，但要符合密碼規則，不符合平台就起不來。第一次登入會強制改密碼。

其他人由 admin 在「帳號權限管理」新增，臨時密碼一樣只顯示一次，對方第一次登入要改。如果 `server.env` 設了 `ACCOUNT_SMTP_*`（見 `server.env.example`），新增和重設密碼時可以改成寄信給對方。這組郵件伺服器只寄臨時密碼，和告警通知的郵件分開設定。它預設要求加密：465 埠直接 TLS，其他埠用 STARTTLS。可以先用 `openssl s_client -starttls smtp -connect 主機:587` 確認伺服器支援哪一種（465 埠拿掉 `-starttls smtp`）。內網不加密的轉發器要明確設 `ACCOUNT_SMTP_SECURITY=none`。

admin 只管帳號，看不到監控，也不能登錄節點。所以第一件事是建一個「維運人員」帳號，之後登錄節點和日常監控都用它。

## CA：正式環境讓 root 離線

如果第一次啟動時資料庫裡沒有 CA，平台會自己簽一組 root 和 intermediate，**root 私鑰加密後存在資料庫**。這只適合開發和試用，server 啟動時會印警告。正式環境請在**第一次啟動前**選一種做法：

```bash
cd deploy/platform/docker-compose
# 產生新的 CA；root 私鑰寫成 root-ca.key，資料庫只留 root 憑證和 intermediate
./up.sh --org-id org-a.example --domain platform-a.example --cert ./fullchain.pem --key ./privkey.pem --offline-root
```

已經有企業 PKI 的話，改用 `--ca-import <目錄>`。目錄裡放 `root.pem`，以及它簽的 `inter.pem` 和 `inter.key`，root 私鑰完全不會進來。

`root-ca.key` 請馬上移到離線保管處，並從這台主機刪掉，之後只有換 intermediate 時才用得到。`docker compose exec server cactl show` 可以看目前狀態和到期日。

**換發 intermediate**：intermediate 有效期 5 年。到期前 90 天起，server 每天在日誌警告，`cactl show` 也會顯示。過期那天所有 agent 都要重新接入，所以要在警告期內換。root 和它的指紋不變，agent 不用重新接入，外部單位的存取金鑰也不用重發。

- root 在資料庫（自簽）：重啟 server 就會自動續簽。也可以先跑 `docker compose exec server cactl rotate-intermediate` 再重啟。
- root 離線：
  ```bash
  docker compose run --rm --user "$(id -u):$(id -g)" -v "$PWD:/out" --entrypoint cactl \
    server rotate-intermediate --csr-out /out/inter.csr
  # 把 inter.csr 交給 root 持有者，以 CA 身分簽發（basicConstraints CA:TRUE、pathlen:0、keyCertSign），拿回 inter.pem
  docker compose run --rm -v "$PWD:/in:ro" --entrypoint cactl \
    server rotate-intermediate --inter-cert /in/inter.pem
  docker compose restart server
  ```

這兩種都沿用同一把 intermediate 私鑰，agent 不受影響。只有**懷疑 intermediate 私鑰外洩**時才加 `--new-key` 換私鑰。舊私鑰簽的憑證會立刻失效，**所有節點都要用新的接入碼重新接入**。root 離線時兩步都要加 `--new-key`：第一步會另外寫出新私鑰（`--key-out`），第二步用 `--inter-key` 帶回，匯入後刪掉這個檔案。

## 順序

1. 每個單位部署一套平台。
2. 各單位自己架 Besu、準備 genesis，平台不裝 Besu、不產 genesis。每個節點裝 agent（見 [`../agent/README.zh-TW.md`](../agent/README.zh-TW.md)），確認平台上顯示在線。
3. 跨單位（商業版）：被看的一方在「跨單位存取設定 → 存取金鑰」發一把金鑰交給對方 admin，對方貼到「跨單位存取設定 → 外部單位」，就會在「我的節點」看到你的節點。8544 預設不開，見 [`docker-compose/README.zh-TW.md`](docker-compose/README.zh-TW.md) 的「跨單位」。
4. 用 `eth_blockNumber` 和 `net_peerCount` 確認有在出塊。
