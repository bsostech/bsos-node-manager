import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowLeft, CaretRight, Cpu, FileText, Graph, LinkSimple, PencilSimple, Warning, type Icon } from '@phosphor-icons/react';
import { api, HttpError } from '@/api/client';
import { isEnterprise } from '@/edition';
import type { NodeDetail as TNodeDetail, ProposerMetric, MetricId, MetricSeries, AlertList, ValidatorTurn } from '@/api/schemas';
import { NodeStatusBadge, AlertStatusBadge } from '@/components/Badges';
import { ValidatorTag } from '@/components/ValidatorTag';
import { HelpTip } from '@/components/HelpTip';
import { refreshMs } from '@/lib/refreshInterval';
import { Pill } from '@/components/Pill';
import { Band } from '@/components/NodeTxPoolPanel';
import { PoolIcon } from '@/components/icons/PoolIcon';
import { CopyButton } from '@/components/CopyButton';
import { GoLink } from '@/components/TextLinks';
import { NameEditor } from '@/components/NameEditor';
import { MetricPanel } from '@/components/MetricPanel';
import { RangeControl } from '@/components/RangeControl';
import { Skeleton, EmptyState, ErrorState } from '@/components/States';
import { useUrlParam } from '@/hooks/useUrlParam';
import { type RangeSel, selWindow } from '@/lib/range';
import { alertTone, toneVars, statusLabel } from '@/lib/status';
import { nodeIssue } from '@/lib/nodeIssue';
import { apiState } from '@/lib/capabilities';
import { fmtInt, fmtDateTime, shortVersion, fmtAddr, fmtDateTimeSec } from '@/lib/format';
import { useAuth } from '@/auth/AuthContext';
import { errMsg } from '@/lib/errors';
import { NodeLogsPanel, type LogSource } from '@/components/NodeLogsPanel';
import { LogLevelControl } from '@/components/LogLevelControl';
import { NodeTxPoolPanel } from '@/components/NodeTxPoolPanel';
import { NodePeersPanel, type PeerSource } from '@/components/NodePeersPanel';
import { type TrendMetric, type Deviation, nodeDeviations } from '@/lib/fleetOutliers';
import { alertText, alertTitle } from '@/lib/alertRules';
import { type Thresholds, useThresholds, thresholdLine, over, cmpText } from '@/lib/thresholds';
import { BackLink } from '@/components/TextLinks';
import { AnimatedNumber } from '@/components/AnimatedNumber';
import { useSavedFlash } from '@/hooks/useSavedFlash';
import { MonitoringDownNote } from '@/components/MonitoringDown';

/** Node alert banner.
 *  With no alerts (loading/empty) it takes no space, since not every node has something to look at; still warn when the monitoring source is down; a query failure gets an error banner.
 *  A thin banner right below the title, above the status row: more urgent than the steady-state sync status, but only about this node,
 *  so not a big block like the cross-page sticky info cards (quiet when normal, loud only when abnormal, and loud means area,
 *  not a whole card). One strip per alert, colored by severity (critical=red / warning=amber), same scale as AlertStatusBadge.
 *  Text matches body text (text-xs): it stands out by background, not by larger type. */
function NodeAlerts({ nodeId }: { nodeId: string }) {
  const { t } = useTranslation();
  const { data, isError, error } = useQuery<AlertList>({
    queryKey: ['nodeAlerts', nodeId],
    queryFn: () => api.getNodeAlerts(nodeId),
    retry: false,
    refetchInterval: 30_000,
  });
  // On query failure (network, platform) react-query still keeps stale data; don't present old alerts as current, show an error banner instead
  if (isError) {
    const tone = toneVars('syncing');
    return (
      <div className="flex items-center gap-3 rounded-lg border border-hair px-4 py-2.5 text-xs" style={{ background: tone.background, color: tone.color }}>
        <Warning size={14} weight="bold" aria-hidden />
        <span>{errMsg(error) || t('error.title')}</span>
      </div>
    );
  }
  const firing = (data?.alerts ?? []).filter((a) => a.status === 'firing');
  // Monitoring source down: warn even with no active alerts, otherwise this node looks perfectly fine
  const down = data?.monitoringAvailable === false;
  if (firing.length === 0 && !down) return null;
  return (
    <div className="flex flex-col gap-1.5">
      {down && <MonitoringDownNote />}
      {firing.map((a) => {
        const tone = toneVars(alertTone(a));
        return (
          <div
            key={a.fingerprint + a.startsAt}
            className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-hair px-4 py-2.5 text-xs"
            style={{ background: tone.background, color: tone.color }}
          >
            <Warning size={14} weight="bold" aria-hidden />
            {/* Default rules show the localized name; custom rules only have a summary */}
            {alertTitle(t, a.alertname) && <span className="font-medium">{alertTitle(t, a.alertname)}</span>}
            {/* The summary follows the UI language (default rules are built from the locale files, see alertText) */}
            <span className="text-ink-2">{alertTitle(t, a.alertname) ? '· ' : ''}{alertText(t, a).summary}</span>
            <span className="ml-auto flex items-center gap-2">
              <AlertStatusBadge alert={a} />
              <span className="text-xs text-ink-3">{fmtDateTime(a.startsAt)}</span>
            </span>
          </div>
        );
      })}
    </div>
  );
}

