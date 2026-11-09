import { useEffect, useState } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Plus, ShareNetwork, SlidersHorizontal } from '@phosphor-icons/react';
import { api, HttpError } from '@/api/client';
import type { JoinToken, NodeSummary } from '@/api/schemas';
import { ButtonLink } from '@/components/Button';
import { ConfirmModal } from '@/components/ConfirmModal';
import { errMsg } from '@/lib/errors';
import { SkeletonRows, EmptyState, ErrorState } from '@/components/States';
import { NodesTable } from '@/components/NodesTable';
import { SharedPeersPanel } from '@/components/SharedPeersPanel';
import { PageTabs } from '@/components/PageTabs';
import { useAuth } from '@/auth/AuthContext';

/**
 * Node registration & settings (/node-join) = own nodes + join codes awaiting join (pending tokens); peer settings (/node-join/peers) = the shared peer list.
 * Registering a node uses the separate page /node-add (NodeAdd); node rows open the node settings page; pending codes can be revoked.
 */
export function NodeJoin() {
  const { t } = useTranslation();
  const loc = useLocation();
  const p2p = loc.pathname === '/node-join/peers';
  // Back from "Done" in node registration: highlight that row, with a one-line status above (awaiting join -> joined once connected).
  // Only for this view: once copied into state, clear the history state, so reload or coming back doesn't highlight it again
  const nav = useNavigate();
  const [added] = useState(() => (loc.state as { added?: string } | null)?.added);
  useEffect(() => { if (loc.state) nav(loc.pathname, { replace: true, state: null }); }, []); // eslint-disable-line react-hooks/exhaustive-deps
  const { can } = useAuth();
  const canOnboard = can('nodes:onboard'); // RBAC: joining nodes requires Operator/Admin
  const qc = useQueryClient();
  // Revoke a waiting code, or remove a registration whose code expired: either way the latest code becomes revoked and the node is no longer listed (history is kept)
  const [removing, setRemoving] = useState<{ tk: JoinToken; expired: boolean } | null>(null);
  const remove = useMutation({
    mutationFn: (tk: JoinToken) => api.revokeJoinToken(tk.id),
    onSuccess: async () => { await qc.invalidateQueries({ queryKey: ['joinTokens'] }); setRemoving(null); },
    // 409: the code was used (the node joined) or revoked meanwhile; refresh now so the row moves on instead of waiting for the next poll
    onError: (e) => {
      if (e instanceof HttpError && e.status === 409) {
        void qc.invalidateQueries({ queryKey: ['joinTokens'] });
        void qc.invalidateQueries({ queryKey: ['nodes'] });
      }
    },
  });

  const nodesQ = useQuery({
    queryKey: ['nodes'],
    queryFn: () => api.listNodes(),
    select: (r) => r.nodes,
    refetchInterval: 15_000,
  });
  const tokensQ = useQuery({
    queryKey: ['joinTokens'],
    queryFn: () => api.listJoinTokens(),
    refetchInterval: 15_000,
  });

  const nodes: NodeSummary[] = nodesQ.data ?? [];
  // Registered but not yet joined nodes stay listed (also with an expired code, saying it expired and must be regenerated to join) until they join.
  // Only the latest code per node counts: generating a new one revokes the older ones, and if the latest is revoked (abandoned) it isn't listed.
  // The two lists refresh independently: after a join the node list may update first while the code list still says pending; anything already in the node list is skipped so the same node doesn't show twice
  const latest = new Map<string, JoinToken>();
  for (const tk of tokensQ.data ?? []) {
    const k = tk.nodeId;
    const cur = latest.get(k);
    if (!cur || Date.parse(tk.createdAt) > Date.parse(cur.createdAt)) latest.set(k, tk);
  }
  const pending = [...latest.values()].filter((tk) => (tk.status === 'pending' || tk.status === 'expired') && !nodes.some((n) => n.id === tk.nodeId));
  const isLoading = nodesQ.isLoading || tokensQ.isLoading;
  const isError = nodesQ.isError || tokensQ.isError;
  const empty = nodes.length === 0 && pending.length === 0;

  const addButton = (
    <ButtonLink to="/node-add" className="shrink-0 gap-1.5">
      <Plus size={15} aria-hidden="true" />
      {t('join.addNode')}
    </ButtonLink>
  );

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-4">
      {/* Two routes, two sidebar entries under platform settings (node settings and peering); each gets its own title, no tab bar */}
      <PageTabs
        title={t(p2p ? 'join.titlePeers' : 'join.title')}
        subtitle={t(p2p ? 'join.subtitlePeers' : 'join.subtitle')}
        help={p2p ? t('join.peersScope') : undefined}
        action={!p2p && canOnboard ? addButton : undefined}
        tabs={[p2p ? { to: '/node-join/peers', label: t('nodesPage.tabP2p'), icon: ShareNetwork } : { to: '/node-join', label: t('nodesPage.tabNodes'), icon: SlidersHorizontal }]}
      />

      {p2p ? (
        <SharedPeersPanel />
      ) : isLoading ? (
        <SkeletonRows rows={4} />
      ) : isError ? (
        <ErrorState onRetry={() => void nodesQ.refetch()} />
      ) : empty ? (
        <EmptyState message={t('join.empty')} action={canOnboard ? addButton : undefined} />
      ) : (
        <NodesTable nodes={nodes} pending={pending} highlight={added} canAdd={canOnboard} onRemove={canOnboard ? (tk, expired) => { remove.reset(); setRemoving({ tk, expired }); } : undefined} />
      )}

      {removing && (
        <ConfirmModal
          title={t(removing.expired ? 'join.removeTitle' : 'join.revokeTitle', { node: removing.tk.nodeName ?? removing.tk.nodeId })}
          body={remove.isError ? errMsg(remove.error) || t('error.title') : t(removing.expired ? 'join.removeBody' : 'join.revokeBody')}
          confirmLabel={t(removing.expired ? 'join.remove' : 'join.revoke')}
          danger
          busy={remove.isPending}
          onConfirm={() => remove.mutate(removing.tk)}
          onCancel={() => setRemoving(null)}
        />
      )}
    </div>
  );
}
