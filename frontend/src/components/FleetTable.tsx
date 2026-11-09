import { Link, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { CaretRight } from '@phosphor-icons/react';
import type { HourState, NodeStatus, NodeSummary } from '@/api/schemas';
import { NodeStatusBadge } from '@/components/Badges';
import { ValidatorTag } from '@/components/ValidatorTag';
import { HelpTip } from '@/components/HelpTip';
import { Listbox } from '@/components/Listbox';
import { CountChip } from '@/components/ValueChip';
import { fmtInt, fmtPct } from '@/lib/format';
import { nodeDeviations, type TrendMetric } from '@/lib/fleetOutliers';
import { useThresholds, cmpText } from '@/lib/thresholds';
import { NodeLabel } from '@/components/NodeLabel';
import { FlashBehind } from '@/components/FlashBehind';
import { useWorsenedMap } from '@/hooks/useWorsened';
import { statusTone, shownStatus } from '@/lib/status';

/** Status filter: all, abnormal (not healthy), or a single status (when coming from the overview card's segmented bar). */
export type FleetFilter = 'all' | 'issues' | NodeStatus;

/** Values accepted by the st URL parameter (the default all is omitted). */
export const FLEET_FILTERS: FleetFilter[] = ['issues', 'healthy', 'syncing', 'rpc_unreachable', 'offline', 'unknown'];

export const matchesFilter = (n: NodeSummary, f: FleetFilter) =>
  f === 'all' ? true : f === 'issues' ? n.status !== 'healthy' : shownStatus(n) === f;

/**
 * Overview node table: search, status filter, host resources. Enterprise external-org nodes are merged in here too; the caller picks
 * which to list (orgFilter is the org dropdown). Blocks behind uses the API's blocksBehind (the backend already merges Besu's report
 * with the chain-head calculation); the frontend never subtracts node heights from each other. Version isn't in the list, only in node details.
 */
export function FleetTable({ nodes, hrefFor, filter, onFilter, orgFilter, orgOf, notice, emptyText }: {
  nodes: NodeSummary[];
  hrefFor: (n: NodeSummary) => string;
  filter: FleetFilter;
  onFilter: (f: FleetFilter) => void;
  orgFilter?: React.ReactNode;
  /** If given, adds an "Org" column (when "All orgs" is selected). */
  orgOf?: (n: NodeSummary) => React.ReactNode;
  /** One-line note above the table (when an external org is unreachable). */
  notice?: React.ReactNode;
  /** Reason shown when there are no nodes to list; if omitted, the generic "no matching nodes". */
  emptyText?: string;
}) {
  const th = useThresholds();
  const { t } = useTranslation();
  const nav = useNavigate();
  const worsened = useWorsenedMap();
  const rows = nodes.filter((n) => matchesFilter(n, filter));
  // Dropdown options: all, abnormal, plus statuses present in the list (a single status arriving from the overview segmented bar must be included too). Each option shows its node count (same as the alert notifications filter)
  const present = [...new Set<NodeStatus>([...nodes.map(shownStatus), ...(filter !== 'all' && filter !== 'issues' ? [filter] : [])])];
  const opt = (value: FleetFilter, label: string) => ({ value, label: t('common.withCount', { label, n: nodes.filter((n) => matchesFilter(n, value)).length }) });
  const filterOptions: { value: FleetFilter; label: string }[] = [
    opt('all', t('overview.filterAll')),
    opt('issues', t('overview.filterIssues')),
    ...present.map((st) => opt(st, t(`status.${st}`))),
  ];

  return (
    <section className="overflow-hidden rounded-lg border border-hair bg-surface">
      {/* Same layout site-wide: the section is a card with its header inside - title left, selector right, count far right */}
      <div className="flex min-h-[54px] flex-wrap items-center gap-2 border-b border-hair px-4 py-2">
        <h2 className="text-sm font-semibold text-ink">{t('overview.tableTitle')}</h2>
        {orgFilter}
        {/* One org has few nodes, so no search; the filter is a single dropdown: all, abnormal, and statuses present in the list */}
        <label className="ml-auto flex items-center gap-2 text-xs text-ink-3">
          {t('overview.filterLabel')}
          <Listbox
            width="w-auto"
            aria-label={t('overview.filterLabel')}
            value={filter}
            options={filterOptions}
            onChange={onFilter}
          />
        </label>
        <CountChip unit={t('chain.countUnit')}>{rows.length}</CountChip>
      </div>
      {notice && <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-hair bg-canvas px-4 py-2 text-xs text-ink-3">{notice}</div>}
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="table-head whitespace-nowrap">
              {orgOf && <th className="px-4">{t('overview.colOrg')}</th>}
              <th className="px-4">{t('overview.colNode')}</th>
              <th className="px-2"><span className="inline-flex items-center gap-1.5">{t('overview.colStatus')}
                <HelpTip title={t('overview.colStatus')}>
                  {/* Status is a judgment of "right now" */}
                  <dl className="flex flex-col gap-1.5">
                    {(['healthy', 'syncing', 'rpc_unreachable', 'offline'] as const).map((k) => (
                      <div key={k}><dt className="font-medium text-ink">{t(`status.${k}`)}</dt><dd>{t(`overview.statusRule.${k}`)}</dd></div>
                    ))}
                  </dl>
                </HelpTip>
              </span></th>
              <th className="px-3"><span className="inline-flex items-center gap-1.5">{t('overview.colAvail')}<HelpTip title={t('overview.colAvail')}>
                {/* Same layout as the 24-hour cells on the chain page: one color per line, preceded by a swatch */}
                <ul className="flex flex-col gap-1">
                  {(['ok', 'degraded', 'down', 'unknown'] as const).map((k) => (
                    <li key={k} className="flex items-start gap-1.5">
                      <span className="mt-1 size-2 shrink-0 rounded-[1px]" style={{ background: HOUR_COLOR[k] }} aria-hidden="true" />
                      <span>{t(`overview.availRule.${k}`, { lag: cmpText(th.lag) })}</span>
                    </li>
                  ))}
                </ul>
              </HelpTip></span></th>
              {/* Column order matches the comparison section's metric dropdown: memory, CPU, disk, (blocks behind ->) block height, peer count.
                  Besu memory sits right after host memory: the two are often confused, and side by side they're clearly different; it is judged only against the alert threshold, not compared with other nodes */}
              {/* Last-24-hours column px-3, status and numeric columns px-2: with the extra Besu memory column, it still fits the card at 1280 width with a classic scrollbar, no horizontal scrolling */}
              <th className="px-2 text-right">{t('overview.colMem')}</th>
              <th className="px-2 text-right">{t('overview.colJvm')}</th>
              <th className="px-2 text-right">{t('overview.colCpu')}</th>
              <th className="px-2 text-right">{t('overview.colDisk')}</th>
              <th className="px-2 text-right">{t('overview.colHeight')}</th>
              <th className="px-2 text-right">{t('overview.colPeers')}</th>
              <th className="w-6" />
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 ? (
              <tr>
                <td colSpan={orgOf ? 11 : 10} className="px-4 py-8 text-center text-xs text-ink-3">{emptyText ?? t('overview.noMatch')}</td>
              </tr>
            ) : rows.map((n) => {
              const behind = n.status === 'syncing' && (n.blocksBehind ?? 0) > 0 ? n.blocksBehind! : 0;
              // When status worsens (e.g. healthy -> offline), the status cell flashes once with the new status's light background; improvements don't flash
              const tone = behind > 0 ? 'syncing' : statusTone(shownStatus(n));
              const worse = worsened(`${n.org}/${n.id}`, tone === 'error' ? 2 : tone === 'syncing' ? 1 : 0);
              // Deviating numbers are orange: same thresholds (nodeDeviations) as the per-node health metric comparison below, so the table and chart agree
              const dev = new Set(nodeDeviations(n.id, nodes, th).map((d) => d.metric));
              const num = (m: TrendMetric) => `px-2 py-3 text-right font-mono tabular-nums ${dev.has(m) ? '' : 'text-ink-2'}`;
              const warn = (m: TrendMetric) => (dev.has(m) ? { color: 'var(--bsos-status-syncing-fg)' } : undefined);
              return (
                <tr key={`${n.org}/${n.id}`} onClick={() => nav(hrefFor(n))} className="group cursor-pointer border-b border-hair transition-colors last:border-0 hover:bg-elevated">
                  {orgOf && <td className="whitespace-nowrap px-4 py-3 text-xs text-ink-2">{orgOf(n)}</td>}
                  <td className="whitespace-nowrap px-4 py-3">
                    {/* A real link: keyboard reachable + Cmd+click opens a tab; tr onClick is just a large mouse hit area */}
                    <span className="inline-flex items-center gap-1.5">
                      <Link
                        to={hrefFor(n)}
                        className="rounded-sm text-ink focus:outline-none focus-visible:ring-1 focus-visible:ring-accent"
                      >
                        <NodeLabel id={n.id} />
                      </Link>
                      {n.isValidator && <ValidatorTag />}
                    </span>
                  </td>
                  <td className="relative isolate whitespace-nowrap px-2 py-3">
                    <FlashBehind token={worse} tone={tone} />
                    {behind > 0 ? (
                      // Same as NodeStatusBadge: for non-errors only the dot is colored, text stays plain
                      <span className="inline-flex items-center gap-1.5 whitespace-nowrap text-xs font-medium text-ink-2">
                        <span className="size-1.5 shrink-0 rounded-full" style={{ background: 'var(--bsos-status-syncing-fg)' }} />
                        {t('overview.behind', { n: fmtInt(behind) })}
                      </span>
                    ) : (
                      <NodeStatusBadge node={n} />
                    )}
                  </td>
                  <td className="px-3 py-3"><AvailabilityStrip hours={n.availability24h} /></td>
                  <td className={num('mem')} style={warn('mem')}>{fmtPct(n.memPercent)}</td>
                  <td className={num('jvm_heap')} style={warn('jvm_heap')}>{fmtPct(n.jvmHeapPercent)}</td>
                  <td className={num('cpu')} style={warn('cpu')}>{fmtPct(n.cpuPercent)}</td>
                  <td className={num('disk')} style={warn('disk')}>{fmtPct(n.diskUsedPercent)}</td>
                  <td className="px-2 py-3 text-right font-mono tabular-nums text-ink-2">{fmtInt(n.chainHeight)}</td>
                  <td className={num('peer_count')} style={warn('peer_count')}>{fmtInt(n.peerCount)}</td>
                  <td className="px-1 text-ink-3">
                    <CaretRight size={14} className="opacity-0 transition-opacity group-hover:opacity-100" />
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}

const HOUR_COLOR: Record<HourState, string> = {
  ok: 'var(--bsos-status-healthy-fg)',
  degraded: 'var(--bsos-status-syncing-fg)',
  down: 'var(--bsos-status-error-fg)',
  unknown: 'var(--bsos-border-strong)',
};

/** Last-24-hours availability bar (statuspage style): one cell per hour, oldest to newest; green normal / yellow lagging / red offline / gray no data. */
function AvailabilityStrip({ hours }: { hours?: HourState[] }) {
  const { t } = useTranslation();
  if (!hours?.length) return <span className="text-xs text-ink-faint">—</span>;
  const count = (v: HourState) => hours.filter((h) => h === v).length;
  const title = t('overview.availTitle', { ok: count('ok'), degraded: count('degraded'), down: count('down'), unknown: count('unknown') });
  return (
    <div className="flex items-center gap-px" role="img" aria-label={title} title={title}>
      {hours.map((h, i) => (
        <span key={i} className="h-3 w-1 rounded-[1px]" style={{ background: HOUR_COLOR[h] }} />
      ))}
    </div>
  );
}