// Monitoring categories: chain / host / network / logs. The detail page is monitoring only; settings and troubleshooting live on the ops page.
type Cat = 'chain' | 'host' | 'net' | 'txpool' | 'logs';
// Chain height just keeps climbing and says nothing, so show blocks behind instead; percentages are fixed 0~100; network in/out share one chart.
type ChartDef = { metric: MetricId; labelKey: string; helpKey?: string; pair?: { metric: MetricId; labelKey: string; firstLabelKey: string }; yRange?: [number, number]; yMaxAtLeast?: number };
const PCT: [number, number] = [0, 100];
const CATS: { id: Cat; charts: ChartDef[] }[] = [
  // "Sync": only this node's own concerns (blocks behind, height trend); block time and TPS are chain-wide and live in chain health; the tx pool has its own tab
  { id: 'chain', charts: [
    { metric: 'blocks_behind', labelKey: 'detail.lagChart', yMaxAtLeast: 10 },
    { metric: 'chain_height', labelKey: 'detail.heightChart' },
  ] },
  { id: 'host', charts: [
    { metric: 'cpu', labelKey: 'detail.cpu', yRange: PCT },
    { metric: 'mem', labelKey: 'detail.memory', yRange: PCT },
    // Besu is a Java program, and its own memory budget (heap max) is separate from host memory: the host may look fine while Besu is nearly full
    { metric: 'jvm_heap' as MetricId, labelKey: 'detail.jvmHeap', helpKey: 'detail.jvmHeapHelp', yRange: PCT },
    { metric: 'disk', labelKey: 'detail.disk', yRange: PCT },
  ] },
  { id: 'net', charts: [
    { metric: 'peer_count', labelKey: 'detail.peers' },
    { metric: 'net_rx', labelKey: 'detail.netRxTx', pair: { metric: 'net_tx', labelKey: 'detail.netTx', firstLabelKey: 'detail.netRx' } },
  ] },
  { id: 'txpool', charts: [] },
  { id: 'logs', charts: [] },
];
const CAT_ICON: Record<Cat, Icon> = { chain: LinkSimple, host: Cpu, net: Graph, txpool: PoolIcon as unknown as Icon, logs: FileText }; // tx pool: a swimming pool icon (drawn by hand, phosphor has no above-ground pool)
const METRIC_CAT: Record<TrendMetric, Cat> = { lag: 'chain', txpool_pending: 'txpool', cpu: 'host', mem: 'host', jvm_heap: 'host', disk: 'host', peer_count: 'net' };

/** One-line "Needs attention": the deviating value + the threshold or this org's median. */
function deviationText(t: TFunction, d: Deviation, th: Thresholds): string {
  const v = Math.round(d.value), m = Math.round(d.median);
  // Above the alert threshold, state the threshold (the same number as in alert settings); otherwise say how far it is from the other nodes
  if (d.metric === 'disk' && over(d.value, th.disk)) return t('detail.devDiskThreshold', { v, th: th.disk.th });
  if (d.metric === 'cpu' && over(d.value, th.cpu)) return t('detail.devCpuThreshold', { v, th: th.cpu.th });
  if (d.metric === 'mem' && over(d.value, th.mem)) return t('detail.devMemThreshold', { v, th: th.mem.th });
  if (d.metric === 'jvm_heap') return t('detail.devJvmThreshold', { v, th: th.jvmHeap.th }); // threshold only, not compared with other nodes
  if (d.metric === 'peer_count' && over(d.value, th.peers)) return t('detail.devPeersThreshold', { v, th: th.peers.th });
  if (d.metric === 'lag') return t('detail.devLag', { v, th: th.lag.th });
  return t(`detail.dev_${d.metric}`, { v, m });
}

/** Gas used: number + share of this block's limit (one decimal, <0.1% when tiny); empty blocks a faint 0, missed ones "—" */
function GasCell({ turn }: { turn: ValidatorTurn }) {
  if (!turn.proposed || turn.gasUsed == null) return <>—</>;
  if (turn.gasUsed === 0) return <span className="text-ink-3">0</span>;
  const pct = turn.gasLimit ? (turn.gasUsed / turn.gasLimit) * 100 : null;
  return <>{fmtInt(turn.gasUsed)}{pct != null && <span className="ml-2 text-ink-3">{pct < 0.1 ? '<0.1' : pct.toFixed(1)}%</span>}</>;
}

/** Show "Nearly full" when the disk runs out within this many days */
const DISK_SOON_DAYS = 30;

