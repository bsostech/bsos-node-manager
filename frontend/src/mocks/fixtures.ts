/** Mock data for the dev server, shaped after the contract schemas. The nodes belong to this organization, org-a. */
import type { ChainInfo, ValidatorVoteRecord, HourState, NodeCapabilities, NodeSummary, NodeDetail, FleetNodeStatus, JoinToken, JoinTokenCreated, JoinTokenCreateRequest, JoinTokenTtl, MetricId, MetricSeries, Me, Role, PermissioningPolicy, ValidatorSetEntry, Alert, AlertSettings, AlertRule, AlertRuleUpdateRequest, RpcUser, RpcUserCreate, RpcMethodCatalog, RpcTokenRequest, RpcTokenIssued, RpcUserEvent, LoginEvent, LoginFailReason, RpcPublicKey, PeerInfo, NodeTxPool } from '@/api/schemas';
import { type RangeSel, durToMs } from '@/lib/range';
import { analyzePool } from '@/lib/txpool';
import { IDENTITIES, enodeOf, addressOfPub, pubFromSeed, type IdentityKey } from './identities';
import { isFreshDemo, DEMO_PASSWORD, MOCK_SESSION_KEY } from './scenario';

export const now = () => new Date().toISOString();

// Fresh deployment scenario (see scenario.ts): nodes, alerts, allowlists, RPC users and join codes all start empty; only one admin who must change password.
// The chain is running (height follows HEAD), but the platform can only read it once the first node connects.
const FRESH = isFreshDemo();

// Chain head: the mock is fixed at this height (node-a2 catches up from 152 blocks behind, see tickNodeSync)
export const HEAD = 1284553;
// The chain head advances: +1 every 2 seconds after load (the configured block period); healthy nodes follow it, a4 (syncing) catches up separately
const BOOT = Date.now();
export const headNow = () => HEAD + Math.floor((Date.now() - BOOT) / 2000);
const BESU = 'besu/v26.7.1/linux-x86_64/openjdk-java-21';

/**
 * Our org's 5 nodes (the size of a real project): 2 validators + 3 regular nodes, each showing one situation:
 * - a1, a2 validators, all good; a3 regular node (DMZ sentry, connected to many outside peers), high disk;
 * - a4 regular node, syncing, without ADMIN / PERM; a5 regular node, agent disconnected.
 */
const node = (id: IdentityKey & `node-${string}`, more: Partial<NodeSummary>): NodeSummary => ({
  id, org: 'org-a', address: IDENTITIES[id].addr, status: 'healthy', besuRunning: true, rpcPort: 8545, metricsPort: 9545, reportedEnode: enodeOf(id), peerMode: 'off', asOf: now(), ...more,
});
export const summaries: NodeSummary[] = FRESH ? [] : [
  node('node-a1', { peerMode: 'shared', name: '台北主驗證者', chainHeight: HEAD, isValidator: true, rpcAuthEnabled: true, cpuPercent: 23, diskUsedPercent: 37, blocksBehind: 0, txpoolPending: 44, clientVersion: BESU }), // 44 = 4 regular + the stuck batch (matches the txpool chart and the transaction pool tab)
  node('node-a2', { peerMode: 'shared', name: '台北備援驗證者', chainHeight: HEAD, isValidator: true, rpcAuthEnabled: true, cpuPercent: 28, diskUsedPercent: 41, blocksBehind: 0, txpoolPending: 6, clientVersion: BESU }),
  node('node-a3', { peerMode: 'shared', name: 'DMZ sentry', chainHeight: HEAD - 1, isValidator: false, rpcAuthEnabled: true, cpuPercent: 19, diskUsedPercent: 86, blocksBehind: 0, txpoolPending: 3, clientVersion: BESU }),
  node('node-a4', { status: 'syncing', chainHeight: HEAD - 152, isValidator: false, rpcAuthEnabled: false, cpuPercent: 61, diskUsedPercent: 52, blocksBehind: 152, txpoolPending: 3, clientVersion: 'besu/v26.4.0/linux-x86_64/openjdk-java-21' }),
  node('node-a5', { status: 'offline', besuRunning: null }),
];

/** Deterministic pseudo-hex (for mocks): fills len characters with a rolling hash of seed. */
export function fakeHex(seed: string, len: number): string {
  let h1 = 0x811c9dc5;
  let h2 = 0x1b873593;
  for (let i = 0; i < seed.length; i++) {
    const c = seed.charCodeAt(i);
    h1 = Math.imul(h1 ^ c, 2654435761) >>> 0;
    h2 = Math.imul(h2 + c, 2246822519) >>> 0;
  }
  let out = '';
  while (out.length < len) {
    h1 = Math.imul(h1 ^ h2, 2654435761) >>> 0;
    h2 = Math.imul(h2 + h1, 2246822519) >>> 0;
    out += ((h1 ^ h2) >>> 0).toString(16).padStart(8, '0');
  }
  return out.slice(0, len);
}

// Capabilities: for a5 (disconnected) the platform has no snapshot = agent offline, everything else unknown
const OFFLINE_CAPS = { agent: false, rpcReachable: null, rpcUnauthorized: false, missingRpcApis: [], allowlistEnabled: null, metricsReachable: null, logsConnected: null, agentVersion: null, agentRuntime: null };
const OK_CAPS = { agent: true, rpcReachable: true, rpcUnauthorized: false, missingRpcApis: [], allowlistEnabled: true, metricsReachable: true, logsConnected: true, agentVersion: '2.0.0', agentRuntime: 'systemd' as const };
// a3 runs an outdated agent with log forwarding off, so capabilities suggest an upgrade and show logs as optional-off; the platform's latest version is in handlers' /platform/agent-bundle
const CAPS: Record<string, NodeCapabilities> = {
  'node-a3': { ...OK_CAPS, agentVersion: '1.4.0', logsConnected: false, agentRuntime: 'docker' as const },
  'node-a4': { ...OK_CAPS, missingRpcApis: ['ADMIN', 'PERM'], allowlistEnabled: null },
  'node-a5': OFFLINE_CAPS,
};
// To see allowlist tab scenarios: in the console set localStorage['mock:allowlist'] = 'none' (no node has it enabled) or 'first' (first use: platform list empty, nodes have lists) and reload
export const allowlistScenario = (): 'normal' | 'none' | 'first' => {
  try { const v = localStorage.getItem('mock:allowlist'); return v === 'none' || v === 'first' ? v : 'normal'; } catch { return 'normal'; }
};
export const capsOf = (s: NodeSummary): NonNullable<NodeSummary['capabilities']> => {
  const c = details[s.id]?.capabilities ?? CAPS[s.id] ?? OK_CAPS;
  return allowlistScenario() === 'none' && c.allowlistEnabled ? { ...c, allowlistEnabled: false } : c;
};

// ── On-chain validators: our a1, one from org-b, one unknown (no recent blocks). Every page's validator count comes from here ──
// 10 validators from the other consortium members (plus our 2 = 12). Names are what users assigned on the platform (peer names); in a fresh deployment none are assigned yet, so all empty.
// The existing environment has one that is not producing and unnamed (demoing removal votes and unknown orgs); a fresh deployment is a healthy chain where everyone produces
const named = (name: string) => (FRESH ? null : name);
const EXTERNAL_VALIDATORS: { key: IdentityKey; name: string | null; producing: boolean }[] = [
  { key: 'org-b-v1', name: named('org-b 驗證者 1'), producing: true },
  { key: 'org-b-v2', name: named('org-b 驗證者 2'), producing: true },
  { key: 'org-c-v1', name: named('org-c 驗證者 1'), producing: true },
  { key: 'org-c-v2', name: named('org-c 驗證者 2'), producing: true },
  { key: 'org-d-v1', name: named('org-d 驗證者 1'), producing: true },
  { key: 'org-d-v2', name: named('org-d 驗證者 2'), producing: true },
  { key: 'org-e-v1', name: named('org-e 驗證者 1'), producing: true },
  { key: 'org-e-v2', name: named('org-e 驗證者 2'), producing: true },
  { key: 'org-f-v1', name: named('org-f 驗證者'), producing: true },
  { key: 'org-x-v1', name: null, producing: FRESH },
];
/** Source node the platform reads the chain from: our first node readable over RPC; null if there is none (fresh deployment), so chain info and the validator set are unavailable. */
const chainSource = (): NodeSummary | null => summaries.find((s) => s.chainHeight != null) ?? null;
const validatorAddrs = () => [
  ...summaries.filter((s) => s.isValidator).map((s) => IDENTITIES[s.id as IdentityKey].addr),
  ...EXTERNAL_VALIDATORS.map((v) => IDENTITIES[v.key].addr),
];

