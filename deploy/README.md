English | [繁體中文](README.zh-TW.md)

# Deployment

Everything needed to deploy BSOS Node Manager. There are two sides:

- **Platform**: one per organization. The management server, Postgres, Prometheus and Alertmanager, run with Docker Compose.
- **Agent**: one per Besu node. A small service next to Besu that connects the node to the platform.

```
deploy/
├── platform/
│   ├── README.md                 start here
│   └── docker-compose/           the Compose stack; images are built from source
│       └── up.sh  docker-compose.yml  server.env.example  certs/  agent-bin/
└── agent/
    ├── README.md                 installing the agent on node hosts
    ├── build-agent.sh            builds the agent binary on the platform host
    └── docker-compose.example.yml  agent.env.example  besu-agent.service   templates for a manual install
```

The platform image is built by `backend/Dockerfile`. It builds `frontend/` first and embeds it in the server binary (`backend/internal/webapp`), so one process serves both the UI and the API.

The install and upgrade scripts exist in one place only, `backend/internal/server/assets/`. The platform serves them at `GET /install.sh` and `GET /upgrade-agent.sh`.

## Order

1. **Platform.** Deploy one per organization: [`platform/README.md`](platform/README.md).
2. **Besu.** Each organization sets up its own Besu nodes and genesis. The platform does not install Besu or create a genesis.
3. **Agent.** Install the agent on each Besu node host: [`agent/README.md`](agent/README.md). Once installed, the node shows as online on the platform.
4. **Peering.** How nodes reach other organizations is up to each organization: `static-nodes.json`, discovery with bootnodes, or waiting for the other side to connect. The platform does not connect your nodes to each other on its own and does not touch `static-nodes.json` or bootnodes. It only pushes the lists you enter under "Node peering settings":
   - **Shared list.** Add an enode with "Add peer", then turn on "Use shared list" for each node that should use it (off by default). Useful when you want a node to reach a peer without editing Besu's config and restarting, for example an archive node that must reach another organization, a peer that only accepts inbound connections, or a temporary connection while troubleshooting. Each round the platform sends the whole list with `admin_addPeer`, so a restarted Besu gets its peers back within one round. Entries removed from the list are dropped with `admin_removePeer`. What was pushed is stored in the database (`nodes.pushed_peers`), so peers deleted while the platform was down are still removed after it restarts. The platform does not judge whether a peer should be connected; that is your call. For long-lived connections, `static-nodes.json` is still the better place.
   - **Per-node peers.** For a node that needs a different set (nodes on separate networks, a sentry setup), select it under "Node peering settings" and list its peers under "Peers added here", for example the internal addresses of your other nodes. Leave "Use shared list" off if it should not use the shared list.
5. **Cross-org read-only access (Enterprise).** The organization being viewed issues an access key under "Cross-org access settings → Access keys", entering an 8544 address the other side can reach, and hands the key to the other organization's admin. They paste it under "Cross-org access settings → External orgs" on their platform, and your nodes appear in their "My nodes". Port 8544 is closed by default: set `CROSS_ORG_ENABLED` and publish 8544 in an override file (see "Cross-org" in [`platform/docker-compose/README.md`](platform/docker-compose/README.md)), then open it to the other side.

The code itself lives in `backend/` (server, `cmd/cactl` and others) and `agent/`. This folder only holds deployment scripts, units and templates.
