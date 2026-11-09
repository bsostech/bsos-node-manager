import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ButtonLink } from '@/components/Button';
import { ArrowLeft, Plus } from '@phosphor-icons/react';
import { api } from '@/api/client';
import { OwnOrgTag } from '@/components/Badges';
import { FleetTable, FLEET_FILTERS, type FleetFilter } from '@/components/FleetTable';
import { OrgFilter, StatusDot, orgSelFromParam, orgSelToParam, type OrgGroup } from '@/components/OrgFilter';
import type { NodeSummary } from '@/api/schemas';
import { enterprise, isEnterprise } from '@/edition';
import { FleetTrend, type TrendView } from '@/components/FleetTrend';
import { FleetStatusBar } from '@/components/FleetStatusBar';
import { useFleetStatus } from '@/hooks/useFleetStatus';
import { useUrlParam } from '@/hooks/useUrlParam';
import { RangeControl } from '@/components/RangeControl';
import { Listbox } from '@/components/Listbox';
import { HelpTip } from '@/components/HelpTip';
import { Pill } from '@/components/Pill';
import { fmtInt } from '@/lib/format';
import { SkeletonRows, EmptyState, ErrorState } from '@/components/States';
import { type RangeSel, selWindow } from '@/lib/range';
import { type TrendMetric, TREND_METRICS, nodeDeviations } from '@/lib/fleetOutliers';
import { useThresholds, cmpText } from '@/lib/thresholds';
import { useAuth } from '@/auth/AuthContext';
import { refreshMs } from '@/lib/refreshInterval';
import { RefreshControls } from '@/components/RefreshControls';

const METRIC_LABEL: Record<TrendMetric, string> = {
  lag: 'overview.mLag', peer_count: 'overview.mPeers', cpu: 'overview.mCpu', mem: 'overview.mMem', jvm_heap: 'overview.mJvm', disk: 'overview.mDisk', txpool_pending: 'overview.mTxpool',
};

// "Refresh" only refetches the data this page uses
const REFRESH_KEYS = ['nodes', 'chain', 'validators', 'series', 'fleet-status', 'alerts', ...(enterprise.overviewOrgs ? [enterprise.overviewOrgs.refreshKey] : [])];
// External orgs (Enterprise only); decided at module level so every render calls the same hooks
const useOrgGroups = enterprise.overviewOrgs?.useGroups ?? (() => undefined);
const ownHref = (n: NodeSummary) => `/nodes/${encodeURIComponent(n.id)}`;

