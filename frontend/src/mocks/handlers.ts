import type { ErrorReason } from '@/api/client';
import type { ChainNode, ValidatorVoteRecord, NodeSummary, PolicyApplyNodeResult, NodeAllowlist, Role } from '@/api/schemas';
/** MSW handlers: return fake data following the contract contract/openapi.yaml. */
import { http, HttpResponse, delay } from 'msw';
import type { ManualPeer, SharedPeers, CustomPeer, NodeCustomPeers, JoinTokenCreateRequest, MetricId, MetricSeries, LoginRequest, PasswordChange, SmtpSettings, TelegramSettings, SlackSettings, AlertLocale, AlertWebhookUrl, AlertRuleUpdateRequest, RpcUserCreate, PermissioningPolicy, RpcTokenRequest, LoginFailReason } from '@/api/schemas';
import type { RangeSel } from '@/lib/range';
import { tickNodeSync, summaries, details, joinTokens, session, persistSession, mockUsers, meForUser, makeJoinToken, makeSeries, fleetStatuses, now, currentValidators, validatorVoteHistory, chainInfo, epochUnknownScenario, chainUnreadableScenario, alerts, alertSettingsState, alertRules, restoreDefaultAlertRules, updateAlertRule, createRpcUserFixture, issueRpcTokenFixture, deleteRpcUserFixture, rpcPublicKeyFixture, rpcUsers, rpcUserEvents, rpcMethodCatalog, permissioningPolicy, nodeAllowlists, allowlistScenario, capsOf, reportedPeersOf, rawPeersOf, seededPeerNames, seededAccountNames, randHex, txpoolOf, txpoolUnreachable, loginEvents, withReads, markAlertsReadFixture } from './fixtures';
import { roleCan, roleKnown } from '@/auth/permissions';
import { IDENTITIES, addressOfPub, enodeOf, pubOfEnode, onCurve, pubFromSeed } from './identities';
import { HEAD, headNow, availabilityOf, dataAgeSec } from './fixtures';
import { isFreshDemo, freshProgress } from './scenario';

// The mock deployment acts as if a password mail server is configured (ACCOUNT_SMTP_*). To see the unconfigured screen: run localStorage.setItem('mock.accountMail', 'off') in the console and reload.
// Email addresses ending in .invalid simulate a send failure (falls back to showing the password, with mailReason: mail host not resolvable).
const ACCOUNT_MAIL_CONFIGURED = (() => { try { return localStorage.getItem('mock.accountMail') !== 'off'; } catch { return true; } })();
const mockMailFailure = (email: string | null) => {
  if (!email?.endsWith('.invalid')) return null;
  const host = email.split('@')[1], detail = `lookup ${host}: no such host`;
  return { mailReason: 'conn_resolve_failed' as const, mailParams: { host, detail }, mailError: `Could not resolve host ${host} (${detail})` };
};

// RPC username: alphanumerics, - and _ only (bad characters would break the [Users.<name>] header in config.toml; mirrors the backend's rpcUsernameRe).
const RPC_USERNAME_RE = /^[A-Za-z0-9_-]+$/;

const BASE = '/api/v1';

// Error with a reason (same as the backend's apierr.New; message is English, the UI translates reason)
// The backend's message is an English sentence; the UI translates reason, so only the code goes in message here instead of copying the English
/** Same default as the backend when security is left out: port 465 means implicit TLS, anything else none. */
const smtpSecurity = (s: SmtpSettings) => s.security ?? (s.port === 465 ? 'tls' : 'none');

export const apiErr = (code: string, reason: ErrorReason, params?: Record<string, string>) => ({ code, reason, message: reason, ...(params && { params }) });
// Malformed enode: return a few of the backend's most common reasons (same as internal/enode's reasons)
const enodeErr = (raw: string) => {
  const value = JSON.stringify(raw);
  if (!raw.startsWith('enode://')) return apiErr('invalid', 'enode_scheme', { value });
  const pub = raw.slice(8).split('@')[0];
  if (pub.length !== 128) return apiErr('invalid', 'enode_pubkey_length', { got: String(pub.length), value });
  if (!/^[0-9a-fA-F]{128}$/.test(pub)) return apiErr('invalid', 'enode_pubkey_hex', { value });
  if (!onCurve(pub)) return apiErr('invalid', 'enode_pubkey_invalid', { value });
  return apiErr('invalid', 'enode_host_not_ip', { value: JSON.stringify(raw.split('@')[1]?.split(':')[0] ?? '') });
};
// List limit (same as the backend's manualPeerLimit)
const PEER_LIMIT = 100;
// Last enabled admin (same check as the backend's users.go): both disabling and deleting are blocked
const isLastAdmin = (name: string) => mockUsers[name]?.role === 'admin' && !mockUsers[name]?.disabled && Object.values(mockUsers).filter((x) => x.role === 'admin' && !x.disabled).length === 1;

// The alert-settings endpoints share one projection: passwords, bot tokens and Slack URLs are write-only and never echoed (same as the real backend's Service.View).
// Without nodes:onboard you cannot even see webhook URLs, only how many there are (same as the real backend's GetAlertSettings).
const alertSettingsView = () => {
  const { smtp, telegram, slack, webhookUrls, ...rest } = alertSettingsState;
  const canEdit = !!session.me && roleCan(session.me.role, 'nodes:onboard');
  return HttpResponse.json({
    ...rest,
    webhookUrls: canEdit ? webhookUrls : webhookUrls.map((w) => ({ ...w, url: '' })),
    smtp: smtp ? { ...smtp, password: undefined } : null,
    telegram: telegram ? { ...telegram, botToken: undefined } : null,
    slack: slack ? {} : null,
  });
};

const peerNames: Record<string, string> = { ...seededPeerNames };
const accountNames: Record<string, string> = { ...seededAccountNames };
// Shared list: demo with other-org nodes already in the mock topology, so names and connection status line up
// Memory usage (mock): a host number like CPU; the disconnected agent's node has none
const memOf = (s: NodeSummary): number | null => (s.status === 'offline' ? null : ({ 'node-a1': 41, 'node-a2': 44, 'node-a3': 39, 'node-a4': 47 } as Record<string, number>)[s.id] ?? 40);
// Besu memory: matches the baseline of the node detail jvm_heap chart (node-a2 high at 78%, others 58%); null when Besu cannot be read (offline, RPC unreachable)
const jvmOf = (s: NodeSummary): number | null => (s.status === 'offline' || s.status === 'rpc_unreachable' ? null : s.id === 'node-a2' ? 78 : 58);

const manualPeers: ManualPeer[] = isFreshDemo() ? [] : (['org-b-v1', 'org-c-n9'] as const).map((k) => ({ pubkey: IDENTITIES[k].pub, address: addressOfPub(IDENTITIES[k].pub), enode: enodeOf(k), createdAt: new Date().toISOString() }));
// Pending votes on our own validators: address (lowercase) -> true = add / false = remove
// withdrawingSince: when a vote already in a block is withdrawn, the platform casts an opposite vote for our org to cancel it; 8 seconds later (our next block) it lands and the count drops
const mockVotes: Record<string, { add: boolean; at: number; withdrawingSince?: number }> = {};
// Proposals that close while voting (majority = passed; count back to zero with even the proposing org withdrawn = not passed): removed from voting and recorded at the top of the vote history (the mock does not change the validator set)
const mockHistory: ValidatorVoteRecord[] = [];
const mockClosed: Record<string, true> = {};
// Seed vote history is computed only once: closed proposals never change, and recomputing would push their end times forward so the UI thinks a new entry appeared
let seededHistory: ValidatorVoteRecord[] | null = null;
// Seed: address of a new validator proposed for addition by another org (not in the validator set)
const SEED_CANDIDATE = '0x7a3c19e0b4d2f8a6c5e1d9b07f3a2c4e6d8b1f05'; // our own vote; counts as in a block 8 seconds after sending, and withdrawing after that only marks it withdrawn
const ownValidatorIds = () => currentValidators().filter((v) => v.orgId === 'org-a' && v.nodeId).map((v) => v.nodeId!);
const isAddress = (a: string) => /^0x[0-9a-fA-F]{40}$/.test(a);
// Peer connectivity: each node has an "Use shared list" toggle + self-added peers (same as the backend's pushTargets); seed data with peerMode = shared counts as on
const peerUseShared: Record<string, boolean> = Object.fromEntries(summaries.map((n) => [n.id, n.peerMode === 'shared']));
const customPeers: Record<string, CustomPeer[]> = {};
// Seed: the DMZ sentry (a3) has the shared list on and additionally self-adds a partner's RPC node (already connected), demoing "shared + self-added"
if (!isFreshDemo()) {
  customPeers['node-a3'] = rawPeersOf('node-a3').filter((p) => p.enode.includes('@203.0.113.')).slice(0, 1)
    .map((p) => ({ pubkey: p.pubkey, enode: p.enode, nodeId: null, name: null, quarantined: false, createdAt: new Date().toISOString() }));
}
const usesSharedPeers = (id: string) => peerUseShared[id] ?? false;
// peerMode is derived from the toggle and self-added peers (same as the backend's peerModeOf; used by the "Custom peers" tag in the node list)
const syncPeerMode = (s: NodeSummary) => { s.peerMode = usesSharedPeers(s.id) ? 'shared' : (customPeers[s.id]?.length ? 'custom' : 'off'); };
summaries.forEach(syncPeerMode);
const ownPubOf = (n: NodeSummary) => (n.reportedEnode ? pubOfEnode(n.reportedEnode) : null);
// The part of the shared list pushed to a given node (same as the backend's sharedListFor)
function mockSharedTargets(id: string): { pubkey: string; enode: string }[] {
  // Exclude itself and our isolated nodes
  const skip = new Set(summaries.filter((n) => n.id === id || n.quarantinedAt).flatMap((n) => { const p = ownPubOf(n); return p ? [p] : []; }));
  return manualPeers.filter((m) => !skip.has(m.pubkey)).map((m) => ({ pubkey: m.pubkey, enode: m.enode }));
}

// Everything this node should connect to: shared list first if applied (minus itself), then self-added (deduplicated)
function mockPeerTargets(id: string): { pubkey: string; enode: string }[] {
  const out = new Map<string, { pubkey: string; enode: string }>();
  if (usesSharedPeers(id)) for (const m of mockSharedTargets(id)) out.set(m.pubkey, m);
  for (const p of customPeers[id] ?? []) if (!p.quarantined && !out.has(p.pubkey)) out.set(p.pubkey, { pubkey: p.pubkey, enode: p.enode });
  return [...out.values()];
}