// ── P2P topology: /nodes/:id/peers, peerCount and shared-list connection counts are all computed from here so they agree ──
// Our nodes running Besu (a5 is disconnected) are fully meshed
const P2P_ALIVE = ['node-a1', 'node-a2', 'node-a3', 'node-a4'];
// Connections to other orgs: validators interconnect (a1 / a2 connect to several orgs' validators); the DMZ sentry (a3) also connects to org-c's regular node and several validators
const EXTERNAL_LINKS: Record<string, IdentityKey[]> = {
  'node-a1': ['org-b-v1', 'org-c-v1', 'org-d-v1', 'org-e-v1'],
  'node-a2': ['org-b-v2', 'org-c-v2', 'org-d-v2', 'org-f-v1'],
  'node-a3': ['org-b-v1', 'org-c-n9', 'org-e-v2'],
};

/** Peers this node is currently connected to (whether or not the platform can read them); empty for a node that is not running. */
/** What a user with permission sees: public key and enode included. */
type MockPeer = Omit<PeerInfo, 'name'> & { pubkey: string; enode: string };
export function rawPeersOf(id: string): MockPeer[] {
  if (!P2P_ALIVE.includes(id)) return [];
  const vals = new Set(validatorAddrs());
  const own = P2P_ALIVE.filter((x) => x !== id && summaries.some((s) => s.id === x)) as IdentityKey[];
  return [...own, ...(EXTERNAL_LINKS[id] ?? [])].map((k, i) => {
    const { pub, addr, ip } = IDENTITIES[k];
    // When the other side dials in, remoteAddress is their ephemeral port; the enode carries their advertised listening port (same as Besu admin_peers)
    const isOwn = k.startsWith('node-');
    // Between our own two nodes: the lower number dials out, the higher accepts (both sides see a consistent direction); for other orgs, half are outbound from us and half inbound
    const inbound = isOwn ? k > id : i % 2 === 1;
    return {
      pubkey: pub, address: addr, enode: enodeOf(k), inbound,
      remoteAddress: inbound ? `${ip}:${40000 + ((i * 7919) % 20000)}` : `${ip}:30303`,
      ...(isOwn ? { nodeId: k } : {}),
      ...(vals.has(addr) ? { validatorAddress: addr } : {}),
    } as MockPeer;
  }).concat(id === 'node-a3' ? DMZ_PEERS : []);
}
// The DMZ sentry (a3) also connects to a few outside non-validators (partner RPC, audit nodes and the like); half of them dial in
const DMZ_PEERS: MockPeer[] = Array.from({ length: 3 }, (_, i) => {
  const pub = pubFromSeed(`dmz-${i}`);
  const ip = `203.0.113.${i + 10}`;
  const inbound = i % 2 === 0;
  return { pubkey: pub, address: addressOfPub(pub), enode: `enode://${pub}@${ip}:30303`, inbound, remoteAddress: `${ip}:${inbound ? 41000 + i : 30303}` };
});
/** Names users assigned (address -> name); external peers both with and without names. */
export const seededPeerNames: Record<string, string> = FRESH ? {} : {
  [IDENTITIES['org-b-v1'].addr]: 'org-b 驗證者 1',
  [DMZ_PEERS[0].address]: '合作夥伴 RPC',
  [DMZ_PEERS[2].address]: '稽核節點',
};
/** Only nodes whose admin_peers the platform can read; agent offline, RPC unreachable, no ADMIN, or isolated all mean "not reported". */
export function reportedPeersOf(id: string) {
  const s = summaries.find((n) => n.id === id);
  const c = s && capsOf(s);
  if (!s || s.quarantinedAt || !c?.agent || !c.rpcReachable || c.missingRpcApis.includes('ADMIN')) return null;
  return rawPeersOf(id);
}
// peerCount comes from net_peerCount (available whenever RPC works, ADMIN not needed)
for (const s of summaries) if (s.chainHeight != null) s.peerCount = rawPeersOf(s.id).length;

function ownDetail(s: NodeSummary): NodeDetail {
  const key = s.id as IdentityKey;
  const live = s.chainHeight != null; // readable over RPC = has ledger, P2P, consensus
  const agentUp = !s.quarantinedAt && s.status !== 'offline';
  const vals = validatorAddrs();
  return {
    id: s.id, name: s.name ?? null, org: s.org, status: s.status, clientVersion: s.clientVersion ?? null,
    rpcPort: s.rpcPort ?? 8545, metricsPort: s.metricsPort ?? 9545, rpcAuthEnabled: s.rpcAuthEnabled ?? null,
    // Isolation is a node-level fact; summary and detail must agree (the isolation banner on the node settings page reads detail)
    quarantinedAt: s.quarantinedAt ?? null,
    agentOutdatedAt: s.agentOutdatedAt ?? null,
    validatorAddress: IDENTITIES[key].addr,
    reportedEnode: s.reportedEnode ?? null,
    // CPU and disk come from the same source as the node list so detail and overview agree; a disconnected agent has no host metrics
    hardware: agentUp ? { active: true, cpuPercent: s.cpuPercent ?? 0, memUsedBytes: 6_800_000_000, memTotalBytes: 16_000_000_000, diskUsedPercent: s.diskUsedPercent ?? 0, netRxBytesPerSec: 2_400_000, netTxBytesPerSec: 1_800_000, diskDaysLeft: DISK_DAYS[s.id] ?? null, diskDaysLeftReason: DISK_REASON[s.id] } as NodeDetail['hardware'] : null,
    p2p: live ? {
      enode: s.reportedEnode ?? undefined, peerCount: s.peerCount ?? 0, peerLimit: 25, inSync: s.status !== 'syncing',
    } : null,
    ledger: live ? { chainHeight: s.chainHeight!, blocksBehind: s.blocksBehind ?? 0, txpoolPending: s.txpoolPending ?? 0, tps: 1.6 } : null,
    // Consensus is the on-chain state read via this node's RPC: null if unreadable (same as the backend, which only fills it when there is a ledger)
    consensus: live ? {
      algorithm: 'qbft', validatorCount: vals.length, isValidator: s.isValidator ?? false, validators: vals,
      // Who produced blocks is an on-chain fact, the same from every node; the unknown one has not produced recently
      proposerMetrics: vals.map((address, i) => ({ address, proposedBlockCount: i === vals.length - 1 ? 0 : Math.floor(1000 / Math.max(1, vals.length - 1)), lastProposedBlockNumber: HEAD - i - (i === vals.length - 1 ? 200 : 0) })),
    } : null,
    capabilities: CAPS[s.id] ?? OK_CAPS,
    asOf: now(),
  };
}

// Days of disk left (extrapolated from 7-day growth): a3 is at 86% and growing fast, under a month left; a2 is barely growing
const DISK_DAYS: Record<string, number | null> = { 'node-a1': 214, 'node-a2': null, 'node-a3': 23, 'node-a4': null };
// Why days cannot be computed: a2's disk is not growing ("Over a year"), a4 just connected with under 7 days of data ("Not enough data")
const DISK_REASON: Record<string, NonNullable<NodeDetail['hardware']>['diskDaysLeftReason']> = { 'node-a2': 'not_growing', 'node-a4': 'insufficient_data' };
export const details: Record<string, NodeDetail> = Object.fromEntries(summaries.map((s) => [s.id, ownDetail(s)]));

// node-a4 catches up to the chain head from 152 blocks behind (starting at the first read, 4 blocks per second)
let nodeSyncStartedAt: number | undefined;
export function tickNodeSync(at = Date.now()): void {
  const n = summaries.find((x) => x.id === 'node-a4');
  const detail = details['node-a4'];
  if (!n || !detail?.ledger || !detail.p2p) return;
  nodeSyncStartedAt ??= at;
  const caughtUp = Math.floor(Math.max(0, at - nodeSyncStartedAt) / 1000) * 4;
  const height = Math.min(headNow(), HEAD - 152 + caughtUp);
  const behind = headNow() - height;
  n.chainHeight = detail.ledger.chainHeight = height;
  n.status = detail.status = behind === 0 ? 'healthy' : 'syncing';
  n.blocksBehind = detail.ledger.blocksBehind = behind;
  detail.p2p.inSync = behind === 0;
  n.asOf = detail.asOf = new Date(at).toISOString();
}

