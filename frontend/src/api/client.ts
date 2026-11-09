import type { ChainNode, ValidatorVoteRecord, RpcPublicKey, AgentBundleInfo, PermissioningSaveResult, NodeLogLevelRequest, NodeLogs, NodePortsRequest, RpcTokenRequest, RpcTokenIssued, RpcUserEvent, LoginEventList, AlertList, LocalUser, LocalUserCreated, LocalUserCreateRequest, PasswordResetResult, AccountMailStatus } from './schemas';
import { refreshMs } from '@/lib/refreshInterval';
import type { components } from './types';

export type ErrorReason = NonNullable<components['schemas']['Error']['reason']>;
/**
 * Minimal typed fetch. The frontend only talks to the contract (base = /api/v1).
 * On the dev server MSW answers these requests (see mocks/); production builds reach the real backend at the same paths.
 */
import type { NodeTxPool, NodeSummary, NodeDetail, NodeCertStatus, PermissioningPolicy, MetricId, MetricSeries, FleetNodeStatus, JoinToken, JoinTokenCreated, JoinTokenCreateRequest, Me, AuthConfig, LoginRequest, PasswordChange, ValidatorSetEntry, ChainInfo, ValidatorVotes, ValidatorVoteResult, AlertStatus, AlertSettings, AlertLocale, SmtpSettings, TelegramSettings, SlackSettings, AlertWebhookUrl, AlertRule, AlertRuleUpdateRequest, RpcUser, RpcUserCreate, RpcMethodCatalog, NodePeers, NodeNameRequest, ManualPeer, SharedPeers, CustomPeer, NodeCustomPeers } from './schemas';
import { type RangeSel, selToQuery } from '@/lib/range';

const BASE = '/api/v1';
/** Offline install bundle (same origin, sends the session cookie; not in the contract, content is tar.gz) */
export const AGENT_BUNDLE_URL = `${BASE}/platform/agent-bundle.tar.gz`;

export class HttpError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    /** Whether the response is the platform's own JSON error (has code); if not, it came from a reverse proxy in between */
    public fromPlatform = false,
    /** Fixed error code and its params: the UI translates it into the user's language; without one, message is shown as is */
    public reason?: ErrorReason,
    public params?: Record<string, string>,
  ) {
    super(message);
  }
}

/** fetch itself failed (unreachable, DNS, connection dropped): kept separate from TypeErrors thrown by bugs inside queryFn */
export class NetworkError extends Error {
  name = 'NetworkError';
}

/** fetch that throws NetworkError when unreachable (aborts are rethrown as is) */
export async function apiFetch(url: string, init?: RequestInit): Promise<Response> {
  try {
    return await fetch(url, init);
  } catch (e) {
    if (e instanceof DOMException && e.name === 'AbortError') throw e;
    throw new NetworkError(e instanceof Error ? e.message : String(e));
  }
}

/** Turn non-2xx responses into HttpError; fromPlatform only when the body is a platform JSON error (code is a string) */
export async function httpError(res: Response): Promise<HttpError> {
  const body = await res.json().catch(() => undefined);
  const fromPlatform = typeof body?.code === 'string';
  return new HttpError(res.status, fromPlatform ? body.code : 'internal', body?.message ?? res.statusText, fromPlatform, fromPlatform ? body.reason : undefined, fromPlatform ? body.params : undefined);
}

export async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await apiFetch(`${BASE}${path}`, { headers: { Accept: 'application/json' }, ...init });
  if (!res.ok) throw await httpError(res);
  if (res.status === 204) return undefined as T;
  // 202 may have no body; parse only when there is one
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