// GET /shared-peers: same as the backend's buildSharedPeers.
function mockSharedPeers(): SharedPeers {
  const nameOf = (pub: string) => peerNames[addressOfPub(pub)] ?? null;
  const ownBy = new Map(summaries.flatMap((n) => { const p = ownPubOf(n); return p ? [[p, n] as const] : []; }));
  const peers = manualPeers.map((m) => {
    const own = ownBy.get(m.pubkey);
    return { pubkey: m.pubkey, enode: m.enode, address: addressOfPub(m.pubkey), nodeId: own?.id ?? null, name: own ? own.name ?? null : nameOf(m.pubkey) };
  });
  // missing is computed against "everything to connect to" (shared + self-added); null when admin_peers was not reported
  const nodes = summaries.map((n) => {
    const seen = !n.quarantinedAt ? reportedPeersOf(n.id) : null;
    const missing = seen ? mockPeerTargets(n.id).filter((m) => !seen.some((p) => p.pubkey === m.pubkey)).length : null;
    return { id: n.id, quarantined: !!n.quarantinedAt, missing, useShared: usesSharedPeers(n.id), additions: customPeers[n.id]?.length ?? 0 };
  });
  // Candidates: peers connected to any of our reporting nodes that are neither in the list nor our own, most widely seen first
  const listed = new Set(manualPeers.map((m) => m.pubkey));
  const cand = new Map<string, SharedPeers['candidates'][number]>();
  const seenBy = new Map<string, number>();
  for (const n of summaries) {
    for (const p of reportedPeersOf(n.id) ?? []) {
      if (ownBy.has(p.pubkey) || listed.has(p.pubkey)) continue;
      seenBy.set(p.pubkey, (seenBy.get(p.pubkey) ?? 0) + 1);
      if (!cand.has(p.pubkey)) cand.set(p.pubkey, { pubkey: p.pubkey, address: p.address, enode: p.enode, validatorAddress: p.validatorAddress, name: nameOf(p.pubkey) });
    }
  }
  return { peers, nodes, candidates: [...cand.values()].sort((a, b) => seenBy.get(b.pubkey)! - seenBy.get(a.pubkey)!) };
}

// This node's peer settings: toggle + self-added
function customView(s: NodeSummary): NodeCustomPeers {
  // Name: our nodes use the node name, others use peer_names (same as the shared list)
  const nameFor = (p: CustomPeer) => (p.nodeId ? summaries.find((n) => n.id === p.nodeId)?.name ?? null : peerNames[addressOfPub(p.pubkey)] ?? p.name ?? null);
  return { useShared: usesSharedPeers(s.id), peers: (customPeers[s.id] ?? []).map((p) => ({ ...p, name: nameFor(p) })) };
}

// Mock for the node registration page: after a join code is generated, joining is simulated by elapsed time: after 5 seconds the agent connects and RPC is rejected (401),
// after 10 seconds RPC works but PERM is missing, after 15 seconds metrics are scraped. Only nodes not already in the list are simulated; each new code restarts the simulation.
const mockJoins: Record<string, { at: number; name: string | null; rpcPort: number; metricsPort: number; ip: string; pub: string }> = {};
function mockJoinProgress(id: string) {
  const j = mockJoins[id];
  if (!j) return;
  const s = (Date.now() - j.at) / 1000;
  if (s < 5) return;
  const enode = `enode://${j.pub}@${j.ip}:30303`;
  let sum = summaries.find((n) => n.id === id);
  if (!sum) {
    sum = { id, name: j.name, org: 'org-a', status: 'rpc_unreachable', isValidator: false, rpcPort: j.rpcPort, metricsPort: j.metricsPort, rpcAuthEnabled: true, reportedEnode: enode, peerMode: 'off', cpuPercent: 12, diskUsedPercent: 20, asOf: now() };
    summaries.push(sum);
    const tk = joinTokens.find((t) => t.nodeId === id && t.status === 'pending');
    if (tk) tk.status = 'used';
  }
  const rpcOk = s >= 10;
  const head = summaries.find((n) => n.id === 'node-a1')?.chainHeight ?? HEAD; // a fresh deployment has no a1: the chain is still at HEAD
  sum.status = rpcOk ? 'healthy' : 'rpc_unreachable';
  // The node address can only be read once RPC is up (derived from the public key, same as validatorAddress in the detail); before that the list shows "—"
  if (rpcOk) Object.assign(sum, { address: addressOfPub(j.pub), chainHeight: head, blocksBehind: 0, peerCount: 0, txpoolPending: 0 });
  details[id] = {
    id, name: j.name, org: 'org-a', status: sum.status, asOf: now(), quarantinedAt: null, rpcPort: j.rpcPort, metricsPort: j.metricsPort, rpcAuthEnabled: true,
    reportedEnode: enode, validatorAddress: addressOfPub(j.pub),
    hardware: { active: true, cpuPercent: 12, memUsedBytes: 2_100_000_000, memTotalBytes: 16_000_000_000, diskUsedPercent: 20, netRxBytesPerSec: 300_000, netTxBytesPerSec: 200_000 },
    ...(rpcOk ? { ledger: { chainHeight: head, blocksBehind: 0, txpoolPending: 0, tps: 1.6 }, p2p: { enode, peerCount: 0, peerLimit: 25, inSync: true } } : {}),
    capabilities: { agent: true, rpcReachable: rpcOk, rpcUnauthorized: !rpcOk, missingRpcApis: rpcOk ? ['PERM'] : [], allowlistEnabled: null, metricsReachable: s >= 15 ? true : null, logsConnected: false },
  };
}

// A node's unused codes (pending, or expired but never used) become revoked, and its simulated join stops
function revokeUnusedCodes(nodeId: string) {
  for (const t of joinTokens) if (t.nodeId === nodeId && (t.status === 'pending' || t.status === 'expired')) t.status = 'revoked';
  delete mockJoins[nodeId];
}

// Simulated joins must show up in the list, chain info and validator set (in a fresh deployment chain info only appears once the first node connects), without opening the detail first
const tickJoins = () => { for (const id of Object.keys(mockJoins)) mockJoinProgress(id); };

// To see long-list scrolling: set localStorage['mock:manyNodes']='1' in the browser console and reload; the node list gets 31 more nodes (list only, no detail)
function manyNodes(): NodeSummary[] {
  try { if (!localStorage.getItem('mock:manyNodes')) return []; } catch { return []; }
  return Array.from({ length: 31 }, (_, i) => {
    const id = `node-x${String(i + 1).padStart(2, '0')}`;
    const off = i % 5 === 0, bad = i % 11 === 3;
    return {
      id, org: 'org-a', status: 'healthy', rpcPort: 8545, metricsPort: 9545, asOf: now(), rpcAuthEnabled: !off,
      capabilities: { agent: true, rpcReachable: !bad, rpcUnauthorized: bad, missingRpcApis: [], allowlistEnabled: allowlistScenario() !== 'none', metricsReachable: true, logsConnected: true },
    };
  });
}

// To see "Monitoring source unreachable" (Prometheus or Alertmanager): localStorage['mock:promDown'] = '1', read on every request, no reload needed.
// Alerts still return with monitoringAvailable=false; time series return 503 (same as the backend).
const promDownScenario = () => { try { return !!localStorage.getItem('mock:promDown'); } catch { return false; } };

// Same rule as the server: from and every to must be one bare address, no display name or spaces.
function smtpAddressError(body: SmtpSettings) {
  const bad = [body.from, ...body.to].findIndex((a) => !/^[^\s@<>]+@[^\s@<>]+$/.test(a));
  if (bad < 0) return null;
  return HttpResponse.json(apiErr('invalid', 'invalid_email', { field: bad === 0 ? 'from' : `to.${bad - 1}` }), { status: 400 });
}

