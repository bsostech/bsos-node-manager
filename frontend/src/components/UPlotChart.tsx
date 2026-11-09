import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import { appLocale } from '@/lib/format';
import { brand } from '@/theme/brand';

/**
 * Thin uPlot wrapper: quiet time-series charts in the ink-on-paper style.
 * Single accent line + light fill, hairline grid, mono axis labels. Supports:
 * - synced cursor crosshair (charts with the same syncKey share one crosshair)
 * - x axis fixed to the window chosen by the parent (xWindow); drag-select (setScale:false) is handed to the parent to turn into an absolute window and refetch
 * - double-click to reset, hover tooltip
 */
export interface UPlotChartProps {
  data: uPlot.AlignedData; // [timestamps in seconds, values...] (single line = 1 value series; overlaid = N value series)
  height?: number;
  syncKey?: string;
  xWindow?: [number, number] | null; // fixed x-axis window (seconds); null = fit to the data's ends. Pinning it is what leaves uncovered periods honestly blank
  onZoom?: (min: number, max: number) => void;
  onResetZoom?: () => void;
  valueFmt?: (v: number | null) => string;
  // Overlaid lines: {label, color} per series. When given, draws multiple lines (no fill) and the tooltip lists all of them.
  // width = line width (default 1.25); endLabel = draw the label next to the last data point (used by cross-node trends to label outliers).
  // hoverColor = color drawn when the mouse is near this line (defaults to the primary color).
  seriesDefs?: { label: string; color: string; hoverColor?: string; width?: number; endLabel?: boolean }[];
  // Which line the mouse is near (index into seriesDefs; null on leave), for the parent to sync the legend etc.
  onFocusSeries?: (idx: number | null) => void;
  // Fixed y-axis range (split small multiples must share one scale to be comparable); omitted = automatic from the data.
  yRange?: [number, number];
  // Horizontal threshold line (e.g. disk 80%), drawn dashed.
  thresholds?: number[];
  // Metric name, used to build the chart's text alternative (see useChartSummary below).
  label?: string;
}

function token(el: Element, name: string, fallback: string): string {
  const v = getComputedStyle(el).getPropertyValue(name).trim();
  return v || fallback;
}

/** canvas does not understand CSS variables: resolve `var(--x)` to the actual color first (follows the light/dark theme). */
function resolveColor(el: Element, c: string): string {
  const m = /^var\((--[\w-]+)\)$/.exec(c);
  return m ? token(el, m[1], c) : c;
}

/**
 * x-axis tick format (only deviations from the default get visual weight):
 * - granularity of a day or more (e.g. a 7d range) -> plain date M/D; the time is noise at that granularity.
 * - finer granularity -> plain 24h time HH:mm by default; only the tick that "crosses midnight" also shows the date (M/D HH:mm).
 *     The date of the current window is already shown in the toolbar (RangeControl), so the axis need not repeat it on every tick.
 */
function fmtXAxis(_u: uPlot, splits: number[], _axisIdx: number, _foundSpace: number, foundIncr: number): string[] {
  const DAY = 86400;
  const p = (n: number) => String(n).padStart(2, '0');
  if (foundIncr >= DAY) {
    return splits.map((s) => {
      const d = new Date(s * 1000);
      return `${d.getMonth() + 1}/${d.getDate()}`;
    });
  }
  let prevDate = -1;
  return splits.map((s) => {
    const d = new Date(s * 1000);
    const dateNum = d.getFullYear() * 10000 + (d.getMonth() + 1) * 100 + d.getDate();
    const crossed = prevDate !== -1 && dateNum !== prevDate;
    prevDate = dateNum;
    const time = `${p(d.getHours())}:${p(d.getMinutes())}`;
    return crossed ? `${d.getMonth() + 1}/${d.getDate()} ${time}` : time;
  });
}

/**
 * The chart's text alternative: uPlot draws on canvas, which is a completely empty block to screen readers.
 * Build one summary sentence from the data itself (number of points, latest value, range; for overlaid lines, the latest value per line),
 * attached to the role="img" container. Deliberately not read point by point -- the summary should be listenable to the end, not a table read aloud.
 */
