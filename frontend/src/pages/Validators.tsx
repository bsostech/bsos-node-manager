import { useEffect, useRef, useState } from 'react';
import { CopyButton } from '@/components/CopyButton';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Plus, UserMinus, UserPlus } from '@phosphor-icons/react';
import { api, chainQuery } from '@/api/client';
import type { ValidatorSetEntry, ValidatorVote, ValidatorVoteRecord, ValidatorVotes } from '@/api/schemas';
import { useAuth } from '@/auth/AuthContext';
import { Button } from '@/components/Button';
import { ProposeModal } from '@/components/ProposeModal';
import { useSearchParams } from 'react-router-dom';
import { ProposeConfirm } from '@/components/ProposeConfirm';
import { SkeletonRows, ErrorState } from '@/components/States';
import { SendingNote, VoteProgress, VoteProgressLegend, WithdrawingNote } from '@/components/VoteProgress';
import { fmtAddr, fmtDateTimeMin, fmtDateTimeSec, fmtInt } from '@/lib/format';
import { Modal, ModalTitle } from '@/components/Modal';
import { countedOf, epochProgress, epochUnknown, isOwnValidator, ownActive, ownCounted, ownSending, ownWithdrawing, proposalKey, stillOpen, sameAddr, validatorName } from '@/lib/validators';
import { ConfirmModal } from '@/components/ConfirmModal';
import { errMsg } from '@/lib/errors';
import { Card } from '@/components/Card';
import { Listbox } from '@/components/Listbox';
import { HelpTip } from '@/components/HelpTip';
import { CountChip } from '@/components/ValueChip';
import { Pill } from '@/components/Pill';
import { presenceRef, usePresence, type PresenceState } from '@/hooks/usePresence';

const FOCUS = 'focus:outline-none focus-visible:ring-1 focus-visible:ring-accent';

/**
 * Validator admission and removal: at the top are changes being voted on (network-wide tally this epoch) and the vote history; prerequisites (our vote count, effective threshold) are in the header.
 * Which validators exist and whether each proposes normally is monitoring, in the on-chain node table of chain health, not repeated here.
 * Clicking a proposal opens /validators/proposals/:key; "Propose a vote" is a modal on this page, opened by ?propose=add|remove(&address=) in the URL (validator detail's "Propose removing this validator" comes in this way).
 */
export function Validators() {
  const { t } = useTranslation();
  const { can } = useAuth();
  const canWrite = can('nodes:onboard');
  const set = useQuery({ queryKey: ['validators'], queryFn: api.listValidatorSet, refetchInterval: 15_000 });
  const votes = useQuery({ queryKey: ['validator-votes'], queryFn: api.listValidatorVotes, refetchInterval: 10_000 });
  const v = votes.data;
  const [params, setParams] = useSearchParams();
  const propose = canWrite ? params.get('propose') : null;
  // As soon as a proposal ends (passed or cleared at epoch) it leaves the voting list, so refetch the vote history right away. The backend writes the result to history in the background,
  // and often hasn't finished at that moment, so history itself also polls every 10 seconds like the voting list, and late entries show up next round
  const qc = useQueryClient();
  const voteKeys = (v?.votes ?? []).filter(stillOpen).map(proposalKey).join('|');
  const prevKeys = useRef<string[] | null>(null);
  useEffect(() => {
    const keys = voteKeys ? voteKeys.split('|') : [];
    if (prevKeys.current?.some((k) => !keys.includes(k))) void qc.invalidateQueries({ queryKey: ['validator-vote-history'] });
    prevKeys.current = keys;
  }, [voteKeys, qc]);
  const validators = set.data;
  const own = validators?.filter(isOwnValidator).length ?? 0;

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-4">
      {/* Header in two columns: title and subtitle on the left; on the right "Propose a vote" on the same line as the big title, with the prerequisite facts row right-aligned on the subtitle line */}
      <header className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-6 gap-y-1">
        <h1 className="text-lg font-semibold text-ink">{t('validators.title')}</h1>
        <div className="justify-self-end">
          {canWrite && (
            <Button variant="primary" onClick={() => setParams({ propose: 'add' })} className="inline-flex items-center gap-1.5">
              <Plus size={15} aria-hidden /> {t('validators.propose')}
            </Button>
          )}
        </div>
        <span className="self-start text-xs text-ink-3">{t('validators.subtitle')}</span>
        {/* Prerequisites are shown as a "facts row": small label on top, number below, three equal parts with thin dividers and no card background (not a state to monitor).
            Labels don't wrap: when English is longer the column widens (each fits its own label), Chinese stays in three equal parts */}
        {validators && validators.length > 0 && v ? (
          <dl className="mt-4 grid w-max min-w-[26rem] max-w-full grid-cols-[repeat(3,minmax(max-content,1fr))] justify-self-end divide-x divide-hair">
            {[
              { k: t('validators.factOwn'), v: <>{own}<span className="ml-1 text-xs font-normal text-ink-3">{t('validators.factUnitVotes')}</span></>, note: own === 0 ? t('validators.factOwnNone') : null },
              { k: t('validators.factTotal'), v: <>{validators.length}<span className="ml-1 text-xs font-normal text-ink-3">{t('validators.factUnitPeople')}</span></> },
              { k: t('validators.factNeeded'), v: <><span className="mr-1 text-xs font-normal text-ink-3">{t('validators.factMajority')}</span>{v.needed}<span className="ml-1 text-xs font-normal text-ink-3">{t('validators.factUnitVotes')}</span></> },
            ].map((f) => (
              <div key={f.k} className="flex flex-col gap-0.5 px-6 last:pr-0">
                <dt className="whitespace-nowrap text-2xs text-ink-3">{f.k}</dt>
                <dd className="font-mono text-lg font-semibold leading-tight tabular-nums text-ink">{f.v}</dd>
                {f.note && <dd className="text-2xs text-ink-3">{f.note}</dd>}
              </div>
            ))}
          </dl>
        ) : <span />}
      </header>

      {/* Voting in progress needs the most attention: at the top */}
      <VotesSection votes={votes} canWrite={canWrite} names={validators} />

      {/* "Current validators" and each one's proposing are monitoring, in the on-chain node table of chain health; this page only has voting */}
      <VoteHistory />
      {/* Propose modal: remove ?propose from the URL on close */}
      {propose && <ProposeModal initialAdd={propose !== 'remove'} initialAddress={params.get('address') ?? ''} onClose={() => setParams({}, { replace: true })} />}
    </div>
  );
}