export const handlers = [
  http.get(`${BASE}/nodes`, async () => {
    await delay(300); // simulated latency so the loading skeleton is visible
    tickNodeSync();
    tickJoins();
    // Healthy nodes' heights follow the chain head (keeping their fixed offsets); a4, which is syncing, catches up on its own in tickNodeSync
    const live = (s: NodeSummary) => (s.chainHeight != null && s.status !== 'syncing' ? headNow() - (HEAD - s.chainHeight) : s.chainHeight);
    return HttpResponse.json({ nodes: [...summaries.map((s) => ({ ...s, chainHeight: live(s), capabilities: capsOf(s), availability24h: availabilityOf(s), memPercent: memOf(s), jvmHeapPercent: jvmOf(s), lastSeen: capsOf(s).agent ? new Date(Date.now() - 12_000).toISOString() : null })), ...manyNodes()], asOf: now(), dataAgeSec: dataAgeSec() });
  }),

  http.get(`${BASE}/nodes/:id`, async ({ params }) => {
    await delay(300);
    if (params.id === 'node-a4') tickNodeSync();
    const id = String(params.id);
    mockJoinProgress(id);
    const summary = summaries.find((n) => n.id === id);
    if (!summary) {
      return HttpResponse.json(apiErr('not_found', 'node_not_found'), { status: 404 });
    }
    const detail = details[id];
    if (!detail) {
      return HttpResponse.json({ code: 'internal', message: 'no detail' }, { status: 500 });
    }
    return HttpResponse.json(detail);
  }),

  // Delete node (platform-side removal): revoke the certificate + remove from the registry. The mock just drops it from summaries/details.
  http.delete(`${BASE}/nodes/:id`, async ({ params }) => {
    await delay(300);
    const id = String(params.id);
    const summary = summaries.find((n) => n.id === id);
    if (!summary) {
      return HttpResponse.json(apiErr('not_found', 'node_not_found'), { status: 404 });
    }
    const i = summaries.findIndex((n) => n.id === id);
    if (i >= 0) summaries.splice(i, 1);
    delete details[id];
    revokeUnusedCodes(id); // a rejoin code made earlier must not join the freed id again
    return new HttpResponse(null, { status: 204 });
  }),

  // Node certificate / connection status (mock; certificate status follows node status: offline -> untrusted, simulating the CA reset pitfall)
  http.get(`${BASE}/nodes/:id/cert`, async ({ params }) => {
    await delay(120);
    const node = details[String(params.id)];
    // Isolation is caused by "certificate not signed by the platform's current CA", so it must be untrusted; otherwise the settings page would show
    // "isolated due to certificate verification failure" and "certificate valid" side by side, contradicting each other. unknown/offline just means it could not be fetched; the certificate itself is fine.
    if (!node) return HttpResponse.json(apiErr('not_found', 'node_not_found'), { status: 404 });
    const quarantined = !!node.quarantinedAt;
    const lost = !node.capabilities?.agent;
    const ms = Date.now();
    return HttpResponse.json({
      connection: lost ? 'lost' : 'connected',
      lastSeen: lost ? null : new Date(ms - 12_000).toISOString(),
      cert: quarantined ? 'untrusted' : 'valid',
      notAfter: new Date(ms + 72 * 3_600_000).toISOString(),
      // Each node has its own certificate: the serial is the 128-bit random number from when the platform CA issued it (decimal, as x509 serials are written)
      serial: BigInt('0x' + node.validatorAddress!.slice(2, 34)).toString(),
    });
  }),



  // Local ports (the mock validates the range and remembers them, so a detail refetch shows them).
  http.put(`${BASE}/nodes/:id/ports`, async ({ params, request }) => {
    await delay(200);
    if (!summaries.some(n => n.id === params.id)) return HttpResponse.json(apiErr('not_found', 'node_not_found'), { status: 404 });
    const body = await request.json() as { rpcPort: number; metricsPort: number };
    const ok = (p: number) => Number.isInteger(p) && p >= 1 && p <= 65535;
    if (!ok(body.rpcPort) || !ok(body.metricsPort)) return HttpResponse.json({ code: 'invalid', message: 'rpcPort / metricsPort must be 1-65535' }, { status: 400 });
    const d = details[String(params.id)];
    if (d) { d.rpcPort = body.rpcPort; d.metricsPort = body.metricsPort; }
    return new HttpResponse(null, { status: 204 });
  }),

  // Node name: list and detail change together (same as the backend's single nodes.name)
  http.put(`${BASE}/nodes/:id/name`, async ({ params, request }) => {
    await delay(200);
    const sum = summaries.find((n) => n.id === params.id);
    if (!sum) return HttpResponse.json(apiErr('not_found', 'node_not_found'), { status: 404 });
    const raw = ((await request.json()) as { name: string | null }).name?.trim() || null;
    if (raw && [...raw].length > 64) return HttpResponse.json({ code: 'invalid', message: 'name must be at most 64 characters' }, { status: 400 });
    sum.name = raw;
    const d = details[String(params.id)];
    if (d) d.name = raw;
    return new HttpResponse(null, { status: 204 });
  }),

  // Log level: the platform calls admin_changeLogLevel on the node directly (the mock only validates the level and that it is an own node).
  http.put(`${BASE}/nodes/:id/log-level`, async ({ params, request }) => {
    await delay(200);
    const lv = summaries.find((n) => n.id === params.id);
    if (!lv) return HttpResponse.json(apiErr('not_found', 'node_not_found'), { status: 404 });
    const body = await request.json() as { level?: string };
    if (!['OFF', 'FATAL', 'ERROR', 'WARN', 'INFO', 'DEBUG', 'TRACE', 'ALL'].includes(body.level ?? '')) return HttpResponse.json(apiErr('invalid', 'field_not_allowed', { field: 'level' }), { status: 400 });
    if (!capsOf(lv).agent) return HttpResponse.json(apiErr('internal', 'node_unreachable', { nodeId: lv.id }), { status: 502 });
    return new HttpResponse(null, { status: 204 });
  }),

  http.get(`${BASE}/rpc-users`, async () => {
    await delay(150);
    return HttpResponse.json(rpcUsers);
  }),
  http.post(`${BASE}/rpc-users`, async ({ request }) => {
    await delay(400);
    const body = (await request.json()) as RpcUserCreate;
    const name = body.username ?? '';
    // bsos- is a platform-reserved prefix; bad characters would break the [Users.<name>] header in config.toml -> both 400 (to exercise the UI error path). Same check order as the backend
    if (name === '' || name.startsWith('bsos-')) return HttpResponse.json(apiErr('invalid', 'rpc_username_reserved'), { status: 400 });
    if (!RPC_USERNAME_RE.test(name)) return HttpResponse.json(apiErr('invalid', 'rpc_username_format'), { status: 400 });
    if (!body.permissions?.length) return HttpResponse.json(apiErr('invalid', 'field_schema_mismatch', { field: 'permissions', detail: 'minimum number of items is 1' }), { status: 400 });
    for (const p of body.permissions) {
      const reason = rpcPermissionError(p);
      if (reason) return HttpResponse.json(apiErr('invalid', reason, { value: JSON.stringify(p) }), { status: 400 });
    }
    if (rpcUsers.some((u) => u.username === name)) {
      return HttpResponse.json(apiErr('conflict', 'rpc_user_exists'), { status: 409 });
    }
    return HttpResponse.json(createRpcUserFixture(body), { status: 201 });
  }),
  http.post(`${BASE}/rpc-users/:id/token`, async ({ params, request }) => {
    await delay(300);
    const body = (await request.json()) as RpcTokenRequest;
    // Purpose length and the ttl enum are enforced by the contract (the real backend returns field_* reasons)
    if (body.label !== undefined && [...body.label.trim()].length > 64) return HttpResponse.json(apiErr('invalid', 'field_schema_mismatch', { field: 'label', detail: 'maximum string length is 64' }), { status: 400 });
    if (body.ttl !== undefined && !['1h', '24h', '7d', '30d', '365d'].includes(body.ttl)) return HttpResponse.json(apiErr('invalid', 'field_not_allowed', { field: 'ttl' }), { status: 400 });
    if ((body.ttl === undefined) === (body.expiresAt === undefined)) return HttpResponse.json(apiErr('invalid', 'rpc_token_ttl_xor'), { status: 400 });
    const exp = body.expiresAt === undefined ? NaN : Date.parse(body.expiresAt);
    if (body.expiresAt !== undefined && !(exp > Date.now())) return HttpResponse.json(apiErr('invalid', 'rpc_token_expiry_past'), { status: 400 });
    if (body.expiresAt !== undefined && exp > Date.now() + 365 * 86_400_000) return HttpResponse.json(apiErr('invalid', 'rpc_token_expiry_too_far'), { status: 400 });
    const res = issueRpcTokenFixture(String(params.id), body);
    if (res === null) return HttpResponse.json(apiErr('not_found', 'rpc_user_not_found'), { status: 404 });
    return HttpResponse.json(res);
  }),
  http.delete(`${BASE}/rpc-users/:id`, async ({ params }) => {
    await delay(300);
    if (!deleteRpcUserFixture(String(params.id))) return HttpResponse.json(apiErr('not_found', 'rpc_user_not_found'), { status: 404 });
    return new HttpResponse(null, { status: 204 });
  }),
  http.get(`${BASE}/rpc-users/:id/events`, async ({ params }) => {
    await delay(150);
    const events = rpcUserEvents[String(params.id)];
    if (!events) return HttpResponse.json(apiErr('not_found', 'rpc_user_not_found'), { status: 404 });
    return HttpResponse.json([...events].reverse());
  }),
  http.get(`${BASE}/platform/agent-bundle.tar.gz`, () => new HttpResponse(new Uint8Array([0x1f, 0x8b]), { headers: { 'Content-Type': 'application/gzip' } })),

  http.get(`${BASE}/platform/agent-bundle`, async () => {
    await delay(150);
    return HttpResponse.json({
      installShSha256: 'c41e7a02b9d58f3e6a1c04b7d2e9f5a83c6b1d0e4f7a2c9b5d8e1f3a6c0b7d42',
      agentVersion: '2.0.0',
      bundle: { file: 'besu-agent-2.0.0-linux.tar.gz', sha256: '9f2c4e81a7d03b5c6e1f8a92d4b7c03e5a6f1d82b9c4e07a3d5f6b8c1e2a9d470', arches: ['amd64', 'arm64'] },
    });
  }),

  http.get(`${BASE}/platform/rpc-public-key`, async () => {
    await delay(100);
    return HttpResponse.json(rpcPublicKeyFixture);
  }),
  http.get(`${BASE}/rpc-method-catalog`, async () => {
    await delay(120);
    return HttpResponse.json(rpcMethodCatalog);
  }),

  http.get(`${BASE}/nodes/:id/series`, async ({ params, request }) => {
    if (promDownScenario()) return HttpResponse.json(apiErr('monitoring_unavailable', 'monitoring_down'), { status: 503 });
    await delay(200);
    const id = String(params.id);
    const url = new URL(request.url);
    const metric = url.searchParams.get('metric') as MetricId | null;
    const from = url.searchParams.get('from');
    const to = url.searchParams.get('to');
    const sel: RangeSel = from && to
      ? { kind: 'abs', from: Number(from), to: Number(to) }
      : { kind: 'rel', dur: url.searchParams.get('range') ?? '1h' };
    if (!metric) {
      return HttpResponse.json({ code: 'internal', message: 'metric required' }, { status: 400 });
    }
    const summary = summaries.find((s) => s.id === id);
    if (!summary) return HttpResponse.json(apiErr('not_found', 'node_not_found'), { status: 404 });
    // Agent disconnected (including isolated): nothing can be scraped. Metrics not scraped: host metrics (from the agent) are still there, chain metrics are not.
    const hw: MetricId[] = ['cpu', 'mem', 'disk', 'net_rx', 'net_tx'];
    const c = capsOf(summary);
    if (!c.agent || (!c.metricsReachable && !hw.includes(metric))) {
      return HttpResponse.json({ metric, unit: '', points: [] } satisfies MetricSeries);
    }
    return HttpResponse.json(makeSeries(id, metric, sel));
  }),

  // Node logs: the mock generates a few Besu-format log lines by seq; nodes without log forwarding simulate Besu not sending any.
  http.get(`${BASE}/nodes/:id/logs`, async ({ params, request }) => {
    await delay(120);
    const id = String(params.id);
    const after = Number(new URL(request.url).searchParams.get('after') ?? '-1');
    const s = summaries.find((x) => x.id === id);
    if (!s) return HttpResponse.json(apiErr('not_found', 'node_not_found'), { status: 404 });
    if (!capsOf(s).agent) return HttpResponse.json(apiErr('internal', 'node_unreachable', { nodeId: id }), { status: 502 });
    // Besu did not send logs to the agent: no lines at all
    if (!capsOf(s).logsConnected) return HttpResponse.json({ epoch: 1, connected: false, lines: [], next: Math.max(after, 0), skipped: 0 });
    const now = Math.floor(Date.now() / 2000); // one block, one line every 2 seconds
    const from = after >= 0 ? after + 1 : now - 40;
    // Block number is computed from that line's time: same timeline as headNow, so each poll keeps growing and never goes backwards
    const behind = s.blocksBehind ?? 0;
    const lines = [];
    for (let seq = from; seq <= now; seq++) {
      const ts = new Date(seq * 2000).toISOString().replace('T', ' ').replace('Z', '+0000');
      const block = (headNow() - Math.floor((Date.now() - seq * 2000) / 2000) - behind).toLocaleString('en-US');
      // A validator producing its own block logs Produced; other nodes receiving a block log Imported (Besu's two actual messages)
      const text = s.isValidator && seq % 3 === 0
        ? `${ts} | BftProcessorExecutor-QBFT-0 | INFO  | QbftBesuControllerBuilder | Produced #${block} / 0 tx / 0 pending / 0 (0.0%) gas`
        : `${ts} | EthScheduler-Workers-0 | ${seq % 17 === 0 ? 'WARN ' : 'INFO '} | PersistBlockTask | Imported #${block} / 0 tx / 0 om / 0 (0.0%) gas / (0x${randHex(8)}…) in 0.004s. Peers: ${s.peerCount ?? 0}`;
      lines.push({ seq, text });
    }
    return HttpResponse.json({ epoch: 1, connected: true, lines, next: lines.length ? now : Math.max(after, 0), skipped: 0 });
  }),

  http.get(`${BASE}/nodes/:id/alerts`, async ({ params }) => {
    await delay(150);
    const id = String(params.id);
    return HttpResponse.json({ alerts: withReads(session.username ?? '', alerts.filter((a) => a.nodeId === id)), monitoringAvailable: !promDownScenario() });
  }),

  // Fleet source status (for the status bar / legend; the real backend decides from Prometheus up)
  http.get(`${BASE}/fleet/status`, async () => {
    await delay(120);
    return HttpResponse.json(fleetStatuses());
  }),

  // ── Identity / permissions ──
  http.get(`${BASE}/auth/config`, async () => {
    await delay(80);
    // Public login page config: Core only has local accounts (the Enterprise mock overrides this)
    return HttpResponse.json({ passwordLogin: true, ldapLogin: false, ssoLoginUrl: null });
  }),
  http.post(`${BASE}/auth/login`, async ({ request }) => {
    await delay(300);
    const { username, password } = (await request.json()) as LoginRequest;
    const u = mockUsers[username];
    // Every attempt logs a login event (newest first); for a nonexistent username, what was typed is not recorded. The source IP is fake, always one office computer
    const record = (reason: LoginFailReason | null) => loginEvents.unshift({
      id: `le-${randHex(6)}`, at: now(), username: u ? username : null, result: reason ? 'failure' : 'success', reason, sourceIp: '10.0.2.15',
    });
    if (!u || u.password !== password) {
      record(u ? 'bad_password' : 'unknown_user');
      return HttpResponse.json(apiErr('unauthorized', 'bad_credentials'), { status: 401 });
    }
    if (u.disabled) {
      record('disabled');
      return HttpResponse.json(apiErr('unauthorized', 'bad_credentials'), { status: 401 });
    }
    // Roles this edition does not have cannot log in (same as the backend's authenticate)
    if (!roleKnown(u.role)) {
      record('no_role');
      return HttpResponse.json(apiErr('unauthorized', 'bad_credentials'), { status: 401 });
    }
    record(null);
    u.lastLoginAt = now(); // last login = this one
    session.me = meForUser(username);
    session.username = username;
    persistSession();
    return HttpResponse.json(session.me);
  }),
  http.get(`${BASE}/me`, async () => {
    await delay(100);
    if (!session.me) return new HttpResponse(null, { status: 401 });
    // Display name and role always come from the current fake data (editing fixtures needs no re-login); password change progress stays in the session
    const u = session.username ? mockUsers[session.username] : undefined;
    if (u) session.me = { ...meForUser(session.username!), mustChangePassword: session.me.mustChangePassword };
    return HttpResponse.json(session.me);
  }),
  http.get(`${BASE}/auth/login-events`, async ({ request }) => {
    await delay(150);
    if (!session.username || !roleCan(mockUsers[session.username]?.role, 'members:manage')) return new HttpResponse(null, { status: 403 });
    const q = new URL(request.url).searchParams;
    const since = q.get('since') ?? '';
    const username = q.get('username');
    const result = q.get('result');
    const all = loginEvents.filter((e) => e.at >= since && (!username || e.username === username) && (!result || e.result === result));
    return HttpResponse.json({ events: all.slice(0, 500), truncated: all.length > 500 });
  }),
  http.post(`${BASE}/auth/logout`, async () => {
    await delay(100);
    session.me = null;
    session.username = null;
    persistSession();
    return new HttpResponse(null, { status: 204 });
  }),
  http.post(`${BASE}/auth/password`, async ({ request }) => {
    await delay(300);
    if (!session.username) return new HttpResponse(null, { status: 401 });
    const { currentPassword, newPassword } = (await request.json()) as PasswordChange;
    const u = mockUsers[session.username];
    if (!u || u.password !== currentPassword) {
      return HttpResponse.json(apiErr('unauthorized', 'wrong_current_password'), { status: 401 });
    }
    // Password rules (same as the backend's auth.ValidatePassword; the mock skips the common-password blocklist)
    const weak = (): ErrorReason | null => {
      if ([...newPassword].length < 12) return 'password_too_short';
      if (new TextEncoder().encode(newPassword).length > 72) return 'password_too_long';
      if (newPassword.toLowerCase().includes(session.username!.toLowerCase())) return 'password_contains_username';
      return null;
    };
    const reason = weak();
    if (reason) return HttpResponse.json(apiErr('invalid', reason), { status: 400 });
    u.password = newPassword;
    u.mustChange = false;
    if (session.me) session.me = { ...session.me, mustChangePassword: false };
    persistSession(); // without saving, after F5 /me would return the old mustChangePassword and bounce back to the change-password page
    if (isFreshDemo() && session.username === 'admin') freshProgress.adminReady = true; // fresh deployment: admin's first login is done
    return new HttpResponse(null, { status: 204 });
  }),
  // Local user management (mock: operates on mockUsers directly; usernames lowercased like the backend; password rules only check length, the backend is authoritative for the full rules)
  http.get(`${BASE}/users`, async () => {
    await delay(150);
    return HttpResponse.json(Object.entries(mockUsers).map(([username, u]) => ({
      username, email: u.email, role: u.role, disabled: !!u.disabled, mustChangePassword: !!u.mustChange, createdAt: new Date().toISOString(), lastLoginAt: u.lastLoginAt ?? null,
    })));
  }),
  // Password mail server (ACCOUNT_SMTP_* at deploy time; the mock treats it as configured)
  http.get(`${BASE}/account-mail`, () => HttpResponse.json({ configured: ACCOUNT_MAIL_CONFIGURED })),
  // Create user: the temporary password is always generated by the platform; only with sendEmail is it mailed (requires the password mail server configured at deploy time), otherwise it is shown once for the admin to hand over
  http.post(`${BASE}/users`, async ({ request }) => {
    await delay(200);
    const body = (await request.json()) as { username: string; email?: string; role: Role; sendEmail?: boolean };
    const { role } = body;
    const username = body.username.toLowerCase();
    const email = (body.email ?? '').trim() || null;
    if (mockUsers[username]) return HttpResponse.json(apiErr('conflict', 'user_exists'), { status: 409 });
    if (email && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) return HttpResponse.json(apiErr('invalid', 'invalid_email'), { status: 400 });
    if (body.sendEmail && !email) return HttpResponse.json(apiErr('invalid', 'email_required_to_send'), { status: 400 });
    if (body.sendEmail && !ACCOUNT_MAIL_CONFIGURED) return HttpResponse.json(apiErr('conflict', 'account_mail_not_configured'), { status: 409 });
    const pw = 'Mock-Generated-Pw-1';
    mockUsers[username] = { password: pw, role, name: username, email, mustChange: true };
    const mail = body.sendEmail ? mockMailFailure(email) : null;
    const delivery = body.sendEmail && !mail ? 'email' : 'shown';
    return HttpResponse.json({
      user: { username, email, role, disabled: false, mustChangePassword: true, createdAt: new Date().toISOString() },
      delivery, generatedPassword: delivery === 'shown' ? pw : null, ...mail,
    }, { status: 201 });
  }),
  http.patch(`${BASE}/users/:username`, async ({ params, request }) => {
    const u = mockUsers[String(params.username).toLowerCase()];
    if (!u) return HttpResponse.json(apiErr('not_found', 'user_not_found'), { status: 404 });
    const b = (await request.json()) as { disabled?: boolean };
    if (b.disabled === undefined) return HttpResponse.json({ code: 'invalid', message: 'missing body' }, { status: 400 });
    if (b.disabled && isLastAdmin(String(params.username).toLowerCase())) return HttpResponse.json(apiErr('conflict', 'last_admin'), { status: 409 });
    u.disabled = b.disabled;
    return HttpResponse.json({ username: params.username, email: u.email, role: u.role, disabled: !!u.disabled, mustChangePassword: !!u.mustChange, createdAt: new Date().toISOString() });
  }),
  http.post(`${BASE}/users/:username/reset-password`, async ({ params, request }) => {
    const u = mockUsers[String(params.username).toLowerCase()];
    if (!u) return HttpResponse.json(apiErr('not_found', 'user_not_found'), { status: 404 });
    // Always system-generated; only with sendEmail is it mailed to the user, otherwise the password is returned for the admin to hand over
    const b = (await request.json().catch(() => ({}))) as { sendEmail?: boolean };
    if (b.sendEmail && !u.email) return HttpResponse.json(apiErr('invalid', 'account_has_no_email'), { status: 400 });
    if (b.sendEmail && !ACCOUNT_MAIL_CONFIGURED) return HttpResponse.json(apiErr('conflict', 'account_mail_not_configured'), { status: 409 });
    u.password = 'Mock-Generated-Pw-2';
    u.mustChange = true;
    const mail = b.sendEmail ? mockMailFailure(u.email) : null;
    const delivery = b.sendEmail && !mail ? 'email' : 'shown';
    return HttpResponse.json({ delivery, email: u.email, generatedPassword: delivery === 'shown' ? u.password : null, ...mail });
  }),
  http.delete(`${BASE}/users/:username`, async ({ params }) => {
    const name = String(params.username).toLowerCase();
    if (!mockUsers[name]) return HttpResponse.json(apiErr('not_found', 'user_not_found'), { status: 404 });
    if (isLastAdmin(name)) return HttpResponse.json(apiErr('conflict', 'last_admin'), { status: 409 });
    delete mockUsers[name];
    return new HttpResponse(null, { status: 204 });
  }),
  // Peer observation: what this node is actually connected to (topology in fixtures rawPeersOf); nodes whose admin_peers cannot be read return reported:false
  http.get(`${BASE}/nodes/:id/peers`, async ({ params }) => {
    await delay(200);
    const id = String(params.id);
    if (!summaries.some((n) => n.id === id)) return HttpResponse.json(apiErr('not_found', 'node_not_found'), { status: 404 });
    const peers = reportedPeersOf(id);
    if (!peers) return HttpResponse.json({ reported: false, peers: [] });
    // Our nodes use their own names (same as the backend's overlayOwnNames), others use peer_names
    const named = peers.map((p) => ({ ...p, name: p.nodeId ? summaries.find((n) => n.id === p.nodeId)?.name ?? null : peerNames[p.address] ?? null }));
    // Viewers also see the remote address, enode and public key; only without monitor:view are just the allowlisted fields returned
    if (!session.me || !roleCan(session.me.role, 'monitor:view')) {
      return HttpResponse.json({ reported: true, peers: named.map(({ address, inbound, nodeId, name, validatorAddress }) => ({ address, inbound, nodeId, name, validatorAddress })) });
    }
    return HttpResponse.json({ reported: true, peers: named });
  }),
  // Transaction pool: any logged-in user can view; returns 502 when unreadable (same as the backend)
  http.get(`${BASE}/nodes/:id/txpool`, async ({ params, request }) => {
    await delay(200);
    const id = String(params.id);
    if (!summaries.some((n) => n.id === id)) return HttpResponse.json(apiErr('not_found', 'node_not_found'), { status: 404 });
    // Agent not connected: the backend returns the most fundamental cause (node unreachable)
    if (txpoolUnreachable(id)) return HttpResponse.json(apiErr('internal', 'node_unreachable', { nodeId: id }), { status: 502 });
    const txSum = summaries.find((n) => n.id === id);
    if (txSum && capsOf(txSum).missingRpcApis.includes('TXPOOL')) return HttpResponse.json(apiErr('internal', 'txpool_api_disabled'), { status: 502 });
    return HttpResponse.json(txpoolOf(id, new URL(request.url).searchParams.get('from') ?? undefined));
  }),
  http.get(`${BASE}/shared-peers`, async () => {
    await delay(300);
    return HttpResponse.json(mockSharedPeers());
  }),
  http.post(`${BASE}/manual-peers`, async ({ request }) => {
    const { enode } = (await request.json()) as { enode: string };
    const m = /^enode:\/\/([0-9a-fA-F]{128})@(\d+\.\d+\.\d+\.\d+:\d+)/.exec(enode.trim());
    if (!m || !onCurve(m[1])) return HttpResponse.json(enodeErr(enode.trim()), { status: 400 });
    const pubkey = m[1].toLowerCase();
    if (manualPeers.some((p) => p.pubkey === pubkey)) return HttpResponse.json(apiErr('conflict', 'manual_peer_exists'), { status: 409 });
    if (manualPeers.length >= PEER_LIMIT) return HttpResponse.json(apiErr('invalid', 'manual_peer_list_full', { limit: String(PEER_LIMIT) }), { status: 400 });
    const p: ManualPeer = { pubkey, address: addressOfPub(pubkey), enode: `enode://${pubkey}@${m[2]}`, createdAt: new Date().toISOString() };
    manualPeers.push(p);
    return HttpResponse.json(p, { status: 201 });
  }),
  http.delete(`${BASE}/manual-peers/:pubkey`, async ({ params }) => {
    const i = manualPeers.findIndex((p) => p.pubkey === String(params.pubkey));
    if (i < 0) return HttpResponse.json(apiErr('not_found', 'manual_peer_not_found'), { status: 404 });
    manualPeers.splice(i, 1);
    return new HttpResponse(null, { status: 204 });
  }),
  http.get(`${BASE}/nodes/:id/custom-peers`, async ({ params }) => {
    const id = String(params.id);
    const s = summaries.find((n) => n.id === id);
    if (!s) return HttpResponse.json(apiErr('not_found', 'node_not_found'), { status: 404 });
    return HttpResponse.json(customView(s));
  }),
  http.put(`${BASE}/nodes/:id/peer-shared`, async ({ params, request }) => {
    await delay(400);
    const s = summaries.find((n) => n.id === String(params.id));
    if (!s) return HttpResponse.json(apiErr('not_found', 'node_not_found'), { status: 404 });
    const { useShared } = (await request.json()) as { useShared: boolean };
    peerUseShared[s.id] = useShared;
    syncPeerMode(s);
    return HttpResponse.json(customView(s));
  }),
  // Self-added: can be added whether the toggle is on or not (replaces "only a custom list can add")
  http.post(`${BASE}/nodes/:id/custom-peers`, async ({ params, request }) => {
    const s = summaries.find((n) => n.id === String(params.id));
    if (!s) return HttpResponse.json(apiErr('not_found', 'node_not_found'), { status: 404 });
    const list = (customPeers[s.id] ??= []);
    const { enode } = (await request.json()) as { enode: string };
    const m = /^enode:\/\/([0-9a-fA-F]{128})@(\d+\.\d+\.\d+\.\d+:\d+)/.exec(enode.trim());
    if (!m || !onCurve(m[1])) return HttpResponse.json(enodeErr(enode.trim()), { status: 400 });
    const pubkey = m[1].toLowerCase();
    if (list.some((p) => p.pubkey === pubkey)) return HttpResponse.json(apiErr('conflict', 'peer_exists'), { status: 409 });
    if (usesSharedPeers(s.id) && manualPeers.some((p) => p.pubkey === pubkey)) return HttpResponse.json(apiErr('conflict', 'peer_in_shared_list'), { status: 409 });
    const own = summaries.find((n) => ownPubOf(n) === pubkey);
    if (own?.id === s.id) return HttpResponse.json(apiErr('invalid', 'peer_is_self', { nodeId: own.id }), { status: 400 });
    if (list.length >= PEER_LIMIT) return HttpResponse.json(apiErr('invalid', 'peer_list_full', { limit: String(PEER_LIMIT) }), { status: 400 });
    const p: CustomPeer = { pubkey, enode: `enode://${pubkey}@${m[2]}`, nodeId: own?.id ?? null, name: own?.name ?? null, quarantined: false };
    list.push(p);
    syncPeerMode(s);
    return HttpResponse.json(p, { status: 201 });
  }),
  http.delete(`${BASE}/nodes/:id/custom-peers/:pubkey`, async ({ params }) => {
    const list = customPeers[String(params.id)] ?? [];
    const i = list.findIndex((p) => p.pubkey === String(params.pubkey));
    if (i < 0) return HttpResponse.json(apiErr('not_found', 'peer_not_found'), { status: 404 });
    list.splice(i, 1);
    const s = summaries.find((n) => n.id === String(params.id));
    if (s) syncPeerMode(s);
    return new HttpResponse(null, { status: 204 });
  }),
  http.put(`${BASE}/peer-names/:address`, async ({ params, request }) => {
    const { name } = (await request.json()) as { name: string };
    peerNames[String(params.address).toLowerCase()] = name;
    return new HttpResponse(null, { status: 204 });
  }),
  http.delete(`${BASE}/peer-names/:address`, async ({ params }) => {
    delete peerNames[String(params.address).toLowerCase()];
    return new HttpResponse(null, { status: 204 });
  }),
  http.get(`${BASE}/account-names`, async () => HttpResponse.json(accountNames)),
  http.put(`${BASE}/account-names/:address`, async ({ params, request }) => {
    const { name } = (await request.json()) as { name: string };
    accountNames[String(params.address).toLowerCase()] = name;
    return new HttpResponse(null, { status: 204 });
  }),
  http.delete(`${BASE}/account-names/:address`, async ({ params }) => {
    delete accountNames[String(params.address).toLowerCase()];
    return new HttpResponse(null, { status: 204 });
  }),

  http.get(`${BASE}/join-tokens`, async () => {
    await delay(200);
    return HttpResponse.json(joinTokens);
  }),

  http.post(`${BASE}/join-tokens`, async ({ request }) => {
    await delay(400);
    const body = (await request.json()) as JoinTokenCreateRequest;
    body.nodeId = (body.nodeId ?? '').trim().toLowerCase(); // same as the backend's normNodeID: lowercase before checking
    if (!/^[a-z0-9][a-z0-9-]{0,62}$/.test(body.nodeId)) return HttpResponse.json(apiErr('invalid', 'field_format', { field: 'nodeId' }), { status: 400 });
    const created = makeJoinToken(body);
    // Nodes already in the list are not simulated (join would be blocked), so their fake data is not overwritten. Nodes simulated by the previous code
    // are removed first and the timer restarts; otherwise a regenerated code would show as joined as soon as it reached the confirm step (the mock "connects" on its own once time is up).
    if (mockJoins[body.nodeId]) {
      const i = summaries.findIndex((n) => n.id === body.nodeId);
      if (i >= 0) summaries.splice(i, 1);
      delete details[body.nodeId];
    }
    revokeUnusedCodes(body.nodeId); // same as the backend: a node keeps only its newest unused code
    if (!summaries.some((n) => n.id === body.nodeId)) mockJoins[body.nodeId] = { at: Date.now(), name: body.nodeName ?? null, rpcPort: body.rpcPort ?? 8545, metricsPort: body.metricsPort ?? 9545, ip: body.allowedSources?.[0]?.replace(/\/\d+$/, '') || '10.0.0.20', pub: pubFromSeed(randHex(16)) };
    // The list keeps only metadata; the token appears only once, in this response
    const { token: _t, platformAddr: _p, ...listItem } = created;
    joinTokens.unshift(listItem);
    return HttpResponse.json(created, { status: 201 });
  }),

  http.post(`${BASE}/join-tokens/:id/revoke`, async ({ params }) => {
    await delay(300);
    const jt = joinTokens.find((t) => t.id === String(params.id));
    if (!jt) {
      return HttpResponse.json(apiErr('not_found', 'join_token_not_found'), { status: 404 });
    }
    if (jt.nodeId) mockJoinProgress(jt.nodeId); // if the simulated join has already happened, the code is used: 409 like the backend
    // Expired ones can also be revoked ("Remove" on the node list's "Join code expired" row); used or revoked ones cannot
    if (jt.status !== 'pending' && jt.status !== 'expired') {
      return HttpResponse.json(apiErr('conflict', 'join_token_not_revocable'), { status: 409 });
    }
    jt.status = 'revoked';
    if (jt.nodeId) delete mockJoins[jt.nodeId]; // stop the simulated join for a revoked code
    return HttpResponse.json(jt);
  }),

  // ── Validator governance (on-chain validator set + votes on our own validators) ──
  http.get(`${BASE}/chain`, async () => {
    await delay(120);
    tickJoins();
    return HttpResponse.json(chainInfo());
  }),

  // On-chain nodes: validator set ∪ peers seen by each of our nodes, minus our own; names overlaid from peerNames
  http.get(`${BASE}/chain/nodes`, async () => {
    await delay(150);
    tickJoins();
    // Listed from the whole chain's point of view: our own nodes are included, marked with isOwn (name is the platform-registered one, peer_names not overlaid)
    const own = new Set(summaries.map((n) => n.id));
    const byAddr = new Map<string, { address: string; isValidator: boolean; producing: boolean | null; connected: number; name: string | null; isOwn: boolean; nodeId: string | null }>();
    for (const v of currentValidators()) {
      const isOwn = !!v.nodeId && own.has(v.nodeId);
      byAddr.set(v.address.toLowerCase(), { address: v.address.toLowerCase(), isValidator: true, producing: v.producing ?? null, connected: 0, name: isOwn ? v.nodeId ?? null : v.name ?? null, isOwn, nodeId: isOwn ? v.nodeId ?? null : null });
    }
    for (const n of summaries) {
      const a = (IDENTITIES[n.id as keyof typeof IDENTITIES]?.addr ?? n.address)?.toLowerCase();
      if (!a) continue; // not in the identity table and no reported address (just registered, not connected yet): not visible on chain yet
      if (!byAddr.has(a)) byAddr.set(a, { address: a, isValidator: false, producing: null, connected: 0, name: n.id, isOwn: true, nodeId: n.id }); // our own node's name is its id (nodes.name is a note and does not appear in the on-chain list)
      for (const p of reportedPeersOf(n.id) ?? []) {
        if (p.nodeId) continue; // our own nodes are already listed above
        const pa = p.address.toLowerCase();
        const cur = byAddr.get(pa) ?? { address: pa, isValidator: !!p.validatorAddress, producing: null, connected: 0, name: null, isOwn: false, nodeId: null };
        cur.connected++;
        byAddr.set(pa, cur);
      }
    }
    // Chain unreadable: the backend cannot get the validator list, so isValidator is false for every node
    if (chainUnreadableScenario()) for (const v of byAddr.values()) { v.isValidator = false; v.producing = null; }
    // Two on-chain signals (the backend computes them from block extraData): validators take turns producing, so validator i last produced at head - i;
    // the one not participating properly follows the turns script: last produced 7 rounds ago, and missed all 7 of its turns since
    const vals = currentValidators();
    const head = headNow();
    const period = (chainInfo().blockPeriodSeconds ?? 2) * 1000;
    const signal = (addr: string) => {
      const i = vals.findIndex((v) => v.address.toLowerCase() === addr);
      if (i < 0) return { lastProposed: null, missedTurns1000: null };
      const v = vals[i];
      if (v.producing === false) {
        const height = head - 7 * vals.length - i;
        return { lastProposed: { height, at: new Date(Date.now() - (head - height) * period).toISOString() }, missedTurns1000: 7 };
      }
      // org-e validator 2 occasionally misses a round, the others never do (matches chainInfo's roundChanges1h)
      const missedTurns1000 = addr === IDENTITIES['org-e-v2'].addr.toLowerCase() ? 1 : 0;
      return { lastProposed: { height: head - i, at: new Date(Date.now() - i * period).toISOString() }, missedTurns1000 };
    };
    // Blocks behind: our nodes use their own report; for others it can only be estimated if one of our nodes is connected to them. The org-c regular node n9 is a few blocks behind (over the node lag threshold of 5), the rest keep up
    const behindOf = (x: { address: string; isOwn: boolean; nodeId: string | null; connected: number }) => {
      if (x.isOwn) {
        const s = summaries.find((n) => n.id === x.nodeId);
        return s?.chainHeight != null ? s.blocksBehind ?? 0 : null;
      }
      if (x.connected === 0) return null;
      return x.address === IDENTITIES['org-c-n9'].addr.toLowerCase() ? 8 : 0;
    };
    // Validators first; within each group our own first, then those connected to us, then those only seen on chain (same order as the backend)
    const sorted = [...byAddr.values()].sort((a, b) => Number(b.isValidator) - Number(a.isValidator) || Number(b.isOwn) - Number(a.isOwn) || b.connected - a.connected || a.address.localeCompare(b.address));
    const list: ChainNode[] = sorted.map((x) => ({
      address: x.address, name: x.isOwn ? x.name : peerNames[x.address] ?? x.name, isValidator: x.isValidator, isOwn: x.isOwn, nodeId: x.nodeId, producing: x.producing,
      ...(x.isValidator ? signal(x.address) : { lastProposed: null, missedTurns1000: null }),
      blocksBehind: behindOf(x),
    }));
    return HttpResponse.json(list);
  }),

  http.get(`${BASE}/validators`, async () => {
    await delay(150);
    tickJoins();
    // Overlay user-assigned peer names (same as the backend's peerNames overlay)
    return HttpResponse.json(currentValidators().map((v) => ({ ...v, name: peerNames[v.address.toLowerCase()] ?? v.name ?? null })));
  }),

  // Active votes (mock): our sent votes are kept in memory and count as in a block after 8 seconds, plus one other org's vote already counted; reaching the threshold takes effect and the row disappears;
  // a row proposed by another org that we have not voted on: remove the other org's validator that stopped producing. Rows already reflected in the validator set are not listed (same as the backend).
  // To see "no votes": set localStorage['mock:noVotes']='1' in the console and reload
  // Vote history (mock): a few closed proposals; must come before /validator-votes/:address so it is not taken as an address
  http.get(`${BASE}/validator-votes/history`, async () => {
    await delay(150);
    return HttpResponse.json([...mockHistory, ...(seededHistory ??= validatorVoteHistory())]);
  }),
  http.get(`${BASE}/validator-votes`, async () => {
    await delay(150);
    tickJoins();
    const set = currentValidators();
    const noVotes = (() => { try { return !!localStorage.getItem('mock:noVotes'); } catch { return false; } })();
    const inSet = new Set(set.map((v) => v.address.toLowerCase()));
    const own = set.filter((v) => v.orgId === 'org-a' && v.nodeId);
    const ext = set.filter((v) => !v.nodeId);
    const head = chainInfo().height ?? 0;
    // Proposals from other orgs (we have not voted yet), each with some other-org votes already in blocks:
    // 1. Add a new validator: 5 votes so far, a majority needs 7, missing just our two (after we follow the vote they land in 8 seconds, the threshold is reached and the row disappears)
    // 2. Remove the other org's validator that stopped producing: 3 votes
    const seeded: Record<string, { add: boolean; ext: number }> = { [SEED_CANDIDATE]: { add: true, ext: 5 } };
    const down = ext.find((v) => v.producing === false);
    if (down) seeded[down.address.toLowerCase()] = { add: false, ext: 3 };
    const needed = Math.floor(set.length / 2) + 1;
    const counted = (at: number) => (Date.now() - at > 8_000 ? head - 2 : null);
    // Time of that block: one block every 2 seconds, counted back from the chain head
    const blockTime = (b: number | null) => (b == null ? null : new Date(Date.now() - (head - b) * 2000).toISOString());
    const votes = Object.entries({ ...Object.fromEntries(Object.entries(seeded).map(([a, v]) => [a, v.add])), ...Object.fromEntries(Object.entries(mockVotes).map(([a, v]) => [a, v.add])) })
      .filter(([address, add]) => inSet.has(address) !== add)
      .map(([address, add]) => {
        const mine = mockVotes[address];
        const others = ext.filter((v) => v.address.toLowerCase() !== address);
        // Other orgs' votes: seed proposals use the configured count; one we just proposed has no one else's votes yet, only ours (first "Going on-chain…", in a block after 8 seconds)
        const extVotes = others.slice(0, seeded[address]?.ext ?? 0);
        const voters = [
          ...(mine ? own.map((v) => {
            const since = mine.withdrawingSince;
            const done = since != null && Date.now() - since > 8_000; // the opposite vote is in a block: this node's vote no longer counts
            return { address: v.address.toLowerCase(), nodeId: v.nodeId,
              countedAt: done ? null : counted(mine.at), countedAtTime: done ? null : blockTime(counted(mine.at)),
              pending: since == null, withdrawing: since != null && !done, withdrawnAt: done ? new Date(since + 8_000).toISOString() : null };
          }) : []),
          ...extVotes.map((v, i) => ({ address: v.address.toLowerCase(), nodeId: null, countedAt: head - 300 + i * 37, countedAtTime: blockTime(head - 300 + i * 37), pending: false })),
        ];
        return { address, add, voters: voters.sort((a, b) => a.address.localeCompare(b.address)) };
      })
      // Counted votes reaching the threshold = takes effect: once the validator set reflects it the row leaves voting (the mock does not change the validator set, so it just drops the row) and it is recorded in the vote history
      .filter((v) => {
        const done = v.voters.filter((x) => x.countedAt != null);
        if (!mockClosed[v.address] && done.length >= needed) {
          const byBlock = [...done].sort((a, b) => a.countedAt! - b.countedAt!);
          const ballotOf = new Map(done.map((x) => [x.address, x] as const));
          mockClosed[v.address] = true;
          mockHistory.unshift({
            address: v.address, add: v.add, result: 'passed',
            firstVoteAt: byBlock[0].countedAtTime!,
            endedAt: byBlock[byBlock.length - 1].countedAtTime!, endedBlock: byBlock[byBlock.length - 1].countedAt!,
            votes: done.length, needed,
            ballots: set.map((s) => ({ address: s.address.toLowerCase(), own: s.orgId === 'org-a' && !!s.nodeId, name: s.name ?? null,
              votedAt: ballotOf.get(s.address.toLowerCase())?.countedAtTime ?? null, block: ballotOf.get(s.address.toLowerCase())?.countedAt ?? null, withdrawnAt: null }))
              .sort((a, b) => a.address.localeCompare(b.address)),
          });
        }
        // Count back to zero: our withdrawal is done and nobody else voted -> not passed. It ends at the block where our last vote was cancelled
        const mine = mockVotes[v.address];
        const open = v.voters.some((x) => x.countedAt != null || ('withdrawing' in x && x.withdrawing) || (x.nodeId != null && 'pending' in x && x.pending));
        if (!mockClosed[v.address] && !open && mine?.withdrawingSince != null) {
          const votedMs = mine.at + 8_000, endMs = mine.withdrawingSince + 8_000;
          const blockAt = (ms: number) => head - Math.round((Date.now() - ms) / 2000);
          mockClosed[v.address] = true;
          mockHistory.unshift({
            address: v.address, add: v.add, result: 'expired',
            firstVoteAt: new Date(votedMs).toISOString(),
            endedAt: new Date(endMs).toISOString(), endedBlock: blockAt(endMs),
            votes: 0, needed,
            // Our vote was in a block and later withdrawn: stays under voted with the withdrawal time, same as the seed history entries
            ballots: set.map((s) => {
              const isOwn = own.some((o) => o.address.toLowerCase() === s.address.toLowerCase());
              return { address: s.address.toLowerCase(), own: s.orgId === 'org-a' && !!s.nodeId, name: s.name ?? null,
                votedAt: isOwn ? new Date(votedMs).toISOString() : null, block: isOwn ? blockAt(votedMs) : null, withdrawnAt: isOwn ? new Date(endMs).toISOString() : null };
            }).sort((a, b) => a.address.localeCompare(b.address)),
          });
        }
        return !mockClosed[v.address];
      });
    return HttpResponse.json({ validatorCount: set.length, needed, ownValidators: own.map((v) => v.nodeId!), votes: noVotes ? [] : votes, tallyKnown: !epochUnknownScenario() });
  }),
  http.post(`${BASE}/validator-votes`, async ({ request }) => {
    await delay(200);
    const { address, add } = (await request.json()) as { address: string; add: boolean };
    if (!isAddress(address)) return HttpResponse.json(apiErr('invalid', 'field_format', { field: 'address' }), { status: 400 });
    // Block votes whose direction matches the current validator set (same as the backend's noopVote)
    const inSet = currentValidators().some((v) => v.address.toLowerCase() === address.toLowerCase());
    if (add && inSet) return HttpResponse.json(apiErr('conflict', 'already_validator'), { status: 409 });
    if (!add && !inSet) return HttpResponse.json(apiErr('conflict', 'not_validator'), { status: 409 });
    if (ownValidatorIds().length === 0) return HttpResponse.json(apiErr('conflict', 'no_validator_nodes'), { status: 409 });
    mockVotes[address.toLowerCase()] = { add, at: Date.now() };
    delete mockClosed[address.toLowerCase()]; // same address proposed again: a new round of voting
    // Validators whose agent is not connected cannot send (same as the backend: that node carries node_unreachable)
    return HttpResponse.json({ results: ownValidatorIds().map((nodeId) => {
      const s = summaries.find((n) => n.id === nodeId);
      return s && !capsOf(s).agent
        ? { nodeId, ok: false, reason: 'node_unreachable' as const, params: { nodeId }, error: `Node ${nodeId} can’t be reached right now` }
        : { nodeId, ok: true };
    }) });
  }),
  http.delete(`${BASE}/validator-votes/:address`, async ({ params }) => {
    await delay(150);
    if (!isAddress(String(params.address))) return HttpResponse.json(apiErr('invalid', 'field_format', { field: 'address' }), { status: 400 });
    if (ownValidatorIds().length === 0) return HttpResponse.json(apiErr('conflict', 'no_validator_nodes'), { status: 409 });
    const a = String(params.address).toLowerCase();
    // Not in a block yet: just drop it, as if never voted. Already in a block: the platform casts an opposite vote for our org to cancel it, and the count drops once that lands (withdrawing -> withdrawn)
    if (mockVotes[a] && Date.now() - mockVotes[a].at > 8_000) mockVotes[a].withdrawingSince = Date.now();
    else delete mockVotes[a];
    return HttpResponse.json({ results: ownValidatorIds().map((nodeId) => ({ nodeId, ok: true })) });
  }),

  // ── Alerts (Prometheus/Alertmanager) ──
  // Read state is per user (see alertReads in fixtures)
  http.post(`${BASE}/alerts/read`, async ({ request }) => {
    const { ids } = (await request.json()) as { ids: string[] };
    markAlertsReadFixture(session.username ?? '', ids);
    return new HttpResponse(null, { status: 204 });
  }),
  http.post(`${BASE}/alerts/read-all`, async () => {
    markAlertsReadFixture(session.username ?? '', null);
    return new HttpResponse(null, { status: 204 });
  }),
  http.get(`${BASE}/alerts`, async ({ request }) => {
    await delay(150);
    const url = new URL(request.url);
    const status = url.searchParams.get('status');
    const nodeId = url.searchParams.get('nodeId');
    const alertname = url.searchParams.get('alertname');
    const since = url.searchParams.get('since');
    const filtered = alerts
      .filter((a) => !status || a.status === status)
      .filter((a) => !nodeId || a.nodeId === nodeId)
      .filter((a) => !alertname || a.alertname === alertname)
      .filter((a) => !since || !a.endsAt || a.endsAt >= since)
      .sort((a, b) => new Date(b.startsAt).getTime() - new Date(a.startsAt).getTime());
    return HttpResponse.json({ alerts: withReads(session.username ?? '', filtered), monitoringAvailable: !promDownScenario() });
  }),

  // ── Org-level besu account allowlist (saving pushes perm_* to each node and returns per-node results) ──
  http.get(`${BASE}/permissioning`, async () => {
    await delay(150);
    return HttpResponse.json({ ...permissioningPolicy, nodes: policyApplyFixture(), custom: Object.values(customAllowlists).map((c) => ({ ...c, useShared: c.useShared !== false })) });
  }),

  http.put(`${BASE}/permissioning`, async ({ request }) => {
    await delay(1500); // the platform hits all nodes at once and waits for the slowest, so "Applying" is visible
    const body = await request.json() as PermissioningPolicy;
    if (!Array.isArray(body.accountsAllowlist) || body.accountsAllowlist.some(a => !/^0x[0-9a-fA-F]{40}$/.test(a))) return HttpResponse.json(apiErr('invalid', 'field_format', { field: `accountsAllowlist.${body.accountsAllowlist.findIndex(a => !/^0x[0-9a-fA-F]{40}$/.test(a))}` }), { status: 400 });
    if (body.revision !== (permissioningPolicy.revision ?? 0)) return HttpResponse.json(apiErr('conflict', 'policy_stale'), { status: 409 });
    // The shared list may be saved empty: the UI warns first (only each node's self-added entries are accepted; nodes with none will reject all transactions)
    // a3 in the normal scenario: the node rejects the write (no write permission on the allowlist file), keeps the old list, and a later GET shows the list as inconsistent
    const refused = allowlistScenario() === 'normal' ? 'node-a3' : '';
    if (refused && !nodeAllowlists[refused]) nodeAllowlists[refused] = policyApplyFixture().find(r => r.nodeId === refused)?.currentAllowlist ?? [];
    const revision = (permissioningPolicy.revision ?? 0) + 1;
    Object.assign(permissioningPolicy, { accountsAllowlist: [...new Set(body.accountsAllowlist.map(a => a.toLowerCase()))].sort(), revision });
    // Only push to nodes applying the shared list (same as the real backend); nodes with the toggle off are not in the response
    const results = policyApplyFixture().filter((r) => usesShared(r.nodeId)).map((r): PolicyApplyNodeResult => {
      if (r.nodeId === refused) return { nodeId: r.nodeId, state: 'rejected', reason: 'allowlist_persist_failed', error: 'The node cannot write its allowlist file; check that the file exists and the besu user can write to it.' };
      if (r.state === 'in_sync' || r.state === 'drift') { delete nodeAllowlists[r.nodeId]; return { ...r, state: 'in_sync', currentAllowlist: wantOf(r.nodeId) }; }
      return r;
    });
    return HttpResponse.json({ policyRevision: revision, nodes: results });
  }),

  // A node's extra addresses (added on top of the shared list): revision 0 = create; only pushed to this node
  http.put(`${BASE}/nodes/:id/allowlist`, async ({ params, request }) => {
    await delay(800);
    const id = params.id as string;
    const body = await request.json() as PermissioningPolicy;
    if (!Array.isArray(body.accountsAllowlist) || body.accountsAllowlist.some(a => !/^0x[0-9a-fA-F]{40}$/.test(a))) return HttpResponse.json(apiErr('invalid', 'field_format', { field: `accountsAllowlist.${body.accountsAllowlist.findIndex(a => !/^0x[0-9a-fA-F]{40}$/.test(a))}` }), { status: 400 });
    const cur = customAllowlists[id];
    if (body.revision !== (cur?.revision ?? 0)) return HttpResponse.json(apiErr('conflict', 'policy_stale'), { status: 409 });
    const before = policyApplyFixture().find(r => r.nodeId === id);
    if (!cur && before?.state === 'disabled') return HttpResponse.json(apiErr('conflict', 'allowlist_disabled', { nodeId: id }), { status: 409 });
    if (!cur && (!before || before.state === 'unreachable')) return HttpResponse.json(apiErr('conflict', 'node_unreachable', { nodeId: id }), { status: 409 });
    // When applying the shared list, entries already in it need not be stored separately; when not applying, store them as is
    const shared = new Set(usesShared(id) ? permissioningPolicy.accountsAllowlist : []);
    customAllowlists[id] = { nodeId: id, revision: (cur?.revision ?? 0) + 1, accountsAllowlist: normAccounts(body.accountsAllowlist).filter((a) => !shared.has(a)), ...(cur?.useShared === false ? { useShared: false } : {}) };
    delete nodeAllowlists[id];
    return HttpResponse.json({ policyRevision: customAllowlists[id].revision, nodes: policyApplyFixture().filter(r => r.nodeId === id) });
  }),
  http.put(`${BASE}/nodes/:id/allowlist/shared`, async ({ params, request }) => {
    await delay(800);
    const id = params.id as string;
    const { useShared } = await request.json() as { useShared: boolean };
    const before = policyApplyFixture().find(r => r.nodeId === id);
    if (!before || before.state === 'unreachable') return HttpResponse.json(apiErr('conflict', 'node_unreachable', { nodeId: id }), { status: 409 });
    if (before.state === 'disabled') return HttpResponse.json(apiErr('conflict', 'allowlist_disabled', { nodeId: id }), { status: 409 });
    const cur = customAllowlists[id];
    if (useShared) {
      // Switching back to applying: drop extra addresses that duplicate the shared list; if none remain, delete the whole entry
      const shared = new Set(permissioningPolicy.accountsAllowlist);
      const extra = (cur?.accountsAllowlist ?? []).filter((a) => !shared.has(a));
      if (extra.length) customAllowlists[id] = { nodeId: id, revision: (cur?.revision ?? 0) + 1, accountsAllowlist: extra };
      else delete customAllowlists[id];
    } else {
      customAllowlists[id] = { nodeId: id, revision: (cur?.revision ?? 0) + 1, accountsAllowlist: cur?.accountsAllowlist ?? [], useShared: false };
    }
    delete nodeAllowlists[id];
    return HttpResponse.json({ policyRevision: customAllowlists[id]?.revision ?? 0, nodes: policyApplyFixture().filter(r => r.nodeId === id) });
  }),
  http.post(`${BASE}/nodes/:id/allowlist/sync`, async ({ params }) => {
    await delay(800);
    const id = params.id as string;
    const before = policyApplyFixture().find(r => r.nodeId === id);
    if (!before || before.state === 'unreachable') return HttpResponse.json(apiErr('conflict', 'node_unreachable', { nodeId: id }), { status: 409 });
    if (before.state === 'disabled') return HttpResponse.json(apiErr('conflict', 'allowlist_disabled', { nodeId: id }), { status: 409 });
    delete nodeAllowlists[id];
    return HttpResponse.json({ policyRevision: permissioningPolicy.revision, nodes: policyApplyFixture().filter(r => r.nodeId === id) });
  }),
  http.delete(`${BASE}/nodes/:id/allowlist`, async ({ params }) => {
    await delay(800);
    const id = params.id as string;
    if (!customAllowlists[id]) return HttpResponse.json(apiErr('not_found', 'allowlist_no_extras'), { status: 404 });
    // Nodes not applying the shared list: only clear the extra addresses, keep the toggle (list becomes empty)
    if (customAllowlists[id].useShared === false) customAllowlists[id] = { ...customAllowlists[id], revision: customAllowlists[id].revision + 1, accountsAllowlist: [] };
    else delete customAllowlists[id];
    delete nodeAllowlists[id];
    return HttpResponse.json({ policyRevision: permissioningPolicy.revision, nodes: policyApplyFixture().filter(r => r.nodeId === id) });
  }),

  http.get(`${BASE}/alert-settings`, async () => {
    await delay(150);
    return alertSettingsView();
  }),

  // Each channel is written independently and the full updated settings are returned (same as the real backend); changing the webhook does not touch SMTP, and vice versa.
  http.put(`${BASE}/alert-settings/smtp`, async ({ request }) => {
    await delay(200);
    const body = (await request.json()) as SmtpSettings;
    if (body.username && !body.password) {
      return HttpResponse.json(apiErr('invalid', 'smtp_password_required'), { status: 400 });
    }
    const badAddr = smtpAddressError(body);
    if (badAddr) return badAddr;
    // Without a username there is no login, so the server drops any password it is sent
    alertSettingsState.smtp = { ...body, password: body.username ? body.password : '', security: smtpSecurity(body) };
    return alertSettingsView();
  }),

  http.delete(`${BASE}/alert-settings/smtp`, async () => {
    await delay(200);
    alertSettingsState.smtp = null;
    return alertSettingsView();
  }),

  http.put(`${BASE}/alert-settings/webhooks`, async ({ request }) => {
    await delay(200);
    const body = (await request.json()) as AlertWebhookUrl[];
    const noName = body.findIndex((w) => !w.label.trim());
    if (noName >= 0) return HttpResponse.json(apiErr('invalid', 'webhook_name_required', { index: String(noName + 1) }), { status: 400 });
    const badUrl = body.findIndex((w) => !/^https?:\/\/.+/.test(w.url.trim()));
    if (badUrl >= 0) return HttpResponse.json(apiErr('invalid', 'webhook_url_invalid', { index: String(badUrl + 1) }), { status: 400 });
    alertSettingsState.webhookUrls = body;
    return alertSettingsView();
  }),

  http.put(`${BASE}/alert-settings/telegram`, async ({ request }) => {
    await delay(200);
    const body = (await request.json()) as TelegramSettings;
    if (!body.botToken || !body.chatId) {
      return HttpResponse.json(apiErr('invalid', 'telegram_fields_required'), { status: 400 });
    }
    if (body.messageThreadId != null && body.messageThreadId <= 0) return HttpResponse.json(apiErr('invalid', 'telegram_thread_id_invalid'), { status: 400 });
    alertSettingsState.telegram = body;
    return alertSettingsView();
  }),

  http.delete(`${BASE}/alert-settings/telegram`, async () => {
    await delay(200);
    alertSettingsState.telegram = null;
    return alertSettingsView();
  }),

  http.put(`${BASE}/alert-settings/slack`, async ({ request }) => {
    await delay(200);
    const body = (await request.json()) as SlackSettings;
    if (!body.webhookUrl) return HttpResponse.json(apiErr('invalid', 'slack_url_required'), { status: 400 });
    if (!/^https:\/\/hooks\.slack(-gov)?\.com\/.+/i.test(body.webhookUrl.trim())) {
      return HttpResponse.json(apiErr('invalid', 'slack_url_invalid'), { status: 400 });
    }
    alertSettingsState.slack = body;
    return alertSettingsView();
  }),

  http.delete(`${BASE}/alert-settings/slack`, async () => {
    await delay(200);
    alertSettingsState.slack = null;
    return alertSettingsView();
  }),

  // Same checks as the backend: the language is an enum, a non-empty zone must be one the runtime knows (Intl stands in for Go's time.LoadLocation)
  http.put(`${BASE}/alert-settings/locale`, async ({ request }) => {
    await delay(200);
    const body = (await request.json()) as AlertLocale;
    if (body.language !== 'zh' && body.language !== 'en') return HttpResponse.json(apiErr('invalid', 'field_not_allowed', { field: 'language' }), { status: 400 });
    const zone = body.timezone?.trim() ?? ''; // '' = follow the platform host
    try {
      if (zone === 'Local') throw new RangeError();
      if (zone) new Intl.DateTimeFormat('en', { timeZone: zone });
    } catch {
      return HttpResponse.json(apiErr('invalid', 'field_format', { field: 'timezone' }), { status: 400 });
    }
    alertSettingsState.locale = { language: body.language, timezone: zone, effectiveTimezone: zone || Intl.DateTimeFormat().resolvedOptions().timeZone };
    return alertSettingsView();
  }),

  // The mock never actually sends: a token containing bad is invalid, chat id 1 is not found, the rest succeed; for demoing error messages.
  http.post(`${BASE}/alert-settings/test-telegram`, async ({ request }) => {
    await delay(700);
    const body = (await request.json()) as TelegramSettings;
    if (!body.botToken || !body.chatId) return HttpResponse.json(apiErr('invalid', 'telegram_fields_required'), { status: 400 });
    if (body.botToken.includes('bad')) {
      return HttpResponse.json(apiErr('invalid', 'telegram_token_invalid'), { status: 400 });
    }
    if (body.chatId === 1) {
      return HttpResponse.json(apiErr('invalid', 'telegram_chat_not_found'), { status: 400 });
    }
    return new HttpResponse(null, { status: 204 });
  }),

  http.post(`${BASE}/alert-settings/test-slack`, async ({ request }) => {
    await delay(600);
    const body = (await request.json()) as SlackSettings;
    if (!body.webhookUrl) return HttpResponse.json(apiErr('invalid', 'slack_url_required'), { status: 400 });
    if (!/^https:\/\/hooks\.slack(-gov)?\.com\/.+/i.test(body.webhookUrl.trim())) {
      return HttpResponse.json(apiErr('invalid', 'slack_url_invalid'), { status: 400 });
    }
    if (body.webhookUrl.includes('bad')) {
      return HttpResponse.json(apiErr('invalid', 'slack_http_status', { status: '403', body: 'invalid_token' }), { status: 400 });
    }
    return new HttpResponse(null, { status: 204 });
  }),

  // The mock has no real TCP to dial, so it cannot test a real connection; it only checks fields are filled, for demoing the flow.
  http.post(`${BASE}/alert-settings/test-smtp`, async ({ request }) => {
    await delay(600);
    const body = (await request.json()) as SmtpSettings;
    const badAddr = smtpAddressError(body);
    if (badAddr) return badAddr;
    // A host containing bad is treated as unresolvable, 127.0.0.1 as a blocked local address (the UI requires a host before Test is enabled)
    if (body.host.includes('bad')) {
      return HttpResponse.json(apiErr('invalid', 'conn_resolve_failed', { host: body.host, detail: `lookup ${body.host}: no such host` }), { status: 400 });
    }
    if (body.host === '127.0.0.1') return HttpResponse.json(apiErr('invalid', 'conn_address_blocked', { address: body.host }), { status: 400 });
    // nostarttls: a server without STARTTLS; plainfirst: a server that lists PLAIN before LOGIN, so sign-in fails without encryption;
    // needtls: a server that lists AUTH in the clear but answers it with 530 until STARTTLS
    const security = smtpSecurity(body);
    if (body.host.includes('nostarttls') && security === 'starttls') return HttpResponse.json(apiErr('invalid', 'smtp_starttls_unsupported'), { status: 400 });
    if (body.host.includes('plainfirst') && security === 'none' && body.username) {
      return HttpResponse.json(apiErr('invalid', 'smtp_auth_login_required', { mechs: 'PLAIN LOGIN' }), { status: 400 });
    }
    if (body.host.includes('needtls') && security === 'none' && body.username) {
      return HttpResponse.json(apiErr('invalid', 'smtp_starttls_required'), { status: 400 });
    }
    // untrusted: a server whose certificate no trusted CA issued; the reason depends on whether the deployment sets a CA file
    if (body.host.includes('untrusted') && security !== 'none') {
      return HttpResponse.json(apiErr('invalid', alertSettingsState.customCa ? 'smtp_cert_not_in_ca' : 'smtp_cert_untrusted'), { status: 400 });
    }
    return new HttpResponse(null, { status: 204 });
  }),

  // The mock has no real TCP to dial; it only checks the URL is http(s), for demoing the flow.
  http.post(`${BASE}/alert-settings/test-webhook`, async ({ request }) => {
    await delay(500);
    const body = (await request.json()) as AlertWebhookUrl;
    if (!/^https?:\/\/.+/.test(body.url.trim())) {
      return HttpResponse.json(apiErr('invalid', 'webhook_test_url_invalid'), { status: 400 });
    }
    return new HttpResponse(null, { status: 204 });
  }),

  http.get(`${BASE}/alert-rules`, async () => {
    await delay(150);
    return HttpResponse.json(alertRules);
  }),

  http.put(`${BASE}/alert-rules/:id`, async ({ params, request }) => {
    await delay(200);
    const body = (await request.json()) as AlertRuleUpdateRequest;
    // Demo internal errors: localStorage['mock:ruleFailure'] = promReject (Prometheus rejects the new rule) / auditDown (audit cannot be written) / internal (other internal error)
    const failure = (() => { try { return localStorage.getItem('mock:ruleFailure'); } catch { return null; } })();
    if (failure === 'promReject') return HttpResponse.json(apiErr('internal', 'alert_rule_apply_failed', { detail: 'Prometheus reload rejected (500): error loading config from "/etc/prometheus/prometheus.yml": one or more errors occurred while applying the new configuration' }), { status: 500 });
    if (failure === 'auditDown') return HttpResponse.json(apiErr('internal', 'audit_unavailable'), { status: 503 });
    if (failure === 'internal') return HttpResponse.json(apiErr('internal', 'internal_error'), { status: 500 });
    const rule = updateAlertRule(String(params.id), body, session.username ?? 'unknown');
    if (!rule) return HttpResponse.json(apiErr('not_found', 'rule_not_found'), { status: 404 });
    return HttpResponse.json(rule);
  }),

  http.post(`${BASE}/alert-rules/restore-defaults`, async () => {
    await delay(200);
    return HttpResponse.json(restoreDefaultAlertRules());
  }),
];

