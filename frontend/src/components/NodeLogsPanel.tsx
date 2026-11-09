import { useEffect, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { ENTERPRISE_INFO_PATH } from '@/pages/EnterpriseInfo';
import { isEnterprise } from '@/edition';
import { useAuth } from '@/auth/AuthContext';
import type { Permission } from '@/auth/permissions';
import { Listbox } from '@/components/Listbox';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { api, HttpError } from '@/api/client';
import { errMsg } from '@/lib/errors';
import type { NodeLogLine, NodeLogs } from '@/api/schemas';
import { ErrorState } from '@/components/States';
import { LogSetup } from '@/components/LogSetup';
import { toneVars } from '@/lib/status';
import { ArrowsOut } from '@phosphor-icons/react';
import { Tooltip } from '@/components/Tooltip';

const POLL_MS = 4000;
const BATCH = 1000; // max lines fetched per request (the agent's limit)
const KEEP = 5000; // max lines kept in the frontend (the agent keeps only about 2 MB, so cap here too to avoid eating memory when left open for a long time)
const LEVELS = ['ALL', 'WARN', 'ERROR'] as const;
type Level = (typeof LEVELS)[number];
/** One row on screen: a log line, or a gap marker for "a stretch was pushed out of the agent buffer" (gap = number of missing lines). */
type Row = NodeLogLine & { gap?: number };
const ORDER: Record<string, number> = { FATAL: 0, ERROR: 1, WARN: 2, INFO: 3, DEBUG: 4, TRACE: 5 };

/** Besu format: "date time | thread | level | category | message". */
const LINE = /^(\S+ \S+) \| [^|]* \| (\w+)\s*\| ([^|]*?)\s*\| (.*)$/s;

/** Split into fields; if it cannot be split (stack trace continuation lines, other formats) return null and show the whole line as is. */
export function parseLine(text: string): { time: string; level: string; logger: string; msg: string } | null {
  const m = LINE.exec(text);
  if (!m) return null;
  // Convert the time to the browser's local time, keeping only h:m:s and milliseconds; if it cannot be parsed use the time part of the original string
  const d = new Date(m[1].replace(' ', 'T').replace(/([+-]\d{2})(\d{2})$/, '$1:$2'));
  const p = (n: number, w = 2) => String(n).padStart(w, '0');
  const time = Number.isNaN(d.getTime()) ? m[1].split(' ')[1] ?? m[1] : `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}.${p(d.getMilliseconds(), 3)}`;
  return { time, level: m[2], logger: m[3], msg: m[4] };
}

// The log area always uses a dark terminal palette (same in light and dark theme), separate from the rest of the UI
export const TERM = { bg: '#15171C', fg: '#E6E8EB', dim: '#8B919A', logger: '#6CC4D4', warn: '#F2C14E', error: '#F0857F', rule: '#2A2E36', warnBg: 'rgba(242,193,78,0.08)', markBg: 'rgba(139,145,154,0.10)' };
export const levelColor = (lv: string) => (lv === 'WARN' ? TERM.warn : lv === 'ERROR' || lv === 'FATAL' ? TERM.error : TERM.fg);

/**
 * Received logs and filters live outside the component: the panel remounts when toggling expanded / normal view, and must not lose the accumulated lines, filters or search.
 * Shared within one browser tab and cleared only on reload (as before; the buffer was always in memory only).
 */
type Saved = { lines: Row[]; cursor?: number; epoch?: number; level: Level; q: string; follow: boolean };
const SAVED = new Map<string, Saved>();

/** Log source for an external organization's node; not given for our own nodes, where the panel fetches itself. */
export type LogSource = { queryKey: unknown[]; fetch: (after: number | undefined, limit: number) => Promise<NodeLogs> };

/**
 * Node logs: every 4 seconds ask the platform for new lines with a cursor; the platform reads the agent's in-memory buffer through the agent tunnel.
 * Filtering and search are done in the frontend; always rendered as plain text (log content comes from the node and must not be treated as HTML).
 * When no Besu is connected, show the setup steps.
 */
export function NodeLogsPanel({ nodeId, source, height, onExpand, scrollToTop }: {
  nodeId: string;
  source?: LogSource;
  /** Expanded view: height of the whole panel (same FillHeight as expanded charts); omitted = fixed height of the normal view */
  height?: number;
  /** Expand button at the top right of the normal view */
  onExpand?: () => void;
  /** On entering expanded view: scroll the panel to the top of the viewport (the node title above is still there, reachable by scrolling up) */
  scrollToTop?: boolean;
}) {
  const { t } = useTranslation();
  const savedKey = JSON.stringify(source?.queryKey ?? ['nodeLogs', nodeId]);
  const saved = SAVED.get(savedKey);
  const [lines, setLines] = useState<Row[]>(saved?.lines ?? []);
  // Cursor and agent epoch live in refs: queryFn must always read the latest value, and they do not need to trigger re-renders.
  const cursor = useRef<number | undefined>(saved?.cursor);
  const epoch = useRef<number | undefined>(saved?.epoch);
  const [level, setLevel] = useState<Level>(saved?.level ?? 'ALL');
  const [q, setQ] = useState(saved?.q ?? '');
  const [follow, setFollow] = useState(saved?.follow ?? true);
  useEffect(() => { SAVED.set(savedKey, { lines, cursor: cursor.current, epoch: epoch.current, level, q, follow }); }, [savedKey, lines, level, q, follow]);
  const [showSetup, setShowSetup] = useState(false);
  const box = useRef<HTMLDivElement>(null);
  const panel = useRef<HTMLElement>(null);
  const lastTop = useRef(0);
  // Wait until the expanded page has scrolled itself back to the top, then scroll to the panel (child effects run before the page's, so defer one tick)
  useEffect(() => {
    if (!scrollToTop) return;
    const id = window.setTimeout(() => panel.current?.scrollIntoView({ block: 'start' }), 50);
    return () => window.clearTimeout(id);
  }, [scrollToTop]);

  // External organization's node: how it is wired up is their business, so no setup steps
  const load = source?.fetch ?? ((after: number | undefined, limit: number) => api.getNodeLogs(nodeId, after, limit));
  const query = useQuery({
    queryKey: source?.queryKey ?? ['nodeLogs', nodeId],
    queryFn: async () => {
      const prev = cursor.current;
      let r = await load(prev, BATCH);
      // epoch changed = the agent restarted and seq starts over: refetch the latest chunk without a cursor and clear the old logs on screen.
      const reset = epoch.current != null && r.epoch !== epoch.current;
      // A stretch after the cursor has already been pushed out of the agent buffer (tab in background too long, log burst): do not catch up from the oldest
      // (by the time it catches up the screen cannot hold it anyway); jump straight to the latest and record how many lines were skipped.
      let gap = 0;
      if (reset) {
        r = await load(undefined, BATCH);
      } else if (r.skipped > 0) {
        r = await load(undefined, BATCH);
        gap = r.lines.length > 0 && prev != null ? r.lines[0].seq - prev - 1 : r.skipped;
      }
      epoch.current = r.epoch;
      cursor.current = r.next;
      return { ...r, reset, gap };
    },
    refetchInterval: POLL_MS,
    retry: false,
  });

  // Merge the new batch into the view; a full batch means there is more (under heavy logging), so fetch again immediately to catch up.
  useEffect(() => {
    const r = query.data;
    if (!r) return;
    // Gap: insert a marker row so the view does not pretend to be continuous.
    const gap: Row[] = r.gap > 0 && r.lines.length > 0 ? [{ seq: r.lines[0].seq - 0.5, text: '', gap: r.gap }] : [];
    // Only append lines with a seq newer than the last line on screen: on remount react-query hands back the previous batch again, which must not be appended twice
    setLines((prev) => {
      if (r.reset) return r.lines.slice(-KEEP);
      const lastSeq = prev.length ? prev[prev.length - 1].seq : -Infinity;
      const fresh = r.lines.filter((l) => l.seq > lastSeq);
      return fresh.length ? [...prev, ...gap, ...fresh].slice(-KEEP) : prev;
    });
    // Full batch = more remaining, fetch again immediately; the batch right after jumping to latest does not count (it was taken from the tail anyway).
    if (r.lines.length >= BATCH && r.gap === 0 && !r.reset) void query.refetch();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query.dataUpdatedAt]);

  useEffect(() => {
    if (follow && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [lines, follow]);

  // Filtering: stack trace continuation lines have no level and inherit the level of the previous line.
  // Parse each line only once (thousands of lines; do not recompute while typing a filter)
  const parsed = useMemo(() => lines.map((l) => (l.gap ? null : parseLine(l.text))), [lines]);
  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    let last: string | null = null;
    const out: { row: Row; p: ReturnType<typeof parseLine> }[] = [];
    lines.forEach((l, i) => {
      if (l.gap) { out.push({ row: l, p: null }); return; } // gaps are always shown; even when filtering you need to know a stretch is missing here
      const p = parsed[i];
      const lv = p?.level ?? last;
      last = lv;
      if (level !== 'ALL' && (lv == null || (ORDER[lv] ?? 9) > ORDER[level])) return;
      // Search also matches the local time shown on screen, not just the raw string (the raw string is in the node's time zone)
      if (needle && !l.text.toLowerCase().includes(needle) && !(p && p.time.includes(needle))) return;
      out.push({ row: l, p });
    });
    return out;
  }, [lines, parsed, level, q]);

  const loadError = query.isError ? (query.error instanceof HttpError ? errMsg(query.error) : t('error.title')) : null;
  // Failed before any logs arrived: show the error in place of everything. If logs are already shown, only hint at the top and keep them (the next round retries on its own).
  if (loadError && lines.length === 0) {
    return <ErrorState onRetry={() => void query.refetch()} />;
  }
  if (query.data && !query.data.connected && lines.length === 0) {
    return (
      <section className="flex flex-col gap-3 rounded-lg border border-hair bg-surface p-4">
        <h2 className="text-sm font-semibold text-ink">{t('logs.notConnected')}</h2>
        {!source && <LogSetup />}
      </section>
    );
  }

  const field = 'rounded-md border border-hair bg-canvas px-2.5 py-1.5 text-xs text-ink focus:border-accent focus:outline-none';
  return (
    <div className="flex flex-col gap-2">
    <section ref={panel} className="flex scroll-mt-4 flex-col overflow-hidden rounded-lg border border-hair bg-surface">
      <div className="flex min-h-[54px] flex-wrap items-center gap-2 border-b border-hair px-4 py-2">
        <label className="flex items-center gap-2 text-xs text-ink-3">
          {t('logs.level')}
          <Listbox width="w-auto" aria-label={t('logs.level')} value={level} onChange={setLevel} options={LEVELS.map((l) => ({ value: l, label: t(`logs.level_${l}`) }))} />
        </label>
        <input value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('logs.search')} aria-label={t('logs.searchLabel')} className={`${field} w-64`} />
        <label className="ml-auto flex items-center gap-2 text-sm text-ink-2">
          <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} className="accent-accent" />
          {t('logs.follow', { s: POLL_MS / 1000 })}
        </label>
        {/* Expand button: same icon and position as the charts */}
        {onExpand && (
          <Tooltip label={t('common.expand')}>
            <button type="button" onClick={onExpand} data-expand="logs" className="text-ink-3 transition-colors hover:text-ink" aria-label={t('common.expand')}>
              <ArrowsOut size={14} aria-hidden />
            </button>
          </Tooltip>
        )}
      </div>
      {loadError && (
        <p role="alert" className="px-3 py-2 text-xs" style={toneVars('error')}>{loadError}</p>
      )}
      {query.data && !query.data.connected && (
        <div className="flex flex-col gap-2 px-3 py-2 text-xs" style={{ background: 'var(--bsos-status-syncing-bg)' }}>
          {source ? (
            <span className="font-medium" style={{ color: 'var(--bsos-status-syncing-fg)' }}>{t('logs.notConnected')}</span>
          ) : (
            <>
              <button type="button" onClick={() => setShowSetup((v) => !v)} aria-expanded={showSetup} className="w-fit text-left font-medium" style={{ color: 'var(--bsos-status-syncing-fg)' }}>
                {t('logs.disconnectedHint')}
              </button>
              {showSetup && <LogSetup />}
            </>
          )}
        </div>
      )}
      <div
        ref={box}
        role="log"
        aria-live="off"
        // Only scrolling "up" pauses following (to read earlier lines without being pulled back to the bottom every 4 seconds); scrolling down never cancels it. Scrolling back to the bottom resumes following automatically.
        // Judging by position alone would misread the instant right after new logs arrive, before the auto-scroll to the bottom, as leaving the bottom
        onScroll={(e) => {
          const el = e.currentTarget;
          const up = el.scrollTop < lastTop.current - 2;
          lastTop.current = el.scrollTop;
          const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
          if (atBottom && !follow) setFollow(true);
          else if (up && !atBottom && follow) setFollow(false);
        }}
        className={`${height ? '' : 'h-[24rem] '}overflow-auto px-3 py-2 font-mono text-2xs leading-relaxed`} // one size smaller: a line fits, fewer horizontal scrollbars
        // When expanded: total panel height minus the toolbar and footer (about 88)
        style={{ background: TERM.bg, color: TERM.fg, ...(height ? { height: Math.max(320, height - 88) } : {}) }}
      >
        {shown.length === 0 ? (
          <p style={{ color: TERM.dim }}>{t('logs.empty')}</p>
        ) : (
          shown.map(({ row: l, p }) => {
            if (l.gap) {
              return (
                <div key={`gap-${l.seq}`} className="my-1 border-y border-dashed py-0.5 text-center" style={{ borderColor: TERM.rule, color: TERM.dim }}>
                  {t('logs.gap', { n: l.gap })}
                </div>
              );
            }
            // Always plain text (log content comes from the node and must not be treated as HTML); lines that cannot be split into fields are shown whole as is
            return p ? (
              // Time, level and category have fixed widths and do not wrap; the message wraps when it exceeds the box width, so no horizontal scrollbar
              <div key={l.seq} className="flex w-full gap-x-2.5">
                <span className="shrink-0 whitespace-pre" style={{ color: TERM.dim }}>{p.time}</span>
                <span className="w-12 shrink-0 whitespace-pre" style={{ color: levelColor(p.level) }}>{p.level}</span>
                <span className="w-44 shrink-0 truncate" title={p.logger} style={{ color: TERM.logger }}>{p.logger}</span>
                <span className="min-w-0 flex-1 whitespace-pre-wrap [overflow-wrap:anywhere]">{p.msg}</span>
              </div>
            ) : (
              <div key={l.seq} className="whitespace-pre-wrap [overflow-wrap:anywhere]" style={{ color: TERM.dim }}>{l.text}</div>
            );
          })
        )}
      </div>
      <p className="border-t border-hair px-3 py-2 text-xs text-ink-3">
        {/* No filter: "N lines"; with a filter (level or keyword): "M matching (N lines)" */}
        {query.data?.connected ? t('logs.connected') : t('logs.disconnected')}，{(() => {
          const total = lines.filter((l) => !l.gap).length;
          const hit = shown.filter((x) => !x.row.gap).length;
          return hit === total ? t('logs.countAll', { total }) : t('logs.countHit', { hit, total });
        })()}
      </p>
    </section>
    {/* The scope note sits outside the card at the very bottom, replacing the old "Source" line inside the box. Takes no height when expanded.
        The open-source edition links to the commercial edition; the commercial platform itself retains logs, so it points to the "Node logs" page instead; roles without access to retained logs only get the scope, with no pointer elsewhere */}
    {!height && <ScopeNote />}
    </div>
  );
}

function ScopeNote() {
  const { t } = useTranslation();
  const { can } = useAuth();
  const link = 'text-accent underline-offset-2 hover:underline';
  if (!isEnterprise) {
    return <p className="text-2xs text-ink-3">{t('logs.scopeBefore')}<Link to={ENTERPRISE_INFO_PATH} className={link}>{t('logs.scopeLink')}</Link>{t('logs.scopeAfter')}</p>;
  }
  // /audit/node-logs is an Enterprise page; the open-source edition never reaches here
  if (can('nodelogs:view' as Permission)) {
    return <p className="text-2xs text-ink-3">{t('logs.scopeArchiveBefore')}<Link to="/audit/node-logs" className={link}>{t('logs.scopeArchiveLink')}</Link>{t('logs.scopeArchiveAfter')}</p>;
  }
  return <p className="text-2xs text-ink-3">{t('logs.scopeRecentOnly')}</p>;
}