function useChartSummary(
  data: uPlot.AlignedData,
  valueFmt: ((v: number | null) => string) | undefined,
  seriesDefs: { label: string; color: string }[] | undefined,
  label: string | undefined,
): string {
  const { t } = useTranslation();
  return useMemo(() => {
    const name = label ?? '';
    const fmt = (v: number | null) => (valueFmt ? valueFmt(v) : v == null ? '—' : String(v));
    const nums = (i: number) => (data[i] as (number | null)[] | undefined)?.filter((v): v is number => v != null) ?? [];

    // With overlaid lines the first one may happen to have no data while others do, so check each line rather than only the first
    const anyData = data.length >= 2 && data.slice(1).some((_, i) => nums(i + 1).length > 0);
    if (!anyData) return t('chart.ariaEmpty', { label: name });

    if (seriesDefs && seriesDefs.length > 0) {
      const series = seriesDefs
        .map((s, i) => {
          const vs = nums(i + 1);
          return vs.length === 0 ? null : t('chart.ariaSeriesItem', { name: s.label, last: fmt(vs[vs.length - 1]) });
        })
        .filter(Boolean);
      return t('chart.ariaMulti', { label: name, count: series.length, series: series.join('、') });
    }

    const vs = nums(1);
    return t('chart.ariaSingle', {
      label: name,
      n: vs.length,
      last: fmt(vs[vs.length - 1]),
      min: fmt(Math.min(...vs)),
      max: fmt(Math.max(...vs)),
    });
  }, [data, valueFmt, seriesDefs, label, t]);
}

