import { useMemo } from 'react';
import { ValueChip } from '@/components/ValueChip';
import { fmtInt } from '@/lib/format';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { ArrowsOut, ArrowClockwise } from '@phosphor-icons/react';
import type uPlot from 'uplot';
import { api, HttpError } from '@/api/client';
import { monitoringIssue } from '@/lib/errors';
import type { MetricId, MetricSeries } from '@/api/schemas';
import { type RangeSel, selKey, selWindow, breakGaps, stepForSeconds } from '@/lib/range';
import { UPlotChart } from './UPlotChart';
import { Skeleton } from './States';
import { Tooltip } from './Tooltip';
import { HelpTip } from './HelpTip';
import { AnimatedNumber } from '@/components/AnimatedNumber';

// The header's current value only uses samples at the tail of the window. A healthy node's last point is less than one step
// before the window end (at least MIN_STALE_SEC), plus CLOCK_SKEW_SEC to tolerate browser/server clock drift; beyond that the node
// has stopped reporting, so show "—" instead of a stale value. Long ranges have large steps, so a just-disconnected node's header
// may keep the old value for about one step; offline is indicated by the status badge and status bar.
const MIN_STALE_SEC = 60;
const CLOCK_SKEW_SEC = 15;

/** Compact number: 1.28M / 4.2k / integer / one decimal. Shared by axis labels and the current value. */
export function compactNum(v: number | null): string {
  if (v == null) return '—';
  const a = Math.abs(v);
  if (a >= 1e6) return (v / 1e6).toFixed(2) + 'M';
  if (a >= 1e4) return (v / 1e3).toFixed(1) + 'k';
  return Number.isInteger(v) ? String(v) : v.toFixed(1);
}

/** Numbers where every digit matters, like block height, aren't abbreviated: 1,284,553 not 1.28M (abbreviated, three axis ticks would look identical). */
const fmtFor = (metric: MetricId) => (metric === 'chain_height' ? (v: number | null) => (v == null ? '—' : fmtInt(Math.round(v))) : compactNum);

/**
 * Time-series panel for one metric: title + current value (mono) + zoom button + uPlot chart.
 * The crosshair is synced across panels via syncKey; the x-axis is pinned to the selected window (xWindow); sel/autoRefresh control fetching.
 * pair: overlay a second line on the same chart (network in / out); each is fetched separately and aligned by timestamp.
 */
