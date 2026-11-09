import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { NameEditor } from '@/components/NameEditor';
import type { TFunction } from 'i18next';
import type { ChainNode } from '@/api/schemas';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { PencilSimple } from '@phosphor-icons/react';
import { api, chainQuery } from '@/api/client';
import { useAuth } from '@/auth/AuthContext';
import { ChainStatus } from '@/components/OverviewStatus';
import { MetricPanel } from '@/components/MetricPanel';
import { SkeletonRows, EmptyState, ErrorState } from '@/components/States';
import { CopyButton } from '@/components/CopyButton';
import { useNow } from '@/hooks/useNow';
import { useChainHalted } from '@/hooks/useChainHalted';
import { refreshMs } from '@/lib/refreshInterval';
import { RANGE_PRESETS, type Preset } from '@/lib/range';
import { RefreshControls } from '@/components/RefreshControls';
import { Pill } from '@/components/Pill';
import { ValidatorTag } from '@/components/ValidatorTag';
import { Listbox } from '@/components/Listbox';
import { CountChip } from '@/components/ValueChip';
import { HelpTip } from '@/components/HelpTip';
import { fmtAddr, fmtInt } from '@/lib/format';
import { cmpText, over, useThresholds } from '@/lib/thresholds';
import { FlashBehind } from '@/components/FlashBehind';
import { useWorsenedMap } from '@/hooks/useWorsened';
import { useSavedFlash } from '@/hooks/useSavedFlash';
import { statusLabel } from '@/lib/status';
import { isUnreachable } from '@/lib/validators';

/** Light left of the name: red = abnormal, orange = attention, same as Pill's err / warn colors */
const DOT: Record<'err' | 'warn', string> = { err: 'var(--bsos-status-error-fg)', warn: 'var(--bsos-status-syncing-fg)' };
type Light = { level: 'err' | 'warn'; why: string[] };
const REFRESH_KEYS = ['chain', 'validators', 'chain-nodes', 'alerts'];

/**
 * Chain health: first whether the whole chain is healthy (status card), then every node on chain we can see (nameable).
 * The platform can only read the chain through this org's nodes, so everything here is "on-chain fact", not the state of others' machines; that needs cross-org monitoring (Enterprise).
 */
export function ChainHealth() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const chainQ = useQuery(chainQuery);
  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-4">
      <header className="flex items-center gap-4">
        <h1 className="text-lg font-semibold text-ink">{t('chain.title')}</h1>
        <RefreshControls updatedAt={chainQ.data ? chainQ.dataUpdatedAt : null} ageSec={chainQ.data?.dataAgeSec} onRefresh={() => void qc.invalidateQueries({ predicate: (q) => REFRESH_KEYS.includes(q.queryKey[0] as string) })} />
      </header>

      <ChainStatus chain={chainQ.data} />

      {chainQ.data?.sourceNodeId && <ChainTrends sourceNodeId={chainQ.data.sourceNodeId} />}

      <ChainNodes />
    </div>
  );
}

/** Chain trends: block time and transaction volume curves sharing one time range. Uses the series of the source node (blocks are on-chain facts, every node sees the same). */
function ChainTrends({ sourceNodeId }: { sourceNodeId: string }) {
  const { t } = useTranslation();
  const [dur, setDur] = useState<Preset>('1h');
  const sel = { kind: 'rel' as const, dur };
  return (
    <section className="flex flex-col gap-3 rounded-lg border border-hair bg-surface px-4 py-3">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-sm font-semibold text-ink">{t('chain.trendsTitle')}</h2>
        {/* Range uses "label + dropdown": one name per control, options in plain words; the steps are shared with overview and node detail charts (RANGE_PRESETS) */}
        <label className="ml-auto flex items-center gap-2 text-xs text-ink-3">
          {t('chain.rangeLabel')}
          <Listbox width="w-auto" aria-label={t('chain.rangeLabel')} value={dur} options={RANGE_PRESETS.map((d) => ({ value: d, label: t(`range.${d}`) }))} onChange={setDur} />
        </label>
      </div>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <MetricPanel seriesKey={sourceNodeId} metric="block_time" label={t('chain.blockTimeTitle')} sel={sel} syncKey="chain-trends" autoRefresh yMaxAtLeast={4} />
        <MetricPanel seriesKey={sourceNodeId} metric="tps" label={t('chain.tpsTitle')} sel={sel} syncKey="chain-trends" autoRefresh yMaxAtLeast={1} />
      </div>
    </section>
  );
}