/** Summary card: just label and big number. A deviating category gets a yellow border; disk over threshold turns the number red. */
function SummaryCards({ d, flagged, granted }: { d: TNodeDetail; flagged: Set<TrendMetric>; granted: (c: Cat) => boolean }) {
  const { t } = useTranslation();
  const th = useThresholds();
  const behind = d.ledger?.blocksBehind ?? null;
  // Sync is an ongoing state, not a one-off result. With no outbound peers Besu can't tell it is behind (the highest known block is its own), so check peers first
  const isolated = d.p2p?.peerCount === 0;
  // Ledger unreadable = node offline or RPC unreachable: not "not behind" but "unknown"
  const unknown = !d.ledger;
  const sync = unknown ? t('detail.syncUnknown') : isolated ? t('detail.isolated') : behind != null && behind > 0 ? t('detail.cardBehind', { n: fmtInt(behind) }) : d.p2p?.inSync === false ? t('detail.notSynced') : t('detail.synced');
  // Last proposal: validators only; find our own address in the signer metrics
  const me = d.validatorAddress?.toLowerCase();
  const last = d.consensus?.isValidator ? d.consensus.proposerMetrics?.find((m) => m.address.toLowerCase() === me)?.lastProposedBlockNumber : undefined;
  const disk = d.hardware?.diskUsedPercent;
  const daysLeft = d.hardware?.diskDaysLeft ?? null;
  const daysReason = d.hardware?.diskDaysLeftReason;
  // This node's on-chain consensus signals (same data and criteria as the on-chain node table): not participating normally, missed turns
  const chainNodes = useQuery({ queryKey: ['chain-nodes'], queryFn: api.listChainNodes, enabled: !!me && !!d.consensus?.isValidator });
  const mine = chainNodes.data?.find((n) => n.address.toLowerCase() === me);
  // Tag at the top right: only when something is wrong, with the same criteria as elsewhere (my nodes table, comparison chart, on-chain node table)
  type Tag = { tone: 'warn' | 'err'; text: string };
  type Card = { label: string; value: ReactNode; tag?: Tag | null; cat?: Cat; help?: ReactNode };
  const cards = ([
    { label: t('detail.cardSync'), value: sync,
      // Sections: one per state, a title + one sentence of criteria
      help: (
        <dl className="flex flex-col gap-1.5">
          {(['synced', 'behind', 'catching', 'isolated', 'unknown'] as const).map((k) => (
            <div key={k}><dt className="font-medium text-ink">{t(`detail.syncRule.${k}.term`)}</dt><dd>{t(`detail.syncRule.${k}.text`)}</dd></div>
          ))}
        </dl>
      ),
      tag: unknown ? { tone: 'err', text: d.status === 'rpc_unreachable' ? statusLabel(t, d) : t('status.offline') } : isolated ? { tone: 'err', text: t('detail.isolated') } : flagged.has('lag') ? { tone: 'warn', text: t('detail.tagBehind') } : null },
    // For numbers that keep changing (latest block, peer count, disk) only the changed digits slide in
    { label: t('detail.cardProposed'), value: last != null ? <AnimatedNumber value={`#${fmtInt(last)}`} /> : '—',
      tag: mine?.producing === false ? { tone: 'warn', text: t('chain.validatorIdle') } : mine?.missedTurns1000 ? { tone: 'warn', text: t('chain.missedTurns', { n: mine.missedTurns1000 }) } : null },
    { label: t('detail.peers'), value: d.p2p?.peerCount != null ? <><AnimatedNumber value={String(d.p2p.peerCount)} />{d.p2p.peerLimit != null && <span className="text-base text-ink-3"> / {d.p2p.peerLimit}</span>}</> : '—', cat: 'net',
      tag: flagged.has('peer_count') ? { tone: 'warn', text: t('detail.tagLow') } : null },
    // Disk card has two numbers: how much is used / how many days left at the last 7 days' growth rate (computed by the backend; not growing or over a year shows "Over a year", under 7 days of data shows "Not enough data", no backend data yet is "—").
    // The pill takes the more urgent one: almost full (under DISK_SOON_DAYS days) first, then high (over the alert threshold or far from other nodes)
    { label: t('detail.cardDisk'), cat: 'host', help: t('detail.diskCardHelp', { th: cmpText(th.disk, '%'), days: DISK_SOON_DAYS }),
      value: disk == null ? '—' : (
        <><AnimatedNumber value={`${Math.round(disk)}%`} /><span className="text-base text-ink-3"> / </span>
          {daysLeft != null ? <><AnimatedNumber value={fmtInt(Math.max(0, Math.floor(daysLeft)))} /><span className="text-base text-ink-3"> {t('detail.diskDaysUnit')}</span></>
            : daysReason ? <span className="text-lg">{t(daysReason === 'insufficient_data' ? 'detail.diskDaysInsufficient' : 'detail.diskDaysLong')}</span>
            : '—'}</>
      ),
      tag: daysLeft != null && daysLeft < DISK_SOON_DAYS ? { tone: 'warn', text: t('detail.tagSoonFull') } : flagged.has('disk') ? { tone: 'warn', text: t('detail.tagHigh') } : null },
  ] as Card[]).filter((c) => !c.cat || granted(c.cat)); // categories the other side didn't grant get no card, so "—" doesn't look like missing data
  return (
    // Five per row on wide screens. Long labels wrap before the full-width parenthesis in zh labels, values sit at the card bottom, so numbers in a row still line up
    // How many per row depends on the content area width (not the window): four only if four fit, otherwise two per row
    <div className="@container">
    <div className="grid grid-cols-2 gap-3 @3xl:grid-cols-4">
      {cards.map((c) => (
        <div key={c.label} className="flex min-w-0 flex-col gap-1.5 rounded-lg border border-hair bg-surface px-4 py-3.5">
          <span className="flex items-center gap-1.5 whitespace-nowrap text-xs text-ink-2">
            {c.label}
            {c.help && <HelpTip title={c.label}>{c.help}</HelpTip>}
            {c.tag && <Pill tone={c.tag.tone} className="ml-auto">{c.tag.text}</Pill>}
          </span>
          <span className="whitespace-nowrap font-mono text-2xl leading-tight tabular-nums text-ink">{c.value}</span>
        </div>
      ))}
    </div>
    </div>
  );
}

