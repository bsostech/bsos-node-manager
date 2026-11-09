import { useState } from 'react';
import { Database } from '@phosphor-icons/react';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import type { ChainInfo } from '@/api/schemas';
import type { HourState } from '@/api/schemas';
import { api } from '@/api/client';
import { fmtAddr, fmtInt, fmtDateTimeSec } from '@/lib/format';
import { GoLink } from '@/components/TextLinks';
import { HelpTip } from '@/components/HelpTip';
import { Pill } from '@/components/Pill';
import { refreshMs } from '@/lib/refreshInterval';
import { Listbox } from '@/components/Listbox';
import { isOwnValidator, isUnreachable } from '@/lib/validators';
import { useThresholds } from '@/lib/thresholds';
import { blockAge, chainSpeed } from '@/lib/blockSpeed';
import { FlashBehind } from '@/components/FlashBehind';
import { useWorsened } from '@/hooks/useWorsened';
import { AnimatedNumber } from '@/components/AnimatedNumber';

/**
 * First layer of the overview: at first glance it must answer "is the chain still moving" and "is there anything I need to handle now".
 * - ChainStatus: one sentence (normal / stopped producing / not readable yet), with a line below for height, consensus and chain ID.
 */

const median = (vs: number[]) => {
  const s = [...vs].sort((a, b) => a - b);
  const m = s.length >> 1;
  return s.length === 0 ? 0 : s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
};

// Must stay readable when stopped for a long time: seconds under 2 minutes, minutes under 2 hours, hours under 2 days, days beyond that
const secs = (v: number) => (v < 120 ? `${Math.round(v)}s` : v < 7200 ? `${Math.round(v / 60)}m` : v < 172_800 ? `${Math.round(v / 3600)}h` : `${Math.round(v / 86_400)}d`);
/** Block time: one decimal place (a tool should give the number, not say "about"). */
const blockSecs = (v: number) => v.toFixed(1);

type Level = 'ok' | 'warn' | 'err' | 'none';
type TpsWin = '1m' | '10m' | '1h';

/**
 * Main cell, three lines: small label, large black figure + level pill, one supporting sentence. Color only on the pill; a little space between the lines so they do not clump.
 * Kept outside ChainStatus: ChainStatus re-renders on every data update, so a component defined inside it would be new each time,
 * remount entirely, and close any open ⓘ.
 */
function Primary({ label, help, x }: { label: string; help?: React.ReactNode; x: { level: Level; title: string; sub: string } }) {
  const { t } = useTranslation();
  // When it worsens (normal -> warning -> critical) the cell flashes once with the new level's light background; unknown (none) does not count
  const worse = useWorsened(x.level === 'none' ? null : x.level === 'err' ? 2 : x.level === 'warn' ? 1 : 0);
  return (
    <div className="relative isolate flex flex-col gap-1.5 px-4 py-4">
      <FlashBehind token={worse} tone={x.level === 'err' ? 'error' : x.level === 'warn' ? 'syncing' : undefined} />
      <span className="flex items-center gap-1.5 text-xs text-ink-3">{label}{help && <HelpTip title={label}>{help}</HelpTip>}</span>
      <span className="flex items-center gap-2">
        <span className="text-xl font-semibold leading-tight text-ink">{x.title}</span>
        {x.level !== 'none' && <Pill tone={x.level}>{t(`overview.level.${x.level}`)}</Pill>}
      </span>
      {x.sub && <span className="text-xs text-ink-3">{x.sub}</span>}
    </div>
  );
}

/**
 * Overall chain health card. An operator's questions about the whole chain, by urgency:
 * 1. Is the chain moving right now? -> the "Block production" main cell (producing / slow / stopped / unreadable)
 * 2. Can consensus hold? -> the "Consensus participation" main cell (how many participate, how many more may go offline); warning signs always show here before the chain stops
 * 3. Is there anything I need to act on? -> the "Needs attention" list, one item per line with a destination, shown only when non-empty
 * 4–6. Context: block height, transaction volume, stability over the last 24 hours -> three small cells
 * Votes are not here (governance lives in "Validator admission votes"); this page is only about health.
 */