/** On-chain node list: validators first; names can be edited inline on the row (stored in peer-names, applies site-wide). */
/** How long ago: seconds -> minutes -> hours -> days, one unit only; switch to days at 2 days (same as the overview block cell). */
function ago(t: TFunction, ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return t('chain.agoS', { s });
  if (s < 3600) return t('chain.agoM', { m: Math.round(s / 60) });
  if (s < 172_800) return t('chain.agoH', { h: Math.round(s / 3600) });
  return t('chain.agoD', { d: Math.round(s / 86_400) });
}

/** On-chain nodes: listed from the whole chain's point of view, not centered on this org; own nodes are only marked with a small tag, and role / status use tags rather than colored text. */
function ChainNodes() {
  const { t } = useTranslation();
  const { can } = useAuth();
  const canName = can('nodes:onboard');
  const [filter, setFilter] = useState<'all' | 'validators' | 'regular'>('all');
  // Filter follows the light left of the name: abnormal (red), attention (orange), normal (unlit)
  const [status, setStatus] = useState<'all' | 'err' | 'warn' | 'ok'>('all');
  const th = useThresholds();
  const q = useQuery({ queryKey: ['chain-nodes'], queryFn: api.listChainNodes, refetchInterval: refreshMs });
  // Own nodes' machine state (offline, RPC unreachable) only exists in "My nodes"; whether the validator list is readable comes from /chain. All are queries the page already uses
  const own = useQuery({ queryKey: ['nodes'], queryFn: () => api.listNodes(), select: (r) => r.nodes, refetchInterval: refreshMs }).data;
  const chainQ = useQuery(chainQuery);
  const chain = chainQ.data;
  const qc = useQueryClient();
  const [editing, setEditing] = useState<string | null>(null);
  const now = useNow(5_000); // "N seconds ago" only needs to tick every 5s; don't redraw the whole table every second
  // After naming: refetch the list and the validator list (used by the status card sentence)
  const renamed = () => { setEditing(null); for (const k of ['chain-nodes', 'validators', 'nodes']) void qc.invalidateQueries({ queryKey: [k] }); };
  // Light rules match alerting: node offline (NodeOffline) is abnormal; validator not in consensus (ValidatorsMissing) and node behind (NodeBehind) are attention.
  // When consensus is halted (useChainHalted, same as the consensus participation cell above) the turn record stops before the halt and can't show who participates: only validators actually unreachable now are marked (red),
  // other validators aren't marked either way; it is the same set the overview's needs-attention list calls out (isUnreachable)
  const ownNode = new Map((own ?? []).map((x) => [x.id, x]));
  const halted = useChainHalted();
  const vals = useQuery({ queryKey: ['validators'], queryFn: () => api.listValidatorSet(), refetchInterval: refreshMs }).data;
  const unreachable = new Set(halted ? (vals ?? []).filter((v) => isUnreachable(v, own)).map((v) => v.address.toLowerCase()) : []);
  const worsened = useWorsenedMap();
  const savedFlash = useSavedFlash();
  const lightOf = (n: ChainNode): Light | null => {
    const err: string[] = [];
    const warn: string[] = [];
    const o = n.isOwn && n.nodeId ? ownNode.get(n.nodeId) : undefined;
    const down = o?.status === 'offline' || o?.status === 'rpc_unreachable';
    // Own validator machine down: the light reason only says offline / RPC unreachable (which is why it is unreachable), not also "not participating in consensus" or "unreachable"
    if (o && down) err.push(statusLabel(t, o));
    else if (halted) { if (n.isValidator && unreachable.has(n.address.toLowerCase())) err.push(t('chain.dotUnreachable')); }
    else if (n.isValidator && n.producing === false) warn.push(t('chain.validatorIdle'));
    if (over(n.blocksBehind, th.lag)) warn.push(t('chain.dotBehind', { n: fmtInt(n.blocksBehind ?? 0) }));
    return err.length ? { level: 'err', why: [...err, ...warn] } : warn.length ? { level: 'warn', why: warn } : null;
  };
  // When the validator list can't be read (GET /chain validatorCount is null, or /chain itself fails), isValidator is always false: role is treated as unknown and the type filter is disabled
  const rolesKnown = chain ? chain.validatorCount != null : !chainQ.isError;
  const byType = (n: ChainNode, f: typeof filter) => !rolesKnown || f === 'all' || (f === 'validators') === n.isValidator;
  const byLight = (n: ChainNode, s: typeof status) => s === 'all' || s === (lightOf(n)?.level ?? 'ok');
  const list = (q.data ?? []).filter((n) => byType(n, filter) && byLight(n, status));
  // Each dropdown option shows a node count, computed under the other dropdown's current choice (once picked, it is the count you see in the table)
  const typeOpt = (value: typeof filter, label: string) => ({ value, label: t('common.withCount', { label, n: (q.data ?? []).filter((n) => byType(n, value) && byLight(n, status)).length }) });
  const lightOpt = (value: typeof status, label: string) => ({ value, label: t('common.withCount', { label, n: (q.data ?? []).filter((n) => byType(n, filter) && byLight(n, value)).length }) });
  return (
    <section className="overflow-hidden rounded-lg border border-hair bg-surface">
      <div className="flex min-h-[54px] flex-wrap items-center gap-2 border-b border-hair px-4 py-2">
        <h2 className="flex items-center gap-1.5 text-sm font-semibold text-ink">
          {t('chain.nodesTitle')}
          <HelpTip title={t('chain.nodesTitle')}>
            {/* Sections: first who is listed, then each column explained, finally where the data comes from and what can't be seen */}
            <dl className="flex flex-col gap-2">
              <div>
                <dt className="font-medium text-ink">{t('chain.nodesHelp.who.term')}</dt>
                <dd>{t('chain.nodesHelp.who.text')}</dd>
              </div>
              {/* The light left of the name: the table's only judgement, one rule line each for red and orange, both matching alerting */}
              <div>
                <dt className="font-medium text-ink">{t('chain.dotHelp.term')}</dt>
                <dd>
                  <ul className="mt-0.5 flex flex-col gap-1">
                    {(['err', 'warn'] as const).map((lv) => (
                      <li key={lv} className="flex items-start gap-1.5">
                        <span aria-hidden="true" className="mt-[0.45em] size-1.5 shrink-0 rounded-full" style={{ background: DOT[lv] }} />
                        <span>{t(`chain.dotHelp.${lv}`, { lag: cmpText(th.lag) })}</span>
                      </li>
                    ))}
                    {/* With consensus halted, participation can't be seen: only unreachable validators are marked */}
                    <li className="pl-3">{t('chain.dotHelp.stalled')}</li>
                  </ul>
                </dd>
              </div>
              <div>
                <dt className="font-medium text-ink">{t('chain.dotHelp.idleTerm')}</dt>
                <dd>{t('chain.dotHelp.idle')}</dd>
              </div>
              {(['behind', 'missed', 'lastProposed'] as const).map((k) => (
                <div key={k}>
                  <dt className="font-medium text-ink">{t(`chain.nodesHelp.${k}.term`)}</dt>
                  <dd>{t(`chain.nodesHelp.${k}.text`)}</dd>
                </div>
              ))}
              <p className="border-t border-hair pt-2 text-ink-3">{t('chain.nodesHelp.source')}</p>
            </dl>
          </HelpTip>
        </h2>
        <label className="ml-auto flex items-center gap-2 text-xs text-ink-3">
          {t('chain.filterLabel')}
          <Listbox
            width="w-auto"
            aria-label={t('chain.filterLabel')}
            value={filter}
            options={[typeOpt('all', t('chain.filterAll')), typeOpt('validators', t('chain.filterValidators')), typeOpt('regular', t('chain.filterRegular'))]}
            onChange={setFilter}
            disabled={!rolesKnown}
          />
        </label>
        <label className="flex items-center gap-2 text-xs text-ink-3">
          {t('chain.statusLabel')}
          <Listbox
            width="w-auto"
            aria-label={t('chain.statusLabel')}
            value={status}
            options={[lightOpt('all', t('chain.filterAll')), lightOpt('err', t('chain.statusErr')), lightOpt('warn', t('chain.statusWarn')), lightOpt('ok', t('chain.statusOk'))]}
            onChange={setStatus}
          />
        </label>
        {/* Count follows the filter: picking validators shows how many validators there are */}
        {q.data && <CountChip unit={t('chain.countUnit')}>{list.length}</CountChip>}
      </div>
      {q.isLoading ? (
        <div className="p-4"><SkeletonRows rows={4} /></div>
      ) : q.isError ? (
        <ErrorState onRetry={() => void q.refetch()} />
      ) : !q.data?.length ? (
        <EmptyState message={t('chain.empty')} />
      ) : list.length === 0 ? (
        <EmptyState message={t('chain.emptyFilter')} />
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="table-head whitespace-nowrap">
                {/* Fixed-width name column: when rename swaps in an input (filling the column), columns on the right don't get pushed */}
                <th className="w-[14rem] pl-8 pr-4">{t('chain.colName')}</th>
                <th className="px-4">{t('chain.colAddress')}</th>
                <th className="px-4">{t('chain.colRole')}</th>
                <th className="px-4 text-center">{t('chain.colBehind')}</th>
                {/* The statistics range (last 1000 blocks) is in the ⓘ, the column name stays short. Numbers are centered: mostly 0, and centered lines up with the header */}
                <th className="px-4 text-center">{t('chain.colMissed')}</th>
                <th className="px-4">{t('chain.colLastProposed')}</th>
              </tr>
            </thead>
            <tbody>
              {list.map((n) => {
                const light = lightOf(n);
                // When the light worsens (fine -> attention -> severe) the name cell flashes once with the new level's light background
                const worse = worsened(n.address, light?.level === 'err' ? 2 : light?.level === 'warn' ? 1 : 0);
                return (
                <tr key={n.address} className="border-b border-hair transition-colors last:border-0 hover:bg-elevated">
                  <td className="relative isolate py-2.5 pl-8 pr-4">
                    <FlashBehind token={worse} tone={light?.level === 'err' ? 'error' : 'syncing'} />
                    {/* The light sits in the widened left padding: on the same left line as the card title with a little gap before the name, so the name doesn't shift and the column stays aligned. This is the only judgement; number columns only show numbers */}
                    {light && (
                      <span role="img" aria-label={light.why.join('、')} title={light.why.join('、')} className="absolute left-4 top-1/2 size-1.5 -translate-y-1/2 rounded-full" style={{ background: DOT[light.level] }} />
                    )}
                    {n.isOwn ? (
                      <span className="flex items-center gap-1.5">
                        {/* Own node: clicking the id goes to its detail page (where the note is edited too) */}
                        {n.nodeId ? (
                          <Link to={`/nodes/${encodeURIComponent(n.nodeId)}`} className="rounded-sm font-mono text-ink underline-offset-2 hover:underline focus:outline-none focus-visible:ring-1 focus-visible:ring-accent">{n.nodeId}</Link>
                        ) : (
                          <span className="font-mono text-ink">{n.name}</span>
                        )}
                        <Pill tone="accent">{t('chain.ownTag')}</Pill>
                      </span>
                    ) : editing === n.address ? (
                      <NameEditor
                        fill
                        value={n.name}
                        label={t('chain.rename')}
                        placeholder={t('chain.namePlaceholder')}
                        onDone={(saved) => { renamed(); if (saved) savedFlash.mark(n.address); }}
                        onSave={(name) => (name ? api.setPeerName(n.address, name) : api.deletePeerName(n.address))}
                      />
                    ) : (
                      <span className="flex items-center gap-1.5">
                        <span key={savedFlash.key(n.address)} className={`${n.name ? 'text-ink' : 'text-ink-faint'} ${savedFlash.cls(n.address)}`}>{n.name ?? t('chain.unnamed')}</span>
                        {/* Own nodes rename the platform's node name (same name as in "My nodes"); others get a peer name chosen by this org */}
                        {canName && (
                          <button type="button" onClick={() => setEditing(n.address)} aria-label={t('chain.rename')} title={t('chain.rename')} className="rounded p-0.5 text-ink-faint transition-colors hover:bg-elevated hover:text-ink">
                            <PencilSimple size={13} aria-hidden="true" />
                          </button>
                        )}
                      </span>
                    )}
                  </td>
                  <td className="whitespace-nowrap px-4 py-2.5 font-mono text-ink-2">
                    <span className="inline-flex items-center gap-1" title={n.address}>{fmtAddr(n.address)}<CopyButton value={n.address} variant="cell" /></span>
                  </td>
                  <td className="whitespace-nowrap px-4 py-2.5">
                    {/* Both grey: validators get a hint of blue, regular nodes plain grey, so the two tags stand apart */}
                    {!rolesKnown ? <span className="text-ink-3">—</span> : n.isValidator ? <ValidatorTag className="-ml-1.5" /> : <Pill className="-ml-1.5">{t('chain.regular')}</Pill>}
                  </td>
                  {/* Blocks behind: 0 is normal, faint text; otherwise show the number as is, and whether it hit the threshold is shown by the light left of the name */}
                  <td className="whitespace-nowrap px-4 py-2.5 text-center font-mono tabular-nums">
                    {n.blocksBehind == null ? <span className="text-ink-faint">—</span>
                      : n.blocksBehind === 0 ? <span className="text-ink-3">0</span>
                      : <span className="text-ink-2">{fmtInt(n.blocksBehind)}</span>}
                  </td>
                  {/* Two on-chain signals: how many times in the last 1000 blocks its turn timed out and moved on, and the last block it proposed on its turn. Both are facts derived from block extraData, not reported by the other machine.
                      Not "share of commits signed": QBFT blocks only record the batch of signatures that met the threshold, so healthy validators are often missing, the number reads low and can't show misses.
                      Missed turns of 0 is normal, faint text; misses show the count as is with no extra color (missing once or twice is fine; real problems show in the light left of the name) */}
                  <td className="whitespace-nowrap px-4 py-2.5 text-center font-mono tabular-nums">
                    {n.missedTurns1000 == null ? <span className="text-ink-faint">—</span>
                      : n.missedTurns1000 === 0 ? <span className="text-ink-3">0</span>
                      : <span className="text-ink-2">{fmtInt(n.missedTurns1000)}</span>}
                  </td>
                  <td className="whitespace-nowrap px-4 py-2.5 font-mono tabular-nums text-ink-2">
                    {n.lastProposed ? <>{fmtInt(n.lastProposed.height)}<span className="ml-2 font-sans text-ink-3">{ago(t, now - new Date(n.lastProposed.at).getTime())}</span></> : <span className="text-ink-faint">—</span>}
                  </td>
                </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