// ── Validator governance (on-chain validator set + votes on our own validators) ──────────────────
export function currentValidators(): ValidatorSetEntry[] {
  if (!chainSource()) return []; // no node can read the chain, so the validator set is unknown (same as the backend)
  const set: Omit<ValidatorSetEntry, 'turns'>[] = [
    ...summaries
      .filter((s) => s.isValidator)
      // Our validators' names = node names (same as the backend's peerNames overlay); number of our nodes connected to it = computed from the topology
      .map((s) => ({ address: IDENTITIES[s.id as IdentityKey].addr, orgId: s.org, nodeId: s.id, name: s.name ?? null, connectedNodes: P2P_ALIVE.filter((x) => x !== s.id).length, producing: true })),
    // Addresses in the on-chain validator set the platform does not know must still be listed, just with org / nodeId blank
    ...EXTERNAL_VALIDATORS.map((v) => ({
      address: IDENTITIES[v.key].addr, orgId: null, nodeId: null, name: v.name,
      connectedNodes: Object.values(EXTERNAL_LINKS).filter((l) => l.includes(v.key)).length, producing: v.producing,
    })),
  ];
  // Each of its turns in the last 1000 blocks (same window as the signer metrics, about 83 turns for 12 validators): the unknown one missed its last 7, everyone else produced
  const nTurns = Math.floor(1000 / set.length);
  return set.map((v, i) => ({
    ...v,
    name: v.name ?? null,
    // at = time of that block (one block every 2 seconds, counted back from the height at page load)
    turns: Array.from({ length: nTurns }, (_, k) => { const block = HEAD - (nTurns - 1 - k) * set.length - i; const proposed = v.producing !== false || k < nTurns - 7; return { block, proposed, at: proposed ? new Date(BOOT - (HEAD - block) * 2000).toISOString() : null,
      // Transactions per block: usually 0-8 (about 1.6 tx/s on chain × 2 seconds), occasionally empty, occasionally large
      ...(() => {
        const txCount = proposed ? ((k * 7 + i * 3) % 29 === 0 ? 40 + ((k * 11 + i) % 30) : (k * 13 + i * 7) % 9) : null;
        // gas: roughly 21k to 90k per transaction; the limit depends on each validator's target-gas-limit, some 30,000,000, some 45,000,000
        const gasUsed = txCount == null ? null : txCount * (21000 + ((k * 37 + i * 11) % 7) * 11000);
        return { txCount, gasUsed, gasLimit: proposed ? [30_000_000, 45_000_000, 30_000_000][i % 3] : null };
      })() }; }),
  }));
}

// To see epoch unknown (none of our nodes has ADMIN): localStorage['mock:noEpoch'] = '1' and reload
export const epochUnknownScenario = () => { try { return !!localStorage.getItem('mock:noEpoch'); } catch { return false; } };
// To see "Blocks slowing" (last block 40 seconds ago, many round timeouts in the last hour): localStorage['mock:slowBlocks'] = '1' and reload
export const slowBlocksScenario = () => { try { return !!localStorage.getItem('mock:slowBlocks'); } catch { return false; } };
// To see "Chain not readable" (all our nodes stopped, the platform cannot read height or the validator list): localStorage['mock:noChain'] = '1' and reload
export const chainUnreadableScenario = () => { try { return !!localStorage.getItem('mock:noChain'); } catch { return false; } };

/** Sources of round timeouts in the last hour (mock): the unknown one misses the most, org-e validator 2 occasionally. The slow scenario scales the counts up. */
function roundChanges1h(): NonNullable<ChainInfo['roundChanges1h']> {
  const slow = slowBlocksScenario();
  const by = (key: IdentityKey, name: string | null, count: number) => ({ address: IDENTITIES[key].addr, name, orgId: null, nodeId: null, count });
  const byProposer = [
    by('org-x-v1', null, slow ? 9 : 2),
    by('org-e-v2', named('org-e 驗證者 2'), slow ? 2 : 1),
    ...(slow ? [by('org-b-v1', named('org-b 驗證者 1'), 1)] : []),
  ];
  return { total: byProposer.reduce((a, b) => a + b.count, 0), byProposer };
}

const EPOCH = 30592; // puts HEAD near the end of an epoch (311 blocks left), so the approaching rollover is visible
const LOADED_AT = Date.now();

/** Data age (mock): the backend samples every 10 seconds; seconds since the last round at response time */
export const dataAgeSec = () => (Date.now() % 10_000) / 1000;

/** Chain-level info (mock): the last 36 blocks are mostly 2 seconds, one in the middle 9.1 seconds, so the height card shows a bar highlighted in yellow. */
export function chainInfo(): ChainInfo {
  const src = chainSource();
  if (!src || chainUnreadableScenario()) return { blockIntervals: [], asOf: now(), stability24h: Array.from({ length: 24 }, () => 'unknown' as HourState) }; // no node connected yet: the platform knows nothing about the chain (same as the backend's chainInfo response with no source)
  const n = currentValidators().length;
  const intervals = Array.from({ length: 36 }, (_, i) => (i === 20 ? 9.1 : 2 + ((i * 7) % 3) * 0.1));
  // Stability over the last 24 hours: the existing environment is mostly stable with one slow afternoon slot; a fresh deployment just connected, so only the latest slot has data
  const stability24h: HourState[] = FRESH
    ? [...Array.from({ length: 23 }, () => 'unknown' as HourState), 'ok']
    : Array.from({ length: 24 }, (_, i) => (i === 15 ? 'degraded' : 'ok'));
  return {
    stability24h,
    blockPeriodSeconds: 2, emptyBlockPeriodSeconds: 2, expectedBlockPeriodSeconds: 2,
    sourceNodeId: src.id, chainId: 20250901, consensus: 'qbft', height: slowBlocksScenario() ? HEAD : headNow(),
    // Slow scenario: last block 40 seconds ago (over the 30-second threshold, short of the 60-second halt), height stays at HEAD
    latestBlockAt: new Date(Date.now() - (slowBlocksScenario() ? 40_000 : 2000)).toISOString(), blockIntervals: intervals,
    roundChanges1h: roundChanges1h(),
    genesisAt: new Date(BOOT - HEAD * 2000).toISOString(), // block 0: HEAD blocks × 2 seconds back from now, so it agrees with the height
    validatorCount: n, blockQuorum: Math.ceil((2 * n) / 3), asOf: now(), dataAgeSec: dataAgeSec(),
    // epoch: for the first 20 seconds after page load it shows backfilling (the platform just restarted), after that the tally is complete
    // The EVM version, like epoch, is read from admin_nodeInfo: unavailable when no node has ADMIN
    evmVersion: epochUnknownScenario() ? null : 'Cancun',
    ...(epochUnknownScenario() ? {} : {
      epochLength: EPOCH, epochStart: HEAD - (HEAD % EPOCH),
      votesFrom: Date.now() - LOADED_AT < 20_000 ? HEAD - 400 : HEAD - (HEAD % EPOCH),
    }),
  };
}

// "X minutes ago" is computed from a fixed origin stored in localStorage: after a reload alert start times stay the same, so notification identifiers (fingerprint + startsAt)
// still match the read records (the real backend's startsAt is fixed anyway). The origin is renewed after 6 hours so the demo data does not keep getting older.
const MOCK_T0 = (() => {
  const KEY = 'mock:t0', now = Date.now();
  try {
    const v = Number(localStorage.getItem(KEY));
    if (v && now - v < 6 * 3600_000) return v;
    localStorage.setItem(KEY, String(now));
  } catch { /* private browsing: recompute every time */ }
  return now;
})();
const ago = (min: number) => new Date(MOCK_T0 - min * 60_000).toISOString();

// Alerts: mirror the default rules (backend/internal/alerting/defaults.go), attached to nodes that really have a problem.
// When the same alert fires again its fingerprint stays the same (Alertmanager behavior); the UI keys on fingerprint + startsAt.
// value: the value at firing time; only threshold alerts have it, and the UI builds the summary per language from it. id as in the backend (fingerprint|startsAt); everything starts unread, and half are picked below
const alert = (name: string, nodeId: string | null, scope: string, severity: 'critical' | 'warning', summary: string, description: string, startsAt: string, endsAt?: string, value?: string): Alert => ({
  id: `fp-${name}-${nodeId}|${startsAt}`, readFire: false, readResolve: false,
  fingerprint: `fp-${name}-${nodeId}`, alertname: name, nodeId, severity,
  status: endsAt ? 'resolved' : 'firing',
  labels: { alertname: name, ...(nodeId ? { node: nodeId } : {}), org: 'org-a', severity, scope },
  annotations: { summary, description, ...(value != null ? { value } : {}) }, startsAt, ...(endsAt ? { endsAt } : {}),
});
export const alerts: Alert[] = FRESH ? [] : [
  alert('NodeOffline', 'node-a5', 'net', 'critical', '節點 node-a5（org-a）連線中斷', 'Prometheus 抓不到 node-a5 的指標：agent 沒有連上平台，或 agent 已停止。', ago(12)),
  alert('NodeDiskHigh', 'node-a3', 'hw', 'warning', '節點 node-a3 磁碟使用率過高（86%）', '鏈資料會持續成長，建議及早擴容。', ago(40), undefined, '86'),
  alert('NodeOffline', 'node-a5', 'net', 'critical', '節點 node-a5（org-a）連線中斷', 'Prometheus 抓不到 node-a5 的指標：agent 沒有連上平台，或 agent 已停止。', ago(60 * 9), ago(60 * 7)),
  alert('ChainSlowSevere', null, 'consensus', 'critical', '出塊嚴重變慢', '可能有驗證者掉線，或驗證者之間失去共識。', ago(60 * 30), ago(60 * 29)),
  // Older history: so the inbox looks like it has been in use for a while
  alert('NodeBehind', 'node-a4', 'ledger', 'warning', '節點 node-a4 落後 120 塊', '這台的區塊高度追不上鏈頭，可能是同步變慢或連線不足。連線正常卻一直落後時，重啟 Besu。', ago(95), ago(70), '120'),
  alert('NodePeerCountLow', 'node-a4', 'net', 'warning', '節點 node-a4 連線數過低（1）', 'P2P 連線太少，有孤立的風險。', ago(60 * 5), ago(60 * 4), '1'),
  alert('NodeMemHigh', 'node-a2', 'hw', 'warning', '節點 node-a2 記憶體使用率過高（92%）', '主機記憶體吃緊，Besu 可能變慢或被系統終止。', ago(60 * 14), ago(60 * 13 + 20), '92'),
  alert('NodeCpuHigh', 'node-a1', 'hw', 'warning', '節點 node-a1 CPU 使用率過高（94%）', 'CPU 長時間滿載，出塊與同步可能變慢。', ago(60 * 26), ago(60 * 25 + 40), '94'),
  alert('BesuMetricsDown', 'node-a3', 'ledger', 'critical', '節點 node-a3 Besu 指標讀不到', 'agent 連得上，但抓不到 Besu 的指標：Besu 沒有在跑，或指標功能沒開。', ago(60 * 40), ago(60 * 39 + 30)),
  alert('NodeOffline', 'node-a2', 'net', 'critical', '節點 node-a2（org-a）連線中斷', 'Prometheus 抓不到 node-a2 的指標：agent 沒有連上平台，或 agent 已停止。', ago(60 * 52), ago(60 * 51 + 50)),
];
// Read state is per user (same as the backend): username -> the "id|fire" and "id|resolve" entries read. Mock state, reset on reload
const alertReads: Record<string, Set<string>> = {};
// For the demo: the first time each account looks, the older half counts as read, so the inbox looks used
const seedReads = () => new Set([...alerts].sort((x, y) => Date.parse(y.endsAt ?? y.startsAt) - Date.parse(x.endsAt ?? x.startsAt))
  .slice(Math.ceil(alerts.length / 2)).flatMap((a) => (a.endsAt ? [`${a.id}|fire`, `${a.id}|resolve`] : [`${a.id}|fire`])));
