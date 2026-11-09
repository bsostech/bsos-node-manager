import { type ReactNode } from 'react';
import { useAuth } from '@/auth/AuthContext';
import { CountChip } from '@/components/ValueChip';
import { Pill } from '@/components/Pill';
import { HelpTip } from '@/components/HelpTip';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { CaretLeft, CaretRight, MagnifyingGlass } from '@phosphor-icons/react';
import { api, HttpError } from '@/api/client';
import { errMsg } from '@/lib/errors';
import i18n from '@/i18n';
import type { MetricId, NodeTxPool, TxPoolTx } from '@/api/schemas';
import { MetricPanel } from '@/components/MetricPanel';
import { SkeletonRows, EmptyState, ErrorState } from '@/components/States';
import { Listbox } from '@/components/Listbox';
import { Card } from '@/components/Card';
import { CopyButton } from '@/components/CopyButton';
import type { RangeSel } from '@/lib/range';
import { useUrlParam } from '@/hooks/useUrlParam';
import { fmtAddr, fmtInt, fmtTime } from '@/lib/format';
import { analyzePool, fmtAge, fmtRanges, isAddress, sourceKey, type Lane } from '@/lib/txpool';
import { GoLink } from '@/components/TextLinks';
import { useThresholds, over, cmpText } from '@/lib/thresholds';

export const POLL_MS = 5000;

/** 502 text for the tx pool: txpool_failed already says the pool could not be read, other reasons get that wrapper. */
export const txpoolErrMsg = (e: HttpError) =>
  e.reason === 'txpool_failed' ? errMsg(e) : i18n.t('txpool.unreachable', { reason: errMsg(e) });
const ROWS_MAX = 4;
const RED = { color: 'var(--bsos-status-error-fg)' };

/**
 * Transaction pool tab: the monitoring page for this node's transaction pool. Top to bottom --
 * summary card (snapshot) -> time-series charts (metrics, follow the page's time range) -> rejection reasons (snapshot) -> account table (snapshot; blocked first, click a row for the account page).
 * The snapshot polls /nodes/:id/txpool every 5 seconds; time series go through the existing MetricPanel.
 */
export function NodeTxPoolPanel({ nodeId, txpoolOff = false, sel, syncKey, autoRefresh, onZoom, onResetZoom, toolbar }: {
  nodeId: string;
  /** Node has the TXPOOL namespace off (optional): the snapshot cannot be read, so show not-enabled and how to enable it; charts come from metrics and display as usual */
  txpoolOff?: boolean;
  sel: RangeSel;
  syncKey: string;
  autoRefresh: boolean;
  onZoom: (min: number, max: number) => void;
  onResetZoom: () => void;
  /** Time range toolbar (state owned by the page), placed between the summary card and the charts */
  toolbar: ReactNode;
}) {
  // Going to the Besu setup guide to enable TXPOOL is for operators; viewers only see "Transaction pool not enabled"
  const canGuide = useAuth().can('nodes:onboard');
  const { t } = useTranslation();
  const q = useQuery<NodeTxPool, HttpError>({ queryKey: ['txpool', nodeId], queryFn: () => api.getNodeTxPool(nodeId), refetchInterval: POLL_MS, retry: false, enabled: !txpoolOff });
  const now = Date.now();
  const pool = q.data && !q.data.syncing ? q.data : undefined;
  const lanes = pool ? analyzePool(pool.senders, now) : [];

  const charts: { metric: MetricId; label: string; pair?: { metric: MetricId; label: string; firstLabel: string }; yMaxAtLeast?: number }[] = [
    { metric: 'txpool_pending', label: t('txpool.chartSize'), yMaxAtLeast: 10 },
    { metric: 'txpool_added', label: t('txpool.chartFlow'), pair: { metric: 'txpool_removed', label: t('txpool.chartRemoved'), firstLabel: t('txpool.chartAdded') }, yMaxAtLeast: 1 },
    { metric: 'txpool_rejected', label: t('txpool.chartRejected'), yMaxAtLeast: 5 },
  ];

  return (
    <div className="flex flex-col gap-4">
      {txpoolOff ? (
        <Box>
          <EmptyState
            message={t('txpool.off')}
            action={canGuide ? (
              <GoLink to={`/nodes/${encodeURIComponent(nodeId)}/settings?tab=besu`}>{t('txpool.offHow')}</GoLink>
            ) : undefined}
          />
        </Box>
      ) : q.isLoading ? <SkeletonRows rows={1} />
        // 502 = transaction pool unreadable (agent absent, RPC unreachable, or TXPOOL not enabled); the backend message says which
        : q.isError ? (q.error.status === 502 ? <Box><EmptyState message={txpoolErrMsg(q.error)} /></Box>
          : <ErrorState onRetry={() => void q.refetch()} />)
        : !pool ? <Box><EmptyState message={t('txpool.syncing')} /></Box>
        : <Cards pool={pool} />}

      {toolbar}

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        {charts.map((c) => (
          <MetricPanel
            key={c.metric}
            seriesKey={nodeId}
            metric={c.metric}
            label={c.label}
            pair={c.pair}
            yMaxAtLeast={c.yMaxAtLeast}
            sel={sel}
            syncKey={syncKey}
            autoRefresh={autoRefresh}
            onZoom={onZoom}
            onResetZoom={onResetZoom}
          />
        ))}
        {pool && <RejectedCard rows={pool.rejected ?? null} />}
      </div>

      {pool && <Accounts nodeId={nodeId} pool={pool} lanes={lanes} now={now} />}
    </div>
  );
}