/**
 * Voting in progress: the body of this page, always expanded. When a proposal awaits our vote (we have validators and haven't voted on it),
 * the title row adds a primary-color dot and count. While there are votes, a sentence below the table states the tally deadline (epoch); with no votes the epoch isn't interesting, so it is hidden.
 */
function VotesSection({ votes, canWrite, names }: { votes: ReturnType<typeof useQuery<ValidatorVotes>>; canWrite: boolean; names?: ValidatorSetEntry[] }) {
  const [detail, setDetail] = useState<HistoryRow | null>(null);
  const { t } = useTranslation();
  const v = votes.data;
  const canFollow = canWrite && (v?.ownValidators.length ?? 0) > 0;
  // The modal follows the latest vote data: our vote changes right after approve / withdraw. Once the proposal ends and leaves the voting list, switch to the same item in vote history
  // (same target, same direction, same first-vote time); until history has it, keep the last seen in-progress state rather than reverting to when it was opened
  // Proposals whose votes dropped to zero (everyone withdrew) are over: not listed as in progress, they appear in vote history (not passed)
  const open = (v?.votes ?? []).filter(stillOpen);
  const liveVote = detail && open.find((x) => proposalKey(x) === detail.key);
  // Newest on top (like vote history): by the block holding the first vote, newest first; those with no vote on chain yet (just sent) are newest and go first
  const firstBlock = (x: ValidatorVote) => Math.min(...x.voters.map((y) => y.countedAt ?? Infinity));
  const sorted = [...open].sort((a, b) => { const fa = firstBlock(a), fb = firstBlock(b); return fa === fb ? 0 : fb > fa ? 1 : -1; });
  const rows = usePresence(sorted, proposalKey, !!v);
  const hist = useQuery({ ...histQuery, enabled: !!detail });
  const lastLive = useRef<HistoryRow | null>(null);
  const liveRow = liveVote ? openVoteRow(liveVote, v!.needed, names) : null;
  if (!detail) lastLive.current = null;
  else if (liveRow) lastLive.current = liveRow;
  const ended = detail && !liveRow ? (hist.data ?? []).map(histRow).find((r) => sameAddr(r.address, detail.address) && r.add === detail.add && r.startedAt === detail.startedAt) : undefined;
  const shownDetail = liveRow ?? ended ?? lastLive.current ?? detail;
  return (
    <section className="overflow-hidden rounded-lg border border-hair bg-surface">
      <h2 className="flex min-h-[54px] items-center gap-2 border-b border-hair px-4 py-2 text-sm font-semibold text-ink">
        {/* Only title and count: which ones still need a vote is visible from the "Vote" buttons in the rows */}
        {t('validators.secVotes')}
        {/* Count always at the far right of the title row, dark block plus unit (site-wide rule) */}
        {v && <CountChip unit={t('validators.histUnit')} className="ml-auto">{open.length}</CountChip>}
      </h2>
      <div>
          {votes.isLoading ? (
            <div className="p-4"><SkeletonRows rows={2} /></div>
          ) : votes.isError ? (
            <ErrorState inCard onRetry={() => void votes.refetch()} />
          ) : !v || open.length === 0 ? (
            // No votes: one line of text is enough; the propose button is at the top right of the header, no second one here
            <p className="px-4 py-3 text-xs text-ink-3">{v && v.ownValidators.length === 0 ? t('validators.noOwnValidators') : t('validators.noVotes')}</p>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="table-head">
                    {/* At 1280 wide the full address must fit (including scrollbars that take width, like on Windows): tighten the padding between columns (px-2.5),
                        the outermost columns keep px-4, aligned with the card title */}
                    <th className="pl-4 pr-2.5">{t('validators.colAction')}</th>
                    <th className="px-2.5">{t('validators.colTarget')}</th>
                    <th className="px-2.5">{t('validators.colVoteState')}</th>
                    {/* The approve / withdraw / transition column: right-aligned like the buttons */}
                    <th className="pl-2.5 pr-4 text-right">{t('validators.colVote')}</th>
                  </tr>
                </thead>
                <tbody>
                  {/* Rows expand and fade in / fade out and collapse when proposals appear / end (usePresence); no animation on first load */}
                  {rows.map(({ key, item: vote, state }) => <VoteRow key={key} presence={state} vote={vote} needed={v.needed} known={v.tallyKnown} canFollow={canFollow} onOpen={() => setDetail(openVoteRow(vote, v.needed, names))} />)}
                </tbody>
              </table>
              {v.tallyKnown && <VoteProgressLegend />}
              <EpochNote />
            </div>
          )}
      </div>
      {shownDetail && <BallotModal row={shownDetail} tone={shownDetail.status === 'passed' ? 'ok' : shownDetail.status === 'expired' ? 'neutral' : 'info'} canFollow={canFollow} onClose={() => setDetail(null)} />}
    </section>
  );
}