const readsOf = (user: string) => (alertReads[user] ??= seedReads());
/** Attach this account's read state */
export const withReads = (user: string, list: Alert[]): Alert[] =>
  list.map((a) => ({ ...a, readFire: readsOf(user).has(`${a.id}|fire`), readResolve: readsOf(user).has(`${a.id}|resolve`) }));
/** Mark the current notification of these alerts as read: firing marks the fire one, resolved marks both (same as the backend) */
export function markAlertsReadFixture(user: string, ids: string[] | null) {
  for (const a of alerts) {
    if (ids && !ids.includes(a.id)) continue;
    readsOf(user).add(`${a.id}|fire`);
    if (a.endsAt) readsOf(user).add(`${a.id}|resolve`);
  }
}

// ── RPC users ──────────────────────────────────────────────
// RPC user = a named permission set, one set per org (mock in-memory state; reset on reload). The platform stores no passwords;
// Issuing a token returns a fake JWT, shown only once in the response. Each user's activity log is stored separately and deleted with the user.
const DAY = 24 * 60;
// Expiry truncated to seconds (same as the backend)
const toSecond = (ms: number) => new Date(Math.floor(ms / 1000) * 1000).toISOString();

type EventSeed = Omit<RpcUserEvent, 'id'>;
// Callable methods cannot be changed after creation: every token of the same identity has the same permissions
const DAPP = ['eth:blockNumber', 'eth:call', 'eth:sendRawTransaction', 'net:version'];
// tokenId: generated at issuance and written into the JWT's jti; label: optional purpose given at issuance
let tokenSeq = 0;
const tokenIdOf = () => `tk_${(0x7f3a9c2be104 + ++tokenSeq * 0x1b2c3d4e5).toString(16).slice(-12)}`;
const issued = (agoMin: number, actor: string, ttlMin: number, permissions: string[], label: string | null = null): EventSeed =>
  ({ kind: 'token_issued', at: ago(agoMin), actor, permissions, expiresAt: toSecond(Date.now() + (ttlMin - agoMin) * 60_000), tokenId: tokenIdOf(), label });

const seedUsers: { user: RpcUser; events: EventSeed[] }[] = FRESH ? [] : [
  {
    user: { id: 'rpcu-dapp-web', username: 'dapp-web', permissions: DAPP, createdAt: ago(23 * DAY) },
    events: [
      { kind: 'created', at: ago(23 * DAY), actor: 'amy', permissions: DAPP },
      issued(23 * DAY - 3, 'amy', DAY, DAPP, '上線前測試'),
      issued(16 * DAY, 'ken', 7 * DAY, DAPP, '測試環境'),
      issued(9 * DAY, 'ken', DAY, DAPP),
      issued(4 * DAY, 'amy', 30 * DAY, DAPP, '交給 A 廠商整合'),
      issued(DAY, 'ken', 7 * DAY, DAPP, '正式環境'),
    ],
  },
  {
    user: { id: 'rpcu-explorer', username: 'explorer', permissions: ['eth:*', 'net:*', 'txpool:*'], createdAt: ago(21 * DAY) },
    events: [
      { kind: 'created', at: ago(21 * DAY), actor: 'ken', permissions: ['eth:*', 'net:*', 'txpool:*'] },
      issued(DAY / 2, 'ken', 365 * DAY, ['eth:*', 'net:*', 'txpool:*'], '區塊瀏覽器'),
    ],
  },
  {
    user: { id: 'rpcu-monitor', username: 'monitor', permissions: ['eth:blockNumber', 'net:peerCount', 'txpool:besuStatistics'], createdAt: ago(12 * DAY) },
    events: [{ kind: 'created', at: ago(12 * DAY), actor: 'ken', permissions: ['eth:blockNumber', 'net:peerCount', 'txpool:besuStatistics'] }],
  },
];

// To see long-list scrolling: with localStorage['mock:manyNodes']='1' (the same switch as the node list), users are padded to 30 and dapp-web gets 30 more issuance records
function manyUsers() {
  if (!seedUsers.length) return;
  try { if (!localStorage.getItem('mock:manyNodes')) return; } catch { return; }
  for (let i = 0; i < 30; i++) seedUsers[0].events.push(issued((40 + i) * DAY, i % 2 ? 'amy' : 'ken', DAY, DAPP));
  const sets = [['eth:blockNumber'], ['eth:*', 'net:*'], ['eth:call', 'eth:getBalance', 'net:version'], ['txpool:*']];
  for (let i = 1; i <= 27; i++) {
    const permissions = sets[i % sets.length];
    seedUsers.push({
      user: { id: `rpcu-svc-${i}`, username: `svc-${String(i).padStart(2, '0')}`, permissions, createdAt: ago((30 + i) * DAY) },
      events: [{ kind: 'created', at: ago((30 + i) * DAY), actor: 'ken', permissions }, ...(i % 3 ? [issued(i * DAY, 'amy', 30 * DAY, permissions)] : [])],
    });
  }
}
manyUsers();

let rpcEventSeq = 0;
const withId = (e: EventSeed): RpcUserEvent => ({ id: ++rpcEventSeq, ...e });
export const rpcUsers: RpcUser[] = seedUsers.map((s) => s.user);
/** User id -> records (oldest to newest; the handler reverses to newest first in responses). */
export const rpcUserEvents: Record<string, RpcUserEvent[]> = Object.fromEntries(
  seedUsers.map((s) => [s.user.id, [...s.events].sort((a, b) => a.at.localeCompare(b.at)).map(withId)]),
);
const record = (id: string, e: Omit<EventSeed, 'at' | 'actor'>) => rpcUserEvents[id].push(withId({ ...e, at: now(), actor: session.username ?? '' }));

export function createRpcUserFixture(body: RpcUserCreate): RpcUser {
  const u: RpcUser = { id: `rpcu-${randHex(8)}`, username: body.username, permissions: body.permissions, createdAt: now() };
  rpcUsers.unshift(u);
  rpcUserEvents[u.id] = [];
  record(u.id, { kind: 'created', permissions: body.permissions });
  return u;
}

const RPC_TOKEN_TTL_MS: Record<NonNullable<RpcTokenRequest['ttl']>, number> = { '1h': 3_600_000, '24h': 86_400_000, '7d': 7 * 86_400_000, '30d': 30 * 86_400_000, '365d': 365 * 86_400_000 };

/** Sign token: returns a fake JWT (header.payload.sig shape) + exp; null if the user is not found. */
export function issueRpcTokenFixture(id: string, body: RpcTokenRequest): RpcTokenIssued | null {
  const u = rpcUsers.find((x) => x.id === id);
  if (!u) return null;
  const expiresAt = toSecond(body.expiresAt ? Date.parse(body.expiresAt) : Date.now() + RPC_TOKEN_TTL_MS[body.ttl ?? '24h']);
  const tokenId = `tk_${randHex(12)}`;
  const label = body.label?.trim() || null;
  const payload = btoa(JSON.stringify({ sub: u.username, jti: tokenId, permissions: u.permissions, exp: Math.floor(Date.parse(expiresAt) / 1000) })).replace(/=+$/, '');
  record(id, { kind: 'token_issued', permissions: u.permissions, expiresAt, tokenId, label });
  return { token: `eyJhbGciOiJSUzI1NiJ9.${payload}.mock-signature`, expiresAt, permissions: u.permissions, tokenId, label };
}

