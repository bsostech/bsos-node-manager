import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { monitoringIssue } from '@/lib/errors';
import { refreshMs } from '@/lib/refreshInterval';
import { useTranslation } from 'react-i18next';
import { useQueries } from '@tanstack/react-query';
import { ArrowsOutSimple } from '@phosphor-icons/react';
import type uPlot from 'uplot';
import { api } from '@/api/client';
import type { MetricId } from '@/api/schemas';
import type { FleetUiStatus } from '@/hooks/useFleetStatus';
import { fmtDur } from '@/lib/format';
import { type RangeSel, selKey, selWindow, liveWindowAndStep } from '@/lib/range';
import { type TrendMetric, detectOutliers } from '@/lib/fleetOutliers';
import { useThresholds, thresholdLine } from '@/lib/thresholds';
import { seriesColor } from '@/theme/series';
import { UPlotChart } from './UPlotChart';
import { compactNum } from './MetricPanel';
import { Skeleton } from './States';
import { Tooltip } from './Tooltip';
import { ValueChip } from '@/components/ValueChip';

export type TrendView = 'overlay' | 'split';
export type TrendColoring = 'outliers' | 'each';

// In "highlight deviations only" mode, normal nodes' lines are light gray and thin; deviating ones use the warning color, bolder, with a name label.
const QUIET = 'var(--bsos-text-faint)';
const LOUD = 'var(--bsos-status-syncing-fg)';
/** Brighter orange for a hovered deviating line (one step brighter than the normal orange, without drifting toward red or brown). */
const LOUD_HOVER = '#FF8A1F';

/**
 * Time series of one metric across all nodes, aligned by timestamp (the alert rule page's metric chart uses this too).
 * One fetch per node (retry:false, so a failing node settles immediately without holding up other lines); all nodes share
 * the same time grid computed by liveWindowAndStep.
 * lag uses blocks_behind (the larger of Besu's report and the platform's chain-head calculation; tolerance is handled in
 * the backend). The frontend never subtracts node heights: Prometheus scrapes nodes a few seconds apart, so subtracting yields fake lag.
 */
