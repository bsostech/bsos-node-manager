-- Platform schema: node inventory, embedded CA, identity and permissions, alerting, etc. Kept idempotent with
-- IF NOT EXISTS / ON CONFLICT and applied directly at startup.

-- nodes holds only this org's nodes: the only writer is join (joinapi.go, org fixed to this org), so queries
-- do not filter by org. Cross-org nodes are never written here; otherwise every ListNodes caller would
-- have to tell sources apart.
CREATE TABLE IF NOT EXISTS nodes (
    id                 TEXT PRIMARY KEY,
    org                TEXT NOT NULL,
    validator_address  TEXT,                          -- validator address derived from the node key (the platform derives it from the enode in admin_nodeInfo)
    -- The enode reported by net_enode. Its address is Besu's --p2p-host, used only for identity (public key)
    -- and display; the platform never uses it as the address other nodes should connect to.
    reported_enode     TEXT,
    quarantined_at     TIMESTAMPTZ,                   -- set when a revoked cert keeps dialing in (not a network problem); cleared automatically once a valid cert is used
    -- Node-local RPC / metrics ports: filled into X-Target (127.0.0.1:<port>) when the platform relays
    -- through the agent tunnel; the agent itself does not know where Besu is.
    rpc_port           INTEGER NOT NULL DEFAULT 8545 CHECK (rpc_port BETWEEN 1 AND 65535),
    metrics_port       INTEGER NOT NULL DEFAULT 9545 CHECK (metrics_port BETWEEN 1 AND 65535),
    -- User-assigned display name (may be Chinese); NULL = the UI shows the id. Names are stored only here,
    -- not in peer_names.
    name               TEXT,
    -- Whether this node applies the shared peer list (manual_peers); default off = the platform does not push
    -- it. Peers this node added itself (node_peers) are pushed regardless of the switch
    peer_use_shared    BOOLEAN NOT NULL DEFAULT false,
    -- The enodes the platform sent to this node via admin_addPeer last round. Next round removes only those
    -- here that are no longer wanted; stored in the DB so the platform still remembers after a restart
    pushed_peers       TEXT[] NOT NULL DEFAULT '{}',
    -- Whether the account allowlist applies the shared list; when off, this node only takes the addresses it
    -- added itself (this node's row in permissioning_policy)
    allowlist_use_shared BOOLEAN NOT NULL DEFAULT true,
    -- Enrollment time; cleared to NULL once the first allowlist pass after enrollment is done (pushed, or
    -- nothing to push). Only allowlists read after this are trusted: after delete-and-re-enroll, the
    -- collector may still hold the old node's cache.
    allowlist_initial_since TIMESTAMPTZ DEFAULT now(),
    -- How the agent last said it runs. Kept after it goes offline, because a re-join is generated exactly
    -- when the agent cannot connect, and the command should default to the same runtime. NULL = never reported.
    agent_runtime      TEXT CHECK (agent_runtime IN ('systemd', 'docker'))
);

CREATE TABLE IF NOT EXISTS join_tokens (
    id          TEXT PRIMARY KEY,
    node_name   TEXT,           -- display name for the node after enrollment (optional); written to nodes.name when a new node joins
    allowed_sources TEXT[] NOT NULL DEFAULT '{}', -- source-IP allowlist (normalized CIDRs); empty = unrestricted, otherwise enforced at join / re-enroll time
    secret_hash TEXT NOT NULL,  -- sha256(full token); the platform never keeps the plaintext
    status      TEXT NOT NULL CHECK (status IN ('pending', 'used', 'revoked')), -- expired is computed from expires_at
    node_id     TEXT NOT NULL,  -- node id bound at creation (agent join sends only the token; the id comes from here)
    rpc_port     INTEGER NOT NULL DEFAULT 8545 CHECK (rpc_port BETWEEN 1 AND 65535),     -- written to nodes.rpc_port after enrollment
    metrics_port INTEGER NOT NULL DEFAULT 9545 CHECK (metrics_port BETWEEN 1 AND 65535), -- written to nodes.metrics_port after enrollment
    created_at  TIMESTAMPTZ NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL
);

-- Manual peers: nodes the user wants connected, one shared list per org, pushed to nodes with peer_use_shared
-- on (admin_addPeer). Keyed by public key: one entry per public key; the platform does not judge who the peer
-- is or whether to connect. May be one of this org's nodes, with the address given by the user.
CREATE TABLE IF NOT EXISTS manual_peers (
    pubkey     TEXT PRIMARY KEY,
    enode      TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Peers each node added itself: always pushed regardless of the switch; toggling the switch does not touch
-- them. When a node is deleted, DeleteNode removes its own list, plus any entries in other lists whose public
-- key belongs to it.
CREATE TABLE IF NOT EXISTS node_peers (
    node_id    TEXT NOT NULL,
    pubkey     TEXT NOT NULL,
    enode      TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (node_id, pubkey)
);

-- Peer / validator names: user-assigned, keyed by address. The peers page derives the address from the public
-- key, the validators page uses the address directly, so the same node has the same name on both. Names come
-- only from this org's users; no external list is consulted.
CREATE TABLE IF NOT EXISTS peer_names (
    address    TEXT PRIMARY KEY,               -- 0x + lowercase; derived from the public key for peers, used directly for validators
    name       TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Names for authorized transaction addresses: the platform's own address book, never written to nodes or on chain
CREATE TABLE IF NOT EXISTS account_names (
    address    TEXT PRIMARY KEY,               -- 0x + lowercase
    name       TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ── embedded CA ────────────────────────────
CREATE TABLE IF NOT EXISTS ca_certificates (
    kind       TEXT PRIMARY KEY CHECK (kind IN ('root', 'intermediate')),
    cert_pem   TEXT NOT NULL,
    key_enc    BYTEA NOT NULL, -- AES-256-GCM encrypted private key (derived from the master key), never plaintext
    created_at TIMESTAMPTZ NOT NULL,
    not_after  TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS issued_certificates (
    serial     TEXT PRIMARY KEY, -- revocation lookup key
    subject    TEXT NOT NULL,    -- cert Subject DN (O/OU/CN)
    node_id    TEXT,             -- node the agent cert belongs to; NULL for the platform's own
    not_before TIMESTAMPTZ NOT NULL,
    not_after  TIMESTAMPTZ NOT NULL,
    cert_pem   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS revoked_certificates (
    serial     TEXT PRIMARY KEY,
    reason     TEXT NOT NULL,
    revoked_by TEXT NOT NULL,
    revoked_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL -- cert NotAfter; can be purged afterwards (short lifetimes keep the list from growing)
);

-- ── identity / permissions (AuthN + RBAC) ─────────
-- Local accounts: bootstrap admin (Grafana-style, always present; password change forced on first login).
-- Could hold service accounts in the future.
CREATE TABLE IF NOT EXISTS local_users (
    username             TEXT PRIMARY KEY,
    password_hash        TEXT NOT NULL,        -- bcrypt
    role                 TEXT NOT NULL,        -- see auth.rolePerms
    is_bootstrap         BOOLEAN NOT NULL DEFAULT false,
    disabled             BOOLEAN NOT NULL DEFAULT false,
    must_change_password BOOLEAN NOT NULL DEFAULT false, -- must change on first login (initial password)
    created_at           TIMESTAMPTZ NOT NULL,
    last_login_at        TIMESTAMPTZ,
    email                TEXT                  -- optional; temporary password emails are sent here
);

-- Alert notification settings: a single row (id=1). The platform renders this table into alertmanager.yml in
-- the shared volume and triggers an Alertmanager hot reload (internal/alerting).
CREATE TABLE IF NOT EXISTS alert_settings (
    id           INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    smtp_config  JSONB,  -- non-secret fields (host/port/username/from/to)
    smtp_secret  BYTEA,  -- AES-256-GCM (SMTP password)
    webhook_urls JSONB,  -- [{label,url}, ...]
    telegram_config JSONB, -- non-secret fields (chatId/messageThreadId)
    telegram_secret BYTEA, -- AES-256-GCM(bot token)
    slack_secret    BYTEA, -- AES-256-GCM (Slack incoming webhook URL; the URL itself is the credential)
    notify_language TEXT NOT NULL DEFAULT 'en', -- zh | en: notification text and the default rules' summaries
    notify_timezone TEXT NOT NULL DEFAULT '',   -- IANA name for notification times; '' = the platform host's zone
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Account allowlist: the node_id='*' row is the shared list (created at init, never deleted); other rows are
-- addresses a node added itself (on top of the shared list). A row exists = this node has self-added
-- addresses; effective list = (nodes.allowlist_use_shared ? shared list : empty) union this row. '*' is not a
-- valid node id, so it cannot collide.
CREATE TABLE IF NOT EXISTS permissioning_policy (
    node_id            TEXT PRIMARY KEY,
    accounts_allowlist TEXT[] NOT NULL DEFAULT '{}',
    revision           BIGINT NOT NULL DEFAULT 0,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO permissioning_policy(node_id) VALUES('*') ON CONFLICT DO NOTHING;

-- Alert rules: both default rules and user-added rules live here. Structured columns, no raw PromQL (see the
-- allowlist rendering logic in internal/alerting/rules.go). The platform renders them into a Prometheus rule
-- group in the shared volume and triggers a Prometheus hot reload.
CREATE TABLE IF NOT EXISTS custom_alert_rules (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,              -- display name; goes only into annotations, not alert: (keeps the identifier stable)
    preset       TEXT UNIQUE,                -- default rule code (= alert name, e.g. NodeCpuHigh; see internal/alerting/defaults.go); NULL for user-added rules
    metric       TEXT NOT NULL,              -- MetricId enum value (validated against the allowlist); '' for default rules with fixed conditions
    node_id      TEXT,                       -- NULL = whole fleet (evaluated per node); otherwise a single node of this org
    operator     TEXT NOT NULL,              -- gt|lt|gte|lte (allowlist); '' for default rules with fixed conditions
    threshold    DOUBLE PRECISION NOT NULL,
    for_duration TEXT NOT NULL,              -- 0s|1m|2m|5m|10m|30m|1h (allowlist; no free-form input)
    severity     TEXT NOT NULL,              -- critical | warning
    enabled      BOOLEAN NOT NULL DEFAULT true,
    created_by   TEXT NOT NULL,              -- session username (audit)
    updated_by   TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    keep_firing_for TEXT NOT NULL DEFAULT '0s', -- how long after the condition clears before it counts as resolved (same allowlist as for_duration)
    clear_threshold DOUBLE PRECISION           -- resolve threshold for threshold rules; NULL = same as threshold
);

-- Login records: one row per login attempt (password and OIDC). Not audit; exists in the open-source edition
-- too, a failed write does not block login; kept 90 days.
CREATE TABLE IF NOT EXISTS login_events (
    id        BIGSERIAL PRIMARY KEY,
    at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    username  TEXT,                     -- NULL when the account does not exist: we do not record what the user typed in the account field
    result    TEXT NOT NULL CHECK (result IN ('success', 'failure')),
    reason    TEXT CHECK (reason IN ('bad_password', 'unknown_user', 'disabled', 'no_role')), -- only for failure
    source_ip TEXT NOT NULL DEFAULT ''  -- source IP, same as audit (reqinfo); empty string when unknown
);
CREATE INDEX IF NOT EXISTS login_events_at_idx ON login_events (at);

-- Validator vote records: the platform records on-chain votes in the background, starting from the epoch in
-- which it began watching.
-- open = vote still in progress, tracked only by the recorder itself (not returned by the API); when it ends
-- it becomes passed / expired and ended_* is filled in.
-- ballots: every validator eligible to vote at the time [{address, block, at}]; block / at are null for those
-- who did not vote.
CREATE TABLE IF NOT EXISTS validator_vote_history (
    id            BIGSERIAL PRIMARY KEY,
    address       TEXT NOT NULL,             -- 0x + lowercase
    add_validator BOOLEAN NOT NULL,          -- true = add, false = remove
    result        TEXT NOT NULL CHECK (result IN ('open', 'passed', 'expired')),
    first_block   BIGINT NOT NULL,
    first_at      TIMESTAMPTZ NOT NULL,
    ended_block   BIGINT,
    ended_at      TIMESTAMPTZ,
    votes         INT NOT NULL,
    needed        INT NOT NULL,
    ballots       JSONB NOT NULL,
    UNIQUE (address, add_validator, ended_block)
);
-- Only one in-progress row per address and direction at a time (UNIQUE does not block NULL ended_block)
CREATE UNIQUE INDEX IF NOT EXISTS validator_vote_history_open_idx ON validator_vote_history (address, add_validator) WHERE ended_block IS NULL;
CREATE INDEX IF NOT EXISTS validator_vote_history_ended_idx ON validator_vote_history (ended_at) WHERE ended_block IS NOT NULL;

-- Server-side sessions (deletable = forced logout). The cookie holds only an opaque token.
CREATE TABLE IF NOT EXISTS sessions (
    token                TEXT PRIMARY KEY,
    username             TEXT,                    -- local account; subject for IdP logins
    account              TEXT,                    -- account shown for SSO (preferred_username / email / LDAP login name); NULL for local accounts
    name                 TEXT NOT NULL,
    role                 TEXT NOT NULL,
    org                  TEXT NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL,
    expires_at           TIMESTAMPTZ NOT NULL,
    last_seen_at         TIMESTAMPTZ NOT NULL,    -- for idle timeout (updated at most once every 60 seconds)
    must_change_password BOOLEAN NOT NULL,        -- copied from the local account at login; the backend then allows only the password change
    -- Sessions from local-account logins. username shares the same column with IdP subjects, so managing
    -- local accounts must only touch local ones; otherwise changing local alice's role would also change the
    -- LDAP-logged-in alice, bypassing group mapping
    local                BOOLEAN NOT NULL
);

-- ── RPC users ──
-- An RPC user = a named set of method permissions; to use it, sign a JWT for it. The platform stores no
-- passwords. One set per org, not bound to nodes: the platform has a single signing key, and the token works
-- on every node of this org with auth enabled.
CREATE TABLE IF NOT EXISTS rpc_users (
    id          TEXT PRIMARY KEY,            -- uuid; recreating with the same name gets a new id and does not inherit old records
    username    TEXT NOT NULL UNIQUE,        -- unique within the org; the token's sub
    permissions JSONB NOT NULL DEFAULT '[]', -- array of besu permission strings (namespace:* or namespace:method)
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Activity records for RPC users (created / token issued); the detail page uses them to compute valid tokens. The
-- token itself is not stored. Deleted along with the user (ON DELETE CASCADE), with no extra record. Not the audit
-- table (only Enterprise has one); the handler still calls s.audit as usual, and the duplication is intentional.
CREATE TABLE IF NOT EXISTS rpc_user_events (
    id          BIGSERIAL PRIMARY KEY,
    rpc_user_id TEXT NOT NULL REFERENCES rpc_users(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN ('created', 'token_issued')),
    at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor       TEXT NOT NULL,  -- session username; empty string when it cannot be identified
    permissions JSONB NOT NULL, -- created: permissions at the time (never changes); token_issued: permissions carried by the token
    expires_at  TIMESTAMPTZ,    -- token_issued only
    token_id    TEXT UNIQUE,    -- token_issued only: the JWT jti (tk_ + 12 hex chars); unique, so a collision fails the write and that token is not handed out
    label       TEXT            -- token_issued only: the purpose entered at issuance; NULL if not given
);
CREATE INDEX IF NOT EXISTS rpc_user_events_user_idx ON rpc_user_events (rpc_user_id, id);

-- The key pair the platform uses to sign node RPC tokens (single row). The private key is AES-GCM encrypted
-- (auth/secret.go, derived from the master key); the public key is plaintext, and the node side puts it in
-- Besu's --rpc-http-authentication-jwt-public-key-file itself.
CREATE TABLE IF NOT EXISTS rpc_signing_key (
    id              INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    private_key_enc BYTEA NOT NULL,
    public_key_pem  TEXT NOT NULL,
    algorithm       TEXT NOT NULL,                -- RS256 (Besu does not support Ed25519)
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Alerts: landing table for Alertmanager webhook ingestion, serving GET /alerts. One row per firing:
-- fingerprint is Alertmanager's stable key for the same alert (same rule, same labels), and starts_at tells
-- firings apart. Repeated pushes of the same firing (firing->resolved) = upsert the status; a new firing = a
-- new row, the old one kept as history (retention: alertRetention in server/alerts.go).
CREATE TABLE IF NOT EXISTS alerts (
    fingerprint TEXT NOT NULL,
    alertname   TEXT NOT NULL,
    node_id     TEXT,                          -- only for node-level alerts (hw/p2p/ledger/cert expiry); NULL for platform events (votes, etc.)
    org         TEXT,                          -- org of the target node (the org label from SD); NULL for platform events, not visible across orgs
    scope       TEXT NOT NULL,                 -- hw|net|p2p|ledger|consensus|security
    severity    TEXT NOT NULL,                 -- critical|warning (the rule's severity label)
    status      TEXT NOT NULL,                 -- firing|resolved (alert-level status in the Alertmanager webhook payload)
    labels      JSONB NOT NULL,                -- full labels (for showing details in the UI, not split into columns)
    annotations JSONB NOT NULL,                -- full annotations (summary/description)
    starts_at   TIMESTAMPTZ NOT NULL,
    ends_at     TIMESTAMPTZ,                   -- only when resolved; NULL while firing
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (fingerprint, starts_at)
);
CREATE INDEX IF NOT EXISTS alerts_node_id_idx ON alerts (node_id);
CREATE INDEX IF NOT EXISTS alerts_alertname_idx ON alerts (alertname, starts_at);
CREATE INDEX IF NOT EXISTS alerts_ends_at_idx ON alerts (ends_at);

-- Per-user read state for alert notifications (inbox: one item on firing, one on resolution; resolving marks
-- it unread again). Deleted along with the alert. username is the same as in sessions (local account; subject
-- for SSO).
CREATE TABLE IF NOT EXISTS alert_reads (
    username    TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    starts_at   TIMESTAMPTZ NOT NULL,
    phase       TEXT NOT NULL CHECK (phase IN ('fire', 'resolve')),
    read_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (username, fingerprint, starts_at, phase),
    FOREIGN KEY (fingerprint, starts_at) REFERENCES alerts (fingerprint, starts_at) ON DELETE CASCADE
);