// What is wrong with the permissions (same order as the backend's validatePermissions); null if nothing
function rpcPermissionError(p: string): ErrorReason | null {
  const i = p.indexOf(':');
  const ns = p.slice(0, i), method = p.slice(i + 1);
  if (i < 0 || !ns || !method) return 'rpc_permission_format';
  const entry = rpcMethodCatalog.find(n => n.namespace === ns);
  if (!entry) return 'rpc_permission_unknown_namespace';
  return method === '*' || entry.methods.some(m => m.method === method) ? null : 'rpc_permission_unknown_method';
}
// Per-node comparison (shared by GET and PUT, matching the backend's cachedPolicyState / syncedPolicyState): nodes whose RPC is unreadable return unreachable;
// those without PERM or without the allowlist file enabled return disabled with a reason; the rest are in sync and include the node's current list.
// A node's extra addresses (add only, never remove): the node's actual list = (applies shared list ? shared list : empty) + extra addresses. node-a3 has two more by default (for its own applications)
const customAllowlists: Record<string, Omit<NodeAllowlist, 'useShared'> & { useShared?: boolean }> = !isFreshDemo() && allowlistScenario() === 'normal'
  ? { 'node-a3': { nodeId: 'node-a3', revision: 1, accountsAllowlist: ['0x00095a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f', '0x000a5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f'] } } : {};