function VoteRow({ vote, needed, known, canFollow, onOpen, presence = null }: { vote: ValidatorVote; needed: number; known: boolean; canFollow: boolean; onOpen: () => void; presence?: PresenceState }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [confirm, setConfirm] = useState(false);
  const follow = useMutation({
    mutationFn: () => api.proposeValidatorVote(vote.address, vote.add),
    // Close the confirm dialog only after the list updates: by then the row already shows "Sent by your organization, not yet in a block" and "Withdraw vote", so the button needs no separate "Sent"
    onSuccess: async () => { await qc.invalidateQueries({ queryKey: ['validator-votes'] }); setConfirm(false); },
  });
  // What is withdrawn is our vote, not the whole vote: our vote stops counting (votes already in blocks are cancelled by the platform casting the opposite vote), and other orgs can still vote
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const discard = useMutation({
    mutationFn: () => api.discardValidatorVote(vote.address),
    // After withdrawing, "Vote" must be clickable again, so clear the last sent state
    onSuccess: async () => { follow.reset(); await qc.invalidateQueries({ queryKey: ['validator-votes'] }); setConfirmDiscard(false); },
  });
  return (
    // Clicking a row opens the read-only vote detail modal (same as vote history); the "Vote" button is in the row
    // Each cell's content is wrapped in .row-cell: when rows are added / removed, the height animation runs on that layer
    <tr onClick={onOpen} ref={presenceRef(presence)} className="group cursor-pointer border-b border-hair transition-colors last:border-0 hover:bg-elevated">
      <td className="whitespace-nowrap py-2.5 pl-4 pr-2.5"><div className="row-cell"><ActionPill add={vote.add} /></div></td>
      <td className="whitespace-nowrap px-2.5 py-2.5">
        <div className="row-cell">
          <button type="button" onClick={(e) => { e.stopPropagation(); onOpen(); }} className={`rounded-sm text-left font-mono text-ink ${FOCUS}`}>{vote.address}</button>
          <span className="ml-1 inline-flex"><CopyButton value={vote.address} variant="cell" stop /></span>
        </div>
      </td>
      <td className="whitespace-nowrap px-2.5 py-2.5">
        <div className="row-cell"><VoteProgress vote={vote} needed={needed} known={known} /></div>
      </td>
      {/* Approve / withdraw our vote goes in the rightmost column, right-aligned: not yet voted can approve, our vote on chain can withdraw; in between only the status is shown, no button */}
      <td className="whitespace-nowrap py-2.5 pl-2.5 pr-4 text-right">
        {/* Always reserve one button's height (button py-1.5 + text-xs line height + top/bottom border) and width (fits the widest "Withdraw vote"; at 1280 wide the full address must fit, so no wider):
            when the button changes to "Vote" / "Going on-chain…" / "Withdrawing…", the row height doesn't jump and earlier columns don't shift */}
        <div className="row-cell">
        <div className="ml-auto flex min-h-[calc(1.75rem+2px)] w-[7rem] items-center justify-end">
        {ownSending(vote) && <SendingNote />}
        {canFollow && ownCounted(vote) && (
          <Button variant="dangerOutline" onClick={(e) => { e.stopPropagation(); discard.reset(); setConfirmDiscard(true); }} disabled={discard.isPending} className="font-sans">
            {t('validators.discardOwn')}
          </Button>
        )}
        {/* Not yet voted, or our withdrawal completed, can approve (withdrawn = never voted, no separate revote); not clickable while withdrawing */}
        {/* While withdrawing: the button's spot temporarily says "Withdrawing…" */}
        {ownWithdrawing(vote) && <WithdrawingNote />}
        {canFollow && !ownActive(vote) && !ownWithdrawing(vote) && (
          <Button
            onClick={(e) => { e.stopPropagation(); follow.reset(); setConfirm(true); }}
            disabled={follow.isPending || follow.isSuccess}
            className="inline-flex items-center gap-1 font-sans"
          >
            {t('validators.follow')}
          </Button>
        )}
        </div>
        </div>
        {/* The modal renders via a portal, but events still bubble to this row's onClick */}
        {confirmDiscard && (
          <span onClick={(e) => e.stopPropagation()}>
            <ConfirmModal
              title={t('validators.discardOwn')}
              body={discard.isError ? errMsg(discard.error) || t('error.title') : t('validators.discardBody')}
              confirmLabel={t('validators.discardOwn')}
              danger
              busy={discard.isPending}
              onConfirm={() => discard.mutate()}
              onCancel={() => setConfirmDiscard(false)}
            />
          </span>
        )}
        {confirm && (
          <span onClick={(e) => e.stopPropagation()}>
            <ProposeConfirm
              address={vote.address}
              add={vote.add}
              confirmLabel={t('validators.follow')}
              busy={follow.isPending}
              error={follow.error}
              onConfirm={() => follow.mutate()}
              onCancel={() => setConfirm(false)}
            />
          </span>
        )}
      </td>
    </tr>
  );
}