export function useFleetSeries(ids: string[], metric: TrendMetric | MetricId, sel: RangeSel, autoRefresh: boolean) {
  const prom = (metric === 'lag' ? 'blocks_behind' : metric) as MetricId;
  const results = useQueries({
    queries: ids.map((id) => ({
      queryKey: ['series', id, prom, ...selKey(sel)],
      queryFn: () => api.getNodeSeries(id, prom, sel, liveWindowAndStep(sel)),
      refetchInterval: autoRefresh ? refreshMs : false,
      retry: false,
    })),
  });
  // Failures don't update dataUpdatedAt, so status and error time must be included too, otherwise it stays loading forever when everything fails
  const updated = results.map((r) => `${r.status}:${r.dataUpdatedAt}:${r.errorUpdatedAt}`).join(',');

  return useMemo(() => {
    const has = (i: number) => (results[i]?.data?.points?.length ?? 0) > 0;
    const settled = results.length > 0 && results.every((r) => !r.isLoading);
    const perNode = ids.map((_, i) => {
      const m = new Map<number, number | null>();
      for (const p of results[i]?.data?.points ?? []) m.set(Math.round(new Date(p.t).getTime() / 1000), p.v ?? null);
      return m;
    });
    const xsSet = new Set<number>();
    perNode.forEach((m) => m.forEach((_, t) => xsSet.add(t)));
    const xs = [...xsSet].sort((a, b) => a - b);
    const cols = perNode.map((m) => xs.map((t) => m.get(t) ?? null));
    const values = new Map(ids.map((id, i) => [id, cols[i]]));
    const latest = new Map(ids.map((id, i) => {
      const c = cols[i];
      let j = c.length - 1;
      while (j >= 0 && c[j] == null) j--;
      return [id, j >= 0 ? c[j] : null] as [string, number | null];
    }));
    return {
      xs, values, latest, settled,
      unit: metric === 'lag' ? '' : (results.find((_, i) => has(i))?.data?.unit ?? ''),
      withData: ids.filter((_, i) => has(i)),
      failed: settled ? ids.filter((_, i) => results[i]?.isError) : [],
      // If any node reports a monitoring-source problem, the whole monitoring is affected (all series come from the same Prometheus)
      monitoring: settled ? results.map((r) => monitoringIssue(r?.error)).find(Boolean) ?? null : null,
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ids.join(','), metric, updated]);
}

/**
 * Fixed y-axis ranges. Percentages are fixed at 0~100: otherwise autoscaling pushes threshold lines off screen and blows a 3%
 * fluctuation up to look serious. Lag is normally all 0, which autoscales to a blank 0~100; give at least 0~10 blocks and grow
 * as lag increases. Other metrics autoscale.
 */
function yRangeFor(metric: TrendMetric, max: number): [number, number] | undefined {
  if (metric === 'cpu' || metric === 'mem' || metric === 'jvm_heap' || metric === 'disk') return [0, 100];
  if (metric === 'lag') return [0, Math.max(10, Math.ceil(max * 1.15))];
  return undefined;
}

const maxOf = (vs: (number | null)[]) => vs.reduce<number>((m, v) => (v != null && v > m ? v : m), 0);

/**
 * Cross-node trend: one metric at a time, overlaid or split.
 * Default "highlight deviations only": normal nodes light gray, only those deviating from the median are colored and named
 * (normal is quiet, abnormal is loud); "color each node" uses the fixed palette, with colors locked to each
 * node's position in the list so hiding any line doesn't change colors.
 */
export function FleetTrend({
  ids, metric, metricLabel, view, coloring, hidden, onToggle, focus, onFocus, sel, autoRefresh, statusById, onZoom, onResetZoom, outlierIds,
}: {
  /** Deviating nodes are decided outside (same source as the diagnosis line); computed from the series only when not given */
  outlierIds?: Set<string>;
  ids: string[];
  metric: TrendMetric;
  metricLabel: string;
  view: TrendView;
  coloring: TrendColoring;
  hidden: Set<string>;
  onToggle: (id: string, additive: boolean) => void;
  focus: string | null; // node currently zoomed in (like Grafana's View panel); null = normal view
  onFocus: (id: string | null) => void;
  sel: RangeSel;
  autoRefresh: boolean;
  statusById: Map<string, FleetUiStatus>;
  onZoom: (min: number, max: number) => void;
  onResetZoom: () => void;
}) {
  const { t } = useTranslation();
  const fs = useFleetSeries(ids, metric, sel, autoRefresh);
  // After leaving zoom, return focus to that node's zoom button (so keyboard users aren't thrown back to the page top)
  const lastFocus = useRef<string | null>(null);
  useEffect(() => {
    if (focus) { lastFocus.current = focus; return; }
    const id = lastFocus.current;
    lastFocus.current = null;
    if (id) document.querySelector<HTMLElement>(`[data-focus-btn="${CSS.escape(id)}"]`)?.focus();
  }, [focus]);
  const th = useThresholds();
  const detected = useMemo(() => detectOutliers(metric, fs.latest, th), [metric, fs.latest, th]);
  const outliers = outlierIds ?? detected;
  const [hoverId, setHoverId] = useState<string | null>(null);
  // While refetching after switching metric / range, hold the previous height: if the content collapses the page jumps and loses scroll position
  const boxRef = useRef<HTMLDivElement>(null);
  const lastH = useRef(260);
  useEffect(() => { if (boxRef.current && boxRef.current.offsetHeight > 0) lastH.current = boxRef.current.offsetHeight; });
  const xWindow = useMemo<[number, number]>(() => selWindow(sel), [selKey(sel).join(':'), fs.xs.at(-1)]); // eslint-disable-line react-hooks/exhaustive-deps
  // Dashed threshold line = the alert rule's threshold (drawn only for metrics with a matching rule)
  const thresholds = thresholdLine(metric as MetricId | 'lag', th);
  const pctRange = yRangeFor(metric, maxOf([...fs.values.values()].flat()));
  const unit = metric === 'lag' ? t('overview.lagUnit') : fs.unit;
  const fmt = (v: number | null) => (v == null ? '—' : `${compactNum(v)}${unit ? ` ${unit}` : ''}`);
  const colorOf = (id: string) => (coloring === 'each' ? seriesColor(ids.indexOf(id)) : outliers.has(id) ? LOUD : QUIET);

  const visible = ids.filter((id) => !hidden.has(id) && fs.withData.includes(id));
  // Draw deviating lines last, on top of the gray lines
  const drawOrder = coloring === 'outliers' ? [...visible.filter((id) => !outliers.has(id)), ...visible.filter((id) => outliers.has(id))] : visible;

  if (!fs.settled && fs.withData.length === 0) return <Skeleton className="w-full" style={{ height: lastH.current }} />;

  const emptyMsg = fs.failed.length >= ids.length ? (fs.monitoring ? t(fs.monitoring) : t('overview.fleetAllDown')) : fs.withData.length === 0 ? t('overview.fleetEmpty') : t('overview.fleetAllHidden');

  if (focus) {
    return (
      <FocusPanel
        id={focus}
        fs={fs}
        off={outliers.has(focus)}
        fmt={fmt}
        thresholds={thresholds}
        metric={metric}
        metricLabel={metricLabel}
        xWindow={xWindow}
        status={statusById.get(focus)}
        onBack={() => onFocus(null)}
        onZoom={onZoom}
        onResetZoom={onResetZoom}
      />
    );
  }

  return (
    <div ref={boxRef} className="flex flex-col gap-3">
      {view === 'overlay' ? (
        <div className="flex flex-col gap-3 rounded-lg border border-hair bg-surface p-3">
          {drawOrder.length === 0 ? (
            <div className="flex h-[260px] items-center justify-center text-xs text-ink-3">{emptyMsg}</div>
          ) : (
            <UPlotChart
              data={[fs.xs, ...drawOrder.map((id) => fs.values.get(id) ?? [])] as uPlot.AlignedData}
              height={260}
              syncKey="fleet"
              xWindow={xWindow}
              onZoom={onZoom}
              onResetZoom={onResetZoom}
              valueFmt={compactNum}
              thresholds={thresholds}
              yRange={pctRange}
              label={metricLabel}
              seriesDefs={drawOrder.map((id) => ({
                label: id,
                color: colorOf(id),
                // Hovered: deviating lines get more orange, others turn primary green
                hoverColor: outliers.has(id) ? LOUD_HOVER : undefined,
                width: 1.25, // color is enough for deviating lines, no extra width
                endLabel: coloring === 'outliers' && outliers.has(id),
              }))}
              onFocusSeries={(i) => setHoverId(i == null ? null : drawOrder[i] ?? null)}
            />
          )}
          <Legend ids={ids} hidden={hidden} onToggle={onToggle} colorOf={colorOf} now={(id) => fs.withData.includes(id) ? fmt(fs.latest.get(id) ?? null) : null} statusById={statusById} hoverId={hoverId} outliers={outliers} />
        </div>
      ) : (
        <SplitGrid
          ids={[...ids.filter((id) => outliers.has(id)), ...ids.filter((id) => !outliers.has(id))]}
          fs={fs}
          outliers={outliers}
          fmt={fmt}
          thresholds={thresholds}
          metric={metric}
          metricLabel={metricLabel}
          xWindow={xWindow}
          statusById={statusById}
          onFocus={onFocus}
          onZoom={onZoom}
          onResetZoom={onResetZoom}
        />
      )}
    </div>
  );
}

/** Split: one small chart per node, sharing the same y-axis scale so they can be compared; deviating nodes go first. Split view has no legend, so the overlay's hidden lines don't apply. */
function SplitGrid({ ids, fs, outliers, fmt, thresholds, metric, metricLabel, xWindow, statusById, onFocus, onZoom, onResetZoom }: {
  ids: string[];
  fs: ReturnType<typeof useFleetSeries>;
  outliers: Set<string>;
  fmt: (v: number | null) => string;
  thresholds?: number[];
  metric: TrendMetric;
  metricLabel: string;
  xWindow: [number, number];
  statusById: Map<string, FleetUiStatus>;
  onFocus: (id: string) => void;
  onZoom: (min: number, max: number) => void;
  onResetZoom: () => void;
}) {
  const { t } = useTranslation();
  const yRange = useMemo<[number, number]>(() => {
    if (metric === 'cpu' || metric === 'mem' || metric === 'jvm_heap' || metric === 'disk') return [0, 100];
    let max = 0;
    for (const id of ids) for (const v of fs.values.get(id) ?? []) if (v != null && v > max) max = v;
    return [0, max > 0 ? Math.ceil(max * 1.15) : 1];
  }, [metric, ids, fs.values]);

  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
      {ids.map((id) => {
        const st = statusById.get(id);
        const off = outliers.has(id);
        const hasData = fs.withData.includes(id);
        return (
          <div key={id} className="flex flex-col gap-2 rounded-lg border border-hair bg-surface p-3">
            <header className="flex items-center gap-2">
              <span className="size-1.5 shrink-0 rounded-full" style={{ background: !hasData ? QUIET : off ? LOUD : 'var(--bsos-status-healthy-fg)' }} aria-hidden />
              <span className="truncate font-mono text-xs text-ink-2">{id}</span>
              {/* Small cards use small chips; deviating is orange, disconnected is red reading "disconnected", no data without disconnection is "—" */}
              <span className="ml-auto">
                {hasData ? <ValueChip size="sm" tone={off ? 'warn' : 'dark'}>{fmt(fs.latest.get(id) ?? null)}</ValueChip>
                  : st ? <ValueChip size="sm" tone="err">{t('overview.disconnected')}</ValueChip>
                  : <span className="font-mono text-sm text-ink-faint">—</span>}
              </span>
              {/* The zoom button lives in the header, not on the chart: clicks on the chart already mean drag-to-zoom and double-click-to-reset */}
              <button type="button" data-focus-btn={id} onClick={() => onFocus(id)} aria-label={t('overview.focusOpen', { id })} title={t('overview.focusOpen', { id })} className="rounded p-0.5 text-ink-3 hover:bg-elevated hover:text-ink focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent">
                <ArrowsOutSimple size={14} aria-hidden />
              </button>
            </header>
            {st ? (
              <div className="flex h-[120px] items-center justify-center text-xs text-ink-3">
                ⊘ {t(`overview.src_${st.kind}`)}{st.reason ? ` · ${t('overview.srcAgentNoResponse')}` : ''}
              </div>
            ) : !hasData ? (
              <div className="flex h-[120px] items-center justify-center text-xs text-ink-3">{t('overview.fleetEmpty')}</div>
            ) : (
              <UPlotChart
                data={[fs.xs, fs.values.get(id) ?? []] as uPlot.AlignedData}
                height={120}
                syncKey="fleet"
                xWindow={xWindow}
                yRange={yRange}
                thresholds={thresholds}
                onZoom={onZoom}
                onResetZoom={onResetZoom}
                valueFmt={compactNum}
                label={`${id} ${metricLabel}`}
                seriesDefs={[{ label: id, color: off ? LOUD : 'var(--bsos-accent)', width: 1.5 }]}
              />
            )}
          </div>
        );
      })}
    </div>
  );
}