// When the pool is too big and the list exceeds the agent channel limit, the backend returns only stats: mark as truncated, with no accounts
const isStatsOnly = (pool: NodeTxPool) => pool.truncated && pool.senders.length === 0;

/**
 * Summary metric strip: one full-width gray band, hairline dividers, no card frame, type one step smaller than the summary cards at the top of node detail --
 * that row is for the whole node, this one belongs only to the txpool tab, and the hierarchy should be visible.
 */
function Cards({ pool }: { pool: NodeTxPool }) {
  const { t } = useTranslation();
  // Nearly-full and too-old thresholds come from the NodeTxpoolFull / NodeTxpoolStale alert settings
  const th = useThresholds();
  const total = pool.localCount + pool.remoteCount;
  const max = pool.maxSize;
  const full = max != null && max > 0 && over((total / max) * 100, th.poolFull);
  const oldestMs = pool.totals.oldestAddedAt ? Date.now() - new Date(pool.totals.oldestAddedAt).getTime() : null;
  const rejected = pool.rejected?.reduce((n, r) => n + r.count, 0);
  // When truncated, pending / queued cover only the listed accounts, noted in the label; when the pool is too big and only stats came back (statsOnly) these two cells are unknown
  const statsOnly = isStatsOnly(pool);
  const top = (label: string) => (pool.truncated && !statsOnly ? t('txpool.cardTop', { label, count: pool.senders.length }) : label);
  const cells: BandCell[] = [
    { label: t('txpool.cardSize'), value: <>{fmtInt(total)}{max != null && <span className="text-sm text-ink-3">{` / ${fmtInt(max)}`}</span>}</>,
      help: t('txpool.helpSize', { pct: cmpText(th.poolFull, '%') }),
      tag: full ? { tone: 'warn', text: t('txpool.tagFull') } : null },
    { label: top(t('txpool.cardPending')), value: statsOnly ? '—' : fmtInt(pool.totals.pending), help: t('txpool.helpPending') },
    { label: top(t('txpool.cardQueued')), value: statsOnly ? '—' : fmtInt(pool.totals.queued), help: t('txpool.helpQueued'),
      tag: pool.totals.queued > 0 ? { tone: 'warn', text: t('txpool.tagBlocked') } : null },
    { label: t('txpool.cardOldest'), value: oldestMs == null ? '—' : fmtAge(oldestMs), help: t('txpool.helpOldest', { sec: cmpText(th.poolOldSec) }),
      tag: oldestMs != null && over(oldestMs / 1000, th.poolOldSec) ? { tone: 'warn', text: t('txpool.tagLong') } : null },
    { label: t('txpool.cardRejected'), value: rejected == null ? '—' : fmtInt(rejected), help: t('txpool.helpRejected') },
  ];
  return <Band cells={cells} />;
}

