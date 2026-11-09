// Monitoring time range model: relative (sliding window, can be live) or absolute (a fixed start/end window frozen by drag-zoom).
// Relative = the backend counts back from now; absolute = fixed from/to (epoch seconds) that does not slide with live updates.

export const RANGE_PRESETS = ['15m', '1h', '3h', '6h', '24h', '7d'] as const;
export type Preset = (typeof RANGE_PRESETS)[number];

export type RangeSel =
  | { kind: 'rel'; dur: string } // '15m' | '1h' | ... | custom like '3d' / '90m'
  | { kind: 'abs'; from: number; to: number }; // epoch seconds (produced by drag-zoom)

const UNIT_MS: Record<string, number> = { m: 60_000, h: 3_600_000, d: 86_400_000 };

/** Parse a duration ('15m'/'3d'/'90m') -> ms; unparseable returns 1h. */
export function durToMs(dur: string): number {
  const m = /^(\d+)([mhd])$/.exec(dur.trim());
  return m ? Number(m[1]) * UNIT_MS[m[2]] : 3_600_000;
}

/** Query string for the series API: range=dur for relative, from/to for absolute. */
export function selToQuery(sel: RangeSel): string {
  return sel.kind === 'rel'
    ? `range=${encodeURIComponent(sel.dur)}`
    : `from=${Math.round(sel.from)}&to=${Math.round(sel.to)}`;
}

/** react-query key fragment (relative uses only dur to stay stable -> live updates come from refetchInterval; absolute includes from/to). */
export function selKey(sel: RangeSel): (string | number)[] {
  return sel.kind === 'rel' ? ['rel', sel.dur] : ['abs', Math.round(sel.from), Math.round(sel.to)];
}

/**
 * The "window start/end" (epoch seconds) for a selection. Used to pin the chart x axis to the range the user asked for
 * rather than hugging the data ends; when data covers only part of the window, the empty part stays honestly empty.
 * Relative = [now-dur, now]; absolute = [from, to].
 */
export function selWindow(sel: RangeSel, nowSec: number = Date.now() / 1000): [number, number] {
  if (sel.kind === 'abs') return [sel.from, sel.to];
  const end = Math.floor(nowSec);
  return [end - Math.round(durToMs(sel.dur) / 1000), end];
}

/** Compute the sampling step (seconds) from span, copied from stepFor in the backend internal/prom: target ~96 points, minimum 15s.
 * Returns an integer (the OpenAPI step param is an integer, and the grid alignment below must use integers or the aligned timestamps would not be integers).
 */
export function stepForSeconds(fromSec: number, toSec: number): number {
  const span = Math.max(0, toSec - fromSec);
  return Math.max(15, Math.round(span / 96));
}

/**
 * For the fleet overlay chart only: compute the [from, to, step] to use "right now" so every node in the same useQueries round shares one set of values.
 * An absolute window (drag-zoom, frozen) is returned as is; a relative window (1h / live) floors `to` to the step grid
 * so the query params change only once per step period. This is what lets "absolute timestamps + live refresh" work together:
 * it must be called each time queryFn actually runs (using the current Date.now()), not computed once at render and stored in memo/state,
 * or the window freezes at the moment of first load and never moves forward.
 */
export function liveWindowAndStep(
  sel: RangeSel,
  nowSec: number = Date.now() / 1000,
): { from: number; to: number; step: number } {
  if (sel.kind === 'abs') {
    return { from: sel.from, to: sel.to, step: stepForSeconds(sel.from, sel.to) };
  }
  const [rawFrom, rawTo] = selWindow(sel, nowSec);
  const step = stepForSeconds(rawFrom, rawTo);
  const to = Math.floor(rawTo / step) * step;
  const from = to - Math.round((rawTo - rawFrom) / step) * step;
  return { from, to, step };
}

/**
 * Insert a null where the time interval grows abnormally large so uPlot draws an honest break (spanGaps defaults to false).
 * Real Prometheus returns no rows for periods without samples, which would otherwise be bridged by a misleading straight line; gaps are detected from the median step and filled with null.
 * (Mocks express outages as null values with an unchanged step, which this function leaves alone; the two are compatible.)
 */
export function breakGaps(xs: number[], ys: (number | null)[]): [number[], (number | null)[]] {
  if (xs.length < 3) return [xs, ys];
  const steps = xs.slice(1).map((x, i) => x - xs[i]).sort((a, b) => a - b);
  const med = steps[steps.length >> 1];
  if (!(med > 0)) return [xs, ys];
  const ox: number[] = [];
  const oy: (number | null)[] = [];
  for (let i = 0; i < xs.length; i++) {
    if (i > 0 && xs[i] - xs[i - 1] > med * 1.8) { ox.push(xs[i - 1] + med); oy.push(null); }
    ox.push(xs[i]); oy.push(ys[i]);
  }
  return [ox, oy];
}

/** Epoch seconds -> datetime-local input string ('YYYY-MM-DDTHH:mm', local time zone). */
export function toLocalInput(epochSec: number): string {
  const p = (n: number) => String(n).padStart(2, '0');
  const d = new Date(epochSec * 1000);
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

/** datetime-local input string -> epoch seconds (local time zone); empty / invalid returns null. */
export function fromLocalInput(s: string): number | null {
  if (!s) return null;
  const ms = new Date(s).getTime();
  return Number.isNaN(ms) ? null : Math.floor(ms / 1000);
}

/** Compact label for an absolute window: same day shows the date once at the start (6/15 14:00 – 16:30), across days both ends get it (6/15 14:00 – 6/16 09:00). */
export function absLabel(from: number, to: number): string {
  const p = (n: number) => String(n).padStart(2, '0');
  const df = new Date(from * 1000);
  const dt = new Date(to * 1000);
  const sameDay = df.getFullYear() === dt.getFullYear() && df.getMonth() === dt.getMonth() && df.getDate() === dt.getDate();
  const date = (d: Date) => `${d.getMonth() + 1}/${d.getDate()} `;
  const time = (d: Date) => `${p(d.getHours())}:${p(d.getMinutes())}`;
  return `${date(df)}${time(df)} – ${sameDay ? '' : date(dt)}${time(dt)}`;
}
