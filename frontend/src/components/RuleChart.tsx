import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import type { AlertRuleOperator, MetricId } from '@/api/schemas';
import { useFleetSeries } from '@/components/FleetTrend';
import { UPlotChart } from '@/components/UPlotChart';
import { compactNum } from '@/components/MetricPanel';
import { Skeleton } from '@/components/States';
import { durToMs } from '@/lib/range';
import { crosses, isPercentMetric } from '@/lib/alertRules';

const QUIET = 'var(--bsos-text-faint)';
const LOUD = 'var(--bsos-status-error-fg)';

/** Data for a threshold-type rule: each node's metric series + who is currently over the threshold. Shared by the rule page overview, the settings preview and new rule. */
export function useRuleSeries(ids: string[], metric: MetricId, op: AlertRuleOperator, th: number, range: string) {
  const fs = useFleetSeries(ids, metric, { kind: 'rel', dur: range }, true);
  const unit = isPercentMetric(metric) ? '%' : fs.unit ? ` ${fs.unit}` : '';
  const fmt = (v: number | null) => (v == null ? '—' : `${compactNum(v)}${unit}`);
  const hot = (id: string) => crosses(fs.latest.get(id), op, th);
  return { fs, fmt, hot, hotIds: ids.filter(hot) };
}

/** One metric line per node and a dashed threshold; nodes over the threshold are drawn red and labeled, the rest faint gray. */
export function RuleChart({ ids, series, th, range, metric, label, height = 220 }: {
  ids: string[];
  series: ReturnType<typeof useRuleSeries>;
  th: number;
  range: string;
  metric: MetricId;
  label: string;
  height?: number;
}) {
  const { t } = useTranslation();
  const { fs, fmt, hot } = series;
  const visible = ids.filter((id) => fs.withData.includes(id));
  // Draw those over the threshold last, on top of the gray lines
  const order = [...visible.filter((id) => !hot(id)), ...visible.filter(hot)];
  const xWindow = useMemo<[number, number]>(() => {
    const now = Date.now() / 1000;
    return [now - durToMs(range) / 1000, now];
  }, [range, fs.xs.at(-1)]); // eslint-disable-line react-hooks/exhaustive-deps

  if (!fs.settled && fs.withData.length === 0) return <Skeleton className="w-full" style={{ height }} />;
  if (visible.length === 0) return <p className="py-8 text-center text-xs text-ink-3">{t(fs.monitoring ?? 'overview.fleetEmpty')}</p>;
  return (
    <UPlotChart
      data={[fs.xs, ...order.map((id) => fs.values.get(id) ?? [])]}
      seriesDefs={order.map((id) => ({ label: id, color: hot(id) ? LOUD : QUIET, width: hot(id) ? 2 : 1.25, endLabel: hot(id) }))}
      thresholds={[th]}
      yRange={isPercentMetric(metric) ? [0, 100] : undefined}
      xWindow={xWindow}
      valueFmt={fmt}
      height={height}
      label={label}
    />
  );
}