/**
 * Hint shown when the tally progress can't be read (no node has the ADMIN namespace on): a settings problem the user can fix, so it goes under the vote table.
 * The normal tally deadline (reset per epoch, automatic recast when short of a majority) is a background mechanism the user needn't act on, so it isn't shown.
 */
function EpochNote() {
  const { t } = useTranslation();
  const chain = useQuery(chainQuery);
  if (epochProgress(chain.data) || !epochUnknown(chain.data)) return null;
  return <p className="border-t border-hair px-4 py-2.5 text-xs text-ink-3">{t('validators.epochNeedsAdminPaused', { needsAdmin: t('validators.epochNeedsAdmin'), paused: t('validators.cleanupPaused') })}</p>;
}

/** Action column: add / remove, a fixed-format pill (icon + text), shared by both tables */
function ActionPill({ add }: { add: boolean }) {
  const { t } = useTranslation();
  const Icon = add ? UserPlus : UserMinus;
  // Light distinction: add uses a faint primary background, remove blue-grey, both quiet palette colors that don't read as warnings; a bit more horizontal padding, left edge aligned with the header
  return <Pill tone={add ? 'accent' : 'slate'} className="gap-1 px-2.5"><Icon size={14} aria-hidden />{t(add ? 'validators.add' : 'validators.remove')}</Pill>;
}

/** Only our own votes have this: withdrawing = withdrawal in progress (the opposite vote isn't in a block yet, so this vote still counts for now); withdrawn = withdrawn (no longer counted), withdrawnAt is when it completed */
type Ballot = { address: string; votedAt: string | null; block: number | null; own?: boolean; name?: string | null; withdrawing?: boolean; withdrawn?: boolean; withdrawnAt?: string | null };
type HistoryRow = {
  key: string; address: string; add: boolean; status: 'voting' | 'passed' | 'expired';
  votes: number; needed: number; startedAt: string | null; endedAt: string | null; endedBlock: number | null;
  /** Every validator eligible to vote and their vote; for in-progress votes only the block is known (votedAt is null) */
  ballots: Ballot[];
  /** Our nodes still hold this vote (withdrawable); only used for in-progress votes */
  ownActive?: boolean;
  /** Our vote is on chain (withdrawable) / sent but not yet on chain (confirming); only used for in-progress votes */
  ownCounted?: boolean;
  ownSending?: boolean;
  /** We are withdrawing (waiting for the opposite vote to land in a block); only used for in-progress votes */
  ownWithdrawing?: boolean;
};
const HIST_PAGE = 20;
const histQuery = { queryKey: ['validator-vote-history'], queryFn: api.listValidatorVoteHistory, refetchInterval: 10_000 };

