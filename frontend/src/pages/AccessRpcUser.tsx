import { useState } from 'react';
import { useAuth } from '@/auth/AuthContext';
import { useTranslation } from 'react-i18next';
import { useNavigate, useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Key } from '@phosphor-icons/react';
import { api } from '@/api/client';
import type { RpcUserEvent } from '@/api/schemas';
import { Button } from '@/components/Button';
import { Card } from '@/components/Card';
import { Listbox } from '@/components/Listbox';
import { EmptyState, ErrorState, Skeleton, SkeletonRows } from '@/components/States';
import { DeleteRpcUserModal, PermChips, RpcEventModal, SignTokenModal, TokenState, isValidToken, validTokens } from '@/components/RpcUserModals';
import { fmtDateTimeMin } from '@/lib/format';
import { BackLink } from '@/components/TextLinks';
import { HelpTip } from '@/components/HelpTip';

const FOCUS = 'focus:outline-none focus-visible:ring-1 focus-visible:ring-accent';

function Back() {
  const { t } = useTranslation();
  return (
    <BackLink to="/access">{t('rpcUsers.title')}</BackLink>
  );
}

type Open = 'sign' | 'delete' | null;

/**
 * A single RPC identity: callable methods, history (created / tokens issued), issuing, deletion. Methods can't change after creation (tokens under one identity share the same permissions);
 * for different permissions create another identity.
 * Routes use the id, not the name: deleting and recreating the same name is a different user and history doesn't carry over. Tokens can't be recalled, so there is no revoke.
 */