/** Delete: remove the user and its records (false if not found). */
export function deleteRpcUserFixture(id: string): boolean {
  const i = rpcUsers.findIndex((x) => x.id === id);
  if (i < 0) return false;
  rpcUsers.splice(i, 1);
  delete rpcUserEvents[id];
  return true;
}

export const rpcPublicKeyFixture: RpcPublicKey = {
  algorithm: 'RS256',
  // A real RSA-2048 public key (private key discarded, for dev display only); sha256 is the hash of the DER, same algorithm as the backend
  publicKeyPem: '-----BEGIN PUBLIC KEY-----\nMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAs9w+1MK+zXMm9xYOsvEg\nCitsEa3OAanRuJsNxAIFhkPkG0lkJwNrfvFN6Zw17AiLHyukw5EuDgbcgh29a2C+\n6QIlAvb5VhcAJctlm5eE8GEu0kk744epQP++v8qGM7x4ZtHcmZdJd/YdZkIV9pnP\nNQ4BjP/w5cpWOQsDFo1TMPidfAZ+gUrUuedJPvq2d2KTQRd3lQvKf0O+TxqeceeX\n22qrrKu98WE1HXrtvuDEpiU2WQ+h+QnqySezOly2nTYZALu0+kKl31QuPmuU3zmj\nVw0gDhDlIUSyVjwoe/V6o80vekfL8tKS1VKcVfxSvyAgTkbugn0kMjOT+ukyNDaz\nXQIDAQAB\n-----END PUBLIC KEY-----\n',
  sha256: '4d52ab341d29545eee5b265ed87df5923e03f1d311ee316a00af729ca868d01f',
};

/** RPC method catalog (mock: a curated set of namespaces + common methods; same shape as the backend's rpccatalog.go, enough to practice selection). */
export const rpcMethodCatalog: RpcMethodCatalog = [
  { namespace: 'eth', description: '帳戶餘額、交易、區塊、合約呼叫(最常授權)', methods: [
    { method: 'blockNumber', description: '目前區塊高度' },
    { method: 'getBalance', description: '查帳戶餘額' },
    { method: 'call', description: '唯讀呼叫合約(不上鏈)' },
    { method: 'sendRawTransaction', description: '送出已簽章交易' },
  ] },
  { namespace: 'net', description: '網路與對等節點資訊', methods: [
    { method: 'version', description: '網路 ID' },
    { method: 'peerCount', description: '對等節點數' },
    { method: 'enode', description: '本節點 enode' },
  ] },
  { namespace: 'txpool', description: '交易池狀態(besu 專屬)', methods: [
    { method: 'besuStatistics', description: '交易池統計' },
    { method: 'besuTransactions', description: '交易池內交易清單' },
  ] },
  { namespace: 'admin', description: '節點管理(對等節點增刪;高權限)', methods: [
    { method: 'peers', description: '已連對等節點清單' },
    { method: 'nodeInfo', description: '本節點資訊(enode 等)' },
  ] },
  // The chain is QBFT (see chainInfo), so governance methods are in the qbft namespace
  { namespace: 'qbft', description: 'QBFT 共識驗證者治理', methods: [
    { method: 'getValidatorsByBlockNumber', description: '查指定區塊的驗證者集' },
    { method: 'proposeValidatorVote', description: '投票增/刪驗證者' },
    { method: 'discardValidatorVote', description: '撤回自己的投票' },
    { method: 'getPendingVotes', description: '查目前待決投票' },
  ] },
];

/** Availability over the last 24 hours (mock): extrapolated back from the node's current state: offline nodes red for the last few hours, syncing ones yellow, RPC-unreachable ones no data,
 *  and the rest get an occasional yellow slot so the bar is not dead flat. 24 slots, oldest to newest. */
export function availabilityOf(s: NodeSummary): HourState[] {
  const hours: HourState[] = Array.from({ length: 24 }, () => 'ok');
  const seed = s.id.split('').reduce((h, c) => (h * 31 + c.charCodeAt(0)) >>> 0, 7);
  if (seed % 3 === 0) hours[seed % 20] = 'degraded'; // fell behind during some daytime hour
  const tail = (n: number, v: HourState) => { for (let i = 24 - n; i < 24; i++) hours[i] = v; };
  if (s.status === 'offline') tail(s.quarantinedAt ? 1 : 4, 'down');
  else if (s.status === 'syncing') tail(2, 'degraded');
  else if (s.status === 'rpc_unreachable') tail(6, 'unknown');
  return hours;
}

/** Fleet source status: like the backend, only abnormal nodes are listed (agent disconnected over 10 minutes -> unreachable); healthy ones do not appear. */
export function fleetStatuses(): FleetNodeStatus[] {
  return summaries
    .filter((s) => s.status === 'offline')
    .map((s) => ({ nodeId: s.id, status: 'unreachable' as const, since: s.quarantinedAt ?? ago(12), reason: 'node agent not responding' }));
}

// ── Identity / permissions (mock) ──
// Mock accounts: username -> password/role/display name (one per role, for testing RBAC).
// admin is the administrator the platform creates on first start (the real platform also calls it admin; there is no bootstrap account); the rest simulate roles mapped from LDAP groups.
// mustChange: forces a password change at first login. To see that flow, switch to the "Fresh deployment" scenario and log in as admin (see scenario.ts).
// Note: this object is mutable (password changes / clearing mustChange inside MSW modify it in place; reset on reload).
// Fresh deployment: only admin, password not changed yet (on the real platform the initial password is in the log or BOOTSTRAP_ADMIN_PASSWORD; the mock uses admin).
// Demo accounts share one password (DEMO_PASSWORD, see scenario.ts) that satisfies the platform's password rules.
// Display name = username (the open-source build only has local accounts, and the backend also returns the username as display name)
// lastLoginAt: last login (to the minute); updated on successful login. In a fresh deployment nobody has logged in yet.
export const mockUsers: Record<string, { password: string; role: Role; name: string; email: string | null; mustChange?: boolean; disabled?: boolean; lastLoginAt?: string | null }> = FRESH ? {
  admin: { password: DEMO_PASSWORD, role: 'admin', name: 'admin', email: 'admin@org-a.example', mustChange: true, lastLoginAt: null },
} : {
  admin: { password: DEMO_PASSWORD, role: 'admin', name: 'admin', email: 'admin@org-a.example', mustChange: false, lastLoginAt: ago(26) },
  ops: { password: DEMO_PASSWORD, role: 'operator', name: 'ops', email: 'ops@org-a.example', lastLoginAt: ago(3 * 60 + 12) },
  viewer: { password: DEMO_PASSWORD, role: 'viewer', name: 'viewer', email: 'viewer@org-a.example', lastLoginAt: ago(2 * 24 * 60 + 47) },
};
// To see the open-source build meeting a role left by the commercial build: localStorage['mock:legacyRole'] = '1' and reload; adds an auditor account (the open-source build has no such role)
try { if (!FRESH && localStorage.getItem('mock:legacyRole')) mockUsers.auditor ??= { password: DEMO_PASSWORD, role: 'auditor' as string as Role, name: 'auditor', email: null, lastLoginAt: null }; } catch { /* private browsing: skip */ }

// Login events (mutable inside MSW, a login inserts one at the front; reset on reload). In a fresh deployment nobody has logged in yet.
// Last 30 days: three people log in regularly; ops mistyped a password once; someone tried a nonexistent username a few times from an external IP (username recorded as null).
const OFFICE = ['10.0.2.15', '10.0.2.23', '10.0.2.41'];
export const loginEvents: LoginEvent[] = FRESH ? [] : (() => {
  const out: LoginEvent[] = [];
  let n = 0;
  const push = (min: number, username: string | null, reason: LoginFailReason | null, ip: string) =>
    out.push({ id: `le-${++n}`, at: ago(min), username, result: reason ? 'failure' : 'success', reason, sourceIp: ip });
  for (let d = 0; d < 30; d++) {
    const day = d * 24 * 60;
    if (d % 2 === 0) push(day + 26, 'admin', null, OFFICE[0]);
    push(day + 3 * 60 + 12, 'ops', null, OFFICE[1]);
    if (d % 3 === 2) push(day + 2 * 60 + 47, 'viewer', null, OFFICE[2]);
  }
  push(3 * 60 + 14, 'ops', 'bad_password', OFFICE[1]);
  push(3 * 60 + 13, 'ops', 'bad_password', OFFICE[1]);
  push(5 * 24 * 60 + 410, null, 'unknown_user', '203.0.113.77');
  push(5 * 24 * 60 + 409, null, 'unknown_user', '203.0.113.77');
  push(5 * 24 * 60 + 408, null, 'unknown_user', '203.0.113.77');
  push(12 * 24 * 60 + 95, 'viewer', 'bad_password', OFFICE[2]);
  return out.sort((a, b) => b.at.localeCompare(a.at));
})();