/** Single-node zoom: the whole overview page shows only breadcrumbs, time controls and this chart (like Grafana's View), keeping the metric and range; Esc or the back button returns to the previous view. */
function FocusPanel({ id, fs, off, fmt, thresholds, metric, metricLabel, xWindow, status, onBack, onZoom, onResetZoom }: {
  id: string;
  fs: ReturnType<typeof useFleetSeries>;
  off: boolean;
  fmt: (v: number | null) => string;
  thresholds?: number[];
  metric: TrendMetric;
  metricLabel: string;
  xWindow: [number, number];
  status?: FleetUiStatus;
  onBack: () => void;
  onZoom: (min: number, max: number) => void;
  onResetZoom: () => void;
}) {
  const { t } = useTranslation();
  const onBackRef = useRef(onBack);
  onBackRef.current = onBack;
  useEffect(() => {
    // The range menu and dialogs preventDefault when they handle Esc; that press closes them, not the zoom
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape' && !e.defaultPrevented) onBackRef.current(); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);
  // Chart height follows the viewport: fills from the chart's top to the bottom of the window, at least 320.
  // 112 = card bottom padding 12 + card-to-hint-row gap 12 + hint row ~20 + main bottom padding 24 + slack.
  // When measuring, main may not have scrolled back to the top yet (Overview's effect runs after this), so add scrollTop back
  // to compute the position as if scrolled to the top.
  // Don't draw before measuring, so uPlot doesn't build once at the default height and then rebuild.
  const chartRef = useRef<HTMLDivElement>(null);
  const [height, setHeight] = useState<number | null>(null);
  useLayoutEffect(() => {
    const fit = () => {
      const el = chartRef.current;
      if (!el) return;
      const top = el.getBoundingClientRect().top + (document.querySelector('main')?.scrollTop ?? 0);
      setHeight(Math.max(320, Math.floor(window.innerHeight - top - 112)));
    };
    fit();
    window.addEventListener('resize', fit);
    return () => window.removeEventListener('resize', fit);
  }, []);
  const vals = fs.values.get(id) ?? [];
  const yRange = yRangeFor(metric, maxOf(vals));
  const hasData = fs.withData.includes(id);

  return (
    <div className="flex flex-col gap-3 rounded-lg border border-hair bg-surface p-3">
      {/* Back and the node name are in the page breadcrumbs (Overview); the card keeps only the current value and the chart */}
      <span className="self-end">
        {hasData ? <ValueChip tone={off ? 'warn' : 'dark'}>{fmt(fs.latest.get(id) ?? null)}</ValueChip>
          : status ? <ValueChip tone="err">{t('overview.disconnected')}</ValueChip>
          : <span className="font-mono text-sm text-ink-faint">—</span>}
      </span>
      <div ref={chartRef}>
      {height == null ? null : status ? (
        <div className="flex items-center justify-center text-xs text-ink-3" style={{ height }}>
          ⊘ {t(`overview.src_${status.kind}`)}{status.reason ? ` · ${t('overview.srcAgentNoResponse')}` : ''}
        </div>
      ) : !hasData ? (
        <div className="flex items-center justify-center text-xs text-ink-3" style={{ height }}>{t('overview.fleetEmpty')}</div>
      ) : (
        <UPlotChart
          data={[fs.xs, vals] as uPlot.AlignedData}
          height={height}
          xWindow={xWindow}
          yRange={yRange}
          thresholds={thresholds}
          onZoom={onZoom}
          onResetZoom={onResetZoom}
          valueFmt={compactNum}
          label={`${id} ${metricLabel}`}
          seriesDefs={[{ label: id, color: off ? LOUD : 'var(--bsos-accent)', width: 1.75 }]}
        />
      )}
      </div>
    </div>
  );
}

/**
 * Legend (Grafana-style): click to show only this line, click again to restore all; Ctrl / ⌘ + click toggles lines one by one. Current values shown alongside. Nodes without data get ⊘ instead of a color dot and are grayed out; hover for the reason.
 */
function Legend({ ids, hidden, onToggle, colorOf, now, statusById, hoverId, outliers }: {
  /** The line the mouse is near: the same node in the legend gets a rounded background highlight, orange if deviating, green otherwise */
  hoverId: string | null;
  outliers: Set<string>;
  ids: string[];
  hidden: Set<string>;
  onToggle: (id: string, additive: boolean) => void;
  colorOf: (id: string) => string;
  now: (id: string) => string | null;
  statusById: Map<string, FleetUiStatus>;
}) {
  const { t } = useTranslation();
  return (
    // Leave room for the y-axis on the left (UPlotChart's y-axis size 44px) so the legend aligns with the plot area's left edge
    <div className="-ml-1.5 flex flex-wrap items-center gap-x-2.5 gap-y-1 pl-[44px]">
      {ids.map((id) => {
        const off = hidden.has(id);
        const st = statusById.get(id);
        const failed = !!st;
        const value = now(id);
        const tip = st
          ? `${t(`overview.src_${st.kind}`)}${st.reason ? ` · ${t('overview.srcAgentNoResponse')}` : ''} · ${fmtDur(st.since)}`
          : undefined;
        return (
          <Tooltip key={id} label={tip}>
            <button
              type="button"
              onClick={(e) => onToggle(id, e.metaKey || e.ctrlKey)}
              aria-pressed={!off}
              className="inline-flex items-center gap-1.5 rounded-md px-1.5 py-0.5 text-xs transition-colors hover:opacity-70"
              style={hoverId === id ? { background: outliers.has(id) ? 'var(--bsos-status-syncing-bg)' : 'var(--bsos-accent-soft)' } : undefined}
            >
              {failed || value == null ? (
                <span className="leading-none text-ink-3" aria-hidden>⊘</span>
              ) : (
                <span className="h-0.5 w-3.5 shrink-0 rounded-full" style={{ background: colorOf(id) }} aria-hidden />
              )}
              <span className={`font-mono ${failed ? 'text-ink-3' : 'text-ink-2'} ${off ? 'line-through' : ''}`}>{id}</span>
              {value != null && <span className="font-mono tabular-nums text-ink-3">{value}</span>}
            </button>
          </Tooltip>
        );
      })}
    </div>
  );
}
