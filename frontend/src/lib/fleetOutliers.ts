import type { NodeSummary } from '@/api/schemas';
import { type Thresholds, over } from '@/lib/thresholds';

/** Metrics selectable in cross-node trends. lag maps to the contract's blocks_behind (the larger of what Besu reports and the lag the platform computes from the chain head); jvm_heap is the % of Besu's own memory allowance (Java heap) in use. */
export type TrendMetric = 'lag' | 'peer_count' | 'cpu' | 'mem' | 'jvm_heap' | 'disk' | 'txpool_pending';

/** Dropdown order: host resources first (memory first, since Besu is a JVM and memory pressure is the most common reason a node dies; Besu memory right after, matching the list column order), chain and network last. */
export const TREND_METRICS: TrendMetric[] = ['mem', 'jvm_heap', 'cpu', 'disk', 'txpool_pending', 'lag', 'peer_count'];

function median(vs: number[]): number {
  const s = [...vs].sort((a, b) => a - b);
  const m = s.length >> 1;
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
}

/**
 * Nodes to mark orange, the union of two conditions:
 * 1. Above the alert threshold (th, from the default rules in alert settings): the same number as alert notifications; the page marks it immediately, while a notification fires only after it persists for a while.
 * 2. Far from the other nodes (latest value vs the median): marked on the page only, no notification. Uses absolute difference rather than standard deviation:
 *    with few nodes (3~10) standard deviation is unstable, and a threshold like "25 percentage points off" is obvious to operators.
 */
export function detectOutliers(metric: TrendMetric, latest: Map<string, number | null>, th: Thresholds): Set<string> {
  const vals = [...latest.values()].filter((v): v is number => v != null);
  const out = new Set<string>();
  if (vals.length === 0) return out;
  const med = median(vals);
  const many = vals.length > 1; // with only one node there is no median to compare against, so only the alert threshold applies
  for (const [id, v] of latest) {
    if (v == null) continue;
    const alarm =
      metric === 'lag' ? over(v, th.lag) :
      metric === 'disk' ? over(v, th.disk) :
      metric === 'mem' ? over(v, th.mem) :
      metric === 'cpu' ? over(v, th.cpu) :
      metric === 'jvm_heap' ? over(v, th.jvmHeap) :
      metric === 'peer_count' ? over(v, th.peers) :
      false;
    // Besu memory uses only the alert threshold: each node may give Besu a different allowance, so a usage difference does not mean anyone has a problem
    const apart = !many || metric === 'lag' || metric === 'jvm_heap' ? false :
      metric === 'disk' ? v - med >= 25 :
      metric === 'mem' || metric === 'cpu' ? Math.abs(v - med) >= 25 :
      metric === 'peer_count' ? (v === 0 && med > 0) || med - v >= Math.max(2, med * 0.3) : // only "fewer than the others" is a problem: 30% or more fewer (at least 2 peers) counts, 0 always counts. A ratio rather than a fixed count so it works for both a 4-node and a 30-node chain
      v - med >= Math.max(20, med * 2) || (med >= 20 && v <= med / 2);     // txpool: a backlog counts (double or more and at least 20 more); also counts if the others are receiving (median ≥ 20) and it has less than half (not getting tx gossip)
    if (alarm || apart) out.add(id);
  }
  return out;
}

/** Node list fields for each metric (used by the node detail "Needs attention" section; same thresholds as cross-node trends). */
const SUMMARY_FIELD: Record<TrendMetric, (n: NodeSummary) => number | null | undefined> = {
  lag: (n) => n.blocksBehind,
  peer_count: (n) => n.peerCount,
  cpu: (n) => n.cpuPercent,
  mem: (n) => n.memPercent,
  jvm_heap: (n) => n.jvmHeapPercent,
  disk: (n) => n.diskUsedPercent,
  txpool_pending: (n) => n.txpoolPending,
};

export type Deviation = { metric: TrendMetric; value: number; median: number };

/** Metrics to mark orange for one node: run detectOutliers over the current values of all our org's nodes and return the ones flagged for this node (with the median, for the explanation). */
export function nodeDeviations(id: string, nodes: NodeSummary[], th: Thresholds): Deviation[] {
  const out: Deviation[] = [];
  for (const metric of TREND_METRICS) {
    const latest = new Map(nodes.map((n) => [n.id, SUMMARY_FIELD[metric](n) ?? null] as [string, number | null]));
    const v = latest.get(id);
    if (v == null || !detectOutliers(metric, latest, th).has(id)) continue;
    const vals = [...latest.values()].filter((x): x is number => x != null);
    out.push({ metric, value: v, median: median(vals) });
  }
  return out;
}