export const get = <T>(path: string) => request<T>(path);
export const post = <T>(path: string, body?: unknown) =>
  request<T>(path, {
    method: 'POST',
    ...(body !== undefined && {
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  });
const put = <T>(path: string, body?: unknown) =>
  request<T>(path, {
    method: 'PUT',
    ...(body !== undefined && {
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  });
const patch = <T>(path: string, body: unknown) =>
  request<T>(path, {
    method: 'PATCH',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
export const del = <T>(path: string) => request<T>(path, { method: 'DELETE' });

export interface NodeListResponse {
  nodes: NodeSummary[];
  asOf: string;
  dataAgeSec?: number | null;
}

export const api = {
  listNodes: () => get<NodeListResponse>('/nodes'),
  getNode: (id: string) => get<NodeDetail>(`/nodes/${encodeURIComponent(id)}`),
  deleteNode: (id: string) => del<void>(`/nodes/${encodeURIComponent(id)}`),
  getNodeCert: (id: string) => get<NodeCertStatus>(`/nodes/${encodeURIComponent(id)}/cert`),
  getNodeLogs: (id: string, after?: number, limit = 1000) =>
    get<NodeLogs>(`/nodes/${encodeURIComponent(id)}/logs?limit=${limit}${after != null ? `&after=${after}` : ''}`),
  // Log level: the platform calls admin_changeLogLevel on the node directly; takes effect immediately and does not persist (a Besu restart reverts to logging in config.toml).
  setNodeLogLevel: (id: string, body: NodeLogLevelRequest) => put<void>(`/nodes/${encodeURIComponent(id)}/log-level`, body),
  // Node-local RPC / metrics ports (the targets the platform forwards to through the agent tunnel).
  setNodePorts: (id: string, body: NodePortsRequest) => put<void>(`/nodes/${encodeURIComponent(id)}/ports`, body),
  setNodeName: (id: string, body: NodeNameRequest) => put<void>(`/nodes/${encodeURIComponent(id)}/name`, body),

  // ── Org-level besu account allowlist: on save the platform calls perm_* RPCs on every node and reads back to compare ──
  getPermissioningPolicy: () => get<PermissioningPolicy>('/permissioning'),
  setPermissioningPolicy: (p: PermissioningPolicy) => put<PermissioningSaveResult>('/permissioning', p),
  setNodeAllowlist: (id: string, p: PermissioningPolicy) => put<PermissioningSaveResult>(`/nodes/${encodeURIComponent(id)}/allowlist`, p),
  deleteNodeAllowlist: (id: string) => del<PermissioningSaveResult>(`/nodes/${encodeURIComponent(id)}/allowlist`),
  // Whether this node applies the shared list
  setNodeAllowlistShared: (id: string, useShared: boolean) => put<PermissioningSaveResult>(`/nodes/${encodeURIComponent(id)}/allowlist/shared`, { useShared }),
  // Re-push the platform's copy (shared list + this node's own additions) to this node; the list itself does not change
  syncNodeAllowlist: (id: string) => post<PermissioningSaveResult>(`/nodes/${encodeURIComponent(id)}/allowlist/sync`, {}),

  // ── RPC users (shared across the org, not bound to a node) + method catalog. The token appears only once, in the issue response.──
  listRpcUsers: () => get<RpcUser[]>('/rpc-users'),
  createRpcUser: (body: RpcUserCreate) => post<RpcUser>('/rpc-users', body),
  issueRpcUserToken: (id: string, body: RpcTokenRequest) => post<RpcTokenIssued>(`/rpc-users/${encodeURIComponent(id)}/token`, body),
  deleteRpcUser: (id: string) => del<void>(`/rpc-users/${encodeURIComponent(id)}`),
  listRpcUserEvents: (id: string) => get<RpcUserEvent[]>(`/rpc-users/${encodeURIComponent(id)}/events`),
  getRpcMethodCatalog: () => get<RpcMethodCatalog>('/rpc-method-catalog'),
  getPlatformRpcPublicKey: () => get<RpcPublicKey>('/platform/rpc-public-key'),
  getAgentBundle: () => get<AgentBundleInfo>('/platform/agent-bundle'),
  // explicit: for cross-node trends (FleetTrend.tsx). Sets [from,to,step] explicitly, overriding the range= derived from sel,
  // so every node in the same round of requests shares one time grid (see liveWindowAndStep in lib/range.ts). Single-node charts omit it and behave as before.
  getNodeSeries: (
    id: string,
    metric: MetricId,
    sel: RangeSel = { kind: 'rel', dur: '1h' },
    explicit?: { from: number; to: number; step: number },
  ) =>
    get<MetricSeries>(
      `/nodes/${encodeURIComponent(id)}/series?metric=${metric}&${
        explicit ? `from=${Math.round(explicit.from)}&to=${Math.round(explicit.to)}&step=${Math.round(explicit.step)}` : selToQuery(sel)
      }`,
    ),
  // Peer view: the node's own admin_peers; names are keyed by the address derived from the public key and shared within the org
  listNodePeers: (id: string) => get<NodePeers>(`/nodes/${encodeURIComponent(id)}/peers`),
  getNodeTxPool: (id: string, from?: string) =>
    get<NodeTxPool>(`/nodes/${encodeURIComponent(id)}/txpool${from ? `?from=${encodeURIComponent(from)}` : ''}`),
  listSharedPeers: () => get<SharedPeers>('/shared-peers'),
  createManualPeer: (enode: string) => post<ManualPeer>('/manual-peers', { enode }),
  deleteManualPeer: (pubkey: string) => del<void>(`/manual-peers/${encodeURIComponent(pubkey)}`),
  // Extra peers: the toggle for applying the shared list + this node's own additions
  getNodeCustomPeers: (id: string) => get<NodeCustomPeers>(`/nodes/${encodeURIComponent(id)}/custom-peers`),
  setNodePeerShared: (id: string, useShared: boolean) => put<NodeCustomPeers>(`/nodes/${encodeURIComponent(id)}/peer-shared`, { useShared }),
  createNodeCustomPeer: (id: string, enode: string) => post<CustomPeer>(`/nodes/${encodeURIComponent(id)}/custom-peers`, { enode }),
  deleteNodeCustomPeer: (id: string, pubkey: string) => del<void>(`/nodes/${encodeURIComponent(id)}/custom-peers/${encodeURIComponent(pubkey)}`),
  // Names keyed by address: the peer page passes the peer's address (derived from its public key), the validator page passes the validator address directly
  setPeerName: (address: string, name: string) => put<void>(`/peer-names/${encodeURIComponent(address)}`, { name }),
  deletePeerName: (address: string) => del<void>(`/peer-names/${encodeURIComponent(address)}`),
  // Names for allowlisted transaction addresses: the platform's own address book, keyed by address, never pushed to nodes
  listAccountNames: () => get<Record<string, string>>('/account-names'),
  setAccountName: (address: string, name: string) => put<void>(`/account-names/${encodeURIComponent(address)}`, { name }),
  deleteAccountName: (address: string) => del<void>(`/account-names/${encodeURIComponent(address)}`),
  getNodeAlerts: (id: string) => get<AlertList>(`/nodes/${encodeURIComponent(id)}/alerts`),
  // Read state of alert notifications (per user): mark the current one; mark all as read
  markAlertsRead: (ids: string[]) => post<void>('/alerts/read', { ids }),
  markAllAlertsRead: () => post<void>('/alerts/read-all', {}),
  // Fleet source status (status bar / legend; replaces per-chart red error text)
  getFleetStatus: () => get<FleetNodeStatus[]>('/fleet/status'),


  listJoinTokens: () => get<JoinToken[]>('/join-tokens'),
  createJoinToken: (req: JoinTokenCreateRequest) => post<JoinTokenCreated>('/join-tokens', req),
  revokeJoinToken: (id: string) => post<JoinToken>(`/join-tokens/${encodeURIComponent(id)}/revoke`),

  // ── Identity / permissions ──
  getAuthConfig: () => get<AuthConfig>('/auth/config'),
  login: (req: LoginRequest) => post<Me>('/auth/login', req),
  getMe: () => get<Me>('/me'),
  logout: () => post<void>('/auth/logout'),
  changePassword: (body: PasswordChange) => post<void>('/auth/password', body),
  // Local user management (admin)
  listLocalUsers: () => get<LocalUser[]>('/users'),
  createLocalUser: (body: LocalUserCreateRequest) => post<LocalUserCreated>('/users', body),
  updateLocalUser: (username: string, body: { disabled: boolean }) => patch<LocalUser>(`/users/${encodeURIComponent(username)}`, body),
  resetLocalUserPassword: (username: string, sendEmail = false) => post<PasswordResetResult>(`/users/${encodeURIComponent(username)}/reset-password`, { sendEmail }),
  getAccountMailStatus: () => get<AccountMailStatus>('/account-mail'),
  deleteLocalUser: (username: string) => del<void>(`/users/${encodeURIComponent(username)}`),
  // Login events (admin): since is required; omitting username or result means all
  listLoginEvents: (params: { since: string; username?: string; result?: 'success' | 'failure' }) => {
    const q = new URLSearchParams({ since: params.since });
    if (params.username) q.set('username', params.username);
    if (params.result) q.set('result', params.result);
    return get<LoginEventList>(`/auth/login-events?${q.toString()}`);
  },

  // ── Validator governance (on-chain validator set + voting through our own validators)──
  listValidatorSet: () => get<ValidatorSetEntry[]>('/validators'),
  listChainNodes: () => get<ChainNode[]>('/chain/nodes'),
  getChainInfo: () => get<ChainInfo>('/chain'),
  // Validator votes: sent once to each of our own validators, with no record kept; pending votes are read from each node's *_getPendingVotes
  listValidatorVotes: () => get<ValidatorVotes>('/validator-votes'),
  listValidatorVoteHistory: () => get<ValidatorVoteRecord[]>('/validator-votes/history'),
  proposeValidatorVote: (address: string, add: boolean) => post<ValidatorVoteResult>('/validator-votes', { address, add }),
  discardValidatorVote: (address: string) => del<ValidatorVoteResult>(`/validator-votes/${encodeURIComponent(address)}`),


  // ── Alerting (Prometheus/Alertmanager)──
  listAlerts: (params?: { status?: AlertStatus; nodeId?: string; alertname?: string; since?: string }) => {
    const q = new URLSearchParams();
    if (params?.status) q.set('status', params.status);
    if (params?.nodeId) q.set('nodeId', params.nodeId);
    if (params?.alertname) q.set('alertname', params.alertname);
    if (params?.since) q.set('since', params.since);
    const qs = q.toString();
    return get<AlertList>(`/alerts${qs ? `?${qs}` : ''}`);
  },
  getAlertSettings: () => get<AlertSettings>('/alert-settings'),
  // Each channel is written separately: changing the webhook does not require resending the SMTP password (GET never returns passwords, so a whole-object PUT would force retyping it).
  // All three endpoints return the full updated config; callers can overwrite the cache with the response.
  putAlertSettingsSmtp: (smtp: SmtpSettings) => put<AlertSettings>('/alert-settings/smtp', smtp),
  deleteAlertSettingsSmtp: () => del<AlertSettings>('/alert-settings/smtp'),
  putAlertSettingsWebhooks: (webhooks: AlertWebhookUrl[]) =>
    put<AlertSettings>('/alert-settings/webhooks', webhooks),
  // Test the connection with the form's current contents on the spot, without sending mail or saving first; 204 = success, failure throws HttpError (with the raw error message).
  testAlertSettingsSmtp: (smtp: SmtpSettings) => post<void>('/alert-settings/test-smtp', smtp),
  putAlertSettingsTelegram: (tg: TelegramSettings) => put<AlertSettings>('/alert-settings/telegram', tg),
  deleteAlertSettingsTelegram: () => del<AlertSettings>('/alert-settings/telegram'),
  putAlertSettingsSlack: (slack: SlackSettings) => put<AlertSettings>('/alert-settings/slack', slack),
  deleteAlertSettingsSlack: () => del<AlertSettings>('/alert-settings/slack'),
  putAlertSettingsLocale: (locale: AlertLocale) => put<AlertSettings>('/alert-settings/locale', locale),
  // The Telegram and Slack tests really send a test message (fixed format, to confirm the bot is in the group and the webhook points at the right channel).
  testAlertSettingsTelegram: (tg: TelegramSettings) => post<void>('/alert-settings/test-telegram', tg),
  testAlertSettingsSlack: (slack: SlackSettings) => post<void>('/alert-settings/test-slack', slack),
  // Tests only the TCP+TLS connection layer and sends no HTTP request (see alerting.TestWebhookConnectivity in the backend).
  testAlertSettingsWebhook: (webhook: AlertWebhookUrl) => post<void>('/alert-settings/test-webhook', webhook),

  // ── Custom alert rules ──
  listAlertRules: () => get<AlertRule[]>('/alert-rules'),
  updateAlertRule: (id: string, req: AlertRuleUpdateRequest) =>
    request<AlertRule>(`/alert-rules/${encodeURIComponent(id)}`, {
      method: 'PUT',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: JSON.stringify(req),
    }),
  restoreDefaultAlertRules: () => post<AlertRule[]>('/alert-rules/restore-defaults'),
};

/** Chain-level info query (overview and validator governance share one cache). */
// Poll every 5 seconds: /chain reads the platform cache, not the nodes; the backend updates height and latest block time every 2 seconds, so polling slower makes the height jump several blocks at once
export const chainQuery = { queryKey: ['chain'], queryFn: () => api.getChainInfo(), refetchInterval: refreshMs };