export function ChainStatus({ chain }: { chain?: ChainInfo }) {
  const th = useThresholds();
  const { t } = useTranslation();
  const validators = useQuery({ queryKey: ['validators'], queryFn: () => api.listValidatorSet(), refetchInterval: refreshMs }).data;
  const nodes = useQuery({ queryKey: ['nodes'], queryFn: () => api.listNodes(), select: (r) => r.nodes, refetchInterval: refreshMs }).data;
  // Transaction volume: transactions per second on chain. Every node sees the same chain-wide TPS, so the tps series of the read-source node is the chain's.
  // The averaging window is chosen by the user (how busy it is depends on the business scenario), and it is not used as a warning
  const src = chain?.sourceNodeId ?? null;
  const [tpsWin, setTpsWin] = useState<TpsWin>('10m');
  const tpsQ = useQuery({ queryKey: ['series', src, 'tps', tpsWin], queryFn: () => api.getNodeSeries(src!, 'tps', { kind: 'rel', dur: tpsWin }), enabled: !!src, refetchInterval: 15_000 });
  const tpsPts = (tpsQ.data?.points ?? []).map((p) => p.v).filter((v): v is number => v != null);
  const tpsAvg = tpsPts.length ? tpsPts.reduce((a, b) => a + b, 0) / tpsPts.length : null;

  const age = blockAge(chain);
  const med = median(chain?.blockIntervals ?? []);
  const known = !!chain?.sourceNodeId && chain.height != null;
  const period = chain?.blockPeriodSeconds ?? null;
  const idle = chain?.emptyBlockPeriodSeconds != null && period != null && chain.emptyBlockPeriodSeconds > period ? chain.emptyBlockPeriodSeconds : null;
  // Normal block time: the platform says "how many seconds per block it should be now" based on whether the txpool has transactions (empty-block interval when idle); same number as the ChainSlow alert
  const base = chain?.expectedBlockPeriodSeconds ?? null;
  const speed = chainSpeed(chain, th);
  const ratio = age != null && base ? Math.round(age / base) : null;
  const consensus = chain?.consensus ? chain.consensus.toUpperCase().replace('IBFT2', 'IBFT 2.0') : null;

  // Consensus: non-participating count d against fault tolerance f
  const n = chain?.validatorCount ?? 0;
  const quorum = chain?.blockQuorum ?? 0;
  const vReady = !!validators && n > 0 && quorum > 0;
  const down = vReady ? validators!.filter((v) => v.producing === false) : [];
  const on = vReady ? n - down.length : 0;
  const f = n - quorum;
  const spare = f - down.length;
  // Halted = the block cell's "production severely slow or stopped" (chainSpeed's 'err'; the on-chain nodes page's indicator reads the same value).
  // When halted count 0 participants: nobody is producing, and turn records froze before the halt, so going by them would say "OK" and "N more may go offline".
  // Exceeding fault tolerance counts as insufficient validators only if the chain has actually stopped (same as the QuorumLost alert); while blocks are still produced it is yellow
  const halted = speed === 'err';
  const consLevel: Level = !vReady ? 'none' : halted ? 'err' : down.length === 0 ? 'ok' : 'warn';

  // Main cell 1: block production speed
  const eachText = base ? blockSecs(base) : '—';
  const blocks: { level: Level; title: string; sub: string } = !known
    ? { level: 'none', title: t('overview.chipUnknown'), sub: t('overview.chainUnknownHint') }
    : speed === 'err' || speed === 'warn'
      ? { level: speed, title: t(speed === 'err' ? 'overview.chipSevere' : 'overview.chipSlow'), sub: speed === 'err' ? t('overview.blocksSubAge', { s: secs(age ?? 0) }) : t('overview.blocksSubRatio', { s: secs(age ?? 0), r: ratio ?? '—' }) }
      : { level: 'ok', title: t('overview.chipProducing'), sub: period ? t(idle ? 'overview.blocksSubIdle' : 'overview.blocksSub', { each: med ? blockSecs(med) : '—', period, idle }) : t('overview.blocksSubNoPeriod', { each: med ? blockSecs(med) : '—' }) };
  // Main cell 2: consensus participation
  const cons: { level: Level; title: string; sub: string } = !vReady
    ? { level: 'none', title: t('overview.consUnknown'), sub: known ? t('overview.consUnknownHint') : '' }
    : halted
      ? { level: 'err', title: t('overview.consTitle', { on: 0, n }), sub: t('overview.consSubBelow', { q: quorum }) }
      : { level: consLevel, title: t('overview.consTitle', { on, n }), sub: spare <= 0 ? t('overview.consSubZero') : t('overview.consSubSpare', { spare }) };

  // Needs attention: at most one item per dimension; if both hold, show both
  const describe = (v: { address: string; orgId?: string | null; nodeId?: string | null; name?: string | null }) => {
    const base = v.name ?? v.nodeId ?? fmtAddr(v.address);
    // Use the name if one was given; only mark "unknown organization" when there is neither a name nor a known organization
    return isOwnValidator(v) ? t('overview.whoOwn', { who: base }) : v.orgId ? t('overview.whoOrg', { who: base, org: v.orgId }) : v.name ? base : t('overview.whoUnknown', { who: base });
  };
  const ownDown = down.find(isOwnValidator);
  const who = down.map(describe).join('、');
  // No separate ⓘ for trigger conditions here: the ⓘ on "Block production" and "Consensus participation" above already describe each state's conditions
  const items: { key: string; level: Level; title: string; detail: string; to?: string; link?: string }[] = [];
  if (halted && vReady) {
    // When halted, list only the nodes that are actually unreachable now (isUnreachable, same as the red light on the on-chain nodes page)
    const unreachable = validators!.filter((v) => isUnreachable(v, nodes));
    const ownOut = unreachable.find(isOwnValidator);
    const to = ownOut?.nodeId ? `/nodes/${encodeURIComponent(ownOut.nodeId)}` : undefined;
    if (unreachable.length > 0) {
      items.push({
        key: 'validators',
        level: 'err',
        title: t('overview.unreachableTitle', { n: unreachable.length }),
        detail: t('overview.unreachableDetail', { who: unreachable.map(describe).join('、') }),
        to,
        link: to ? t('overview.noticeSeeNode') : undefined,
      });
    }
  } else if (vReady && down.length > 0) {
    // Chain still producing: any number of non-producing validators is yellow; just name them (the margin is already shown in the consensus cell above)
    const to = ownDown?.nodeId ? `/nodes/${encodeURIComponent(ownDown.nodeId)}` : undefined;
    items.push({
      key: 'validators',
      level: 'warn',
      title: t('overview.chipValidatorsDown', { n: down.length }),
      detail: t('overview.downDetail', { who }),
      to,
      link: to ? t('overview.noticeSeeNode') : undefined,
    });
  }
  if (speed === 'warn' || speed === 'err') {
    // Slowness is usually a validator timing out on its turn: if there are round-timeout stats, name the biggest source so people know whom to look at
    const rc = chain?.roundChanges1h;
    const top = rc?.byProposer[0];
    const source = rc && top && rc.total > 0 ? ` ${t('overview.roundSource', { total: rc.total, count: top.count, who: describe(top) })}` : '';
    items.push({
      key: 'speed',
      level: speed,
      title: t(speed === 'err' ? 'overview.chipSevere' : 'overview.chipSlow'),
      // Beyond severe slowness the multiplier only keeps growing (stopped 6 hours = tens of thousands of times) and stops being useful, so just say how long it has been stopped
      detail: (speed === 'err' ? t('overview.slowDetailAge', { s: secs(age ?? 0) }) : t('overview.slowDetail', { s: secs(age ?? 0), each: eachText, r: ratio ?? '—' })) + source + (speed === 'err' ? ` ${t('overview.actionNow')}` : ''),
    });
  }

  const stability = chain?.stability24h ?? [];
  // EVM version comes after consensus: both are fixed settings of this chain; hover explains what it is and how it matters when deploying contracts; if unreadable show "EVM —" with the reason.
  // Same as the other text on this line: no dotted underline and no help cursor
  const evm = chain?.evmVersion;
  const meta = known ? [
    t('overview.metaSource', { node: chain!.sourceNodeId }),
    consensus,
    <span key="evm" title={t(evm ? 'overview.metaEvmHelp' : 'overview.metaEvmUnknown')}>{t('overview.metaEvm', { v: evm ?? '—' })}</span>,
    chain?.chainId != null ? t('overview.subChainId', { id: chain.chainId }) : null,
  ].filter(Boolean).map((x, i) => <span key={i}>{i > 0 && ' · '}{x}</span>) : null;
  // Freshness of the source node itself: every number on the page is read from it. The source is our org's chain head, so the platform's head-based lag is always 0 for it and only Besu's self-reported blocksBehind remains; with no peers it cannot tell it is behind, so peerCount is checked too
  const source = nodes?.find((x) => x.id === chain?.sourceNodeId);
  const sourceNote = !source ? null
    : source.status === 'offline' || source.status === 'rpc_unreachable' ? t('overview.sourceUnreachable')
    : source.peerCount === 0 ? t('overview.sourceIsolated')
    : (source.blocksBehind ?? 0) > 0 ? t('overview.sourceBehind', { n: fmtInt(source.blocksBehind) })
    : null;
  return (
    <section className="overflow-hidden rounded-lg border border-hair bg-surface" aria-label={t('overview.checksTitle')}>
      {/* First layer: the two health dimensions, block production and consensus, side by side */}
      <div className="grid grid-cols-1 divide-y divide-hair sm:grid-cols-2 sm:divide-x sm:divide-y-0">
        {/* ⓘ for block production status: how it is judged and each state's thresholds (multipliers are the same set as the alert rules) */}
        <Primary
          label={t('overview.blocksLabel')}
          help={(
            <dl className="flex flex-col gap-1.5">
              <p>{t('overview.blocksHelp.how', { each: eachText })}</p>
              {([['chipProducing', 'ok', 'ok'], ['chipSlow', 'warn', 'warn'], ['chipSevere', 'err', 'err'], ['chipUnknown', 'none', 'unknown']] as const).map(([chip, lv, k]) => (
                <div key={k}>
                  <dt className="flex items-center gap-1.5 font-medium text-ink">{t(`overview.${chip}`)}{lv !== 'none' && <Pill tone={lv}>{t(`overview.level.${lv}`)}</Pill>}</dt>
                  <dd>{t(`overview.blocksHelp.${k}`, { slow: th.slowX, severe: th.severeX })}</dd>
                </div>
              ))}
              <p className="border-t border-hair pt-1.5 text-ink-3">{t('overview.blocksHelp.note')}</p>
            </dl>
          )}
          x={blocks}
        />
        {/* ⓘ for consensus participation: how many validators participate normally, the threshold and how many may go offline; when the list is unreadable use wording without numbers */}
        <Primary
          label={t('overview.consLabel')}
          help={(
            <dl className="flex flex-col gap-1.5">
              <p>{vReady ? t('overview.consHelp.how', { n, quorum, f }) : t('overview.consHelp.howAny')}</p>
              {([['all', 'ok', 'ok'], ['some', 'warn', vReady ? 'warn' : 'warnAny'], ['below', 'err', 'err'], ['unknown', 'none', 'unknown']] as const).map(([term, lv, k]) => (
                <div key={term}>
                  <dt className="flex items-center gap-1.5 font-medium text-ink">{t(`overview.consHelp.term.${term}`)}{lv !== 'none' && <Pill tone={lv}>{t(`overview.level.${lv}`)}</Pill>}</dt>
                  <dd>{t(`overview.consHelp.${k}`, { f, severe: th.severeX })}</dd>
                </div>
              ))}
            </dl>
          )}
          x={cons}
        />
      </div>
      {/* Second layer: needs attention, shown only when non-empty. The pill is the level, so no separate "Needs attention" heading; one item per line, with the description after the title */}
      {items.length > 0 && (
        <ul className="flex flex-col gap-1.5 border-t border-hair px-4 py-3">
          {items.map((it) => (
            // A newly appearing item floats up gently (.rise-in); existing ones stay put
            <li key={it.key} className="rise-in flex items-start gap-2 text-xs">
              {/* Text may wrap */}
              <span className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-0.5">
                <Pill tone={it.level === 'err' ? 'err' : 'warn'}>{t(`overview.level.${it.level}`)}</Pill>
                <span className="font-semibold text-ink">{it.title}</span>
                <span className="text-ink-3">{it.detail}</span>
                {it.to && it.link && <GoLink to={it.to}>{it.link}</GoLink>}
              </span>
            </li>
          ))}
        </ul>
      )}
      {/* Third layer: context */}
      {known && (
        <ul className="grid grid-cols-1 divide-y divide-hair border-t border-hair sm:grid-cols-3 sm:divide-x sm:divide-y-0">
          <li className="flex min-w-0 flex-col gap-0.5 px-4 py-2.5">
            <span className="flex items-center gap-1.5 text-2xs text-ink-3">
              {t('overview.checkBlocksLabel')}
              {/* Height is "blocks accumulated since the genesis block"; the starting moment goes in ⓘ, to the second */}
              <HelpTip title={t('overview.checkBlocksLabel')}>{t('overview.heightHelp', { at: fmtDateTimeSec(chain?.genesisAt) })}</HelpTip>
            </span>
            <AnimatedNumber value={fmtInt(chain!.height!)} className="font-mono text-xl font-medium tabular-nums leading-tight lg:text-2xl text-ink" />
          </li>
          <li className="flex min-w-0 flex-col gap-0.5 px-4 py-2.5">
            <span className="flex flex-wrap items-center justify-between gap-x-2 gap-y-0.5 text-2xs text-ink-3">
              <span className="whitespace-nowrap">{t('overview.checkTpsLabel')}</span>
              {/* Large figure = average over the selected window, one number one definition; the window is a dropdown, right-aligned so it takes no space (controls go right; ⓘ always sits right after the title site-wide).
                  When the cell is narrow the dropdown wraps below the title instead of pushing into the neighboring cell (cell is min-w-0, content does not overflow) */}
              <Listbox
                variant="quiet"
                aria-label={t('overview.tpsWinLabel')}
                value={tpsWin}
                options={(['1m', '10m', '1h'] as const).map((w) => ({ value: w, label: t(`overview.tpsWin.${w}`) }))}
                onChange={setTpsWin}
              />
            </span>
            <AnimatedNumber value={tpsAvg != null ? tpsAvg.toFixed(1) : '—'} className="font-mono text-xl font-medium tabular-nums leading-tight lg:text-2xl text-ink" />
          </li>
          <li className="flex min-w-0 flex-col gap-0.5 px-4 py-2.5">
            <span className="flex items-center gap-1.5 text-2xs text-ink-3">
              {t('overview.stabTitle')}
              {/* How color is decided is a "rule", so it goes in ⓘ; the cell itself shows only facts. Orange and red use the same two lines as block production status (multipliers of the ChainSlow / ChainSlowSevere alerts) */}
              <HelpTip title={t('overview.stabTitle')}>
                <ul className="flex flex-col gap-1">
                  {(['ok', 'degraded', 'down', 'unknown'] as const).map((k) => (
                    <li key={k} className="flex items-start gap-1.5">
                      <span className="mt-1 size-2 shrink-0 rounded-[1px]" style={{ background: HOUR_COLOR[k] }} aria-hidden="true" />
                      <span>{t(`overview.stabRule.${k}`, { slow: th.slowX, severe: th.severeX })}</span>
                    </li>
                  ))}
                </ul>
                <p className="mt-1.5 border-t border-hair pt-1.5 text-ink-3">{t('overview.stabRule.note')}</p>
              </HelpTip>
            </span>
            {stability.length > 0 ? <StabilityStrip hours={stability} period={period} /> : <span className="text-sm text-ink-faint">—</span>}
          </li>
        </ul>
      )}
      {meta && (
        <div className="flex items-center justify-end gap-2 border-t border-hair px-4 py-1.5 text-2xs text-ink-3">
          {sourceNote && <Pill tone="warn">{sourceNote}</Pill>}
          {/* The data source line: a small database icon in front, to set it apart from ordinary description text */}
          <span className="inline-flex items-center gap-1"><Database size={14} aria-hidden="true" className="shrink-0" /><span>{meta}</span></span>
        </div>
      )}
    </section>
  );
}



