import type { NodeCapabilities } from '@/api/schemas';

/**
 * The single definition of required / optional. The capability checklist, node list issue column, detail page status bar, settings tab dots and requirements table all read this.
 * Required: agent, RPC reachable (ETH, NET, IBFT/QBFT), metrics. Missing ones are "Missing" and count toward needs-attention and abnormal.
 * Optional: ADMIN, TXPOOL, PERM / account allowlist file, logs, RPC auth. When off they are "Off", not counted and not in the issue column.
 */
export const OPTIONAL_APIS = ['ADMIN', 'TXPOOL', 'PERM'] as const;
export type OptionalApi = (typeof OPTIONAL_APIS)[number];

const isOptional = (ns: string) => (OPTIONAL_APIS as readonly string[]).includes(ns);

/** Missing required namespaces (NET, IBFT/QBFT; if ETH is unreachable the whole RPC counts as unreachable). */
export const requiredMissing = (c: Pick<NodeCapabilities, 'missingRpcApis'>) => c.missingRpcApis.filter((ns) => !isOptional(ns));

/** Optional namespaces that are off. */
export const optionalOff = (c: Pick<NodeCapabilities, 'missingRpcApis'>) => c.missingRpcApis.filter(isOptional);

/** Whether RPC is connected (agent present, not rejected, reachable). Optional namespace state only means something when connected. */
export const rpcUp = (c: NodeCapabilities | null | undefined) => !!c?.agent && !c.rpcUnauthorized && c.rpcReachable === true;

/** An optional namespace: on / off (not enabled); RPC not connected = null (unknown). */
export function apiState(c: NodeCapabilities | null | undefined, ns: OptionalApi): 'on' | 'off' | null {
  if (!rpcUp(c)) return null;
  return c!.missingRpcApis.includes(ns) ? 'off' : 'on';
}

/** Account allowlist: PERM off and the allowlist file not enabled are the same "Off". */
export function allowlistState(c: NodeCapabilities | null | undefined): 'on' | 'off' | null {
  if (!rpcUp(c)) return null;
  if (c!.allowlistEnabled) return 'on';
  return c!.missingRpcApis.includes('PERM') || c!.allowlistEnabled === false ? 'off' : null;
}

// ponytail: "was connected" lives only in this tab's memory and is forgotten on reload; surviving reloads needs the backend to report "was connected".
const logsSeen = new Set<string>();

/**
 * Logs: on / off (not enabled, optional) / lost (connected earlier in this session, now disconnected) / null (unknown).
 * The name says observe: every call records "was connected" into a module-level set (called during render, idempotent), so it is not a pure function.
 */
export function observeLogsState(nodeId: string, c: NodeCapabilities | null | undefined): 'on' | 'off' | 'lost' | null {
  if (!c?.agent || c.logsConnected == null) return null;
  if (c.logsConnected) {
    logsSeen.add(nodeId);
    return 'on';
  }
  return logsSeen.has(nodeId) ? 'lost' : 'off';
}

/** Whether a required item is missing: 'error' = the platform cannot see it or is rejected, 'syncing' = visible but missing a required piece, null = all required items present (or agent offline, unknown). */
export function requiredTone(c: NodeCapabilities | null | undefined): 'error' | 'syncing' | null {
  if (!c?.agent) return null;
  if (c.rpcUnauthorized || c.rpcReachable === false) return 'error';
  if (c.metricsReachable === false || requiredMissing(c).length) return 'syncing';
  return null;
}

/** Compare a.b.c versions (numeric parts only; unrecognized parts count as 0). If the agent is older than what the platform ships, suggest an upgrade */
export const olderThan = (v: string, latest: string) => {
  const p = (x: string) => x.replace(/^v/, '').split('.').map((n) => parseInt(n, 10) || 0);
  const [a, b] = [p(v), p(latest)];
  for (let i = 0; i < Math.max(a.length, b.length); i++) if ((a[i] ?? 0) !== (b[i] ?? 0)) return (a[i] ?? 0) < (b[i] ?? 0);
  return false;
};