export function Overview() {
  const { t } = useTranslation();
  // View, metric, coloring and range go into the URL: kept across reload, share and back. Zoom and hidden lines are transient and stay in state.
  // r allows custom durations (RangeControl's 3h and the like), so validate the format, not an enum; others are validated as enums, bad values fall back to defaults.
  const [rel, setRel] = useUrlParam<string>('r', '1h', (v) => /^\d+[mhd]$/.test(v));
  const [view, setView] = useUrlParam<TrendView>('view', 'overlay', (v) => v === 'split');
  const [metric, setMetric] = useUrlParam<TrendMetric>('m', 'mem', (v) => TREND_METRICS.includes(v as TrendMetric));
  // Node table status filter (the overview card's segmented bar can also pass in a single status)
  const [filter, setFilter] = useUrlParam<FleetFilter>('st', 'all', (v) => FLEET_FILTERS.includes(v as FleetFilter));
  // Org filter: defaults to this org; hidden when there are no external orgs (Core)
  const [orgParam, setOrgParam] = useUrlParam<string>('org', '');
  const groups = useOrgGroups();
  const [abs, setAbs] = useState<{ from: number; to: number } | null>(null);
  const [hidden, setHidden] = useState<Set<string>>(new Set());

  const { data, isLoading, isError, refetch, dataUpdatedAt } = useQuery({
    queryKey: ['nodes'],
    queryFn: () => api.listNodes(),
    refetchInterval: refreshMs, // live updates: polling, at the site-wide rate
  });
  const { me, can } = useAuth();
  const th = useThresholds();
  const qc = useQueryClient();

  const ids = (data?.nodes ?? []).map((n) => n.id);
  // Who deviates on this metric (same thresholds as the chart's highlighting): used by the conclusion line and the pills
  const deviating = (data?.nodes ?? []).flatMap((n) => {
    const d = nodeDeviations(n.id, data!.nodes, th).find((x) => x.metric === metric);
    return d ? [{ id: n.id, value: d.value }] : [];
  });
  const sel: RangeSel = abs ? { kind: 'abs', from: abs.from, to: abs.to } : { kind: 'rel', dur: rel };
  const autoRefresh = !abs; // Only an absolute window (drag-zoom) freezes; otherwise follow the header refresh rate
  const win = selWindow(sel);
  // Fleet source status (node-level, one per page): shared by the status row and legend; polling follows the live cadence
  const fleet = useFleetStatus(autoRefresh);
  // Unreachable nodes have no data and don't count toward "consistent"
  const unreachable = new Set(fleet.groups.flatMap((g) => g.nodes.map((n) => n.id)));
  const withData = ids.filter((id) => !unreachable.has(id)).length;
  const pickRel = (dur: string) => { setRel(dur); setAbs(null); };
  // The zoomed node (like Grafana's viewPanel): in the URL so it can be shared and survives reload; values not in the list mean not zoomed
  const [focusRaw, setFocusRaw] = useUrlParam<string>('focus', '');
  const focus = ids.includes(focusRaw) ? focusRaw : null;
  // Scroll to the top on entering zoom: the page only has the trend left, and staying at the old scroll position shows blank space. On leaving, focus returns to the zoom button and the browser scrolls there itself
  // Coming from the zoom button, once it unmounts focus falls to body, so move it to the breadcrumb's back button; don't steal focus when opening a shared link directly
  const backRef = useRef<HTMLButtonElement>(null);
  const focusBack = useRef(false);
  useEffect(() => {
    if (!focus) return;
    document.querySelector('main')?.scrollTo(0, 0);
    if (focusBack.current) backRef.current?.focus();
    focusBack.current = false;
  }, [focus]);
  const setFocus = (id: string | null) => {
    focusBack.current = id != null;
    setFocusRaw(id ?? '');
  };
  // Legend (like Grafana): one click shows only this node, clicking again when already solo restores all; additive (Ctrl/⌘) toggles one line at a time
  const toggleNode = (id: string, additive: boolean) =>
    setHidden((prev) => {
      if (additive) {
        const next = new Set(prev);
        next.has(id) ? next.delete(id) : next.add(id);
        return next;
      }
      const only = ids.every((x) => (x === id) !== prev.has(x));
      return only ? new Set() : new Set(ids.filter((x) => x !== id));
    });

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-4">
      <header className="flex items-center gap-4">
        <div className="flex flex-col gap-0.5">
          <h1 className="text-lg font-semibold text-ink">{t('overview.title')}</h1>
          {isEnterprise && me?.org && <span className="text-xs text-ink-3">{me.org}</span>}
        </div>
        <RefreshControls updatedAt={data ? dataUpdatedAt : null} ageSec={data?.dataAgeSec} onRefresh={() => void qc.invalidateQueries({ predicate: (q) => REFRESH_KEYS.includes(q.queryKey[0] as string) })} />
      </header>


      {/* Table; when one node is zoomed the whole page goes to that chart */}
      {focus ? null : isLoading ? (
        <SkeletonRows rows={5} />
      ) : isError ? (
        <ErrorState onRetry={() => void refetch()} />
      ) : !data || (data.nodes.length === 0 && !groups?.length) ? (
        // Fresh deployment: no nodes yet and the platform knows nothing about the chain, so take operators straight to registering the first node (admins don't see this page)
        <EmptyState
          message={t('overview.empty')}
          action={can('nodes:onboard') ? (
            <ButtonLink to="/node-add" className="gap-1.5">
              <Plus size={15} aria-hidden="true" />
              {t('join.addNode')}
            </ButtonLink>
          ) : undefined}
        />
      ) : (
        <OrgFleetTable own={data.nodes} ownOrg={me?.org} groups={groups} orgParam={orgParam} onOrgParam={setOrgParam} filter={filter} onFilter={setFilter} />
      )}

      {/* Cross-node trends: one metric at a time, overlaid or split */}
      {data && data.nodes.length > 0 && (
        <section className="overflow-hidden rounded-lg border border-hair bg-surface">
          {focus ? (
            // Zoomed: the title row becomes a breadcrumb (click "cross-node trends" to return), and the toolbar keeps only the range and live that set the time
            <div className="flex min-h-[54px] flex-wrap items-center gap-2 border-b border-hair px-4 py-2">
              <h2 className="inline-flex min-w-0 items-center gap-1.5 text-sm">
                <button ref={backRef} type="button" onClick={() => setFocus(null)} className="inline-flex items-center gap-1.5 rounded font-semibold text-ink-2 hover:text-ink focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent">
                  <ArrowLeft size={16} aria-hidden />{t('overview.fleetTitle')}
                </button>
                <span className="text-ink-faint" aria-hidden>/</span>
                <span className="truncate font-mono font-semibold text-ink">{focus}</span>
                <span className="text-ink-3">· {t(METRIC_LABEL[metric])}</span>
              </h2>
              <span className="ml-auto flex flex-wrap items-center gap-3">
                <RangeControl variant="dropdown" rel={rel} abs={abs} window={win} onPickRel={pickRel} onSetAbs={(f, tt) => setAbs({ from: f, to: tt })} onClearAbs={() => setAbs(null)} />
              </span>
            </div>
          ) : (
            <>
              <div className="flex min-h-[54px] flex-wrap items-center gap-2 border-b border-hair px-4 py-2">
                <h2 className="inline-flex items-center gap-1.5 text-sm font-semibold text-ink">
                  {t('overview.fleetTitle')}
                  {/* What the metric is and what counts as deviating go in the ⓘ */}
                  <HelpTip title={t('overview.fleetTitle')}>
                    <dl className="flex flex-col gap-2">
                      {TREND_METRICS.map((m) => (
                        <div key={m}>
                          <dt className="font-medium text-ink">{t(METRIC_LABEL[m])}</dt>
                          <dd>{t(`overview.fleetHelp.${m}`, { cpu: cmpText(th.cpu, '%'), mem: cmpText(th.mem, '%'), jvm: cmpText(th.jvmHeap, '%'), disk: cmpText(th.disk, '%'), peers: cmpText(th.peers), lag: cmpText(th.lag) })}</dd>
                        </div>
                      ))}
                    </dl>
                  </HelpTip>
                </h2>
                {/* Rule: selectors always go on the right of the title row; data choices (metric, range) first, presentation (view) after. When wrapping on narrow screens the group stays right-aligned */}
                <div className="ml-auto flex flex-wrap items-center justify-end gap-3">
                <label className="flex items-center gap-2 text-xs text-ink-3">
                  {t('overview.metricLabel')}
                  <Listbox width="w-auto" aria-label={t('overview.metricLabel')} value={metric} options={TREND_METRICS.map((m) => ({ value: m, label: t(METRIC_LABEL[m]) }))} onChange={setMetric} />
                </label>
                <RangeControl variant="dropdown" rel={rel} abs={abs} window={win} onPickRel={pickRel} onSetAbs={(f, tt) => setAbs({ from: f, to: tt })} onClearAbs={() => setAbs(null)} />
                {/* The view is a dropdown too: you can see which one is active, unlike a toggle you have to guess. The live button is gone; polling follows the header refresh rate */}
                <label className="flex items-center gap-2 text-xs text-ink-3">
                  {t('overview.viewLabel')}
                  <Listbox width="w-auto" aria-label={t('overview.viewLabel')} value={view} options={(['overlay', 'split'] as TrendView[]).map((v) => ({ value: v, label: t(v === 'overlay' ? 'overview.viewOverlay' : 'overview.viewSplit') }))} onChange={setView} />
                </label>
                </div>
              </div>
              {/* Conclusion area: a quiet tinted box that separates "who deviates" and "who has no data" from the controls above and the chart below. First line is the conclusion, second the nodes without data */}
              <div className="mx-4 mt-4 flex flex-col gap-1.5 rounded-md bg-canvas px-3 py-2 text-xs">
                <div className="flex flex-wrap items-center gap-2">
                  {/* "Diagnosis" tag (blue, says what this line is); the count only includes nodes with data, those without get their own line */}
                  <Pill tone="info">{t('overview.diagnosis')}</Pill>
                  {deviating.length === 0 ? (
                    <span className="font-medium text-ink">{t('overview.fleetAllSame', { n: withData })}</span>
                  ) : (
                    <>
                      <span className="font-medium text-ink">{t('overview.fleetDeviate', { n: deviating.length })}</span>
                      {deviating.map((d) => <Pill key={d.id} tone="warn">{d.id} · {t(`overview.dev.${metric}`, { v: fmtInt(Math.round(d.value)) })}</Pill>)}
                    </>
                  )}
                </div>
                <FleetStatusBar groups={fleet.groups} />
              </div>
            </>
          )}

          <div className="p-4">
          <FleetTrend
            ids={ids}
            metric={metric}
            metricLabel={t(METRIC_LABEL[metric])}
            view={view}
            coloring="outliers"
            outlierIds={new Set(deviating.map((d) => d.id))}
            hidden={hidden}
            onToggle={toggleNode}
            focus={focus}
            onFocus={setFocus}
            sel={sel}
            autoRefresh={autoRefresh}
            statusById={fleet.byId}
            onZoom={(a, b2) => setAbs({ from: a, to: b2 })}
            onResetZoom={() => setAbs(null)}
          />
          </div>
        </section>
      )}


    </div>
  );
}