/** Whether this node applies the shared list (unset = applies) */
const usesShared = (id: string) => customAllowlists[id]?.useShared !== false;
/** The list this node should have: shared list first if applied, then its extra addresses */
const wantOf = (id: string) => normAccounts([...(usesShared(id) ? permissioningPolicy.accountsAllowlist : []), ...(customAllowlists[id]?.accountsAllowlist ?? [])]);
const normAccounts = (l: string[]) => [...new Set(l.map(a => a.toLowerCase()))].sort();
function policyApplyFixture(): PolicyApplyNodeResult[] {
  return [...summaries.map((s) => ({ id: s.id, c: capsOf(s) })), ...manyNodes().map((s) => ({ id: s.id, c: s.capabilities! }))].map(({ id, c }): PolicyApplyNodeResult => {
    if (!c.agent || !c.rpcReachable) return { nodeId: id, state: 'unreachable' };
    if (c.missingRpcApis.includes('PERM')) return { nodeId: id, state: 'disabled', reason: 'allowlist_perm_unavailable', error: 'The node does not expose the PERM namespace (rpc-http-api)' };
    if (c.allowlistEnabled === false) return { nodeId: id, state: 'disabled', reason: 'allowlist_file_disabled', error: 'The node has not enabled the accounts allowlist file (permissions-accounts-config-file-enabled)' };
    const want = wantOf(id);
    const have = nodeAllowlists[id] ?? want;
    const same = have.length === want.length && have.every((a) => want.includes(a));
    return { nodeId: id, state: same ? 'in_sync' : 'drift', currentAllowlist: [...have] };
  });
}