/**
 * This validator's own block count: how many of the last N blocks it proposed, compared with its fair share under even distribution.
 * QBFT rotates proposers, so healthy validators should be roughly equal; under 70% of the expected share counts as low (the flip side of "missed turns").
 * The distribution across all validators isn't here; see the on-chain node table in chain health.
 */
/** How many of its turns to list at a time */
const TURN_PAGE = 25;

function OwnProposals({ metrics, me }: { metrics: ProposerMetric[]; me: string }) {
  const { t } = useTranslation();
  // The window is the signer metrics' fixed range: normally 1000 blocks, and Besu's default 100 for a freshly joined node's first round. Show a whole-number window, not a summed-up fraction.
  // When the chain is shorter than the window, the range is the current block count; the expected count uses the actual blocks, or every validator on a new chain would be judged low
  const total = metrics.reduce((a, m) => a + m.proposedBlockCount, 0);
  const windowSize = Math.min(total, total <= 100 ? 100 : 1000);
  const mine = metrics.find((m) => m.address.toLowerCase() === me)?.proposedBlockCount ?? 0;
  const expected = metrics.length ? windowSize / metrics.length : 0;
  const low = expected > 0 && mine < expected * 0.7;
  // Each recent turn: proposed (green) or missed (orange), hover for the block number; the latest few proposed blocks are listed below
  const set = useQuery({ queryKey: ['validators'], queryFn: () => api.listValidatorSet(), refetchInterval: refreshMs });
  const turns = set.data?.find((v) => v.address.toLowerCase() === me)?.turns ?? [];
  const [shown, setShown] = useState(TURN_PAGE);
  const missed = turns.filter((x) => !x.proposed).length;
  // No "proposed late" judgement: block time is whole seconds, and lateness may come from the previous block arriving late (others signing slowly, slow network), not necessarily itself; truly slow enough to time out is a missed turn
  return (
    <div className="flex flex-col gap-3 rounded-lg border border-hair bg-surface px-4 py-3 text-xs sm:col-span-2">
      {/* Four cells read at a glance (same layout as the tx pool summary); the range is stated once above, cell labels stay short */}
      <span className="text-ink-3">{t('detail.ownWindow', { window: fmtInt(windowSize) })}</span>
      <Band cells={[
        { label: t('detail.ownProducedLabel'), value: fmtInt(mine), tag: low ? { tone: 'warn', text: t('detail.tagFew') } : null },
        { label: t('detail.ownExpectedLabel'), value: `≈ ${fmtInt(Math.round(expected))}`, help: t('detail.ownExpectedHelp', { window: fmtInt(windowSize), v: metrics.length }) },
        { label: t('detail.ownMissedLabel'), value: turns.length ? `${fmtInt(missed)} / ${fmtInt(turns.length)}` : '—', help: t('detail.ownMissedHelp'), tag: missed > 0 ? { tone: 'warn', text: t('chain.missedTurns', { n: missed }) } : null },
      ]} />
      {turns.length > 0 && (
        <>
          {/* No scrollbar: list 25 first, with "Show 25 more" below */}
          <div className="overflow-hidden rounded-md border border-hair">
            <table className="w-full text-xs">
              <thead>
                <tr className="table-head">
                  {/* Block and result first: every row has them; proposal time only exists when it proposed, so it goes last */}
                  <th className="px-3">{t('detail.turnBlock')}</th>
                  <th className="px-3">
                    <span className="inline-flex items-center gap-1.5">{t('detail.turnResult')}
                      {/* One row is one turn; the result is just proposed or missed */}
                      <HelpTip title={t('detail.turnResult')}>
                        <div className="flex flex-col gap-2">
                          <p>{t('detail.turnRule.intro')}</p>
                          <dl className="flex flex-col gap-1.5">
                            {(['ontime', 'missed'] as const).map((k) => (
                              <div key={k}><dt className="font-medium text-ink">{t(`detail.turnRule.${k}.term`)}</dt><dd>{t(`detail.turnRule.${k}.text`)}</dd></div>
                            ))}
                          </dl>
                        </div>
                      </HelpTip>
                    </span>
                  </th>
                  {/* Tx count sits left of the proposal time, right-aligned: only its own blocks have it, plain uncolored number (empty blocks are normal without traffic) */}
                  <th className="px-3 text-right">{t('detail.turnTxs')}</th>
                  {/* gas: usage followed by its share of the limit; every block has a limit and validators may differ, explained in the ⓘ */}
                  <th className="px-3 text-right"><span className="inline-flex items-center gap-1.5">{t('detail.turnGas')}<HelpTip title={t('detail.turnGas')}>{t('detail.turnGasHelp')}</HelpTip></span></th>
                  {/* Fixed-width proposal-time column (a bit wider than content, with a little space on the right): spare width goes to the earlier columns, so the time sits near the table's right edge without a big gap in the column */}
                  <th className="w-[13.5rem] whitespace-nowrap pl-10 pr-3"><span className="inline-flex items-center gap-1.5">{t('detail.turnTime')}<HelpTip title={t('detail.turnTime')}>{t('detail.turnTimeHelp')}</HelpTip></span></th>
                </tr>
              </thead>
              <tbody>
                {[...turns].reverse().slice(0, shown).map((x) => (
                  <tr key={x.block} className="border-b border-hair last:border-0">
                    <td className="px-3 py-1.5 font-mono tabular-nums text-ink-2">#{fmtInt(x.block)}</td>
                    <td className="px-3 py-1.5">{/* -ml-1.5 cancels the pill padding so text aligns with the header. Two kinds: proposed / missed */}{!x.proposed ? <Pill tone="warn" className="-ml-1.5">{t('detail.turnMissed')}</Pill> : <Pill tone="ok" className="-ml-1.5">{t('detail.turnProposed')}</Pill>}</td>
                    <td className="px-3 py-1.5 text-right font-mono tabular-nums text-ink-2">{x.proposed && x.txCount != null ? fmtInt(x.txCount) : '—'}</td>
                    <td className="whitespace-nowrap px-3 py-1.5 text-right font-mono tabular-nums text-ink-2"><GasCell turn={x} /></td>
                    <td className="whitespace-nowrap py-1.5 pl-10 pr-3 font-mono tabular-nums text-ink-2">{/* A missed turn's block was proposed by someone else, so don't show their time */}{x.proposed && x.at ? fmtDateTimeSec(x.at) : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {turns.length > shown && (
            <button type="button" onClick={() => setShown((n) => n + TURN_PAGE)} className="self-center rounded px-2 py-1 text-xs text-ink-3 transition-colors hover:bg-elevated hover:text-ink focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent">
              {t('detail.turnMore', { n: Math.min(TURN_PAGE, turns.length - shown), left: turns.length - shown })}
            </button>
          )}
        </>
      )}
    </div>
  );
}

/** Height of the zoomed chart: fills from the chart's top to the window bottom (leaving room for the hint row and padding), min 320; don't draw before measuring, to avoid building the chart twice. */
function FillHeight({ children }: { children: (height: number) => ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const [h, setH] = useState<number | null>(null);
  useLayoutEffect(() => {
    const fit = () => {
      const el = ref.current;
      if (!el) return;
      // main may not have scrolled back to the top yet, so add scrollTop back to get the position after scrolling.
      // 100 = panel padding and title row about 60 + main bottom padding about 28 + slack
      const top = el.getBoundingClientRect().top + (document.querySelector('main')?.scrollTop ?? 0);
      setH(Math.max(320, Math.floor(window.innerHeight - top - 100)));
    };
    fit();
    window.addEventListener('resize', fit);
    return () => window.removeEventListener('resize', fit);
  }, []);
  return <div ref={ref}>{h != null && children(h)}</div>;
}

export function NodeDetail() {
  const { id = '' } = useParams();
  return <NodeDetailView queryKey={['node', id]} seriesKey={id} fetchNode={() => api.getNode(id)} own />;
}

/**
 * own: things only this org's nodes have: log level, admin entry points, local alerts, static nodes, tx pool.
 * Data for external org nodes never includes these (the other side's allowlist); this only decides whether to draw the entry points.
 * scopes: only for external org nodes, the tabs the other side's key grants (beyond chain); host, network and logs aren't drawn unless granted.
 */
export function NodeDetailView({ queryKey, seriesKey, fetchNode, fetchSeries, peers, logs, scopes, own }: {
  queryKey: unknown[];
  seriesKey: string;
  fetchNode: () => Promise<TNodeDetail>;
  fetchSeries?: (metric: MetricId, sel: RangeSel) => Promise<MetricSeries>;
  /** peer list source for external org nodes; not given for own nodes, the card reads it itself */
  peers?: PeerSource;
  /** log source for external org nodes */
  logs?: LogSource;
  scopes?: string[];
  own: boolean;
}) {
  const { t } = useTranslation();
  const { can } = useAuth();
  const th = useThresholds();
  const qc = useQueryClient();
  // The note can only be edited on own nodes, with operator permission
  const canNote = own && can('nodes:onboard');
  const [noteEditing, setNoteEditing] = useState(false);
  const savedFlash = useSavedFlash();
  const noteDone = () => { setNoteEditing(false); for (const k of [queryKey, ['nodes'], ['chain-nodes']]) void qc.invalidateQueries({ queryKey: k }); };
  const canOnboard = own && can('nodes:onboard');

  // The relative range goes into the URL (kept across reload / share / back); zoom and live are transient and stay in state.
  // Custom durations are allowed (3h etc.), so validate the format, not an enum; bad values fall back to the default
  const [rel, setRel] = useUrlParam<string>('r', '1h', (v) => /^\d+[mhd]$/.test(v));
  const [abs, setAbs] = useState<{ from: number; to: number } | null>(null);   // absolute window from drag-zoom

  const sel: RangeSel = abs ? { kind: 'abs', from: abs.from, to: abs.to } : { kind: 'rel', dur: rel };
  const autoRefresh = !abs; // Only an absolute window (drag-zoom) freezes; otherwise follow the header refresh rate, no separate live button
  // Current actual range (absolute seconds); in live mode it is recomputed with the data query's 10s polling, so the toolbar always shows which span is being viewed.
  const win = selWindow(sel);

  const { data, isLoading, isError, error, refetch } = useQuery<TNodeDetail, HttpError>({
    queryKey,
    queryFn: fetchNode,
    retry: false,
    refetchInterval: refreshMs,
  });

  // This org's node list: "Needs attention" compares this node with the others (same thresholds as the overview trends). External org nodes aren't compared.
  const fleet = useQuery({ queryKey: ['nodes'], queryFn: () => api.listNodes(), enabled: own, refetchInterval: 15_000 });
  // This node's own values come from the detail (same as the summary cards), other nodes from the list; otherwise "Needs attention" and the cards may differ by a few blocks
  const devs = own && data && fleet.data ? nodeDeviations(data.id, fleet.data.nodes.map((n) => (n.id === data.id ? {
    ...n, blocksBehind: data.ledger?.blocksBehind, peerCount: data.p2p?.peerCount, txpoolPending: data.ledger?.txpoolPending,
    cpuPercent: data.hardware?.cpuPercent, diskUsedPercent: data.hardware?.diskUsedPercent,
  } : n)), th) : [];
  // Why the platform can't get data (same as the "Details" column in the node list); the cross-org endpoint has no capability list, so only judged for own nodes
  const issue = own && data ? nodeIssue(data, t) : null;
  // Logs: viewers can see them too; writes such as changing the log level stay operator-only
  const canSeeLogs = can('monitor:view') && (own || !!logs);
  const granted = (c: Cat) => own || c === 'chain' || (scopes ?? []).includes(c);
  // The tx pool is only for own nodes (external orgs have no such data); every role can view it
  const cats = CATS.filter((c) => granted(c.id) && (c.id !== 'logs' || canSeeLogs));
  // Tab goes into the URL; when unspecified always land on the first tab (sync), never jumping to a deviating category for the user: every node opens in the same place,
  // deviations show in "Needs attention" above and switch tabs only when clicked
  const [rawView, setView] = useUrlParam<string>('v', '', (v) => cats.some((c) => c.id === v));
  const tabsRef = useRef<HTMLDivElement>(null);
  const view: Cat = (rawView as Cat) || 'chain';
  // Full-page zoom (same as the overview's cross-node trends): ?f=<metric> or proposer; it is in the URL so it can be shared and survives reload
  const charts = CATS.find((c) => c.id === view)?.charts ?? [];
  // Logs can be zoomed too (?f=logs): same full-page zoom as the line charts, Esc to return
  const [rawFocus, setFocus] = useUrlParam<string>('f', '', (v) => charts.some((c) => c.metric === v) || (view === 'logs' && v === 'logs'));
  const focus = rawFocus && view !== 'txpool' && (view !== 'logs' || rawFocus === 'logs') ? rawFocus : '';
  const focusLogs = focus === 'logs';
  const focusDef = charts.find((c) => c.metric === focus);
  const lastFocus = useRef<string>('');
  const backRef = useRef<HTMLButtonElement>(null);
  const focusBack = useRef(false);
  const openFocus = (m: string) => { focusBack.current = true; setFocus(m); };
  useEffect(() => {
    if (!focus) {
      // Leaving zoom: return focus to that chart's zoom button
      if (lastFocus.current) document.querySelector<HTMLElement>(`[data-expand="${lastFocus.current}"]`)?.focus();
      lastFocus.current = '';
      return;
    }
    lastFocus.current = focus;
    document.querySelector('main')?.scrollTo(0, 0);
    // Coming from the zoom button, it unmounts, so move focus to the breadcrumb's back button; don't steal focus when opening a shared link directly
    if (focusBack.current) backRef.current?.focus();
    focusBack.current = false;
    // The range menu calls preventDefault on Esc; that press closes the menu, it doesn't leave zoom
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape' && !e.defaultPrevented) setFocus(''); };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [focus]); // eslint-disable-line react-hooks/exhaustive-deps
  const syncKey = `node-${seriesKey}`;
  const pickRel = (dur: string) => { setRel(dur); setAbs(null); };          // pick range -> clear zoom
  const onZoom = (min: number, max: number) => setAbs({ from: min, to: max }); // drag -> refetch absolute window
  const onResetZoom = () => setAbs(null);                                    // double-click -> back to relative
  // Toolbar: time range (preset / custom / zoom window) + live; shared by the time-series tabs and the tx pool tab
  const rangeToolbar = (
    // Same control as elsewhere: a "Range" label with a dropdown, right-aligned
    <div className="flex flex-wrap items-center justify-end gap-3">
      <RangeControl variant="dropdown" rel={rel} abs={abs} window={win} onPickRel={pickRel} onSetAbs={(f, tt) => setAbs({ from: f, to: tt })} onClearAbs={() => setAbs(null)} />
    </div>
  );

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-4">
      <BackLink to="/nodes">{t('nav.overview')}</BackLink>

      {isLoading ? (
        <div className="flex flex-col gap-4">
          <Skeleton className="h-10 w-64" />
          <div className="grid grid-cols-2 gap-3">{Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-40" />)}</div>
        </div>
      ) : isError ? (
        // 404: the node doesn't exist (or isn't ours); retrying won't help, so no retry button
        error?.status === 404 ? <EmptyState message={t('detail.nodeNotFound')} /> : <ErrorState onRetry={() => void refetch()} />
      ) : data ? (
        <>
          {/* Title row: id; subtitle org, validator tag (none for full nodes), version. Settings (log level, identity, peers) live on the node settings page, reached from node registration & settings */}
          <header className="flex flex-wrap items-center gap-3">
            <div className="flex flex-col gap-1.5">
              <h1 className="text-lg font-semibold text-ink"><span className="font-mono">{data.id}</span></h1>
              {/* Note: a line for yourself, editable right here; with no note and permission, show "Add a note" */}
              {noteEditing ? (
                <NameEditor value={data.name} label={t('overview.editNote')} placeholder={t('overview.notePlaceholder')} onSave={(v) => api.setNodeName(data.id, { name: v })} onDone={(saved) => { noteDone(); if (saved) savedFlash.mark('note'); }} />
              ) : (data.name || canNote) && (
                <span className="inline-flex items-center gap-1.5 text-xs text-ink-3">
                  {data.name ? <span key={savedFlash.key('note')} className={savedFlash.cls('note')}>{data.name}</span> : <span className="text-ink-faint">{t('overview.addNote')}</span>}
                  {canNote && (
                    <button type="button" onClick={() => setNoteEditing(true)} aria-label={t('overview.editNote')} title={t('overview.editNote')} className="rounded p-0.5 text-ink-faint transition-colors hover:bg-elevated hover:text-ink">
                      <PencilSimple size={13} aria-hidden="true" />
                    </button>
                  )}
                </span>
              )}
              <span className="inline-flex flex-wrap items-center gap-3 text-xs text-ink-3">
                {isEnterprise && <span className="font-mono">{data.org}</span>}
                {data.consensus?.isValidator && <ValidatorTag />}
                {/* Node address: its on-chain identity, matching the on-chain node list and validator governance; copyable */}
                {data.validatorAddress && (
                  <span className="inline-flex items-center gap-1 font-mono" title={data.validatorAddress}>
                    {fmtAddr(data.validatorAddress)}<CopyButton value={data.validatorAddress} variant="cell" />
                  </span>
                )}
                {data.clientVersion && <span>Besu <span className="font-mono">{shortVersion(data.clientVersion)}</span></span>}
              </span>
            </div>
            <div className="ml-auto flex flex-wrap items-center gap-3">
              <NodeStatusBadge node={data} />
            </div>
          </header>

          {/* Node status hints and alerts are one group: kept close (gap-1.5, same as between multiple alerts) and spaced from the blocks below; with none the group takes no space (empty:hidden) */}
          {!focus && (
            <div className="flex flex-col gap-1.5 empty:hidden">
              {issue && (
                <div className="flex items-center gap-3 rounded-lg border border-hair px-4 py-2.5 text-xs font-medium" style={toneVars(issue.tone)}>
                  <Warning size={14} weight="bold" aria-hidden />
                  {issue.text}
                </div>
              )}
              {own && <NodeAlerts nodeId={data.id} />}
            </div>
          )}

          {focus ? (
            // Zoomed: the page keeps only the title row, breadcrumb (click the category name to return), time range and this chart
            <>
              <div className="flex flex-wrap items-center gap-3 border-b border-hair pb-3">
                <h2 className="inline-flex min-w-0 items-center gap-1.5 text-sm">
                  <button ref={backRef} type="button" onClick={() => setFocus('')} className="inline-flex items-center gap-1.5 rounded font-semibold text-ink-2 hover:text-ink focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent">
                    <ArrowLeft size={16} aria-hidden />{t(`detail.cat_${view}`)}
                  </button>
                  <span className="text-ink-faint" aria-hidden>/</span>
                  <span className="truncate font-semibold text-ink">{focusDef ? t(focusDef.labelKey) : focusLogs ? t('detail.logsFull') : ''}</span>
                </h2>
                {focusDef && (
                  <span className="ml-auto flex flex-wrap items-center gap-3">
                    <RangeControl variant="dropdown" rel={rel} abs={abs} window={win} onPickRel={pickRel} onSetAbs={(f, tt) => setAbs({ from: f, to: tt })} onClearAbs={() => setAbs(null)} />
                  </span>
                )}
              </div>
              <FillHeight>
                {(h) => focusLogs ? (
                  <NodeLogsPanel key={data.id} nodeId={data.id} source={logs} height={Math.max(h + 72, window.innerHeight - (document.querySelector('main')?.getBoundingClientRect().top ?? 0) - 44)} scrollToTop />
                ) : focusDef ? (
                  <MetricPanel
                    seriesKey={seriesKey}
                    fetchSeries={fetchSeries}
                    metric={focusDef.metric}
                    label={t(focusDef.labelKey)}
                    pair={focusDef.pair && { metric: focusDef.pair.metric, label: t(focusDef.pair.labelKey), firstLabel: t(focusDef.pair.firstLabelKey) }}
                    yRange={focusDef.yRange}
                    yMaxAtLeast={focusDef.yMaxAtLeast}
                    thresholds={thresholdLine(focusDef.metric, th)}
                    help={focusDef.helpKey && t(focusDef.helpKey, { th: cmpText(th.jvmHeap, '%') })}
                    sel={sel}
                    syncKey={syncKey}
                    autoRefresh={autoRefresh}
                    height={h}
                    onZoom={onZoom}
                    onResetZoom={onResetZoom}
                  />
                ) : null}
              </FillHeight>
            </>
          ) : (
          <>
          {/* Needs attention: only when something deviates, one item per row, clicking switches to the matching tab */}
          {devs.length > 0 && (
            <div className="flex flex-col gap-1.5">
              {devs.map((d) => (
                <button
                  key={d.metric}
                  type="button"
                  // Scroll the tab area into view while switching: the tabs are below "Needs attention" and the summary cards, so switching alone shows no visible change
                  onClick={() => { setView(METRIC_CAT[d.metric]); requestAnimationFrame(() => tabsRef.current?.scrollIntoView({ block: 'start' })); }}
                  className="flex items-center gap-3 rounded-lg border border-hair bg-surface px-4 py-2.5 text-left text-sm text-ink transition-colors hover:bg-elevated focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent"
                >
                  {/* Left: category pill (orange, small icon) says which tab this belongs to; right: "Open" hints the whole row is clickable and switches to that tab */}
                  <Pill tone="warn" className="gap-1">
                    {(() => { const I = CAT_ICON[METRIC_CAT[d.metric]]; return <I size={16} weight="bold" aria-hidden="true" />; })()}
                    {t(`detail.cat_${METRIC_CAT[d.metric]}`)}
                  </Pill>
                  <span className="text-xs text-ink">{deviationText(t, d, th)}</span>
                  <span className="ml-auto inline-flex items-center gap-0.5 text-xs text-ink-3">{t('detail.goTab')}<CaretRight size={12} aria-hidden="true" /></span>
                </button>
              ))}
            </div>
          )}

          <SummaryCards d={data} flagged={new Set(devs.map((x) => x.metric))} granted={granted} />

          <div ref={tabsRef} className="flex scroll-mt-4 gap-1 border-b border-hair">
            {cats.map((c) => {
              const active = view === c.id;
              return (
                <button
                  key={c.id}
                  type="button"
                  aria-current={active ? 'true' : undefined}
                  onClick={() => setView(c.id)}
                  className={`-mb-px border-b-2 px-3 py-2 text-sm transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent ${
                    active ? 'border-accent font-medium text-accent' : 'border-transparent text-ink-2 hover:text-ink'
                  }`}
                >
                  {t(`detail.cat_${c.id}`)}
                </button>
              );
            })}
          </div>

          {view === 'txpool' && own ? (
            // Tx pool: its summary cards sit above the toolbar, so the panel places the toolbar; the time range state is still owned by this page
            <NodeTxPoolPanel key={data.id} nodeId={data.id} txpoolOff={apiState(data.capabilities, 'TXPOOL') === 'off'} sel={sel} syncKey={syncKey} autoRefresh={autoRefresh} onZoom={onZoom} onResetZoom={onResetZoom} toolbar={rangeToolbar} />
          ) : view === 'logs' && canSeeLogs ? (
            // key: start over on node change, don't carry the previous node's logs and cursor over.
            <>
              {/* Log level only for own nodes (the platform can't command external org nodes) */}
              {canOnboard && <LogLevelControl node={data} />}
              <NodeLogsPanel key={data.id} nodeId={data.id} source={logs} onExpand={() => openFocus('logs')} />
            </>
          ) : (
          <>
          {rangeToolbar}

          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {charts.map((c) => (
              <MetricPanel
                key={c.metric}
                seriesKey={seriesKey}
                fetchSeries={fetchSeries}
                metric={c.metric}
                label={t(c.labelKey)}
                pair={c.pair && { metric: c.pair.metric, label: t(c.pair.labelKey), firstLabel: t(c.pair.firstLabelKey) }}
                yRange={c.yRange}
                yMaxAtLeast={c.yMaxAtLeast}
                thresholds={thresholdLine(c.metric, th)}
                help={c.helpKey && t(c.helpKey, { th: cmpText(th.jvmHeap, '%') })}
                sel={sel}
                syncKey={syncKey}
                autoRefresh={autoRefresh}
                onZoom={onZoom}
                onResetZoom={onResetZoom}
                onExpand={() => openFocus(c.metric)}
              />
            ))}
            {/* Validators: only how many blocks this node proposed versus its expected share */}
            {view === 'chain' && data.consensus?.isValidator && !!data.consensus.proposerMetrics?.length && data.validatorAddress && (
              <OwnProposals metrics={data.consensus.proposerMetrics} me={data.validatorAddress.toLowerCase()} />
            )}
            {/* Chain-wide metrics aren't here, give one line pointing there; it is supplementary, so it goes last in this tab */}
            {view === 'chain' && (
              <p className="text-xs text-ink-3 sm:col-span-2">
                {t('detail.chainElsewhere')} <GoLink to="/chain">{t('nav.chain')}</GoLink>
              </p>
            )}
          </div>
          {view === 'net' && (own || peers) && <NodePeersPanel key={data.id} nodeId={data.id} canOnboard={canOnboard} source={peers} />}
          </>
          )}
          </>
          )}
        </>
      ) : null}

    </div>
  );
}
