---
lang: en
title: Add a Besu node
eyebrow: Runbook
lede: |
  On the host running Besu, in order: turn on Besu RPC and metrics, install the agent, then confirm the join on the platform.
facts: |
  Node | {{NODE_ID}} {{NODE_NAME}}
  Platform | {{PLATFORM_URL}}
  Generated | {{GENERATED_AT}}
toc: On this page
copy: Copy
copied: Copied
footer: Add a Besu node · {{NODE_ID}}
---

## Overview

> This node and the Besu settings the platform needs

- Node id: `{{NODE_ID}}`, cannot change after joining
- Platform: `{{PLATFORM_URL}}`
- Besu local ports: RPC `{{RPC_PORT}}`, metrics `{{METRICS_PORT}}`

**Before you start:** Besu is already installed on this host, started with this chain's genesis, and connected to other nodes on the chain (peer count above 0). The platform joins a node that is already running; it does not install Besu or bring it onto the chain. To make this node a validator, propose it under "Validator admission votes" after it joins.

The platform does not install, configure or restart Besu. The agent only dials out to the platform on 8443; the platform reaches RPC and metrics on this host's 127.0.0.1 through the agent. What Besu needs:

| Item | Level | Used for |
| --- | --- | --- |
| RPC | Required | Chain state, node identity and operations |
| metrics | Required | Charts and chain alerts |
| RPC auth | Optional | Accept only platform-issued tokens |
| Account allowlist | Optional | Restrict which accounts can send transactions |
| Logs | Optional | View node logs on the platform |

RPC namespaces (the full config below lists all of them; without an optional one the platform still works, minus that feature):

| Namespace | Level | Used for |
| --- | --- | --- |
| ETH NET | Required | Height, sync, peer count, node identity |
| IBFT or QBFT | Required (one, matching the consensus) | Validator set and voting |
| WEB3 | Optional | Besu version display |
| ADMIN | Optional | Peer push, log level, reading the epoch length |
| TXPOOL | Optional | Transaction pool tab |
| PERM | Optional | Account allowlist |

## Prepare Besu

> Three formats, same content; use one

### Full config

:::tabs
config.toml:

```
{{CONFIG_TOML}}
```

CLI flags:

```
{{CONFIG_CLI}}
```

Environment:

```
{{CONFIG_ENV}}
```
:::

The snippets do not set `rpc-http-host`. Besu listens on 127.0.0.1 by default, which is all the agent needs; when Besu runs in a container, map the RPC and metrics ports to the host's 127.0.0.1.

### Platform public key (RPC auth)

RPC auth verifies tokens with this platform key. Download it to the path in the config:

```
sudo mkdir -p /var/lib/besu
sudo curl -fsSL {{PUBKEY_URL}} \
  -o /var/lib/besu/platform-rpc.pub
```

Any path works as long as the config points to it.

### Account allowlist file (optional)

The allowlist file must exist and be writable by the user running Besu (the platform edits the allowlist through the PERM API):

```
echo 'accounts-allowlist=[]' | sudo tee /var/lib/besu/accounts-allowlist.toml
sudo chown besu:besu /var/lib/besu/accounts-allowlist.toml
```

Replace `besu:besu` with the account that runs Besu. Without an account allowlist, remove the `permissions-accounts-config-file-enabled` and `permissions-accounts-config-file` lines from the config.

### Logs (optional)

Besu keeps printing logs to stdout and also sends a copy to the agent on the same machine. No files, no directory permissions.

Download the logging config (target address defaults to `127.0.0.1:5170`):

```
sudo mkdir -p /etc/besu && sudo curl -fsSL "{{PLATFORM_URL}}/api/v1/platform/besu-log4j2.xml?sink=127.0.0.1%3A5170" -o /etc/besu/log4j2.xml
```

1. Add one line when starting Besu, pointing at this config: for systemd, `Environment=LOG4J_CONFIGURATION_FILE=/etc/besu/log4j2.xml`; for a container, `-e LOG4J_CONFIGURATION_FILE=/etc/besu/log4j2.xml -v /etc/besu/log4j2.xml:/etc/besu/log4j2.xml:ro`.
2. For a container on bridge network, send to the docker0 IP instead (`ip addr show docker0`) and add `--log-sink <same address>` when installing the agent.

### Restart Besu

Restart Besu after changing the config and make sure it comes up before moving on.

## Install agent

> Run on the host running Besu

```
{{INSTALL_CMD}}
```

:::note
**The join code is not in this document.** `<join-code>` in the command above is a placeholder. Copy the full command from step ③ of "Register node" on the platform and run that. The join code is shown only once and stops working after it expires (15 minutes, 1 hour or 24 hours).
:::

- Needs root; systemd mode needs systemd, Docker mode needs docker and compose v2.
- Firewall: the host must reach the platform on 8443 outbound. The agent opens no listening ports to the outside.
- The script downloads the agent, checks the SHA-256 the platform gives, gets a certificate and starts the service.

## Check afterwards

> After running the install command, confirm on the platform

### What to look for on the platform

1. Open "Node registration & settings" on the platform. Until the agent connects, the row reads "Waiting to join" (Join code · expires time).
2. Once the agent connects, the row becomes node `{{NODE_ID}}` and the status column shows its current state.
3. Open node settings and check "Capabilities": the node is usable when Agent, RPC and metrics all show "OK"; ADMIN, TXPOOL, the account allowlist and logs are optional and show "Off" when not enabled, which is not an issue.

### Common problems

| Symptom | Cause | Fix |
| --- | --- | --- |
| Row stays at "Waiting to join" | Host cannot reach the platform on 8443 | Allow outbound to the platform on 8443 |
| | Wrong advertised address | Make sure `--platform` in the command is an address the agent can reach; override it behind NAT or VPN |
| | Join code expired | Generate a new one under "Node registration & settings" → "Register node" and run the new command |
| RPC rejected (401) | The node's JWT public key is not this platform's key, or the node clock is far off | Download the platform key again, fix the clock, restart Besu |
| Cannot reach 127.0.0.1:{{RPC_PORT}} | Besu is not running, `rpc-http-enabled` is off, RPC does not listen on loopback, or the port differs | Check Besu is running, the config is applied and the port matches the platform |
| RPC missing namespace | `rpc-http-api` lacks a required namespace | Include at least `ETH`, `NET` and `IBFT` or `QBFT` and restart Besu; add `WEB3`, `ADMIN`, `TXPOOL`, `PERM` for the optional features you use |
| Metrics not scraped | `metrics-enabled` is off, not listening on loopback, or the port is not {{METRICS_PORT}} | Turn on metrics, check the port, restart Besu |
| Logs "Off" (optional) | `LOG4J_CONFIGURATION_FILE` not set | Follow "Prepare Besu → Logs" if you want logs; otherwise skip |
| Logs "Disconnected" (optional) | Logs were connected and stopped: Besu restarted without `LOG4J_CONFIGURATION_FILE`, the address differs from where the agent listens, or a firewall blocks it | Redo "Prepare Besu → Logs" |