/** The strip itself; the account page summary uses it too */
/** One cell: label (+ ⓘ), number, and a pill at the top right when abnormal. loud marks the value red without a pill. */
export type BandCell = { label: string; value: ReactNode; loud?: boolean; help?: string; tag?: { tone: 'warn' | 'err'; text: string } | null };
export function Band({ cells }: { cells: BandCell[] }) {
  return (
    <div className="flex flex-wrap overflow-hidden rounded-lg border border-hair bg-elevated">
      {cells.map((c, i) => (
        <div key={c.label} className={`flex min-w-[8rem] flex-1 flex-col gap-0.5 px-4 py-2.5 ${i ? 'border-l border-hair' : ''}`}>
          <span className="flex items-center gap-1.5 text-xs text-ink-3">
            {c.label}
            {c.help && <HelpTip title={c.label}>{c.help}</HelpTip>}
          </span>
          {/* The pill sits on the same line as the number, right-aligned: does not overlap the label when the cell is narrow */}
          <span className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-lg leading-tight tabular-nums text-ink" style={c.loud ? RED : undefined}>{c.value}</span>
            {c.tag && <Pill tone={c.tag.tone} className="ml-auto">{c.tag.text}</Pill>}
          </span>
        </div>
      ))}
    </div>
  );
}

