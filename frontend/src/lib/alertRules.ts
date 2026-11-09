import type { TFunction } from 'i18next';
import type { Alert, AlertRule, AlertRuleOperator, MetricId } from '@/api/schemas';

// The metric catalog reuses the same i18n keys as the chart panels in NodeDetail.tsx instead of inventing a second set of metric names.
export const METRIC_OPTIONS: [MetricId, string][] = [
  ['cpu', 'detail.cpu'], ['mem', 'detail.memory'], ['disk', 'detail.disk'],
  ['net_rx', 'detail.netRx'], ['net_tx', 'detail.netTx'], ['peer_count', 'detail.peers'],
  ['chain_height', 'detail.chainHeight'], ['block_time', 'detail.blockTime'],
  ['txpool_pending', 'detail.txpool'], ['tps', 'detail.tps'],
];
export const OPERATOR_SYMBOL: Record<AlertRuleOperator, string> = { gt: '>', lt: '<', gte: '≥', lte: '≤' };

/** Metrics used only by default rules and not listed in the custom rule metric picker */
const EXTRA_LABELS: Record<string, string> = { blocks_behind: 'detail.lagChart', jvm_heap: 'detail.jvmHeap', txpool_usage: 'alerts.metric.txpoolUsage', txpool_oldest: 'alerts.metric.txpoolOldest' };
export const metricLabel = (t: TFunction, m: MetricId) => t(METRIC_OPTIONS.find(([id]) => id === m)?.[1] ?? EXTRA_LABELS[m] ?? m);

/** Percentage metrics: the chart y axis is fixed at 0~100 and the unit is %. */
export const isPercentMetric = (m?: MetricId) => m === 'cpu' || m === 'mem' || m === 'disk' || m === 'txpool_usage' || m === 'jvm_heap';

/** Allowed threshold range: 0~100 for percentage metrics, non-negative for the rest (the backend validThreshold enforces the same). */
export const thresholdRange = (m: MetricId): { min: number; max?: number } => (isPercentMetric(m) ? { min: 0, max: 100 } : { min: 0 });

/** Text for the allowed threshold range (for error hints): "0–100" or "≥ 0". */
export const rangeText = (m: MetricId) => { const { min, max } = thresholdRange(m); return max == null ? `≥ ${min}` : `${min}–${max}`; };

/** Threshold unit (edit screen): % for percentage metrics, seconds for tx pool oldest wait, none otherwise. The condition text puts an extra space before the seconds unit, handled separately. */
export const metricUnit = (t: TFunction, m?: MetricId) => (isPercentMetric(m) ? '%' : m === 'txpool_oldest' ? t('alerts.unitSec').trim() : '');

/** Condition text of a rule (without duration): threshold rules read like "Any node CPU > 90%"; other kinds get a fixed sentence per kind. */
export function ruleCondition(t: TFunction, r: Pick<AlertRule, 'kind' | 'metric' | 'operator' | 'threshold' | 'nodeId'>): string {
  if (r.kind === 'chain_slow') return t('alerts.kind.chain_slow', { x: r.threshold });
  if (r.kind !== 'threshold' || !r.metric || !r.operator) return t(`alerts.kind.${r.kind}`);
  const unit = r.metric === 'txpool_oldest' ? t('alerts.unitSec') : metricUnit(t, r.metric);
  return cjkSpace(t('alerts.cond', {
    node: r.nodeId ?? t('alerts.rules.allNodes'),
    metric: metricLabel(t, r.metric),
    op: OPERATOR_SYMBOL[r.operator],
    th: `${r.threshold}${unit}`,
  }));
}

/** Insert spaces between Chinese and Latin letters/digits: the condition text is assembled from node IDs and metric names, so the Chinese locale file cannot pre-write the spaces. */
const cjkSpace = (s: string) => s.replace(/(\p{Script=Han})([A-Za-z0-9])/gu, '$1 $2').replace(/([A-Za-z0-9%])(\p{Script=Han})/gu, '$1 $2');

/** Threshold comparison (used by the frontend to mark charts red; same comparison as the PromQL rendered by the backend). */
const COMPARE: Record<AlertRuleOperator, (v: number, th: number) => boolean> = {
  gt: (v, th) => v > th, lt: (v, th) => v < th, gte: (v, th) => v >= th, lte: (v, th) => v <= th,
};
export const crosses = (v: number | null | undefined, op: AlertRuleOperator, th: number) => v != null && COMPARE[op](v, th);

/**
 * Human-readable rule name: default rules use their Chinese name (when not renamed), custom rules use the name the user gave.
 * The identifier (alertname) is for notification channels and filtering, not shown to people directly.
 */
export function ruleTitle(t: TFunction, r: Pick<AlertRule, 'name' | 'alertname' | 'isDefault'>): string {
  return r.isDefault && r.name === r.alertname ? t(`alerts.preset.${r.alertname}`, { defaultValue: r.name }) : r.name;
}
/** Human-readable duration: 1m -> "1 min", 1h -> "1 h" */
export const durLabel = (t: TFunction, d: string): string => {
  if (d === '0s') return t('alerts.durNow');
  const m = /^(\d+)([mh])$/.exec(d);
  return m ? t(m[2] === 'h' ? 'alerts.durH' : 'alerts.durM', { n: Number(m[1]) }) : d;
};
/** The clause after the condition: ", for 5 min"; 0s means "fire as soon as it happens" and reads ", fires immediately" */
export const forPhrase = (t: TFunction, d: string): string => (d === '0s' ? t('alerts.condNow') : t('alerts.condFor', { d: durLabel(t, d) }));
/** Chinese name of an alert (default rules only); custom rules return null and the UI uses the alert summary instead */
export const alertTitle = (t: TFunction, alertname: string): string | null => {
  const v = t(`alerts.preset.${alertname}`, { defaultValue: '' });
  return v || null;
};

// Default rules whose summary carries a value (threshold rules): without a value, do not build it ourselves but fall back to the backend summary so the number is not lost
const VALUE_ALERTS = new Set(['NodePeerCountLow', 'NodeCpuHigh', 'NodeMemHigh', 'NodeDiskHigh', 'NodeBehind', 'NodeTxpoolFull', 'NodeTxpoolStale', 'NodeJvmHeapHigh']);

/**
 * Alert summary and likely cause, following the UI language. The backend default rules only have Chinese summaries and descriptions (defaults.go),
 * so default rules are built from the frontend locale files (alerts.summary / alerts.cause, with node and value); anything missing there (custom rules are user-written) shows the backend text as is.
 * The value comes from annotations.value (threshold rules only); integers as is, decimals to one place.
 */
export function alertText(t: TFunction, a: Alert): { summary: string; cause: string } {
  const node = a.labels.node ?? a.nodeId ?? '';
  const n = a.annotations.value != null && a.annotations.value !== '' ? Number(a.annotations.value) : NaN;
  const value = Number.isFinite(n) ? (Number.isInteger(n) ? String(n) : n.toFixed(1)) : null;
  const own = value != null || !VALUE_ALERTS.has(a.alertname) ? t(`alerts.summary.${a.alertname}`, { defaultValue: '', node, value }) : '';
  return {
    summary: own || a.annotations.summary || alertTitle(t, a.alertname) || a.alertname,
    cause: t(`alerts.cause.${a.alertname}`, { defaultValue: '', node }) || a.annotations.description || '',
  };
}
