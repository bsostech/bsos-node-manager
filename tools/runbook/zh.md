---
lang: zh-Hant
title: 新增 Besu 節點
eyebrow: 操作手冊
lede: |
  在執行 Besu 的主機上依序完成：開 Besu 的 RPC 與 metrics、安裝 Agent、到平台確認接入。
facts: |
  節點 | {{NODE_ID}} {{NODE_NAME}}
  平台 | {{PLATFORM_URL}}
  產生於 | {{GENERATED_AT}}
toc: 本頁內容
copy: 複製
copied: 已複製
footer: 新增 Besu 節點 · {{NODE_ID}}
---

## 概要

> 這個節點、平台需要 Besu 開什麼

- 節點 id：`{{NODE_ID}}`，接入後不能改
- 平台：`{{PLATFORM_URL}}`
- Besu 的本機埠：RPC `{{RPC_PORT}}`、metrics `{{METRICS_PORT}}`

**開始之前：** 這台主機上的 Besu 已經裝好，用本鏈的 genesis 啟動，並連得到鏈上其他節點（peer 數大於 0）。平台只接入已經在跑的節點，不負責安裝 Besu 或讓它加入鏈。要讓這台成為驗證者，接入後到「驗證者的准入投票」提議加入。

平台不裝、不設定、也不重啟 Besu。Agent 只撥出到平台 8443，平台經 Agent 打這台主機 127.0.0.1 上的 RPC 與 metrics。Besu 需要開的項目：

| 項目 | 層級 | 用途 |
| --- | --- | --- |
| RPC | 必要 | 鏈狀態、節點身分與各項操作 |
| metrics | 必要 | 監控圖表與鏈告警 |
| RPC 認證 | 選用 | 只接受平台簽發的 token |
| 交易地址授權 | 選用 | 限制哪些地址能送交易 |
| 日誌 | 選用 | 在平台查看節點日誌 |

RPC 的 namespace（下方完整設定全部列入；選用的沒開，平台照樣可用，只少那一項功能）：

| namespace | 層級 | 用途 |
| --- | --- | --- |
| ETH NET WEB3 | 必要 | 高度、同步、peer 數、節點身分 |
| IBFT QBFT | 必要 | 驗證者清單與投票 |
| ADMIN | 選用 | peer 推送、日誌等級、讀取 epoch 長度 |
| TXPOOL | 選用 | 交易池分頁 |
| PERM | 選用 | 交易地址授權 |

## 準備 Besu

> 三種格式內容相同，擇一使用

### 完整設定

:::tabs
config.toml：

```
{{CONFIG_TOML}}
```

啟動參數：

```
{{CONFIG_CLI}}
```

環境變數：

```
{{CONFIG_ENV}}
```
:::

片段不含 `rpc-http-host`。Besu 預設聽 127.0.0.1，Agent 打得到即可；Besu 跑在容器裡時，RPC 與 metrics 埠要對應到主機的 127.0.0.1。

### 平台公鑰（RPC 認證）

RPC 認證用平台這把公鑰驗 token，下載到設定裡指定的路徑：

```
sudo mkdir -p /var/lib/besu
sudo curl -fsSL {{PUBKEY_URL}} \
  -o /var/lib/besu/platform-rpc.pub
```

路徑可自訂，需與設定一致。

### 交易地址授權的白名單檔（選用）

設定指向的白名單檔要先存在，且 Besu 的執行帳號可寫入（平台經 PERM API 改白名單）：

```
echo 'accounts-allowlist=[]' | sudo tee /var/lib/besu/accounts-allowlist.toml
sudo chown besu:besu /var/lib/besu/accounts-allowlist.toml
```

`besu:besu` 換成實際執行 Besu 的帳號。不用交易地址授權時，從設定裡拿掉 `permissions-accounts-config-file-enabled` 與 `permissions-accounts-config-file` 兩行。

### 日誌（選用）

Besu 照舊把日誌印到螢幕，另外送一份給同一台機器上的 Agent。不寫檔，不用處理資料夾權限。