// Alert notification settings (mutable inside MSW, reset on reload). Default: nothing configured.
// locale: a fresh install sends English and follows the host's zone; the mock stands in the browser's zone for the host's.
export const alertSettingsState: AlertSettings = { smtp: null, telegram: null, slack: null, webhookUrls: [], customCa: !FRESH, locale: { language: 'en', timezone: '', effectiveTimezone: Intl.DateTimeFormat().resolvedOptions().timeZone } };

// Org-level besu account allowlist (mutable singleton inside MSW, reset on reload). With mock:manyNodes it has 60 entries, to see scrolling.
const acct = (i: number) => `0x${(i + 1).toString(16).padStart(4, '0')}${'5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f'}`;
const manyAccounts = (() => { try { return !!localStorage.getItem('mock:manyNodes'); } catch { return false; } })();
const POLICY0 = Array.from({ length: manyAccounts ? 60 : 3 }, (_, i) => acct(i));
// Names of allowed transaction addresses: the first two are named, the rest are not
export const seededAccountNames: Record<string, string> = FRESH ? {} : { [acct(0)]: '出納系統', [acct(1)]: '結算服務' };
export const permissioningPolicy: PermissioningPolicy = {
  revision: FRESH ? 0 : 1,
  accountsAllowlist: FRESH || allowlistScenario() === 'first' ? [] : POLICY0,
};
// Node lists that differ from the platform's (other enabled nodes = platform list). Normal: a3 is missing one shared address (list inconsistent); first use: every node already has a list, and a3 differs from the others
export const nodeAllowlists: Record<string, string[]> = FRESH ? {} : allowlistScenario() === 'first'
  ? Object.fromEntries(['node-a1', 'node-a2', 'node-a3'].map((id) => [id, id === 'node-a3' ? [acct(0), acct(1), acct(3)] : [acct(0), acct(1), acct(2)]]))
  // a3's self-added ones (acct(8), acct(9)) are on the node; it is missing the last shared-list entry and has one the platform does not (someone edited the node directly)
  : allowlistScenario() === 'normal' ? { 'node-a3': [...POLICY0.slice(0, -1), acct(8), acct(9), acct(20)] } : {};

// Alert rules (mutable inside MSW): default rules (mirroring backend/internal/alerting/defaults.go) + one user-created rule.
export const defaultRule = (name: string, kind: AlertRule['kind'], forDuration: AlertRule['forDuration'], severity: AlertRule['severity'], cond?: Partial<Pick<AlertRule, 'metric' | 'operator' | 'threshold' | 'clearThreshold'>>): AlertRule => ({
  id: `rule-${name}`, kind, isDefault: true, alertname: name, name, forDuration, keepFiringFor: '2m', severity, enabled: true, ...cond,
  createdBy: 'system', updatedBy: 'system', createdAt: ago(60 * 24 * 30), updatedAt: ago(60 * 24 * 30),
});
/** Default rules registered by Enterprise (mirroring alerting.RegisterFixedRule); empty in the open-source build. */
export const extraDefaultRules: AlertRule[] = [];
const defaultRules = (): AlertRule[] => [
  ...baseDefaultRules(),
  ...extraDefaultRules.map((r) => ({ ...r })),
];
const baseDefaultRules = (): AlertRule[] => [
  defaultRule('PlatformDown', 'platform_down', '1m', 'critical'),
  defaultRule('NodeOffline', 'node_offline', '2m', 'critical'),
  defaultRule('BesuMetricsDown', 'besu_metrics_down', '2m', 'critical'),
  defaultRule('NodePeerCountLow', 'threshold', '5m', 'warning', { metric: 'peer_count', operator: 'lt', threshold: 2, clearThreshold: 3 }),
  defaultRule('NodeCpuHigh', 'threshold', '5m', 'warning', { metric: 'cpu', operator: 'gt', threshold: 90, clearThreshold: 85 }),
  defaultRule('NodeMemHigh', 'threshold', '5m', 'warning', { metric: 'mem', operator: 'gt', threshold: 90, clearThreshold: 85 }),
  defaultRule('NodeDiskHigh', 'threshold', '10m', 'warning', { metric: 'disk', operator: 'gt', threshold: 85, clearThreshold: 80 }),
  // Below are default rules added for the conditions where pages turn orange or red; pages read these thresholds, so both sides use the same number
  defaultRule('NodeBehind', 'threshold', '2m', 'warning', { metric: 'blocks_behind', operator: 'gte', threshold: 5 }),
  defaultRule('ChainSlow', 'chain_slow', '1m', 'warning', { threshold: 5 }),
  defaultRule('ChainSlowSevere', 'chain_slow', '1m', 'critical', { threshold: 20 }),
  defaultRule('ValidatorsMissing', 'validators_missing', '2m', 'warning'),
  defaultRule('QuorumLost', 'quorum_lost', '1m', 'critical'),
  defaultRule('NodeTxpoolFull', 'threshold', '5m', 'warning', { metric: 'txpool_usage', operator: 'gte', threshold: 80, clearThreshold: 70 }),
  defaultRule('NodeTxpoolStale', 'threshold', '5m', 'warning', { metric: 'txpool_oldest', operator: 'gte', threshold: 60, clearThreshold: 30 }),
  defaultRule('NodeJvmHeapHigh', 'threshold', '5m', 'warning', { metric: 'jvm_heap', operator: 'gt', threshold: 90, clearThreshold: 85 }),
];
// Default rules exist as soon as the platform is installed (backend seed); the user-defined one is absent in a fresh deployment
export const alertRules: AlertRule[] = FRESH ? defaultRules() : [
  ...defaultRules(),
  {
    id: 'rule-001',
    kind: 'threshold',
    isDefault: false,
    alertname: 'custom_rule_rule-001',
    name: 'node-a1 CPU 過高',
    metric: 'cpu',
    nodeId: 'node-a1',
    operator: 'gt',
    threshold: 80,
    forDuration: '5m',
    keepFiringFor: '0s',
    severity: 'warning',
    enabled: true,
    createdBy: 'admin',
    updatedBy: 'admin',
    createdAt: ago(1440),
    updatedAt: ago(1440),
  },
];

/** Restore default rules: re-add deleted ones and reset to default values (keeping the original ids). */
export function restoreDefaultAlertRules(): AlertRule[] {
  for (const d of defaultRules()) {
    const i = alertRules.findIndex((r) => r.alertname === d.alertname);
    if (i === -1) alertRules.push(d);
    else alertRules[i] = { ...d, id: alertRules[i].id, updatedAt: now() };
  }
  return alertRules;
}

export function updateAlertRule(id: string, req: AlertRuleUpdateRequest, updatedBy: string): AlertRule | null {
  const i = alertRules.findIndex((r) => r.id === id);
  if (i === -1) return null;
  const rule: AlertRule = { ...alertRules[i], ...req, keepFiringFor: req.keepFiringFor ?? alertRules[i].keepFiringFor, enabled: req.enabled ?? true, updatedBy, updatedAt: now() };
  alertRules[i] = rule;
  return rule;
}

// Mock session (mutable inside MSW). Logged out by default. The username is kept on the session so password changes map back to the account.
// Stored in sessionStorage: a module variable resets on F5 and would kick the user back to the login page (the real backend uses a cookie session, so it does not),
// purely a dev experience fix; cleared when the tab closes, so "logged out by default" still holds.
const savedSession = typeof sessionStorage !== 'undefined' ? sessionStorage.getItem(MOCK_SESSION_KEY) : null;
export const session: { me: Me | null; username: string | null } = savedSession
  ? (JSON.parse(savedSession) as { me: Me | null; username: string | null })
  : { me: null, username: null };
export const persistSession = () => {
  try { sessionStorage.setItem(MOCK_SESSION_KEY, JSON.stringify(session)); } catch { /* SSR/private browsing: skip */ }
};
export function meForUser(username: string): Me {
  const u = mockUsers[username];
  return { user: { id: username, name: u.name }, role: u.role, org: 'org-a', mustChangePassword: u.mustChange ?? false };
}

/** Join tokens. Every node has a used one (nodeName is the name entered at join, allowedSources is that node's IP);
 *  plus one expired and one revoked, bound to node ids that never connected. POST unshifts new ones, simulating a stateful list. */
