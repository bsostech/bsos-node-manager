import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import type { AlertRule, AlertRuleOperator, MetricId } from '@/api/schemas';
import { crosses } from '@/lib/alertRules';

/**
 * There is one set of thresholds for turning the page orange or red: the default rules in alert settings.
 * Pages look at "now" and color as soon as a threshold is crossed; alerts notify only after it persists for the rule's duration. Both compare the same number.
 * When a rule is deleted or not loaded yet, the same values as the backend default rules are used (DEFAULT_THRESHOLDS).
 */
export type Cmp = { op: AlertRuleOperator; th: number };
export type Thresholds = {
  cpu: Cmp;
  mem: Cmp;
  disk: Cmp;
  /** Too few peers (absolute threshold); "far fewer than the others" is handled separately by the outlier rule */
  peers: Cmp;
  /** Blocks behind */
  lag: Cmp;
  /** Slow / severely slow block production: time since the last block as a multiple of the normal block time */
  slowX: number;
  severeX: number;
  /** Tx pool usage (%) and oldest transaction wait in seconds */
  poolFull: Cmp;
  poolOldSec: Cmp;
  /** % of Besu's own memory allowance (Java heap) in use */
  jvmHeap: Cmp;
};

export const DEFAULT_THRESHOLDS: Thresholds = {
  cpu: { op: 'gt', th: 90 },
  mem: { op: 'gt', th: 90 },
  disk: { op: 'gt', th: 85 },
  peers: { op: 'lt', th: 2 },
  lag: { op: 'gte', th: 5 },
  slowX: 5,
  severeX: 20,
  poolFull: { op: 'gte', th: 80 },
  poolOldSec: { op: 'gte', th: 60 },
  jvmHeap: { op: 'gt', th: 90 },
};

/** Rule identifier (alertname) -> threshold field. The identifier is the default rule's preset and cannot be changed by users. */
const CMP_RULES: Record<string, keyof Thresholds> = {
  NodeCpuHigh: 'cpu',
  NodeMemHigh: 'mem',
  NodeDiskHigh: 'disk',
  NodePeerCountLow: 'peers',
  NodeBehind: 'lag',
  NodeTxpoolFull: 'poolFull',
  NodeTxpoolStale: 'poolOldSec',
  NodeJvmHeapHigh: 'jvmHeap',
};

export function thresholdsFromRules(rules: AlertRule[] | undefined): Thresholds {
  const out: Thresholds = { ...DEFAULT_THRESHOLDS };
  for (const r of rules ?? []) {
    if (!r.isDefault || r.threshold == null) continue;
    const key = CMP_RULES[r.alertname];
    if (key && r.operator) (out as Record<string, unknown>)[key] = { op: r.operator, th: r.threshold };
    if (r.alertname === 'ChainSlow') out.slowX = r.threshold;
    if (r.alertname === 'ChainSlowSevere') out.severeX = r.threshold;
  }
  return out;
}

/** Read alert rules and turn them into thresholds. Everyone who can see monitoring pages needs read access. */
export function useThresholds(): Thresholds {
  const q = useQuery({ queryKey: ['alertRules'], queryFn: () => api.listAlertRules(), staleTime: 60_000 });
  return thresholdsFromRules(q.data);
}

export const over = (v: number | null | undefined, c: Cmp) => crosses(v, c.op, c.th);

/** Threshold text: "> 90", "≥ 5". For the ⓘ explanation; the number is the same as in alert settings */
const SYMBOL: Record<AlertRuleOperator, string> = { gt: '>', lt: '<', gte: '≥', lte: '≤' };
export const cmpText = (c: Cmp, unit = '') => `${SYMBOL[c.op]} ${c.th}${unit}`;

/** Dashed threshold line on charts: drawn only for metrics with a matching alert rule */
export function thresholdLine(metric: MetricId | 'lag', th: Thresholds): number[] | undefined {
  const c = metric === 'cpu' ? th.cpu : metric === 'mem' ? th.mem : metric === 'disk' ? th.disk : metric === 'jvm_heap' ? th.jvmHeap
    : metric === 'peer_count' ? th.peers : metric === 'blocks_behind' || metric === 'lag' ? th.lag : null;
  return c ? [c.th] : undefined;
}