const HOUR_COLOR: Record<HourState, string> = {
  ok: 'var(--bsos-status-healthy-fg)',
  degraded: 'var(--bsos-status-syncing-fg)',
  down: 'var(--bsos-status-error-fg)',
  unknown: 'var(--bsos-border-strong)',
};

/** Last 24 hours: large figure (hours normal) + bar. The rule is in the ⓘ beside it; only facts here. */
function StabilityStrip({ hours, period }: { hours: HourState[]; period: number | null }) {
  const { t } = useTranslation();
  const count = (v: HourState) => hours.filter((h) => h === v).length;
  const ok = count('ok'); const unknown = count('unknown');
  const total = hours.length - unknown;
  const label = total === 0 ? t('overview.stabUnknown') : `${ok}/${total} ${t('overview.stabHoursOk')}`;
  void period;
  return (
    <div className="flex flex-col gap-1">
      {total === 0 ? (
        <span className="text-xl font-medium leading-tight text-ink-faint lg:text-2xl">—</span>
      ) : (
        <span className="font-mono text-xl font-medium tabular-nums leading-tight lg:text-2xl text-ink">
          {ok}<span className="font-sans text-xs font-normal text-ink-3"> / {total} {t('overview.stabHoursOk')}</span>
        </span>
      )}
      <div className="flex w-full max-w-[11rem] items-center gap-px" role="img" aria-label={label}>
        {hours.map((h, i) => <span key={i} className="h-2 flex-1 rounded-[1px]" style={{ background: HOUR_COLOR[h] }} />)}
      </div>
    </div>
  );
}