const hoursAgo = (h: number) => new Date(Date.now() - h * 3600_000).toISOString();
export const joinTokens: JoinToken[] = FRESH ? [] : [
  ...summaries.map((s, i): JoinToken => ({
    id: `jt-${String(i + 1).padStart(2, '0')}`, nodeId: s.id, nodeName: s.name ?? null,
    allowedSources: [`${IDENTITIES[s.id as IdentityKey].ip}/32`], status: 'used', createdAt: hoursAgo(72 - i * 6), expiresAt: hoursAgo(71 - i * 6),
  })),
  { id: 'jt-10', nodeId: 'node-a6', nodeName: '測試機', allowedSources: [], status: 'expired', createdAt: hoursAgo(5), expiresAt: hoursAgo(4) },
  { id: 'jt-11', nodeId: 'node-a7', nodeName: '備援節點(誤發)', allowedSources: ['10.0.0.7/32'], status: 'revoked', createdAt: hoursAgo(2), expiresAt: hoursAgo(1) },
];

export const randHex = (n: number) => Array.from({ length: n }, () => Math.floor(Math.random() * 16).toString(16)).join('');

const TTL_MS: Record<JoinTokenTtl, number> = { '15m': 15 * 60_000, '1h': 3600_000, '24h': 24 * 3600_000 };

