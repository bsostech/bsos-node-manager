import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { UserMinus } from '@phosphor-icons/react';
import { api } from '@/api/client';
import type { ValidatorSetEntry } from '@/api/schemas';
import { useAuth } from '@/auth/AuthContext';
import { Button } from '@/components/Button';
import { CopyButton } from '@/components/CopyButton';
import { inputCls } from '@/components/Field';
import { Skeleton, EmptyState, ErrorState } from '@/components/States';
import { ProducingLabel, TurnCells } from '@/components/TurnCells';
import { errMsg } from '@/lib/errors';
import { countedOf, proposalKey, sameAddr, stillOpen } from '@/lib/validators';
import { BackLink, GoLink } from '@/components/TextLinks';

const FOCUS = 'focus:outline-none focus-visible:ring-1 focus-visible:ring-accent';

export function BackToValidators() {
  const { t } = useTranslation();
  return (
    <BackLink to="/validators">{t('validators.title')}</BackLink>
  );
}

/** A single validator: name setting, each of its turns in the last 1000 blocks, propose removal. */
export function ValidatorDetail() {
  const { t } = useTranslation();
  const { address = '' } = useParams();
  const { can } = useAuth();
  const canWrite = can('nodes:onboard');
  const set = useQuery({ queryKey: ['validators'], queryFn: api.listValidatorSet, refetchInterval: 15_000 });
  const votes = useQuery({ queryKey: ['validator-votes'], queryFn: api.listValidatorVotes });
  const v = set.data?.find((x) => sameAddr(x.address, address));
  const removal = votes.data?.votes.find((x) => !x.add && sameAddr(x.address, address) && stillOpen(x));

  if (set.isLoading) return <div className="mx-auto flex max-w-5xl flex-col gap-4"><BackToValidators /><Skeleton className="h-10 w-96" /></div>;
  if (set.isError || !v) {
    return (
      <div className="mx-auto flex max-w-5xl flex-col gap-4">
        <BackToValidators />
        {set.isError ? <ErrorState onRetry={() => void set.refetch()} /> : <EmptyState message={t('validators.notInSet')} />}
      </div>
    );
  }

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-4">
      <BackToValidators />
      <header className="flex flex-wrap items-center gap-3">
        <h1 className="inline-flex min-w-0 items-center gap-2 break-all font-mono text-lg font-semibold text-ink">
          {v.address} <CopyButton value={v.address} variant="cell" />
        </h1>
        <span className="ml-auto"><ProducingLabel turns={v.turns} producing={v.producing} /></span>
      </header>

      {/* Own nodes are renamed in node settings (peer_names doesn't accept own addresses); other validators are named here.
          key: switching validators or saving a new name resets the input */}
      {v.nodeId ? <OwnNodeName v={v} canRename={can('nodes:onboard')} /> : <NameEditor key={`${v.address}:${v.name ?? ''}`} v={v} canWrite={canWrite} />}

      <section className="overflow-hidden rounded-lg border border-hair bg-surface">
        <h2 className="flex min-h-[54px] items-center gap-2 border-b border-hair px-4 py-2 text-sm font-semibold text-ink">{t('validators.colTurns', { n: 40 })}</h2>
        <div className="overflow-x-auto px-4 py-3"><TurnCells turns={v.turns} size="large" /></div>
      </section>

      {removal && votes.data ? (
        <section className="flex flex-wrap items-center gap-2 rounded-lg border border-hair bg-surface px-4 py-3 text-sm text-ink-2">
          <UserMinus size={15} aria-hidden /> {votes.data.tallyKnown ? t('validators.removePending', { n: countedOf(removal), total: votes.data.ownValidators.length }) : `${t('validators.removeProposed')} · ${t('validators.countsNeedAdmin')}`}
          <GoLink to={`/validators/proposals/${proposalKey(removal)}`}>{t('validators.viewProposal')}</GoLink>
        </section>
      ) : canWrite && (
        <div>
          <Link to={`/validators?propose=remove&address=${v.address}`} className={`inline-flex items-center gap-1.5 rounded-md border border-strong px-3 py-1.5 text-xs text-ink-2 hover:bg-elevated hover:text-ink ${FOCUS}`}>
            <UserMinus size={15} aria-hidden /> {t('validators.proposeRemove')}
          </Link>
        </div>
      )}
    </div>
  );
}

/** Own validator: show the node name or id, renaming links to that node's settings. */
function OwnNodeName({ v, canRename }: { v: ValidatorSetEntry; canRename: boolean }) {
  const { t } = useTranslation();
  return (
    <section className="overflow-hidden rounded-lg border border-hair bg-surface">
      <h2 className="flex min-h-[54px] items-center gap-2 border-b border-hair px-4 py-2 text-sm font-semibold text-ink">{t('validators.colName')}</h2>
      <div className="flex flex-wrap items-center gap-3 px-4 py-3 text-sm">
        <span className="text-ink">{v.name ?? <span className="font-mono">{v.nodeId}</span>}</span>
        {v.name && <span className="font-mono text-xs text-ink-3">{v.nodeId}</span>}
        {canRename && (
          <span className="ml-auto"><GoLink to={`/nodes/${encodeURIComponent(v.nodeId!)}/settings`}>{t('validators.renameInSettings')}</GoLink></span>
        )}
      </div>
    </section>
  );
}

/** Name: user-defined, stored by address (PUT /peer-names/{address}); only for validators outside this org. */
function NameEditor({ v, canWrite }: { v: ValidatorSetEntry; canWrite: boolean }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [draft, setDraft] = useState(v.name ?? '');
  const save = useMutation({
    mutationFn: (name: string) => (name ? api.setPeerName(v.address, name) : api.deletePeerName(v.address)),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['validators'] });
      void qc.invalidateQueries({ queryKey: ['nodePeers'] });
    },
  });
  const dirty = draft.trim() !== (v.name ?? '');
  return (
    <section className="overflow-hidden rounded-lg border border-hair bg-surface">
      <h2 className="flex min-h-[54px] items-center gap-2 border-b border-hair px-4 py-2 text-sm font-semibold text-ink">{t('validators.colName')}</h2>
      <form
        className="flex flex-wrap items-center gap-2 px-4 py-3"
        onSubmit={(e) => { e.preventDefault(); save.mutate(draft.trim()); }}
      >
        <input
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          disabled={!canWrite}
          maxLength={64}
          aria-label={t('validators.colName')}
          placeholder={t('validators.namePlaceholder')}
          className={`w-80 max-w-full ${inputCls}`}
        />
        {canWrite && (
          <>
            <Button type="submit" variant="primary" disabled={!dirty || save.isPending}>{t('validators.save')}</Button>
            {v.name && <Button onClick={() => { setDraft(''); save.mutate(''); }} disabled={save.isPending}>{t('validators.clearName')}</Button>}
          </>
        )}
        {save.isError && <span role="alert" className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>{errMsg(save.error) || t('error.title')}</span>}
      </form>
    </section>
  );
}