function histRow(r: ValidatorVoteRecord): HistoryRow {
  return {
    key: `${r.address}-${r.add}-${r.endedAt}`, address: r.address, add: r.add, status: r.result,
    votes: r.votes, needed: r.needed, startedAt: r.firstVoteAt, endedAt: r.endedAt, endedBlock: r.endedBlock ?? null,
    ballots: r.ballots.map((b) => ({ ...b, withdrawn: !!b.withdrawnAt })),
  };
}

/**
 * In-progress proposal -> data for the modal and history table. Eligible voters = current validator set; for each vote, which block it landed in and that block's time;
 * votes not yet in a block (ours just sent) have no block. Proposed time = time of the first vote written to a block.
 */
function openVoteRow(x: ValidatorVote, needed: number, names?: ValidatorSetEntry[]): HistoryRow {
  const ballots: Ballot[] = (names ?? []).map((n) => {
    const v = x.voters.find((y) => sameAddr(y.address, n.address));
    // Our vote: withdrawing = the opposite vote isn't in a block yet; withdrawn = the opposite vote is in a block and this one no longer counts
    const withdrawing = !!v && v.nodeId != null && !!v.withdrawing;
    const withdrawn = !!v && v.nodeId != null && v.withdrawnAt != null;
    return { address: n.address.toLowerCase(), own: isOwnValidator(n), name: validatorName(n) ?? null, votedAt: v ? v.countedAtTime ?? null : null, block: v ? v.countedAt ?? null : null,
      withdrawing, withdrawn, withdrawnAt: withdrawn ? v.withdrawnAt ?? null : null };
  }).sort((a, b) => a.address.localeCompare(b.address));
  const startedAt = x.voters.map((v) => v.countedAtTime).filter((t): t is string => !!t).sort()[0] ?? null;
  return { key: proposalKey(x), address: x.address, add: x.add, status: 'voting', votes: countedOf(x), needed, startedAt, endedAt: null, endedBlock: null, ballots, ownActive: ownActive(x), ownCounted: ownCounted(x), ownSending: ownSending(x), ownWithdrawing: ownWithdrawing(x) };
}

/**
 * Vote history: only finished ones, newest first; in-progress ones are in the card above, not repeated. QBFT has no "proposal" as such, only votes each validator writes into blocks, and there are only two outcomes:
 * a majority changes the set (passed), or the epoch ends short of a majority and the on-chain tally is cleared (not passed). Nodes still holding the vote recast it automatically in the new epoch, as a separate entry.
 * Withdrawal only withdraws our vote (no longer counted) and doesn't end the whole vote; it is recorded on our vote in the detail.
 */
