[English](README.md) | 繁體中文

# 部署

部署 BSOS Node Manager 需要的檔案都在這裡，分兩邊：

- **平台**：每個單位一套。管理平台 server、Postgres、Prometheus、Alertmanager，用 Docker Compose 跑。
- **agent**：每個 Besu 節點一個。裝在 Besu 旁邊的小服務，負責讓平台連到這個節點。

```
deploy/
├── platform/
│   ├── README.md                 從這裡開始
│   └── docker-compose/           Compose 部署；映像從原始碼建
│       └── up.sh  docker-compose.yml  server.env.example  certs/  agent-bin/
└── agent/
    ├── README.md                 在節點主機裝 agent
    ├── build-agent.sh            在平台主機建 agent 執行檔
    └── docker-compose.example.yml  agent.env.example  besu-agent.service   手動安裝的範本
```

平台映像由 `backend/Dockerfile` 建置：先建 `frontend/`，再嵌進 server 執行檔（`backend/internal/webapp`），畫面和 API 由同一個程式提供。

安裝與升級腳本只有一份，在 `backend/internal/server/assets/`，平台以 `GET /install.sh`、`GET /upgrade-agent.sh` 提供下載。

## 順序

1. **平台**：每個單位部署一套，見 [`platform/README.zh-TW.md`](platform/README.zh-TW.md)。
2. **Besu**：各單位自己架 Besu、自己準備 genesis。平台不裝 Besu，也不產 genesis。
3. **agent**：每個 Besu 節點主機裝 agent，見 [`agent/README.zh-TW.md`](agent/README.zh-TW.md)。裝好後節點在平台上顯示在線。
4. **節點互連**：節點怎麼連到其他單位由各單位自己決定，可以用 `static-nodes.json`、discovery 加 bootnodes，或等對方連進來。平台不會自動讓本單位的節點互連，也不碰 `static-nodes.json` 和 bootnodes，只推送你在「節點互連設定」填的清單：
   - **共用清單**：按「加入 peer」貼上 enode，再對要用的節點打開「套用共用清單」（每台各一個開關，預設關）。適合不想改 Besu 設定、重啟就要連某個 peer 的情況，例如自己的 archive 節點要連別家、對方只收 inbound，或排查時臨時連一台。平台每一輪用 `admin_addPeer` 整份重送，Besu 重啟後一輪內就接回；從清單拿掉的，下一輪用 `admin_removePeer` 移除。送過哪些記在資料庫（`nodes.pushed_peers`），所以平台停機期間刪掉的 peer，重啟後一樣會移除。該不該連某個 peer 由你判斷，平台不檢查。長期固定的連線還是寫進節點的 `static-nodes.json` 比較好。
   - **個別節點**：節點分在不同網路、或用 sentry 架構時，在「節點互連設定」選這台，在「自行加入的 peer」列它要連的節點，例如本單位其他節點的內網位址。不要套用共用清單就讓開關保持關閉。
5. **跨單位唯讀（商業版）**：被看的一方在「跨單位存取設定 → 存取金鑰」發一把金鑰，8544 位址填對方連得到的那個，把金鑰交給對方 admin。對方貼到他們平台的「跨單位存取設定 → 外部單位」，就會在「我的節點」看到你的節點。8544 預設不開：要設 `CROSS_ORG_ENABLED`，並在 override 檔發布 8544（見 [`platform/docker-compose/README.zh-TW.md`](platform/docker-compose/README.zh-TW.md) 的「跨單位」），再對對方開放。

程式本身在 `backend/`（server、`cmd/cactl` 等）和 `agent/`，這個資料夾只放部署用的腳本、unit 和範本。