下載日誌設定檔（送往位址預設 `127.0.0.1:5170`）：

```
sudo mkdir -p /etc/besu && sudo curl -fsSL "{{PLATFORM_URL}}/api/v1/platform/besu-log4j2.xml?sink=127.0.0.1%3A5170" -o /etc/besu/log4j2.xml
```

1. Besu 啟動時多加一行，指向這份設定檔：systemd 用 `Environment=LOG4J_CONFIGURATION_FILE=/etc/besu/log4j2.xml`；容器用 `-e LOG4J_CONFIGURATION_FILE=/etc/besu/log4j2.xml -v /etc/besu/log4j2.xml:/etc/besu/log4j2.xml:ro`。
2. Besu 跑在 bridge 網路的容器時，送往位址改成 docker0 的 IP（`ip addr show docker0`），安裝 Agent 時也要加 `--log-sink <同一個位址>`。

### 重啟 Besu

改完設定後重啟 Besu，確認它正常起來再往下做。

## 安裝 Agent

> 在執行 Besu 的主機上執行

```
{{INSTALL_CMD}}
```

:::note
**接入碼不在這份文件裡。** 上面指令中的 `<接入碼>` 只是佔位。執行前需到平台「登錄節點」第 ③ 步複製完整指令。接入碼僅顯示一次，過了有效期（15 分鐘、1 小時或 24 小時）就不能用。
:::

- 需要 root；systemd 模式需要 systemd，Docker 模式需要 docker 與 compose v2。
- 防火牆：主機要能連出到平台 8443。Agent 不開任何對外監聽埠。
- 腳本下載 Agent 並比對平台給的 SHA-256，換發憑證後啟動服務。

## 事後檢查

> 執行完安裝指令，到平台確認

### 平台上看什麼

1. 開啟平台「節點登錄與設定」。Agent 連上之前，清單上這一列是「等待接入」（接入碼 · 到期時間）。
2. Agent 連上後，這一列變成節點 `{{NODE_ID}}`，狀態欄顯示它目前的狀態。
3. 進入節點設定，看「能力清單」：Agent、RPC、metrics 都「正常」才算可用；ADMIN、TXPOOL、交易地址授權、日誌是選用，沒開顯示「未啟用」，不算異常。

### 常見問題

| 現象 | 原因 | 處理 |
| --- | --- | --- |
| 清單一直是「等待接入」 | 主機連不出平台 8443 | 開放主機連出到平台 8443 |
| | 連入位址不對 | 確認指令裡 `--platform` 是 Agent 連得到的平台位址；NAT 或 VPN 環境要改寫 |
| | 接入碼過期 | 回「節點登錄與設定」→「登錄節點」重新產生，再執行新指令 |
| RPC 被拒（401） | 節點設的 JWT 公鑰不是平台這把，或節點時鐘差太多 | 重新下載平台公鑰、校正時鐘，重啟 Besu |
| RPC 連不上 127.0.0.1:{{RPC_PORT}} | Besu 沒在跑、`rpc-http-enabled` 沒開、RPC 沒聽在本機 loopback，或埠不同 | 確認 Besu 在跑、設定已套用、埠與平台設定相同 |
| RPC 缺 namespace | `rpc-http-api` 少了必要的項目 | 至少需有 `ETH,NET,WEB3,IBFT,QBFT`，重啟 Besu；要用的選用功能再加 `ADMIN`、`TXPOOL`、`PERM` |
| metrics 讀不到 | `metrics-enabled` 沒開、沒聽在本機 loopback，或埠不是 {{METRICS_PORT}} | 開啟 metrics、確認埠，重啟 Besu |
| 日誌「未啟用」（選用） | 沒設 `LOG4J_CONFIGURATION_FILE` | 要看日誌才照「準備 Besu → 日誌」設定；不需要可略過 |
| 日誌「已斷線」（選用） | 接過之後停了：Besu 重啟後沒帶 `LOG4J_CONFIGURATION_FILE`、送往位址與 Agent 收的不同，或被防火牆擋住 | 照「準備 Besu → 日誌」重設 |