function VoteHistory() {
  const { t } = useTranslation();
  const hist = useQuery(histQuery);
  const [status, setStatus] = useState<'all' | HistoryRow['status']>('all');
  const [action, setAction] = useState<'all' | 'add' | 'remove'>('all');
  const [shown, setShown] = useState(HIST_PAGE);
  const [detail, setDetail] = useState<HistoryRow | null>(null);
  const all: HistoryRow[] = (hist.data ?? []).map(histRow).sort((a, b) => Date.parse(b.endedAt!) - Date.parse(a.endedAt!));
  const byAction = (r: HistoryRow, a: typeof action) => a === 'all' || (a === 'add') === r.add;
  const byStatus = (r: HistoryRow, st: typeof status) => st === 'all' || r.status === st;
  const rows = all.filter((r) => byAction(r, action) && byStatus(r, status));
  // A row expands and fades in when a vote ends and a history entry is added; it looks at changes across all history before filtering, so changing filters doesn't animate, nor does first load
  const presence = usePresence(all, (r) => r.key, hist.isSuccess).filter(({ item: r }) => byAction(r, action) && byStatus(r, status));
  const tone = { voting: 'info', passed: 'ok', expired: 'neutral' } as const;
  const aside = (
    <div className="flex items-center gap-3">
      {/* Filter labels match the table column names (action, result); each option's count is computed under the other filter's current choice */}
      <label className="flex items-center gap-2">
        {t('validators.colAction')}
        <Listbox width="w-auto" aria-label={t('validators.colAction')} value={action} onChange={setAction}
          options={(['all', 'add', 'remove'] as const).map((k) => ({ value: k, label: t('common.withCount', { label: t(k === 'all' ? 'validators.hist_all' : `validators.${k}`), n: all.filter((r) => byAction(r, k) && byStatus(r, status)).length }) }))} />
      </label>
      <label className="flex items-center gap-2">
        {t('validators.histStatus')}
        <Listbox width="w-auto" aria-label={t('validators.histStatus')} value={status} onChange={setStatus}
          options={(['all', 'passed', 'expired'] as const).map((k) => ({ value: k, label: t('common.withCount', { label: t(`validators.hist_${k}`), n: all.filter((r) => byAction(r, action) && byStatus(r, k)).length }) }))} />
      </label>
      <CountChip unit={t('validators.histUnit')}>{rows.length}</CountChip>
    </div>
  );
  return (
    <Card title={t('validators.histTitle')} aside={aside} bodyClass="flex flex-col">
      {hist.isLoading ? <div className="p-4"><SkeletonRows rows={3} /></div>
        : hist.isError ? <ErrorState inCard onRetry={() => void hist.refetch()} />
        : rows.length === 0 ? <p className="px-4 py-3 text-xs text-ink-3">{t('validators.histEmpty')}</p> : (
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead>
              <tr className="table-head">
                <th className="w-24 px-4">{t('validators.colAction')}</th>
                <th className="px-4">{t('validators.colTarget')}</th>
                <th className="whitespace-nowrap px-4">
                  {/* The three results and "withdrawal only affects our vote" go in the ⓘ */}
                  <span className="inline-flex items-center gap-1.5">{t('validators.histResult')}<HelpTip title={t('validators.histResult')}><p className="whitespace-pre-line">{t('validators.histResultHelp')}</p></HelpTip></span>
                </th>
                <th className="whitespace-nowrap px-4 text-right">{t('validators.histVotes')}</th>
                <th className="whitespace-nowrap px-4">{t('validators.histStarted')}</th>
                <th className="whitespace-nowrap px-4">{t('validators.histEnded')}</th>
              </tr>
            </thead>
            <tbody>
              {presence.slice(0, shown).map(({ key, item: r, state }) => (
                // The whole row is clickable: opens a read-only modal with each validator's vote and the proposer. Each cell is wrapped in .row-cell, so the add animation runs on that layer
                <tr key={key} ref={presenceRef(state)} onClick={() => setDetail(r)} className="cursor-pointer border-b border-hair transition-colors last:border-0 hover:bg-elevated">
                  <td className="whitespace-nowrap px-4 py-2.5"><div className="row-cell"><ActionPill add={r.add} /></div></td>
                  <td className="whitespace-nowrap px-4 py-2.5">
                    {/* Address only: on-chain voting only shows addresses, names are chosen by each org and don't belong in vote history */}
                    <div className="row-cell">
                      <span className="inline-flex items-center gap-1 font-mono text-ink">
                        <button type="button" onClick={(e) => { e.stopPropagation(); setDetail(r); }} title={r.address} className={`rounded-sm text-left ${FOCUS}`}>{fmtAddr(r.address)}</button>
                        <CopyButton value={r.address} variant="cell" stop />
                      </span>
                    </div>
                  </td>
                  <td className="whitespace-nowrap px-4 py-2.5">
                    <div className="row-cell"><Pill tone={tone[r.status]} className="-ml-1.5">{t(`validators.hist_${r.status}`)}</Pill></div>
                  </td>
                  <td className="whitespace-nowrap px-4 py-2.5 text-right font-mono tabular-nums text-ink-2"><div className="row-cell">{r.votes} / {r.needed}</div></td>
                  <td className="whitespace-nowrap px-4 py-2.5 font-mono tabular-nums text-ink-3"><div className="row-cell">{r.startedAt ? fmtDateTimeMin(r.startedAt) : '—'}</div></td>
                  <td className="whitespace-nowrap px-4 py-2.5 font-mono tabular-nums text-ink-3">
                    <div className="row-cell">{r.endedAt ? <>{fmtDateTimeMin(r.endedAt)}{r.endedBlock != null && <span className="ml-2 text-ink-faint">#{fmtInt(r.endedBlock)}</span>}</> : '—'}</div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {rows.length > shown && (
            <div className="border-t border-hair px-4 py-2 text-center">
              <button type="button" onClick={() => setShown((n) => n + HIST_PAGE)} className="rounded px-2 py-1 text-xs text-ink-3 hover:bg-elevated hover:text-ink">{t('validators.histMore', { n: Math.min(HIST_PAGE, rows.length - shown) })}</button>
            </div>
          )}
        </div>
      )}
      {/* Finished ones are view-only, no approve / withdraw */}
      {detail && <BallotModal row={detail} tone={tone[detail.status]} canFollow={false} onClose={() => setDetail(null)} />}
    </Card>
  );
}

/**
 * Votes for one proposal (view only, just "Close"). Layout:
 * 1. Header: action and result as two small pills for the eyebrow; the star is the address being voted on.
 * 2. Facts row: vote count, proposer (the earliest vote written to a block), end; small label above, value below, same as the top of the page.
 * 3. Votes: split into voted and "Not voted". Voted ones list time and block in order; not voted only keep a faint address. Ours are tagged "Your org".
 */
function BallotModal({ row, tone, canFollow, onClose }: { row: HistoryRow; tone: 'info' | 'ok' | 'neutral'; canFollow: boolean; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [confirm, setConfirm] = useState(false);
  // Approve: the same action as in the row (each of our validators casts one vote), confirm first
  const follow = useMutation({
    mutationFn: () => api.proposeValidatorVote(row.address, row.add),
    onSuccess: async () => { discard.reset(); await qc.invalidateQueries({ queryKey: ['validator-votes'] }); setConfirm(false); },
  });
  // Not yet voted, or withdrawal completed, can vote (again); not clickable while withdrawing
  const showFollow = canFollow && row.status === 'voting' && !row.ownActive && !row.ownWithdrawing;
  // Withdraw our vote: only shown while voting is ongoing and our nodes still hold this vote; confirm on click
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const discard = useMutation({
    mutationFn: () => api.discardValidatorVote(row.address),
    onSuccess: async () => { follow.reset(); await qc.invalidateQueries({ queryKey: ['validator-votes'] }); setConfirmDiscard(false); },
  });
  const showDiscard = canFollow && row.status === 'voting' && !!row.ownCounted;
  // Counted = written to a block and not withdrawn; while withdrawing the opposite vote isn't on chain yet, so it still counts for now. Withdrawn ones no longer count, go under "Not counted" and are tagged withdrawn
  const voted = row.ballots.filter((b) => b.block != null && !b.withdrawn).sort((a, b) => (a.block ?? 0) - (b.block ?? 0));
  const notVoted = row.ballots.filter((b) => b.block == null || b.withdrawn);
  // Proposer = the first vote written to a block (still the proposer even if withdrawn)
  const first = row.ballots.filter((b) => b.block != null).sort((a, b) => (a.block ?? 0) - (b.block ?? 0))[0];
  const own = (b: Ballot) => b.own && <>
    <Pill tone="accent">{t('validators.own')}</Pill>
    {/* Withdrawing: the opposite vote isn't in a block yet, so this vote still counts for now; withdrawn: no longer counted, so it is in the "Not counted" group */}
    {b.withdrawing && <span title={t('validators.withdrawingHint')}><Pill>{t('validators.ballotWithdrawing')}</Pill></span>}
    {b.withdrawn && <span title={b.withdrawnAt ? t('validators.ballotWithdrawnAt', { time: fmtDateTimeSec(b.withdrawnAt) }) : undefined}><Pill>{t('validators.ballotWithdrawn')}</Pill></span>}
  </>;
  // Named ones get the name in parentheses after the address (chosen by this org); otherwise just the address
  const named = (b: Ballot) => b.name ? <span className="ml-1.5 font-sans text-ink-3">（{b.name}）</span> : null;
  const fact = (k: string, v: React.ReactNode, sub?: React.ReactNode) => (
    <div className="flex min-w-0 flex-col gap-1 px-5 first:pl-0 last:pr-0">
      <dt className="text-2xs text-ink-3">{k}</dt>
      <dd className="truncate font-mono text-sm font-semibold tabular-nums text-ink">{v}</dd>
      {sub && <dd className="truncate font-mono text-2xs tabular-nums text-ink-3">{sub}</dd>}
    </div>
  );
  const group = (label: string, n: number) => (
    <div className="flex items-baseline gap-2 border-b border-hair bg-elevated px-4 py-1.5 text-2xs font-semibold tracking-[0.05em] text-ink-2">
      {label}<span className="font-mono font-normal text-ink-3">{n}</span>
    </div>
  );
  return (
    <Modal width="max-w-4xl" contentClass="gap-5 rounded-lg border border-hair bg-surface p-6" onClose={onClose}>
      <div className="flex flex-col gap-2">
        <span className="flex items-center gap-2"><ActionPill add={row.add} /><Pill tone={tone}>{t(`validators.hist_${row.status}`)}</Pill></span>
        <ModalTitle>
          <span className="inline-flex items-center gap-1.5 font-mono text-base font-semibold tracking-tight">{row.address}<CopyButton value={row.address} variant="cell" /></span>
        </ModalTitle>
      </div>
      <dl className="grid grid-cols-3 divide-x divide-hair">
        {fact(t('validators.histVotes'), <>{row.votes}<span className="font-normal text-ink-3"> / {row.ballots.length}</span></>, t('validators.ballotNeeded', { n: row.needed }))}
        {fact(t('validators.ballotProposer'), first ? fmtAddr(first.address) : '—', first ? (first.votedAt ? fmtDateTimeSec(first.votedAt) : `#${fmtInt(first.block!)}`) : null)}
        {/* Not a deadline: for passed it is the block where the set changed; for not passed it is the block where the epoch cleared it, or where votes dropped to zero (all withdrawn); in-progress has none.
            Votes that had already dropped to zero by the epoch ended at that moment, so votes of 0 means ended by dropping to zero */}
        {fact(t(row.status === 'expired' ? (row.votes === 0 ? 'validators.histZeroedAt' : 'validators.histExpiredAt') : row.status === 'passed' ? 'validators.histEffectiveAt' : 'validators.histEnded'), row.endedAt ? fmtDateTimeSec(row.endedAt).split(' ')[1] ?? fmtDateTimeSec(row.endedAt) : '—', row.endedAt ? <>{fmtDateTimeSec(row.endedAt).split(' ')[0]}{row.endedBlock != null && <> · #{fmtInt(row.endedBlock)}</>}</> : null)}
      </dl>
      <div className="max-h-[46vh] overflow-auto rounded-md border border-hair">
        {group(t('validators.ballotYesGroup'), voted.length)}
        <ul>
          {voted.map((b) => (
            <li key={b.address} className="flex items-center gap-3 border-b border-hair px-4 py-2 text-xs last:border-0">
              <span className="min-w-0 flex-1 truncate font-mono text-ink">{b.address}{named(b)}</span>
              {own(b)}
              <span className="w-36 shrink-0 text-right font-mono tabular-nums text-ink-2">{b.votedAt ? fmtDateTimeSec(b.votedAt) : ''}</span>
              <span className="w-24 shrink-0 text-right font-mono tabular-nums text-ink-3">#{fmtInt(b.block!)}</span>
            </li>
          ))}
        </ul>
        {notVoted.length > 0 && <>
          {group(t('validators.ballotNoGroup'), notVoted.length)}
          <ul>
            {notVoted.map((b) => (
              <li key={b.address} className="flex items-center gap-3 border-b border-hair px-4 py-2 text-xs last:border-0">
                <span className="min-w-0 flex-1 truncate font-mono text-ink-3">{b.address}{named(b)}</span>
                {own(b)}
              </li>
            ))}
          </ul>
        </>}
      </div>
      <div className="flex items-center justify-end gap-2">
        <Button onClick={onClose}>{t('common.close')}</Button>
        {row.status === 'voting' && row.ownSending && <SendingNote />}
        {row.status === 'voting' && row.ownWithdrawing && <WithdrawingNote />}
        {showDiscard && (
          <Button variant="dangerOutline" onClick={() => { discard.reset(); setConfirmDiscard(true); }} disabled={discard.isPending}>
            {t('validators.discardOwn')}
          </Button>
        )}
        {/* Primary action on the far right: only shown while voting is ongoing and we haven't voted */}
        {showFollow && (
          <Button variant="primary" onClick={() => { follow.reset(); setConfirm(true); }} disabled={follow.isPending || follow.isSuccess}>
            {t('validators.follow')}
          </Button>
        )}
      </div>
      {confirmDiscard && (
        <ConfirmModal
          title={t('validators.discardOwn')}
          body={discard.isError ? errMsg(discard.error) || t('error.title') : t('validators.discardBody')}
          confirmLabel={t('validators.discardOwn')}
          danger
          busy={discard.isPending}
          onConfirm={() => discard.mutate()}
          onCancel={() => setConfirmDiscard(false)}
        />
      )}
      {confirm && (
        <ProposeConfirm
          address={row.address}
          add={row.add}
          confirmLabel={t('validators.follow')}
          busy={follow.isPending}
          error={follow.error}
          onConfirm={() => follow.mutate()}
          onCancel={() => setConfirm(false)}
        />
      )}
    </Modal>
  );
}

