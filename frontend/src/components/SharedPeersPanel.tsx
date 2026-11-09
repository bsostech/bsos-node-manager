import { useState } from 'react';
import { PencilSimple } from '@phosphor-icons/react';
import { NameEditor } from '@/components/NameEditor';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Plus } from '@phosphor-icons/react';
import { api } from '@/api/client';
import type { NodeSummary, SharedPeer } from '@/api/schemas';
import { Button, RowAction } from '@/components/Button';
import { ConfirmModal } from '@/components/ConfirmModal';
import { CopyButton } from '@/components/CopyButton';
import { HelpTip } from '@/components/HelpTip';
import { CountChip } from '@/components/ValueChip';
import { NodeLabel } from '@/components/NodeLabel';
import { AddPeerModal } from '@/components/AddPeerModal';
import { SkeletonRows, ErrorState } from '@/components/States';
import { apiState } from '@/lib/capabilities';
import { NodePeerAdditionsCard, NodePeerStatusCard, peerNodeState, type PeerUsage, isOffline } from '@/components/NodePeerCards';
import { errMsg } from '@/lib/errors';
import { useUrlParam } from '@/hooks/useUrlParam';
import { enodeHostPort } from '@/lib/format';
import { useAuth } from '@/auth/AuthContext';
import { EnterpriseTeaser } from '@/components/EnterpriseTeaser';
import { isEnterprise } from '@/edition';
import { useSavedFlash } from '@/hooks/useSavedFlash';
import { presenceRef, usePresence } from '@/hooks/usePresence';


/** Tab key: the shared list; other tabs use the node id */
const SHARED = '*';
const DOT = { healthy: 'var(--bsos-status-healthy-fg)', syncing: 'var(--bsos-status-syncing-fg)', neutral: 'var(--bsos-text-faint)' } as const;

/**
 * Peer settings page (same layout as transaction address authorization): tabs sit outside the card -- "Shared list" first, then one tab per node.
 * The shared list tab has one card; a node tab has one for "connection status + apply-shared-list switch" and one for "Peers added here".
 * Shared list usage comes entirely from GET /shared-peers (admin_peers from the collector's latest round); the platform makes no extra RPC calls.
 */
export function SharedPeersPanel() {
  const { t } = useTranslation();
  const q = useQuery({ queryKey: ['sharedPeers'], queryFn: api.listSharedPeers, refetchInterval: 15_000 });
  const nodesQ = useQuery({ queryKey: ['nodes'], queryFn: () => api.listNodes(), select: (r) => r.nodes, refetchInterval: 15_000 });
  // The tab goes in the URL (?node=node-a1; the shared list has no param), so "manage peers" on node detail opens straight to that node
  const [tab, setTab] = useUrlParam<string>('node', SHARED);

  const usage = new Map((q.data?.nodes ?? []).map((n) => [n.id, n]));
  const nodes = (nodesQ.data ?? []).filter((n) => usage.has(n.id));
  const active = tab === SHARED || nodes.some((n) => n.id === tab) ? tab : SHARED; // fall back to the shared list when the node has been removed
  const node = nodes.find((n) => n.id === active);

  return (
    <>
      <div role="tablist" aria-label={t('join.titlePeers')} className="flex items-end gap-1 overflow-x-auto border-b border-hair">
        {[SHARED, ...nodes.map((n) => n.id)].map((id) => {
          const sel = id === active;
          const u = usage.get(id);
          const st = id === SHARED ? null : peerNodeState(t, u, nodes.find((n) => n.id === id));
          return (
            <button key={id} type="button" role="tab" aria-selected={sel} onClick={() => setTab(id)}
              className={`flex items-center gap-1.5 whitespace-nowrap border-b-2 px-3 py-2 text-sm transition-colors ${sel ? 'border-accent font-medium text-accent' : 'border-transparent text-ink-2 hover:text-ink'}`}>
              {/* Node tab: status dot (all connected green, some not connected orange, unused / unknown gray) + "+N" when there are self-added peers */}
              {st && <span aria-hidden="true" className="size-1.5 shrink-0 rounded-full" style={{ background: DOT[st.tone] }} />}
              {id === SHARED ? t('extraPeers.sharedTitle') : <span className="font-mono">{id}</span>}
              {id === SHARED ? <span className="font-mono text-2xs text-ink-3">{q.data?.peers.length ?? ''}</span>
                : !!u?.additions && <span className="font-mono text-2xs text-ink-3">+{u.additions}</span>}
            </button>
          );
        })}
      </div>
      {active === SHARED || !node ? (
        <SharedListCard usage={usage} nodes={nodes} />
      ) : (
        <div role="tabpanel" className="flex flex-col gap-4">
          <NodePeerStatusCard node={node} usage={usage.get(node.id)} onViewShared={() => setTab(SHARED)} />
          {/* Self-added peers: with ADMIN off (not enabled) or offline the platform can neither see nor change them, so the whole card is hidden (same as node access settings) */}
          {!usage.get(node.id)?.quarantined && apiState(node.capabilities, 'ADMIN') !== 'off' && !isOffline(node) && <NodePeerAdditionsCard nodeId={node.id} adminOff={false} />}
        </div>
      )}
    </>
  );
}