/** Build the nodes to list by org filter; without external orgs it is just this org's table. A deleted org selection is treated as this org. */
function OrgFleetTable({ own, ownOrg, groups, orgParam, onOrgParam, filter, onFilter }: {
  own: NodeSummary[];
  ownOrg?: string;
  groups?: OrgGroup[];
  orgParam: string;
  onOrgParam: (v: string) => void;
  filter: FleetFilter;
  onFilter: (f: FleetFilter) => void;
}) {
  const { t } = useTranslation();
  if (!groups?.length) return <FleetTable nodes={own} hrefFor={ownHref} filter={filter} onFilter={onFilter} />;
  const sel = orgSelFromParam(orgParam, groups);
  const byOrg = new Map(groups.map((x) => [x.org, x]));
  const g = sel.kind === 'org' ? byOrg.get(sel.org) : undefined;
  const nodes = sel.kind === 'all' ? [...own, ...groups.flatMap((x) => x.nodes)] : g ? g.nodes : own;
  return (
    <FleetTable
      nodes={nodes}
      hrefFor={(n) => byOrg.get(n.org)?.hrefFor(n) ?? ownHref(n)}
      filter={filter}
      onFilter={onFilter}
      orgFilter={<OrgFilter value={sel} onChange={(s) => onOrgParam(orgSelToParam(s))} ownOrg={ownOrg} ownCount={own.length} groups={groups} />}
      orgOf={sel.kind === 'all' ? (n) => (byOrg.has(n.org) ? n.org : <OwnOrgTag>{t('overview.orgOwn')}</OwnOrgTag>) : undefined}
      notice={g && g.tone !== 'healthy' && (
        <>
          <span className="inline-flex items-center gap-1.5"><StatusDot tone={g.tone} /><span className="font-mono">{g.org}</span> {g.statusLabel}</span>
          {g.note && <span>{g.note}</span>}
        </>
      )}
      emptyText={g && !g.nodes.length ? g.emptyText : undefined}
    />
  );
}