export function MetricPanel({
  seriesKey, metric, label, sel, syncKey, autoRefresh, color, height = 140, onZoom, onResetZoom, onExpand, fetchSeries,
  pair, yRange, yMaxAtLeast, thresholds, help,
}: {
  seriesKey: string; // cache key: node id for own-org nodes; "org/id" for external-org nodes, so they don't collide with own-org nodes of the same name
  fetchSeries?: (metric: MetricId, sel: RangeSel) => Promise<MetricSeries>; // omitted = own-org node, seriesKey is the node id
  metric: MetricId;
  label: string;
  sel: RangeSel;            // relative/absolute time window
  syncKey: string;
  autoRefresh: boolean;
  color?: string; // given = use this color (matching split mini charts to the legend); omitted = default accent + light fill
  height?: number;
  onZoom?: (min: number, max: number) => void; // drag selection -> parent sets an absolute window and refetches
  onResetZoom?: () => void;                     // double-click -> parent clears the absolute window
  onExpand?: () => void;
  pair?: { metric: MetricId; label: string; firstLabel: string }; // label / firstLabel = end-of-line labels for each of the two lines
  yRange?: [number, number]; // fixed y-axis (0~100 for percentages)
  yMaxAtLeast?: number;      // y-axis starts at 0 and reaches at least this value (lag is normally all 0; otherwise autoscaling yields a blank chart)
  thresholds?: number[];     // dashed threshold lines
  help?: string;             // ⓘ next to the title: what this chart shows and what the threshold is
}) {
  const { t } = useTranslation();
  const fetchOne = (m: MetricId) => (fetchSeries ? fetchSeries(m, sel) : api.getNodeSeries(seriesKey, m, sel));
  const q = useQuery({
    queryKey: ['series', seriesKey, metric, ...selKey(sel)],
    queryFn: () => fetchOne(metric),
    refetchInterval: autoRefresh ? 15_000 : false,
    retry: false, // settle immediately on failure (403/network) so the panel shows an error instead of loading forever
  });
  const q2 = useQuery({
    queryKey: ['series', seriesKey, pair?.metric, ...selKey(sel)],
    queryFn: () => fetchOne(pair!.metric),
    enabled: !!pair,
    refetchInterval: autoRefresh ? 15_000 : false,
    retry: false,
  });

  const data = useMemo<uPlot.AlignedData>(() => {
    const pts = q.data?.points ?? [];
    const xs = pts.map((p) => Math.round(new Date(p.t).getTime() / 1000));
    const ys = pts.map((p) => p.v ?? null);
    if (!pair) return breakGaps(xs, ys) as uPlot.AlignedData; // break the line where data is missing
    // Overlay: take the union of both lines' timestamps (if one has no data the other is still drawn), then break gaps as for a single line; inserted break points are null for both
    const toMap = (pts: MetricSeries['points']) =>
      new Map(pts.map((p) => [Math.round(new Date(p.t).getTime() / 1000), p.v ?? null] as [number, number | null]));
    const m1 = toMap(q.data?.points ?? []), m2 = toMap(q2.data?.points ?? []);
    const all = [...new Set([...m1.keys(), ...m2.keys()])].sort((a, b) => a - b);
    const [ox, oy] = breakGaps(all, all.map((x) => m1.get(x) ?? null));
    return [ox, oy, ox.map((x) => m2.get(x) ?? null)] as uPlot.AlignedData;
  }, [q.data, q2.data, pair]);
  const range = useMemo<[number, number] | undefined>(() => {
    if (yRange) return yRange;
    if (yMaxAtLeast == null) return undefined;
    const max = Math.max(0, ...data.slice(1).flatMap((ys) => (ys as (number | null)[]).map((v) => v ?? 0)));
    return [0, Math.max(yMaxAtLeast, Math.ceil(max * 1.15))];
  }, [yRange, yMaxAtLeast, data]);

  // Pin the x-axis to the selected window (not to the data's extent): when data covers only part of the window, the rest stays blank.
  // Relative windows slide with each refetch, so recompute from dataUpdatedAt.
  const xWindow = useMemo<[number, number]>(() => selWindow(sel), [selKey(sel).join(':'), q.dataUpdatedAt]); // eslint-disable-line react-hooks/exhaustive-deps

  const unit = q.data?.unit ?? q2.data?.unit ?? '';
  const currentValue = (pts?: MetricSeries['points']) => {
    const p = pts?.at(-1);
    if (!p) return null;
    const age = xWindow[1] - new Date(p.t).getTime() / 1000;
    const maxAge = Math.max(MIN_STALE_SEC, stepForSeconds(...xWindow)) + CLOCK_SKEW_SEC;
    return age < maxAge ? p.v ?? null : null;
  };
  const last = currentValue(q.data?.points);
  const last2 = currentValue(q2.data?.points);
  const pairLabels = pair ? [pair.firstLabel, pair.label] : [];
  // Overlay: an error only if both fail, no data only if both are empty
  const loading = q.isLoading || (!!pair && q2.isLoading);
  const failed = q.isError && (!pair || q2.isError);
  const monitoring = monitoringIssue(q.error);
  const empty = !loading && !failed && (q.data?.points?.length ?? 0) === 0 && (!pair || (q2.data?.points?.length ?? 0) === 0);

  return (
    <div className="flex flex-col gap-2 rounded-lg border border-hair bg-surface p-3">
      <header className="flex items-center justify-between gap-2">
        <span className="flex min-w-0 items-center gap-1.5 text-xs text-ink-3"><span className="truncate">{label}</span>{help && <HelpTip title={label}>{help}</HelpTip>}</span>
        <div className="flex shrink-0 items-center gap-2">
          {/* The current value is the point of this chart: inverted in a dark rounded chip so it's seen first. The unit follows the last number; omitted when that is "—", otherwise it would read "—s" */}
          {/* When the number changes only the changed digits slide in (like block height), so a change is visible at a glance */}
          <ValueChip unit={unit && (pair ? last2 : last) != null ? unit : undefined}>
            <AnimatedNumber value={fmtFor(metric)(last)} />
            {pair && <> / <AnimatedNumber value={fmtFor(metric)(last2)} /></>}
          </ValueChip>
          {onExpand && (
            <Tooltip label={t('common.expand')}>
              <button onClick={onExpand} data-expand={metric} className="text-ink-3 transition-colors hover:text-ink" aria-label={t('common.expand')}>
                <ArrowsOut size={14} aria-hidden />
              </button>
            </Tooltip>
          )}
        </div>
      </header>
      {loading ? (
        <div role="status" aria-label={t('common.loading')}>
          <Skeleton className="w-full" style={{ height }} />
        </div>
      ) : failed ? (
        // Real failure (abnormal is loud): revoked authorization gets its own message, the rest can retry.
        <div role="status" className="flex flex-col items-center justify-center gap-1.5 text-center text-xs" style={{ height }}>
          {/* Monitoring source down uses the warning color (same as the outage notice on the alerts page) since this chart itself isn't broken; no monitoring source configured is a deployment state, neutral color, no retry */}
          <span className={monitoring === 'error.monitoringOff' ? 'text-ink-3' : undefined} style={monitoring === 'error.monitoringOff' ? undefined : { color: monitoring ? 'var(--bsos-status-syncing-fg)' : 'var(--bsos-status-error-fg)' }}>
            {q.error instanceof HttpError && q.error.code === 'forbidden' ? t('metric.revoked')
              : monitoring ? t(monitoring) : t('metric.error')}
          </span>
          {monitoring !== 'error.monitoringOff' && (
            <button onClick={() => void q.refetch()} className="inline-flex items-center gap-1 text-ink-3 transition-colors hover:text-ink">
              <ArrowClockwise size={12} aria-hidden /> {t('metric.retry')}
            </button>
          )}
        </div>
      ) : empty ? (
        // No data (warming up / not scraped) = neutral empty state, not an error.
        <div role="status" className="flex items-center justify-center text-center text-xs text-ink-3" style={{ height }}>
          {t('metric.empty')}
        </div>
      ) : (
        <UPlotChart
          data={data}
          height={height}
          syncKey={syncKey}
          xWindow={xWindow}
          onZoom={onZoom}
          onResetZoom={onResetZoom}
          valueFmt={fmtFor(metric)}
          seriesDefs={pair ? [{ label: pairLabels[0], color: 'var(--bsos-accent)', width: 1.5, endLabel: true }, { label: pairLabels[1], color: 'var(--bsos-text-3)', width: 1.5, endLabel: true }] : color ? [{ label, color }] : undefined}
          yRange={range}
          thresholds={thresholds}
          label={label}
        />
      )}
    </div>
  );
}
