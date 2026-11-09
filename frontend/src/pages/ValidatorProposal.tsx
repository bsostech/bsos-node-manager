import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowClockwise, ArrowCounterClockwise } from '@phosphor-icons/react';
import { api, chainQuery } from '@/api/client';
import { useAuth } from '@/auth/AuthContext';
import { Button } from '@/components/Button';
import { ConfirmModal } from '@/components/ConfirmModal';
import { CopyButton } from '@/components/CopyButton';
import { ProposeConfirm, VoteResults } from '@/components/ProposeConfirm';
import { SkeletonRows, EmptyState, ErrorState } from '@/components/States';
import { ProducingLabel } from '@/components/TurnCells';
import { SendingNote, VoteProgress, WithdrawingNote } from '@/components/VoteProgress';
import { errMsg } from '@/lib/errors';
import { fmtInt } from '@/lib/format';
import { countedOf, missingVoters, ownCounted, ownSending, ownWithdrawing, proposalKey, stillOpen, sameAddr, validatorName, votesBackfilling } from '@/lib/validators';
import { BackToValidators } from './ValidatorDetail';

/**
 * A single proposal: validators that cast this vote (network-wide, written to blocks this epoch) and platform-managed validators that haven't voted yet.
 * Resend = send again to the platform's validators (those already in the pending list are unaffected); withdraw = remove it from every node's pending list, so it won't be recast after the epoch either.
 */