export function UPlotChart({ data, height = 140, syncKey, xWindow, onZoom, onResetZoom, valueFmt, seriesDefs, label, yRange, thresholds, onFocusSeries }: UPlotChartProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const tipRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<uPlot | null>(null);
  // Keep the latest callback / fmt in a ref to avoid rebuilding the chart.
  const cb = useRef({ onZoom, onResetZoom, valueFmt, seriesDefs, yRange, thresholds, onFocusSeries });
  cb.current = { onZoom, onResetZoom, valueFmt, seriesDefs, yRange, thresholds, onFocusSeries };
  // Line count, colors and widths are fixed when the chart is built, so a change requires a rebuild; label, threshold and y range go through refs and take effect on redraw.
  const seriesKey = (seriesDefs ?? []).map((d) => `${d.color}/${d.width ?? ''}`).join('|') + (yRange ? '|y' : '');
  const summary = useChartSummary(data, valueFmt, seriesDefs, label);
  // Line colors are resolved from CSS variables when the chart is built; after switching light/dark theme it must be rebuilt for the lines to take the new theme's colors.
  // Watch <html data-theme> instead of reading useTheme(): ThemeProvider changes the attribute in its own effect, which runs after the child's effect.
  const [themeTick, setThemeTick] = useState(0);
  useEffect(() => {
    const mo = new MutationObserver(() => setThemeTick((n) => n + 1));
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    return () => mo.disconnect();
  }, []);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    // The fallback references a brand token to avoid drifting again (used only when the CSS var is missing; takes the light value)
    const accent = token(host, '--bsos-accent', brand.accent);
    const hair = token(host, '--bsos-border-hair', brand.borderHair);
    const ink3 = token(host, '--bsos-text-3', brand.textTertiary);
    const fmt = (v: number | null) => (cb.current.valueFmt ? cb.current.valueFmt(v) : v == null ? '' : String(v));
    // Overlaid lines: the line the mouse is near (uPlot's focus) is redrawn in the primary color; uPlot fades the others itself. The tooltip's small dot changes color too
    let focused: number | null = null;
    const baseStrokes = (seriesDefs ?? []).map((d) => resolveColor(host, d.color));
    // The hover green uses the bright brand color (accent-bright, the original Besu logo color), one step brighter than the primary so "currently pointed at" is visible
    const accentBright = token(host, '--bsos-accent-bright', brand.accentBright);
    const hoverStrokes = (seriesDefs ?? []).map((d) => (d.hoverColor ? resolveColor(host, d.hoverColor) : accentBright));
    const strokeOf = (i: number) => (focused === i + 1 ? hoverStrokes[i] : baseStrokes[i]);

    const opts: uPlot.Options = {
      width: host.clientWidth || 320,
      height,
      // Leave a bit more on the right: the last x-axis tick label (e.g. "10pm") is anchored at the data end and gets clipped if it runs past the canvas.
      padding: [8, 28, 0, 0],
      cursor: {
        sync: syncKey ? { key: syncKey, setSeries: true } : undefined,
        drag: { x: true, y: false, setScale: false }, // drag-select is handed to the parent to turn into an absolute window and refetch
        points: { size: 6 },
        focus: { prox: 16 },
      },
      legend: { show: false },
      scales: { x: { time: true }, y: yRange ? { range: () => cb.current.yRange ?? [0, 1] } : {} },
      axes: [
        {
          stroke: ink3,
          grid: { stroke: hair, width: 1 },
          ticks: { stroke: hair, width: 1, size: 4 },
          font: '11px ui-monospace, monospace',
          size: 28, // Custom values (see fmtXAxis) always output a single line, without uPlot's built-in two-line date stamp, so no extra height is needed.
          values: fmtXAxis,
        },
        {
          stroke: ink3,
          grid: { stroke: hair, width: 1 },
          ticks: { show: false },
          font: '11px ui-monospace, monospace',
          // Width follows the longest tick text: full numbers (1,284,553) need more width than 1.28M
          size: (_u, values) => (values ? Math.max(44, Math.max(...values.map((v) => String(v).length)) * 7 + 12) : 44),
          values: (_u, splits) => splits.map((s) => fmt(s)),
        },
      ],
      series:
        seriesDefs && seriesDefs.length
          ? [{}, ...seriesDefs.map((d, i) => ({ stroke: () => strokeOf(i), width: d.width ?? 1.25, points: { show: false }, value: (_u: uPlot, v: number | null) => fmt(v) }))]
          : [{}, { stroke: accent, width: 1.5, fill: accent + '14', points: { show: false }, value: (_u: uPlot, v: number | null) => fmt(v) }],
      hooks: {
        setSeries: [
          (u, sidx, o) => {
            const f = (o as { focus?: boolean | null }).focus;
            if (f == null) return;
            const next = f ? sidx : null;
            if (next === focused) return;
            focused = next;
            cb.current.onFocusSeries?.(next == null ? null : next - 1);
            // stroke is a function, but uPlot caches the computed color: clear it and redraw for the color to change
            u.series.forEach((sr) => { (sr as unknown as { _stroke?: unknown })._stroke = undefined; });
            u.redraw(false, true);
          },
        ],
        // Dashed threshold and line-end labels: uPlot has no built-in, so draw them on the canvas (coordinates must be converted to device pixels)
        draw: [
          (u) => {
            const ctx = u.ctx;
            const px = uPlot.pxRatio;
            ctx.save();
            for (const v of cb.current.thresholds ?? []) {
              const y = u.valToPos(v, 'y', true);
              if (y < u.bbox.top || y > u.bbox.top + u.bbox.height) continue;
              ctx.strokeStyle = token(host, '--bsos-status-error-fg', '#B3322F');
              ctx.globalAlpha = 0.5;
              ctx.lineWidth = px;
              ctx.setLineDash([5 * px, 4 * px]);
              ctx.beginPath();
              ctx.moveTo(u.bbox.left, y);
              ctx.lineTo(u.bbox.left + u.bbox.width, y);
              ctx.stroke();
            }
            ctx.setLineDash([]);
            ctx.globalAlpha = 1;
            ctx.font = `${11 * px}px ui-monospace, monospace`;
            ctx.textAlign = 'right';
            const used: number[] = []; // label y positions already drawn; when two line ends sit together, shift upward so text does not overlap
            (cb.current.seriesDefs ?? []).forEach((d, i) => {
              if (!d.endLabel) return;
              const ys = u.data[i + 1] as (number | null)[];
              let j = ys.length - 1;
              while (j >= 0 && ys[j] == null) j--;
              if (j < 0) return;
              const x = u.valToPos(u.data[0][j] as number, 'x', true);
              const y = u.valToPos(ys[j] as number, 'y', true);
              let ly = Math.max(u.bbox.top + 12 * px, y - 6 * px);
              const step = ly - 24 * px < u.bbox.top ? 12 * px : -12 * px; // near the top, shift downward instead
              while (used.some((v) => Math.abs(v - ly) < 12 * px)) ly += step;
              used.push(ly);
              ctx.fillStyle = resolveColor(host, d.color);
              ctx.fillText(d.label, x - 6 * px, ly);
            });
            ctx.restore();
          },
        ],
        setSelect: [
          (u) => {
            if (u.select.width > 4) {
              const min = u.posToVal(u.select.left, 'x');
              const max = u.posToVal(u.select.left + u.select.width, 'x');
              cb.current.onZoom?.(min, max);
            }
            u.setSelect({ left: 0, width: 0, top: 0, height: 0 }, false);
          },
        ],
        setCursor: [
          (u) => {
            const tip = tipRef.current;
            if (!tip) return;
            const { idx, left } = u.cursor;
            if (idx == null || left == null || left < 0) {
              tip.style.opacity = '0';
              return;
            }
            const ts = u.data[0][idx] as number;
            const when = new Date(ts * 1000).toLocaleTimeString(appLocale(), { hour: '2-digit', minute: '2-digit' });
            const defs = cb.current.seriesDefs;
            if (defs && defs.length) {
              const rows = defs
                .map((d, i) => {
                  const v = u.data[i + 1][idx];
                  const dot = focused === i + 1 ? (d.hoverColor ?? accentBright) : d.color;
                  return `<div style="display:flex;align-items:center;gap:4px"><span style="width:6px;height:6px;border-radius:9999px;flex:none;background:${dot}"></span>${d.label}<b style="margin-left:auto;padding-left:8px">${fmt(v ?? null)}</b></div>`;
                })
                .join('');
              tip.innerHTML = `<div style="opacity:.55;margin-bottom:3px">${when}</div>${rows}`;
            } else {
              tip.textContent = `${when}  ${fmt(u.data[1][idx] ?? null)}`;
            }
            tip.style.opacity = '1';
            // cursor.left is relative to the plot area (over); the tooltip is in the outer wrapper, so add over's left offset (y-axis width + padding).
            tip.style.left = `${u.over.offsetLeft + left}px`;
          },
        ],
      },
    };

    const chart = new uPlot(opts, data, host);
    chartRef.current = chart;

    const ro = new ResizeObserver(() => chart.setSize({ width: host.clientWidth, height }));
    ro.observe(host);
    const onDbl = () => cb.current.onResetZoom?.();
    host.addEventListener('dblclick', onDbl);

    return () => {
      ro.disconnect();
      host.removeEventListener('dblclick', onDbl);
      // Lock the height before rebuilding the chart (e.g. the legend toggling the number of lines): otherwise the container collapses to 0 the instant the canvas is torn down and the page jumps its scroll position
      host.style.minHeight = `${host.offsetHeight}px`;
      chart.destroy();
      chartRef.current = null;
    };
    // Rebuild only when height/syncKey/series count changes; data/xWindow are hot-updated by the effect below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [height, syncKey, seriesKey, themeTick]);

  useEffect(() => {
    chartRef.current?.setData(data);
  }, [data]);

  useEffect(() => {
    const c = chartRef.current;
    if (!c) return;
    if (xWindow) c.setScale('x', { min: xWindow[0], max: xWindow[1] });
    else c.setScale('x', { min: data[0][0] as number, max: data[0][data[0].length - 1] as number });
  }, [xWindow, data]);

  return (
    // role="img" + aria-label: canvas is empty to assistive technology, so the summary carries the content
    <div className="relative" role="img" aria-label={summary}>
      <div ref={hostRef} aria-hidden="true" />
      <div
        ref={tipRef}
        className="pointer-events-none absolute top-0 z-10 -translate-x-1/2 whitespace-nowrap rounded border border-hair bg-elevated px-2 py-1 text-left font-mono text-2xs tabular-nums text-ink opacity-0 shadow-sm transition-opacity"
      />
    </div>
  );
}
