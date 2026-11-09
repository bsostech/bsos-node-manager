import { useUrlParam } from '@/hooks/useUrlParam';
import { useAuth } from '@/auth/AuthContext';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import { AccessTabs } from '@/components/AccessTabs';
import { ErrorState, Notice, Skeleton } from '@/components/States';
import { NodeIdentityScopeCard, RpcAuthStatusCard, rpcAuthCode, type RpcAuthCode } from '@/components/RpcAuthNodes';
import { isEnterprise } from '@/edition';
import { RpcUsersCard } from '@/components/RpcUsersManager';

const USERS = '*';
// Tab status dot: green when auth is on, red on public-key mismatch, grey otherwise (same as the transaction address allowlist)
const DOT: Record<RpcAuthCode, string> = {
  on: 'var(--bsos-status-healthy-fg)', keyMismatch: 'var(--bsos-status-error-fg)',
  off: 'var(--bsos-text-faint)', unknown: 'var(--bsos-text-faint)', offline: 'var(--bsos-text-faint)',
};

/**
 * Node access settings. Tabs sit outside the card (same as the address allowlist and peer settings): "RPC access identities" comes first (one set per org;
 * its tokens are accepted by every node in this org with auth on), then one tab per node showing whether auth is on, with the enable guide when it is off.
 */
export function AccessOverview() {
  const { t } = useTranslation();
  const canWrite = useAuth().can('nodes:onboard');
  const q = useQuery({ queryKey: ['nodes'], queryFn: () => api.listNodes(), refetchInterval: 15_000 });
  const users = useQuery({ queryKey: ['rpcUsers'], queryFn: api.listRpcUsers });
  // Tab goes into the URL (?node=node-a1; RPC identities adds no param) so links from the node settings page open straight to that node
  const [tab, setTab] = useUrlParam<string>('node', USERS);
  const nodes = q.data?.nodes ?? [];
  const active = tab === USERS || nodes.some((n) => n.id === tab) ? tab : USERS; // fall back to RPC identities when the node is removed
  const noneOn = !!q.data && nodes.length > 0 && !nodes.some((n) => rpcAuthCode(n) === 'on');

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-4">
      <AccessTabs page="rpc" />
      {q.isError && q.data && <Notice>{t('access.stale')}</Notice>}
      {q.isError && !q.data && <ErrorState onRetry={() => void q.refetch()} />}
      {q.isLoading && <div className="flex flex-col gap-3"><Skeleton className="h-8 w-56" /><Skeleton className="h-40" /></div>}
      {q.data && (<>
        <div role="tablist" aria-label={t('access.title')} className="flex items-end gap-1 overflow-x-auto border-b border-hair">
          {[USERS, ...nodes.map((n) => n.id)].map((id) => {
            const sel = id === active;
            const node = nodes.find((n) => n.id === id);
            return (
              <button key={id} type="button" role="tab" aria-selected={sel} onClick={() => setTab(id)}
                className={`flex items-center gap-1.5 whitespace-nowrap border-b-2 px-3 py-2 text-sm transition-colors ${sel ? 'border-accent font-medium text-accent' : 'border-transparent text-ink-2 hover:text-ink'}`}>
                {node && <span aria-hidden="true" className="size-1.5 shrink-0 rounded-full" style={{ background: DOT[rpcAuthCode(node)] }} />}
                {node ? <span className="font-mono">{id}</span> : t('access.tabRpcUsers')}
                {!node && users.data && <span className="font-mono text-2xs text-ink-3">{users.data.length}</span>}
              </button>
            );
          })}
        </div>
        {active === USERS ? (
          <div role="tabpanel" className="flex flex-col gap-4">
            {/* No node has auth on: tokens can be issued but have nowhere to be used, so say so up front */}
            {noneOn && <Notice tone="info">{t('rpcAuth.noneOn')}{canWrite && t('rpcAuth.noneOnFix')}</Notice>}
            <RpcUsersCard />
          </div>
        ) : (
          <div role="tabpanel" className="flex flex-col gap-4">
            {nodes.filter((n) => n.id === active).map((n) => <RpcAuthStatusCard key={n.id} node={n} onViewUsers={() => setTab(USERS)} />)}
            {/* Enterprise teaser: choose per node which identities may connect (Core allows all; the switch is locked). Only shown when this node has auth on:
                without auth anyone can connect, and offline nodes have no readable settings, so listing identities would mean nothing */}
            {!isEnterprise && nodes.some((n) => n.id === active && rpcAuthCode(n) === 'on') && <NodeIdentityScopeCard users={users.data ?? []} />}
          </div>
        )}
      </>)}
    </div>
  );
}