/** Shared list tab: one peer list for our org (add, rename, remove); every node with "Use shared list" on connects to them. */
function SharedListCard({ usage, nodes }: { usage: Map<string, PeerUsage>; nodes: NodeSummary[] }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const q = useQuery({ queryKey: ['sharedPeers'], queryFn: api.listSharedPeers, refetchInterval: 15_000 });
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<SharedPeer | null>(null);
  const savedFlash = useSavedFlash();
  const [removing, setRemoving] = useState<SharedPeer | null>(null);

  const invalidate = () => void qc.invalidateQueries({ queryKey: ['sharedPeers'] });
  const remove = useMutation({
    mutationFn: (pubkey: string) => api.deleteManualPeer(pubkey),
    onSuccess: () => { setRemoving(null); invalidate(); },
  });

  const peers = q.data?.peers ?? [];
  // On add / remove the row expands and fades in, or fades out and collapses; no animation on first load. Each cell's content is wrapped in .row-cell, which is the layer that animates
  const rows = usePresence(peers, (p) => p.pubkey, !!q.data);
  // Viewers are read-only: add, naming and remove do not appear
  const canWrite = useAuth().can('nodes:onboard');
  // When adding, tell the user how many nodes it will be pushed to: those with the switch on and not isolated
  const users = nodes.filter((n) => usage.get(n.id)?.useShared && !n.quarantinedAt);

  return (
    <section role="tabpanel" className="min-w-0 overflow-hidden rounded-lg border border-hair bg-surface">
      <div className="flex min-h-[54px] flex-wrap items-center gap-2 border-b border-hair px-4 py-2">
        <h2 className="flex items-center gap-2 text-sm font-semibold text-ink">
          {t('extraPeers.sharedTitle')}
        </h2>
        <HelpTip title={t('extraPeers.sharedTitle')}>{t('extraPeers.sharedHelp')}</HelpTip>
        {/* Open-source edition: syncing the shared list with other consortium organizations is a commercial-edition tool; its text button sits next to "Add peer" */}
        <span className="ml-auto flex items-center gap-2">
          {!isEnterprise && <EnterpriseTeaser slug="consortium-sync" label={t('edition.consortium-sync.short')} />}
        {canWrite && (
          <Button className="inline-flex items-center gap-1" onClick={() => setAdding(true)}>
            <Plus size={13} aria-hidden="true" />
            {t('sharedPeers.add')}
          </Button>
        )}
          {/* Count at the far right (one rule site-wide) */}
          <CountChip unit={t('chain.countUnit')}>{peers.length}</CountChip>
        </span>
      </div>
      <div className="flex flex-col gap-3 px-4 py-3">
        {q.isLoading ? (
          <SkeletonRows rows={3} />
        ) : q.isError ? (
          <ErrorState inCard onRetry={() => void q.refetch()} />
        ) : (
          <div className="overflow-x-auto rounded-md border border-hair">
            <table className="w-full text-sm">
              <thead>
                <tr className="table-head">
                  {/* Fixed-width name column: when rename turns it into an input (filling this column), the columns to the right are not pushed over */}
                  <th className="w-[17.5rem] px-3">{t('sharedPeers.colPeer')}</th>
                  <th className="px-3">{t('sharedPeers.colAddr')}</th>
                  <th className="w-28" aria-hidden="true" />
                </tr>
              </thead>
              <tbody>
                {rows.length === 0 && (
                  <tr>
                    <td colSpan={3} className="px-3 py-8 text-center text-xs text-ink-3">{t('sharedPeers.empty')}</td>
                  </tr>
                )}
                {rows.map(({ key, item: p, state }) => (
                  <tr key={key} ref={presenceRef(state)} className="border-b border-hair align-top last:border-0">
                    <td className="whitespace-nowrap px-3 py-2">
                      <div className="row-cell">
                      {/* The name is the one shared site-wide (keyed by address): edit it right in the row, same action as in overall chain health; our own nodes' names are edited in node settings */}
                      {p.nodeId ? <NodeLabel id={p.nodeId} name={p.name} /> : editing?.pubkey === p.pubkey ? (
                        <NameEditor
                          fill
                          value={p.name}
                          label={t('chain.rename')}
                          placeholder={t('chain.namePlaceholder')}
                          onSave={(name) => (name ? api.setPeerName(p.address, name) : api.deletePeerName(p.address))}
                          onDone={(saved) => { setEditing(null); invalidate(); if (saved) savedFlash.mark(p.pubkey); }}
                        />
                      ) : (
                        <span className="flex items-center gap-1.5">
                          <span key={savedFlash.key(p.pubkey)} className={`${p.name ? 'text-ink' : 'text-ink-faint'} ${savedFlash.cls(p.pubkey)}`}>{p.name ?? t('peers.unnamed')}</span>
                          {canWrite && (
                            <button type="button" onClick={() => setEditing(p)} aria-label={t('chain.rename')} title={t('chain.rename')} className="rounded p-0.5 text-ink-faint transition-colors hover:bg-elevated hover:text-ink">
                              <PencilSimple size={13} aria-hidden="true" />
                            </button>
                          )}
                        </span>
                      )}
                      </div>
                    </td>
                    {/* The public key column is dropped to give width to the status table on the right; the copy button copies the whole enode */}
                    <td className="whitespace-nowrap px-3 py-2 font-mono text-xs text-ink-2"><div className="row-cell"><span className="inline-flex items-center gap-1" title={p.enode}>{enodeHostPort(p.enode)}<CopyButton value={p.enode} /></span></div></td>
                    <td className="whitespace-nowrap px-3 py-2 text-right">
                      <div className="row-cell">{canWrite && <RowAction danger onClick={() => { remove.reset(); setRemoving(p); }}>{t('sharedPeers.remove')}</RowAction>}</div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {adding && q.data && <AddPeerModal data={q.data} sharedCount={users.length} onClose={() => setAdding(false)} onDone={() => { setAdding(false); invalidate(); }} />}
      {removing && (
        <ConfirmModal
          title={t('sharedPeers.removeTitle')}
          body={remove.isError ? errMsg(remove.error) || t('error.title') : t('extraPeers.sharedRemoveBody', { peer: removing.name ?? t('peers.unnamed') })}
          confirmLabel={t('sharedPeers.remove')}
          danger
          busy={remove.isPending}
          onConfirm={() => remove.mutate(removing.pubkey)}
          onCancel={() => setRemoving(null)}
        />
      )}
    </section>
  );
}

