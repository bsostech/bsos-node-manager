import { useState } from 'react';
import { useAuth } from '@/auth/AuthContext';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { CaretRight, Plus } from '@phosphor-icons/react';
import { api } from '@/api/client';
import { Button } from '@/components/Button';
import { Card } from '@/components/Card';
import { HelpTip } from '@/components/HelpTip';
import { ErrorState, SkeletonRows } from '@/components/States';
import { PermChips, RpcUserFormModal } from '@/components/RpcUserModals';
import { presenceRef, usePresence } from '@/hooks/usePresence';

/**
 * Our org's RPC access identities (named method-permission sets, one set per organization). The platform signs tokens with a single key,
 * and every auth-enabled node of our org accepts them, so they are not bound to nodes. Rows show only name and methods; issuing tokens and other actions are on the detail page.
 */
export function RpcUsersCard() {
  const { t } = useTranslation();
  const nav = useNavigate();
  const q = useQuery({ queryKey: ['rpcUsers'], queryFn: api.listRpcUsers });
  const [adding, setAdding] = useState(false);
  const users = q.data ?? [];
  // After add / delete the row expands and fades in, or fades out and collapses; no animation on first load. Each cell's content is wrapped in .row-cell, which is the layer that animates
  const rows = usePresence(users, (u) => u.id, q.isSuccess);
  // Viewers only see identities and methods, with no add
  const canWrite = useAuth().can('nodes:onboard');

  return (
    <Card
      title={<>{t('rpcUsers.title')}<HelpTip title={t('rpcUsers.title')}>{t('rpcUsers.help')}</HelpTip></>}
      aside={canWrite ? (
        <Button className="inline-flex items-center gap-1" onClick={() => setAdding(true)}>
          <Plus size={13} aria-hidden="true" />{t('rpcUsers.add')}
        </Button>
      ) : undefined}
      bodyClass=""
    >
      {q.isError && <ErrorState inCard onRetry={() => void q.refetch()} />}
      {q.isLoading ? <div className="p-3"><SkeletonRows rows={3} /></div> : q.isSuccess && users.length === 0 ? (
        <p className="px-4 py-8 text-center text-xs text-ink-3">{t('rpcUsers.empty')}</p>
      ) : rows.length > 0 && (
        <div className="max-h-[32rem] overflow-auto overscroll-contain">
          <table className="w-full min-w-[28rem] table-fixed text-sm">
            <colgroup><col className="w-[12rem]" /><col /><col className="w-10" /></colgroup>
            <thead>
              <tr className="table-head">
                <th className="sticky top-0 z-10 px-4">{t('rpcUsers.colName')}</th>
                <th className="sticky top-0 z-10 px-4">{t('rpcUsers.methods')}</th>
                <th className="sticky top-0 z-10" aria-hidden="true" />
              </tr>
            </thead>
            <tbody>
              {rows.map(({ key, item: u, state }) => {
                const to = `/access/rpc-users/${encodeURIComponent(u.id)}`;
                return (
                  <tr key={key} onClick={() => nav(to)} ref={presenceRef(state)} className={`group cursor-pointer border-b border-hair transition-colors align-top last:border-0 hover:bg-elevated`}>
                    <td className="px-4 py-2.5">
                      <div className="row-cell">
                        <Link to={to} onClick={(e) => e.stopPropagation()} className="break-all rounded-sm font-mono text-xs text-ink group-hover:text-accent focus:outline-none focus-visible:ring-1 focus-visible:ring-accent">
                          {u.username}
                        </Link>
                      </div>
                    </td>
                    <td className="px-4 py-2.5"><div className="row-cell"><PermChips perms={u.permissions} /></div></td>
                    {/* Issue and delete are both on the detail page; the row just links there */}
                    <td className="px-3 py-2.5 text-right"><div className="row-cell"><CaretRight size={14} aria-hidden="true" className="inline text-ink-3" /></div></td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {adding && <RpcUserFormModal taken={users.map((u) => u.username)} onClose={() => setAdding(false)} />}
    </Card>
  );
}