export function AccessRpcUser() {
  const { t } = useTranslation();
  // Viewers only see the identity, methods and issue history; delete and issuing new tokens are operator-only
  const canWrite = useAuth().can('nodes:onboard');
  const { id = '' } = useParams();
  const nav = useNavigate();
  const usersQ = useQuery({ queryKey: ['rpcUsers'], queryFn: api.listRpcUsers });
  const u = usersQ.data?.find((x) => x.id === id);
  const evQ = useQuery({ queryKey: ['rpcUserEvents', id], queryFn: () => api.listRpcUserEvents(id), enabled: !!u });
  const [all, setAll] = useState<'valid' | 'all'>('valid');
  const [open, setOpen] = useState<Open>(null);
  const [ev, setEv] = useState<RpcUserEvent | null>(null);

  if (usersQ.isLoading) return <div className="mx-auto flex w-full max-w-5xl flex-col gap-4"><Back /><Skeleton className="h-10 w-72" /></div>;
  if (!u) {
    return (
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-4">
        <Back />
        {usersQ.isError ? <ErrorState onRetry={() => void usersQ.refetch()} /> : <EmptyState message={t('rpcUsers.notFound')} />}
      </div>
    );
  }

  const events = evQ.data ?? [];
  // The history card only lists token issues (valid / including expired); who created it and when goes under the name
  const issued = events.filter((e) => e.kind === 'token_issued');
  const rows = all === 'valid' ? validTokens(events) : issued;
  const created = events.find((e) => e.kind === 'created');

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-4">
      <Back />
      <header className="flex flex-wrap items-center gap-3">
        <div className="flex min-w-0 flex-col gap-0.5">
          <h1 className="break-all font-mono text-lg font-semibold text-ink">{u.username}</h1>
          <span className="text-xs text-ink-3">
            {created?.actor ? t('rpcUsers.createdBy', { actor: created.actor, time: fmtDateTimeMin(created.at) }) : t('rpcUsers.createdAt', { time: fmtDateTimeMin(u.createdAt) })}
          </span>
        </div>
        {canWrite && (
          <div className="ml-auto flex items-center gap-2">
            <Button variant="dangerOutline" disabled={!evQ.isSuccess} onClick={() => setOpen('delete')}>{t('rpcUsers.delete')}</Button>
          </div>
        )}
      </header>

      <Card title={<>{t('rpcUsers.methods')}<HelpTip title={t('rpcUsers.methods')}>{t('rpcUsers.permsFixedHelp')}</HelpTip></>} bodyClass="px-4 py-3"><PermChips perms={u.permissions} /></Card>

      <Card
        title={<>{t('rpcUsers.history')}<HelpTip title={t('rpcUsers.history')}>{t('rpcUsers.noRevoke')}</HelpTip></>}
        aside={(
          <>
            {/* The filter label matches the "Status" column of the table below, and the options use the column's words ("Valid" / "All", incl. expired) */}
            <label className="flex items-center gap-2 text-xs text-ink-3">
              {t('rpcUsers.colState')}
              <Listbox width="w-auto" value={all} onChange={setAll} aria-label={t('rpcUsers.colState')} options={[{ value: 'valid', label: t('common.withCount', { label: t('rpcUsers.histValid'), n: validTokens(events).length }) }, { value: 'all', label: t('common.withCount', { label: t('rpcUsers.histAll'), n: issued.length }) }]} />
            </label>
            {/* Issuing lives on the history card (like the add-identity card button), not as a big page-header button */}
            {canWrite && (
              <Button className="inline-flex items-center gap-1" onClick={() => setOpen('sign')}>
                <Key size={13} aria-hidden="true" />{t('rpcUsers.issue')}
              </Button>
            )}
          </>
        )}
        bodyClass=""
      >
        {evQ.isError ? <ErrorState inCard onRetry={() => void evQ.refetch()} />
          : evQ.isLoading ? <div className="p-3"><SkeletonRows rows={3} /></div>
          : rows.length === 0 ? <p className="px-4 py-8 text-center text-xs text-ink-3">{t(issued.length ? 'rpcUsers.noValid' : 'rpcUsers.noTokens')}</p>
          : (
            <div className="max-h-[28rem] overflow-y-auto overscroll-contain">
              <table className="w-full text-sm">
                <thead className="sticky top-0 z-10">
                  <tr className="table-head">
                    <th className="whitespace-nowrap px-3">{t('rpcUsers.fIssuedAt')}</th>
                    <th className="min-w-24 px-3">{t('rpcUsers.label')}</th>
                    <th className="whitespace-nowrap px-3">{t('rpcUsers.tokenId')}</th>
                    <th className="whitespace-nowrap px-3">{t('rpcUsers.fIssuer')}</th>
                    <th className="whitespace-nowrap px-3">{t('rpcUsers.expires')}</th>
                    <th className="whitespace-nowrap px-3">{t('rpcUsers.colState')}</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((e) => {
                    const token = e.kind === 'token_issued';
                    const valid = isValidToken(e);
                    return (
                      <tr
                        key={e.id}
                        tabIndex={0}
                        role="button"
                        onClick={() => setEv(e)}
                        onKeyDown={(k) => { if (k.key === 'Enter' || k.key === ' ') { k.preventDefault(); setEv(e); } }}
                        className={`group cursor-pointer border-b border-hair transition-colors align-top last:border-0 hover:bg-elevated ${FOCUS} ${token && !valid ? 'text-ink-3' : ''}`}
                      >
                        <td className="whitespace-nowrap px-3 py-2.5 tabular-nums">{fmtDateTimeMin(e.at)}</td>
                        <td className="min-w-24 px-3 py-2.5">{e.label || <span className="text-ink-3">—</span>}</td>
                        <td className="whitespace-nowrap px-3 py-2.5 font-mono text-xs text-ink-2">{e.tokenId ?? <span className="text-ink-3">—</span>}</td>
                        <td className="whitespace-nowrap px-3 py-2.5">{e.actor || '—'}</td>
                        <td className="whitespace-nowrap px-3 py-2.5 tabular-nums">{fmtDateTimeMin(e.expiresAt)}</td>
                        <td className="whitespace-nowrap px-3 py-2.5 text-xs">{token ? <TokenState e={e} /> : <span className="text-ink-3">—</span>}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
      </Card>

      {open === 'sign' && <SignTokenModal user={u} onClose={() => setOpen(null)} />}
      {open === 'delete' && <DeleteRpcUserModal user={u} events={events} onClose={() => setOpen(null)} onDeleted={() => nav('/access')} />}
      {ev && <RpcEventModal user={u} e={ev} onClose={() => setEv(null)} />}
    </div>
  );
}