export function ValidatorProposal() {
  const { t } = useTranslation();
  const { key = '' } = useParams();
  const nav = useNavigate();
  const qc = useQueryClient();
  const { can } = useAuth();
  const canWrite = can('nodes:onboard');
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const [confirmRevote, setConfirmRevote] = useState(false);
  const votes = useQuery({ queryKey: ['validator-votes'], queryFn: api.listValidatorVotes, refetchInterval: 10_000 });
  const set = useQuery({ queryKey: ['validators'], queryFn: api.listValidatorSet, refetchInterval: 15_000 });
  const chain = useQuery(chainQuery);
  const vote = votes.data?.votes.find((v) => proposalKey(v) === key && stillOpen(v));
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ['validator-votes'] });
    void qc.invalidateQueries({ queryKey: ['validators'] });
  };
  const revote = useMutation({ mutationFn: () => api.proposeValidatorVote(vote!.address, vote!.add), onSuccess: () => { invalidate(); setConfirmRevote(false); } });
  const discard = useMutation({
    mutationFn: () => api.discardValidatorVote(vote!.address),
    // Votes still counted this epoch: the proposal is still being voted on, so stay on this page to see the state after withdrawing; otherwise go back to the list
    onSuccess: () => { invalidate(); setConfirmDiscard(false); if (!vote || countedOf(vote) === 0) nav('/validators'); },
  });

  if (votes.isLoading) return <div className="mx-auto flex max-w-5xl flex-col gap-4"><BackToValidators /><SkeletonRows rows={3} /></div>;
  if (votes.isError || !vote || !votes.data) {
    return (
      <div className="mx-auto flex max-w-5xl flex-col gap-4">
        <BackToValidators />
        {votes.isError ? <ErrorState onRetry={() => void votes.refetch()} /> : <EmptyState message={t('validators.proposalGone')} />}
      </div>
    );
  }
  const v = votes.data;
  const missing = missingVoters(vote, v);
  const from = votesBackfilling(chain.data);
  const ownVoted = vote.voters.some((x) => x.nodeId != null);
  // Rows: validators that cast this vote (including other orgs), plus platform-managed ones that haven't voted yet
  const rows = [
    ...vote.voters.map((x) => ({ address: x.address, voter: x })),
    ...missing.map((id) => ({ address: set.data?.find((x) => x.nodeId === id)?.address ?? id, voter: undefined })),
  ];

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-4">
      <BackToValidators />
      <header className="flex flex-wrap items-center gap-3">
        <h1 className="inline-flex min-w-0 items-center gap-2 break-all text-lg font-semibold text-ink">
          {t(vote.add ? 'validators.add' : 'validators.remove')} <span className="font-mono">{vote.address}</span> <CopyButton value={vote.address} variant="cell" />
        </h1>
        {canWrite && (
          <span className="ml-auto flex min-h-[calc(1.75rem+2px)] items-center gap-2">
            {missing.length > 0 && (
              <Button onClick={() => { revote.reset(); setConfirmRevote(true); }} disabled={revote.isPending} className="inline-flex items-center gap-1.5">
                <ArrowClockwise size={15} aria-hidden /> {t(ownVoted ? 'validators.revoteN' : 'validators.voteN', { n: missing.length })}
              </Button>
            )}
            {ownSending(vote) && <SendingNote />}
            {ownWithdrawing(vote) && <WithdrawingNote />}
            {/* Withdrawal only works once our vote is on chain; for proposals started by other orgs that we didn't vote on, or already withdrawn, there is no vote to withdraw */}
            {ownCounted(vote) && (
              <Button variant="dangerOutline" onClick={() => { discard.reset(); setConfirmDiscard(true); }} className="inline-flex items-center gap-1.5">
                <ArrowCounterClockwise size={15} aria-hidden /> {t('validators.discard')}
              </Button>
            )}
          </span>
        )}
      </header>
      {/* Resending changes the validator set just the same, so confirm first like proposing and approving */}
      {confirmRevote && (
        <ProposeConfirm
          address={vote.address}
          add={vote.add}
          confirmLabel={t(ownVoted ? 'validators.revoteN' : 'validators.voteN', { n: missing.length })}
          busy={revote.isPending}
          error={revote.error}
          onConfirm={() => revote.mutate()}
          onCancel={() => setConfirmRevote(false)}
        />
      )}
      {/* No need to list when all were sent: our hollow cells on the vote bar show it; only list per-node reasons when some validators failed to send */}
      {revote.data?.results.some((x) => !x.ok) && <section className="rounded-lg border border-hair bg-surface px-4 py-3"><VoteResults results={revote.data.results} /></section>}
      {revote.isError && !confirmRevote && <p role="alert" className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>{errMsg(revote.error) || t('error.title')}</p>}

      <section className="overflow-hidden rounded-lg border border-hair bg-surface">
        <div className="flex min-h-[54px] flex-wrap items-center gap-2 border-b border-hair px-4 py-2">
          <h2 className="text-sm font-semibold text-ink">{t('validators.voters')}</h2>
          <span className="ml-auto flex items-center gap-3 text-xs text-ink-3">
            {from != null && <span>{t('validators.backfilling', { n: fmtInt(from) })}</span>}
            <VoteProgress vote={vote} needed={v.needed} known={v.tallyKnown} />
          </span>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="table-head">
                <th className="px-4">{t('validators.colNode')}</th>
                <th className="px-4">{t('validators.colThisVote')}</th>
                <th className="px-4">{t('validators.colProducing')}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map(({ address, voter }) => {
                const entry = set.data?.find((x) => sameAddr(x.address, address));
                const name = validatorName(entry) ?? voter?.nodeId;
                return (
                  <tr key={address} className="border-b border-hair last:border-0">
                    <td className="px-4 py-2.5">
                      <span className="block text-ink">{name ?? <span className="font-mono">{address}</span>}</span>
                      {name && <span className="block font-mono text-xs text-ink-3">{address}</span>}
                    </td>
                    <td className="px-4 py-2.5 text-xs text-ink-2">
                      {voter?.countedAt != null
                        ? v.tallyKnown ? <>{t('validators.countedAt')} <span className="font-mono text-ink-3">#{voter.countedAt}</span></> : <span className="text-ink-3">—</span>
                        : voter ? t('validators.sentWaiting') : t('validators.notVoted')}
                    </td>
                    <td className="px-4 py-2.5 text-xs">
                      <ProducingLabel turns={entry?.turns ?? []} producing={entry?.producing} />
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </section>

      {confirmDiscard && (
        <ConfirmModal
          title={t('validators.discard')}
          body={discard.isError ? errMsg(discard.error) || t('error.title') : t('validators.discardBody')}
          confirmLabel={t('validators.discard')}
          danger
          busy={discard.isPending}
          onConfirm={() => discard.mutate()}
          onCancel={() => setConfirmDiscard(false)}
        />
      )}
    </div>
  );
}