// sha256 of the platform root CA certificate (embedded in the token; the agent uses it to verify the platform against MITM)
const ROOT_DIGEST = fakeHex('root-ca:org-a', 64);
const randB64url = (bytes: number) => btoa(String.fromCharCode(...crypto.getRandomValues(new Uint8Array(bytes)))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');

/** Generate a new (one-time) token in the backend's format: BSOS-1-sha256:<root CA digest>-<32 bytes base64url secret>. */
export function makeJoinToken(req: JoinTokenCreateRequest): JoinTokenCreated {
  const id = `jt-${String(joinTokens.length + 1).padStart(2, '0')}`;
  const token = `BSOS-1-sha256:${ROOT_DIGEST}-${randB64url(32)}`;
  return {
    id,
    nodeName: req.nodeName ?? null,
    allowedSources: (req.allowedSources ?? []).map((s) => (s.includes('/') ? s : `${s}/${s.includes(':') ? 128 : 32}`)),
    status: 'pending',
    createdAt: now(),
    expiresAt: new Date(Date.now() + TTL_MS[req.ttl ?? '1h']).toISOString(),
    nodeId: req.nodeId,
    token,
    platformAddr: 'platform.org-a:8443',
  };
}

/** Generate a time series (30 points), with a plausible baseline and noise per metric. */
export function makeSeries(id: string, metric: MetricId, sel: RangeSel): MetricSeries {
  // A stable offset per node id, so overlaid lines differ per node (but stay in the same range).
  const seed = [...id].reduce((a, c) => a + c.charCodeAt(0), 0);
  const bias = ((seed % 7) - 3) / 10; // -0.3 .. +0.3
  // Window: relative -> now back by dur; absolute -> fixed from..to.
  const endMs = sel.kind === 'abs' ? sel.to * 1000 : Date.now();
  const spanMs = sel.kind === 'abs' ? Math.max(60_000, (sel.to - sel.from) * 1000) : durToMs(sel.dur);
  const startMs = endMs - spanMs;
  // Sampling density increases with range (so long ranges still have fine lines), capped to stay lightweight.
  const n = spanMs <= 3600_000 ? 80 : spanMs <= 6 * 3600_000 ? 120 : spanMs <= 24 * 3600_000 ? 160 : 200;
  const base: Record<MetricId, [number, number, string]> = {
    cpu: [40, 18, '%'], mem: [42, 10, '%'], disk: [61, 4, '%'],
    net_rx: [2.4, 1.4, 'MB/s'], net_tx: [1.8, 1.1, 'MB/s'],
    peer_count: [7, 3, ''], chain_height: [HEAD, 0, ''], blocks_behind: [0, 0, ''], block_time: [2, 0, 's'],
    txpool_pending: [4, 6, ''], tps: [1.6, 1.5, ''],
    txpool_added: [1.6, 0, 'tx/s'], txpool_removed: [1.6, 0, 'tx/s'], txpool_rejected: [0, 0, 'tx/min'],
    jvm_heap: [id === 'node-a2' ? 78 : 58, 14, '%'],
    txpool_usage: [1.2, 0.6, '%'], txpool_oldest: [20, 25, 's'],
  };
  const [b0, amp, unit] = base[metric] ?? [0, 0, ''];
  const s = summaries.find((x) => x.id === id);
  // Chain height and peer count end at the node's current value, so the last chart point agrees with the list and detail
  const b = metric === 'chain_height' ? (s?.chainHeight ?? b0) : metric === 'peer_count' && s?.peerCount != null ? s.peerCount : Math.max(0, b0 * (1 + bias * 0.5));
  // The per-step chain height increment scales with the sampling interval (about one block per 5s), keeping a monotonic upward trend over long ranges.
  const heightStep = Math.max(1, Math.round((spanMs / n) / 5000));
  // Random walk + slow mean reversion: lines look like real metrics (with trends, not pure noise).
  let v = b;
  const points = Array.from({ length: n }, (_, i) => {
    const tMs = startMs + (i / (n - 1)) * spanMs;
    const t = new Date(tMs).toISOString();
    if (metric === 'block_time') {
      // Chain-level fact: block time is the same on all nodes, QBFT block period 2s
      v = 2;
    } else if (metric === 'txpool_added' || metric === 'txpool_removed') {
      // Pool in/out rates: both driven by time, outflow lags slightly and runs a bit lower than inflow (the pool slowly accumulates)
      const k = metric === 'txpool_added' ? 0 : 1;
      const bucket = Math.floor(tMs / 60_000) - k;
      const h = Math.sin(bucket * 78.233) * 43758.5453;
      v = Math.max(0, 1.4 + (h - Math.floor(h)) * 0.8 + Math.sin(bucket / 9) * 0.4 - k * 0.05);
    } else if (metric === 'txpool_rejected') {
      // Zero most of the time, with an occasional burst (resent nonce, accounts not on the allowlist)
      const bucket = Math.floor(tMs / 60_000);
      v = bucket % 23 === 0 ? 3 + (bucket % 4) : bucket % 7 === 0 ? 1 : 0;
    } else if (metric === 'txpool_pending') {
      // Mostly a chain-level fact: all nodes see the same mempool, so the value is driven by time; but broadcasts arrive at slightly different times on each node,
      // so each node adds a bit of its own jitter so the four lines separate (real nodes never overlap exactly either)
      const bucket = Math.floor(tMs / 60_000);
      const h = Math.sin(bucket * 12.9898) * 43758.5453;
      const jitter = Math.round(Math.sin(bucket / 3 + seed * 1.7) * 1.5 + (seed % 3));
      v = Math.round(4 + (h - Math.floor(h)) * 6 + Math.sin(bucket / 7) * 2) + jitter;
      // The last point matches the transaction pool tab's snapshot (the blocked batch is always there, as the floor)
      v = Math.max(0, v) + poolFloor(id);
    } else if (metric === 'blocks_behind') {
      // node-a4 stalled for a while earlier, and the lag has accumulated up to its current value (it catches up while syncing)
      if (id === 'node-a4' && (s?.blocksBehind ?? 0) > 0) {
        v = Math.round((s?.blocksBehind ?? 0) * (i / (n - 1)) ** 2);
      } else {
        // Other nodes mostly keep up: irregularly 1 block behind now and then, sometimes 2-3, rarely 4-6; a lag lingers a point or two before recovering.
        // A different hash per node keeps the lines from lining up into a row of spikes
        const h = Math.sin((i + 1) * 12.9898 + seed * 78.233) * 43758.5453;
        const r = h - Math.floor(h);
        const fresh = r > 0.995 ? 4 + (Math.floor(r * 1000) % 3) : r > 0.975 ? 2 + (Math.floor(r * 100) % 2) : r > 0.93 ? 1 : 0;
        v = Math.max(fresh, v > 0 && r > 0.35 ? Math.floor(v / 2) : 0);
      }
    } else if (metric === 'disk') {
      // Disk: chain data only grows, it does not bounce. A steady slow rise across the range (at least 0.6 percentage points, about 0.12 per hour over long ranges), ending at the node's current value; only tiny noise
      const end = s?.diskUsedPercent ?? b0;
      const rise = Math.max(2, (spanMs / 3600_000) * 0.15); // the climb must be visible on a 0-100 axis
      v = end - rise * (1 - i / (n - 1)) + (Math.sin(i * 1.7 + seed) * 0.03);
    } else if (metric === 'chain_height') {
      v = b - (n - 1 - i) * heightStep; // height increases monotonically, last point = current height
    } else {
      const drift = (Math.random() - 0.5) * amp * 0.5; // step size
      const pull = (b - v) * 0.08; // mean reversion, to avoid drifting away
      v = Math.max(0, v + drift + pull);
      if (metric === 'peer_count') v = i === n - 1 ? b : Math.round(v);
    }
    return { t, v: Number(v.toFixed(2)) };
  });
  return { metric, unit, points };
}

// ── Transaction pool ──────────────────────────────────────────────────
// One per node; times are written as "seconds before now" and recomputed on every request, so the blocked duration on screen keeps growing.
// Nonce gaps are deliberately <= 4: the sequenced transaction pool common on consortium chains by default only keeps up to "on-chain nonce + 4" per account; anything further is rejected.
// Nonce-gapped transactions are relayed to all nodes: the same stuck transaction is visible on a1 (sent directly to it) and a4 (relayed from another node).
type MockPoolTx = { nonce: number; ago: number; to?: string | null; local?: boolean };
type MockPoolSender = { seed: string; next: number; txs: MockPoolTx[] };
const poolAcct = (seed: string) => `0x${fakeHex(`acct-${seed}`, 40)}`;
const run = (from: number, to: number, ago: (i: number) => number, more: Partial<MockPoolTx> = {}): MockPoolTx[] =>
  Array.from({ length: to - from + 1 }, (_, i) => ({ nonce: from + i, ago: ago(i), ...more }));
const OPS_GAP: MockPoolSender = { seed: 'ops', next: 42, txs: run(43, 46, (i) => 1500 - i * 300) };
const BATCH: MockPoolSender = { seed: 'batch', next: 1200, txs: run(1200, 1229, (i) => 3 - i * 0.05) };
const ORACLE: MockPoolSender = { seed: 'oracle', next: 88_410, txs: run(88_410, 88_417, (i) => 6 - i * 0.6, { local: false }) };
const POOLS: Record<string, MockPoolSender[]> = {
  'node-a1': [
    OPS_GAP,
    { seed: 'settle', next: 109, txs: run(111, 112, (i) => 190 - i * 30, { local: false }) },
    BATCH,
    ORACLE,
    // Block just produced: the on-chain nonce is already 15, but the pool still holds 14
    { seed: 'user-14', next: 15, txs: [{ nonce: 14, ago: 2, local: false }, { nonce: 15, ago: 1, local: false }] },
    { seed: 'user-77', next: 77, txs: run(77, 78, () => 2, { local: false }) },
  ],
  'node-a3': [
    { ...OPS_GAP, txs: OPS_GAP.txs.map((tx) => ({ ...tx, ago: tx.ago - 1, local: false })) },
    { seed: 'user-77', next: 77, txs: run(77, 78, () => 2, { local: false }) },
  ],
  'node-a2': [{ ...BATCH, txs: BATCH.txs.slice(0, 6).map((tx) => ({ ...tx, local: false })) }],
};
/** Syncing (a4), RPC unreachable (a5, disconnected) */
export const txpoolSyncing = (id: string) => id === 'node-a4';
export const txpoolUnreachable = (id: string) => ['node-a5'].includes(id);
// Rejections in the last hour (by reason); the real source is the current rejected_total{reason} minus the value 1h ago (use the current value if the difference is negative or there was no series 1h ago)
const REJECTED: Record<string, Record<string, number>> = {
  'node-a1': { NONCE_TOO_LOW: 9, GAS_PRICE_TOO_LOW: 4, TX_SENDER_NOT_AUTHORIZED: 3, NONCE_TOO_FAR_IN_FUTURE_FOR_SENDER: 1 },
  'node-a3': { NONCE_TOO_LOW: 2, TX_SENDER_NOT_AUTHORIZED: 11 },
  'node-a2': { TRANSACTION_ALREADY_KNOWN: 6 },
};
/** Transaction pool snapshot count minus the part that flows: the floor of the time series, so the chart's tail agrees with the snapshot */
function poolFloor(id: string): number {
  const n = (POOLS[id] ?? []).reduce((a, s) => a + s.txs.length, 0);
  return Math.max(0, n - 8);
}

/** Same as the backend: with from, only that address; without, the top 200 accounts by transaction count. totals' count and oldest time cover the whole pool */
export function txpoolOf(id: string, from?: string): NodeTxPool {
  const t0 = Date.now();
  const base = { asOf: now(), localCount: 0, remoteCount: 0, totals: { pending: 0, queued: 0 }, truncated: false, senders: [] };
  if (txpoolSyncing(id)) return { ...base, syncing: true };
  const all = (POOLS[id] ?? []).map((s) => ({
    address: poolAcct(s.seed), nextNonce: s.next,
    txs: s.txs.map((tx) => ({
      hash: `0x${fakeHex(`${s.seed}-${tx.nonce}`, 64)}`, nonce: tx.nonce,
      to: tx.to === undefined ? poolAcct('contract-registry') : tx.to,
      addedAt: new Date(t0 - tx.ago * 1000).toISOString(), local: tx.local ?? true,
    })),
  }));
  const pick = from ? all.filter((s) => s.address === from.toLowerCase()) : [...all].sort((a, b) => b.txs.length - a.txs.length).slice(0, 200);
  const txs = all.flatMap((s) => s.txs);
  const localCount = txs.filter((tx) => tx.local).length;
  const lanes = analyzePool(pick, t0);
  const scope = from ? pick.flatMap((s) => s.txs) : txs;
  return {
    ...base, syncing: false, maxSize: 4096, localCount, remoteCount: txs.length - localCount,
    totals: {
      pending: lanes.reduce((n, l) => n + l.ready.length, 0),
      queued: lanes.reduce((n, l) => n + l.blocked.length, 0),
      oldestAddedAt: scope.length ? scope.map((tx) => tx.addedAt).sort()[0] : undefined,
    },
    truncated: !from && all.length > 200,
    senders: pick,
    rejected: Object.entries(REJECTED[id] ?? {}).map(([reason, count]) => ({ reason, count })).sort((a, b) => b.count - a.count),
  };
}

/** Validator vote history (closed): three passed, one cleared at epoch rollover (our org withdrew its vote midway); times count back from MOCK_T0 and block numbers match one block every 2 seconds */
export function validatorVoteHistory(): ValidatorVoteRecord[] {
  const head = chainInfo().height ?? HEAD;
  const at = (minAgo: number) => ({ iso: ago(minAgo), block: head - Math.round((minAgo * 60) / 2) });
  // Validators eligible at the time: the current validator set (ours marked own); the first `votes` of them voted in order between start and end, including ours if we voted
  const set = currentValidators().map((v) => ({ address: v.address.toLowerCase(), own: v.orgId === 'org-a' && !!v.nodeId, name: v.name ?? null }));
  const epoch = chainInfo().epochLength ?? 30000;
  const rec = (address: string, add: boolean, result: 'passed' | 'expired', startMin: number, endMinIn: number, votes: number, ownVoted: boolean, ownWithdraws = false): ValidatorVoteRecord => {
    const s = at(startMin);
    // An epoch-rollover clear ends at the first epoch boundary after the start (a height that is a multiple of the epoch length)
    const endMin = result === 'expired' ? ((head - (s.block - (s.block % epoch) + epoch)) * 2) / 60 : endMinIn;
    const e = at(endMin);
    // Our org withdrew midway: the withdrawal falls between start and end; a vote already in a block stays among those cast
    const ownWithdrawnMin = ownWithdraws ? (startMin + endMin) / 2 : null;
    const seed = [...address].reduce((n, c) => n + c.charCodeAt(0), 0);
    const order = [...set].sort((x, y) => ((seed * 7 + x.address.charCodeAt(5)) % 13) - ((seed * 7 + y.address.charCodeAt(5)) % 13));
    // If we withdrew: withdrawal cancels with an opposite vote, so our vote is not among the counted votes
    const voters = ownVoted && !ownWithdraws ? [...order.filter((x) => !x.own).slice(0, 1), ...order.filter((x) => x.own), ...order.filter((x) => !x.own).slice(1)] : order.filter((x) => !x.own);
    // Votes land in blocks in order between start and end; when we withdraw midway, all votes were cast at least 10 minutes before the withdrawal
    const voteEnd = ownWithdrawnMin != null ? ownWithdrawnMin + 10 : endMin;
    const step = votes > 1 ? (startMin - voteEnd) / (votes - 1) : 0;
    const voted = new Map(voters.slice(0, votes).map((x, i) => { const v = at(startMin - step * i); return [x.address, v] as const; }));
    const ballots = [...set].sort((x, y) => x.address.localeCompare(y.address))
      .map((x) => ({ address: x.address, own: x.own, name: x.name, votedAt: voted.get(x.address)?.iso ?? null, block: voted.get(x.address)?.block ?? null,
        withdrawnAt: x.own && ownWithdrawnMin != null ? ago(ownWithdrawnMin) : null }));
    return { address, add, result, firstVoteAt: s.iso, endedAt: e.iso, endedBlock: e.block, votes, needed: 7, ballots };
  };
  return [
    rec(`0x${fakeHex('hist-org-f', 40)}`, true, 'passed', 60 * 24 * 3 + 40, 60 * 24 * 3, 7, true),
    rec(`0x${fakeHex('hist-test', 40)}`, true, 'expired', 60 * 24 * 5 + 120, 0, 3, true, true),
    rec(`0x${fakeHex('hist-old-c', 40)}`, false, 'passed', 60 * 24 * 16 + 300, 60 * 24 * 16, 8, true),
    rec(`0x${fakeHex('hist-org-e2', 40)}`, true, 'passed', 60 * 24 * 21 + 90, 60 * 24 * 21, 7, false),
  ];
}