/** Rejection reasons: horizontal bars, same card shell as MetricPanel, mixed into the same grid */
function RejectedCard({ rows, height = 140 }: { rows: { reason: string; count: number }[] | null; height?: number }) {
  const { t } = useTranslation();
  if (!rows) {
    // Platform has no Prometheus: rejection counts are unavailable
    return (
      <div className="flex flex-col gap-2 rounded-lg border border-hair bg-surface p-3">
        <span className="text-xs text-ink-3">{t('txpool.rejectedByReason')}</span>
        <div className="flex items-center justify-center text-xs text-ink-3" style={{ height }}>{t('txpool.noProm')}</div>
      </div>
    );
  }
  const total = rows.reduce((n, r) => n + r.count, 0);
  // Card height is fixed: only the top four reasons, the rest merged into one row
  const top = rows.slice(0, ROWS_MAX - 1);
  const rest = rows.slice(ROWS_MAX - 1).reduce((n, r) => n + r.count, 0);
  const shown = rows.length <= ROWS_MAX ? rows : [...top, { reason: 'OTHER', count: rest }];
  const max = Math.max(1, ...shown.map((r) => r.count));
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-hair bg-surface p-3">
      <header className="flex items-center justify-between gap-2">
        <span className="truncate text-xs text-ink-3">{t('txpool.rejectedByReason')}</span>
        <CountChip unit={t('txpool.unitTx')}>{fmtInt(total)}</CountChip>
      </header>
      {rows.length === 0 ? (
        <div className="flex items-center justify-center text-xs text-ink-3" style={{ height }}>{t('txpool.noRejected')}</div>
      ) : (
        <ol className="flex flex-col justify-start gap-2.5 pt-1" style={{ minHeight: height }}>
          {/* Two lines per row: reason and count on top (same baseline), thin bar below; spacing between rows so it is less cramped */}
          {shown.map((r) => (
            <li key={r.reason} className="flex flex-col gap-1 text-2xs">
              <span className="flex items-baseline gap-2">
                <span className="min-w-0 flex-1 truncate text-ink-2" title={r.reason}>{t(`txpool.rsn_${r.reason}`, { defaultValue: r.reason })}</span>
                <span className="font-mono tabular-nums text-ink">{fmtInt(r.count)}</span>
              </span>
              <div className="h-1 w-full rounded-full bg-elevated"><div className="h-full rounded-full bg-accent" style={{ width: `${(r.count / max) * 100}%` }} /></div>
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}

/** Accounts per page; there can be hundreds of accounts, so paginate */
const PAGE = 20;

/** Account table: one row per account, blocked first. Search and filter in the header; click a row for the account page with the nonce track and transaction details.
 *  Filter, search and page go in the URL: coming back from the account page must land in the same place. Key prefix t, so it does not clash with NodeDetail's v/r/f */
function Accounts({ nodeId, pool, lanes, now }: { nodeId: string; pool: NodeTxPool; lanes: Lane[]; now: number }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [filterRaw] = useUrlParam<'' | 'gap' | 'all'>('tf', '', (v) => v === 'gap' || v === 'all');
  const [q] = useUrlParam<string>('tq', '');
  const [pageRaw, setPageRaw] = useUrlParam<string>('tp', '1', (v) => /^[1-9]\d*$/.test(v));
  const page = Number(pageRaw) - 1;
  const setPage = (p: number) => setPageRaw(String(p + 1));
  // Changing the filter or search should also go back to page one: write both params at once. With two separate sets, react-router 6's updater receives
  // the params from render time, and the second set overwrites the first
  const [, setSp] = useSearchParams();
  const setAndFirstPage = (key: 'tf' | 'tq', v: string) => setSp((prev) => {
    const p = new URLSearchParams(prev);
    if (v) p.set(key, v); else p.delete(key);
    p.delete('tp');
    return p;
  }, { replace: true });
  const gaps = lanes.filter((l) => l.state === 'gap');
  // Default shows all accounts (blocked ones are already sorted first); pick from the dropdown to see only blocked ones
  const filter = filterRaw || 'all';
  // Partial address match, case-insensitive; with or without 0x
  const needle = q.trim().toLowerCase().replace(/^0x/, '');
  const local = (filter === 'gap' ? gaps : lanes).filter((l) => !needle || l.sender.address.toLowerCase().includes(needle));
  // When the list is truncated and a full address is typed that is not among the listed accounts -> ask the backend to look up just this address (not subject to the account limit)
  // "Not in the list" is checked against all accounts, not the filtered ones: an account in the list that is not blocked should not appear under the "Blocked" filter anyway
  const listed = lanes.some((l) => l.sender.address === q.trim().toLowerCase());
  const lookup = pool.truncated && isAddress(q) && !listed ? q.trim().toLowerCase() : '';
  const one = useQuery<NodeTxPool, HttpError>({ queryKey: ['txpool', nodeId, lookup], queryFn: () => api.getNodeTxPool(nodeId, lookup), enabled: !!lookup, refetchInterval: POLL_MS, retry: false });
  const matched = lookup ? analyzePool(one.data?.senders ?? [], now) : local;
  const pages = Math.max(1, Math.ceil(matched.length / PAGE));
  const cur = Math.min(page, pages - 1);
  const shown = matched.slice(cur * PAGE, cur * PAGE + PAGE);
  const accountPath = (a: string) => `/nodes/${encodeURIComponent(nodeId)}/txpool/${a}`;
  // Local/remote in the footer is computed from the current filter and search results, not the whole pool
  const mTxs = matched.reduce((n, l) => n + l.sender.txs.length, 0);
  const mLocal = matched.reduce((n, l) => n + l.sender.txs.filter((tx) => tx.local).length, 0);
  const th = 'whitespace-nowrap px-4';
  const td = 'whitespace-nowrap px-4 py-2';
  // With stats only there is no account list: hide the filter (it has no effect on lookup results either); count and source breakdown exist only when a single address was looked up
  const statsOnly = isStatsOnly(pool);
  const hasRows = !statsOnly || !!lookup;
  // Right side of the title bar: search (data) -> show (filter) -> count, same order as other cards
  const aside = (
    <div className="flex flex-wrap items-center justify-end gap-3">
      <label className="flex h-8 w-56 items-center gap-2 rounded-md border border-hair bg-canvas px-2.5 text-ink-3 focus-within:ring-1 focus-within:ring-accent">
        <MagnifyingGlass size={13} aria-hidden />
        <input type="search" value={q} onChange={(e) => setAndFirstPage('tq', e.target.value)}
          placeholder={t('txpool.search')} aria-label={t('txpool.search')}
          className="w-full bg-transparent font-mono text-xs text-ink outline-none placeholder:font-sans placeholder:text-ink-3" />
      </label>
      {!statsOnly && (
        <label className="flex items-center gap-2">
          {t('txpool.show')}
          <Listbox width="w-auto" aria-label={t('txpool.show')} value={filter} onChange={(v) => setAndFirstPage('tf', v)}
            options={[{ value: 'gap', label: t('txpool.showGap', { count: gaps.length }) }, { value: 'all', label: t('txpool.showAll', { count: lanes.length }) }]} />
        </label>
      )}
      {hasRows && <CountChip unit={t('txpool.unitAcct')}>{fmtInt(matched.length)}</CountChip>}
    </div>
  );
  const head = (label: string, help: string, right = false) => (
    <th className={`${th} ${right ? 'text-right' : ''}`}>
      <span className="inline-flex items-center gap-1.5">{label}<HelpTip title={label}>{help}</HelpTip></span>
    </th>
  );
  return (
    <Card title={<span className="whitespace-nowrap">{t('txpool.accountsTitle')}</span>} aside={aside} bodyClass="flex flex-col">
      {statsOnly && !lookup ? <EmptyState message={t('txpool.tooLarge')} />
        : lanes.length === 0 && !lookup ? <EmptyState message={t('txpool.empty')} />
        : lookup && one.isLoading ? <div className="p-4"><SkeletonRows rows={1} /></div>
        : lookup && one.isError ? <EmptyState message={t('txpool.unreachable', { reason: errMsg(one.error) })} />
        : matched.length === 0 ? <EmptyState message={t(needle ? 'txpool.noMatch' : 'txpool.noGap')} /> : (
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead>
              <tr className="table-head">
                <th className={th}>{t('txpool.colAccount')}</th>
                <th className={`${th} text-right`}>{t('txpool.colTxs')}</th>
                <th className={`${th} text-right`}>{t('txpool.cardPending')}</th>
                <th className={`${th} text-right`}>{t('txpool.cardQueued')}</th>
                {head(t('txpool.colMissing'), t('txpool.helpMissing'))}
                {head(t('txpool.colAge'), t('txpool.helpAge'), true)}
                {head(t('txpool.colSource'), t('txpool.helpSource'))}
              </tr>
            </thead>
            <tbody>
              {shown.map((l) => {
                const a = l.sender.address;
                const gap = l.state === 'gap';
                return (
                  <tr key={a} className="cursor-pointer border-b border-hair transition-colors last:border-0 hover:bg-elevated" onClick={() => navigate(accountPath(a))}>
                    <td className={td}>
                      <span className="inline-flex items-center gap-1.5">
                        {/* The whole row is clickable; keyboard goes through this link. Address shortened, full value in title and the copy button */}
                        <Link to={accountPath(a)} title={a} onClick={(e) => e.stopPropagation()} className="rounded-sm font-mono text-ink hover:underline focus:outline-none focus-visible:ring-1 focus-visible:ring-accent">{fmtAddr(a)}</Link>
                        <CopyButton value={a} variant="cell" stop />
                      </span>
                    </td>
                    <td className={`${td} text-right font-mono tabular-nums text-ink`}>{fmtInt(l.sender.txs.length)}</td>
                    <td className={`${td} text-right font-mono tabular-nums text-ink-2`}>{fmtInt(l.ready.length)}</td>
                    <td className={`${td} text-right font-mono tabular-nums ${l.blocked.length ? 'text-ink' : 'text-ink-3'}`}>{fmtInt(l.blocked.length)}</td>
                    {/* Color only on the pill: orange only for a nonce gap */}
                    <td className={td}>{gap ? <Pill tone="warn" className="-ml-1.5 font-mono">{t('txpool.missingTag', { n: fmtRanges(l.missing) })}</Pill> : <span className="text-ink-3">—</span>}</td>
                    <td className={`${td} text-right font-mono tabular-nums text-ink-2`}>{fmtAge(now - l.since)}</td>
                    <td className={`${td} text-ink-2`}>{t(sourceKey(l))}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {/* Footer: source breakdown and snapshot time on the left, pagination on the right; when truncated a pill explains that only the first few accounts are listed */}
      <div className="flex flex-wrap items-center gap-3 border-t border-hair px-4 py-2.5 text-xs text-ink-3">
        <span>{hasRows ? t('txpool.footer', { local: fmtInt(mLocal), relay: fmtInt(mTxs - mLocal), at: fmtTime(pool.asOf) }) : `${t('txpool.snapshot')} ${fmtTime(pool.asOf)}`}</span>
        {pool.truncated && !statsOnly && <Pill tone="warn">{t('txpool.truncated', { count: lanes.length })}</Pill>}
        {pages > 1 && (
          <>
            <span className="ml-auto font-mono tabular-nums">{cur * PAGE + 1}–{cur * PAGE + shown.length} / {matched.length}</span>
            <Pager page={cur} pages={pages} onPage={setPage} />
          </>
        )}
      </div>
    </Card>
  );
}

/** Pagination: ‹ 1 2 3 … 10 ›. With many pages only the first, last and pages around the current one are listed */
function Pager({ page, pages, onPage }: { page: number; pages: number; onPage: (p: number) => void }) {
  const { t } = useTranslation();
  const near = new Set([0, pages - 1, page - 1, page, page + 1].filter((p) => p >= 0 && p < pages));
  const items: (number | null)[] = [];
  for (let p = 0; p < pages; p++) {
    if (near.has(p)) items.push(p);
    else if (items[items.length - 1] !== null) items.push(null);
  }
  const btn = 'inline-flex h-7 min-w-7 items-center justify-center rounded-md px-1.5 font-mono tabular-nums focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent disabled:opacity-40';
  return (
    <nav className="flex items-center gap-1" aria-label={t('txpool.pages')}>
      <button type="button" className={`${btn} text-ink-2 hover:bg-elevated`} disabled={page === 0} onClick={() => onPage(page - 1)} aria-label={t('txpool.prev')}><CaretLeft size={12} /></button>
      {items.map((p, i) => p === null ? <span key={`g${i}`} className="px-1">…</span> : (
        <button key={p} type="button" onClick={() => onPage(p)} aria-current={p === page ? 'page' : undefined}
          className={`${btn} ${p === page ? 'border border-hair bg-surface font-medium text-ink' : 'text-ink-2 hover:bg-elevated'}`}>{p + 1}</button>
      ))}
      <button type="button" className={`${btn} text-ink-2 hover:bg-elevated`} disabled={page === pages - 1} onClick={() => onPage(page + 1)} aria-label={t('txpool.next')}><CaretRight size={12} /></button>
    </nav>
  );
}

/** Table frame (no title bar: the tab name already says this is the transaction pool) */
export function Box({ children }: { children: ReactNode }) {
  return <section className="min-w-0 overflow-hidden rounded-lg border border-hair bg-surface">{children}</section>;
}

/** Account page body: one card -- title bar (title + ⓘ on the left, legend on the right), nonce track (grows in order, gaps blink), transaction detail table (site-wide table layout, flush with the card edges) */
export function LaneDetail({ lane }: { lane: Lane }) {
  const { t } = useTranslation();
  const txs = [...lane.sender.txs].sort((a, b) => a.nonce - b.nonce);
  return (
    <Box>
      <div className="flex min-h-[54px] flex-wrap items-center gap-x-4 gap-y-1 border-b border-hair px-4 py-2">
        <h2 className="flex items-center gap-1.5 text-sm font-semibold text-ink">
          {t('txpool.trackTitle')}
          {/* What the track means goes in ⓘ: one account's transactions go on chain in nonce order, and one missing nonce makes everything after it wait */}
          <HelpTip title={t('txpool.trackTitle')}>{t('txpool.trackHelp')}</HelpTip>
        </h2>
        <Legend />
      </div>
      <Track lane={lane} />
      <TxTable txs={txs} lane={lane} />
    </Box>
  );
}

// ── nonce track ─────────────────────────────────────────────
type CellKind = 'done' | 'landing' | 'ready' | 'miss' | 'blocked';
type Cell = { kind: CellKind; nonce: number } | { kind: 'more'; count: number | null };

const RUN_MAX = 8;
function cellsOf(l: Lane): Cell[] {
  const kind = new Map<number, CellKind>();
  l.ready.forEach((tx) => kind.set(tx.nonce, 'ready'));
  l.missing.forEach((n) => kind.set(n, 'miss'));
  l.blocked.forEach((tx) => kind.set(tx.nonce, 'blocked'));
  const next = l.sender.nextNonce;
  const landing = new Set(l.landing.map((tx) => tx.nonce));
  const seq: Cell[] = [];
  const firstDone = Math.max(0, next - 3);
  if (firstDone > 0) seq.push({ kind: 'more', count: null });
  for (let n = firstDone; n < next; n++) seq.push({ kind: landing.has(n) ? 'landing' : 'done', nonce: n });
  const keys = [...kind.keys()].sort((a, b) => a - b);
  for (let i = 0; i < keys.length; ) {
    let j = i;
    while (j + 1 < keys.length && kind.get(keys[j + 1]) === kind.get(keys[i])) j++;
    const run = keys.slice(i, j + 1).map((n) => ({ kind: kind.get(n)!, nonce: n }));
    if (run.length > RUN_MAX) seq.push(...run.slice(0, 3), { kind: 'more', count: run.length - 5 }, ...run.slice(-2));
    else seq.push(...run);
    i = j + 1;
  }
  return seq;
}

const BOX: Record<CellKind, React.CSSProperties> = {
  done: { background: 'var(--bsos-status-healthy-bg)', border: '1.5px solid var(--bsos-status-healthy-fg)' },
  landing: { background: 'var(--bsos-status-healthy-bg)', border: '1.5px dashed var(--bsos-status-healthy-fg)' },
  ready: { background: 'var(--bsos-status-info-bg)', border: '1.5px solid var(--bsos-status-info-fg)' },
  miss: { background: 'transparent', border: '2px dashed var(--bsos-status-error-fg)' },
  blocked: {
    border: '1.5px solid var(--bsos-status-syncing-fg)',
    background: 'repeating-linear-gradient(135deg, var(--bsos-status-syncing-bg) 0 4px, transparent 4px 8px)',
  },
};

// On entering the account page: cells drop in from left to right; the gap cell keeps blinking once it appears; blocked cells nudge toward the gap, as if queuing
const KEYFRAMES = `
@keyframes txp-in { from { opacity: 0; transform: translateY(-6px) } to { opacity: 1; transform: none } }
@keyframes txp-miss { 0%,100% { opacity: 1 } 50% { opacity: .25 } }
@keyframes txp-push { 0%,100% { transform: none } 50% { transform: translateX(-3px) } }
@media (prefers-reduced-motion: reduce) { .txp-cell, .txp-box { animation: none !important } }`;

function Track({ lane }: { lane: Lane }) {
  const { t } = useTranslation();
  const cells = cellsOf(lane);
  return (
    <div className="overflow-x-auto px-4 pb-4 pt-5">
      <style>{KEYFRAMES}</style>
      <ol className="flex w-max items-start gap-1.5" aria-label={t('txpool.trackAria')}>
        {cells.map((c, i) => {
          const enter = { animation: `txp-in .25s ease-out ${i * 45}ms both` };
          if (c.kind === 'more') return <li key={`m${i}`} className="txp-cell flex h-8 min-w-8 items-center justify-center px-1 font-mono text-xs text-ink-3" style={enter}>{c.count == null ? '…' : `+${c.count}`}</li>;
          const loop = c.kind === 'miss' ? `txp-miss 1.2s ease-in-out ${i * 45 + 250}ms infinite` : c.kind === 'blocked' ? `txp-push 1.6s ease-in-out ${i * 45 + 400}ms infinite` : undefined;
          return (
            <li key={c.nonce} className="txp-cell flex w-8 flex-col items-center gap-1.5" style={enter} title={`nonce ${c.nonce} · ${t(`txpool.leg_${c.kind}`)}`}>
              <span className="txp-box inline-block h-8 w-8 rounded" style={{ ...BOX[c.kind], animation: loop }} />
              <span className={`font-mono text-2xs leading-none tabular-nums ${c.kind === 'miss' ? '' : 'text-ink-3'}`} style={c.kind === 'miss' ? RED : undefined}>{c.nonce}</span>
            </li>
          );
        })}
      </ol>
    </div>
  );
}

/** Track legend: placed on the right of the card title bar */
function Legend() {
  const { t } = useTranslation();
  return (
    <div className="ml-auto flex flex-wrap items-center gap-x-3 gap-y-1 text-2xs text-ink-3">
      {(['done', 'ready', 'miss', 'blocked'] as CellKind[]).map((k) => (
        <span key={k} className="inline-flex items-center gap-1.5"><span className="inline-block size-2.5 rounded-sm" style={BOX[k]} />{t(`txpool.leg_${k}`)}</span>
      ))}
    </div>
  );
}

function TxTable({ txs, lane }: { txs: TxPoolTx[]; lane: Lane }) {
  const { t } = useTranslation();
  const kindOf = (tx: TxPoolTx): CellKind => (lane.landing.includes(tx) ? 'landing' : lane.blocked.includes(tx) ? 'blocked' : 'ready');
  // Status pill colors match the track above: ready blue, waiting-on-earlier orange, being-included green
  const STATE: Record<'ready' | 'blocked' | 'landing', { tone: 'info' | 'warn' | 'ok'; text: string }> = {
    ready: { tone: 'info', text: 'pending' },
    blocked: { tone: 'warn', text: 'queued' },
    landing: { tone: 'ok', text: t('txpool.leg_landing') },
  };
  const td = 'whitespace-nowrap px-3 py-2.5';
  return (
    <table className="w-full text-sm">
      <thead>
        <tr className="table-head whitespace-nowrap">
          <th className="px-4">nonce</th>
          <th className="px-3"><span className="inline-flex items-center gap-1.5">hash<HelpTip title="hash">{t('txpool.helpHash')}</HelpTip></span></th>
          <th className="px-3">to</th>
          <th className="px-3">{t('txpool.colAdded')}</th>
          <th className="px-3">{t('txpool.colSource')}</th>
          <th className="px-3">{t('txpool.colState')}</th>
        </tr>
      </thead>
      <tbody>
        {txs.map((tx) => {
          const st = STATE[kindOf(tx) as 'ready' | 'blocked' | 'landing'];
          return (
            <tr key={tx.hash} className="border-b border-hair last:border-0">
              <td className="whitespace-nowrap px-4 py-2.5 font-mono tabular-nums text-ink">{tx.nonce}</td>
              {/* hash and to, like everywhere else, keep a few characters at each end with the middle elided, full value on hover, copy beside it: one line per row, no wrapping, no horizontal scroll */}
              <td className={`${td} font-mono text-ink-2`}><span className="inline-flex items-center gap-1" title={tx.hash}>{fmtAddr(tx.hash, 10, 8)}<CopyButton value={tx.hash} variant="cell" /></span></td>
              <td className={`${td} font-mono text-ink-2`}>{tx.to ? <span className="inline-flex items-center gap-1" title={tx.to}>{fmtAddr(tx.to)}<CopyButton value={tx.to} variant="cell" /></span> : <span className="font-sans text-ink-3">{t('txpool.deploy')}</span>}</td>
              <td className={`${td} font-mono tabular-nums text-ink-2`}>{fmtTime(tx.addedAt)}</td>
              <td className={`${td} text-ink-2`}>{t(tx.local ? 'txpool.srcLocal' : 'txpool.srcRelay')}</td>
              <td className={td}><Pill tone={st.tone} className="-ml-1.5">{st.text}</Pill></td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}
