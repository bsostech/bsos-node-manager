import { useState, type ReactNode } from 'react';
import { useAuth } from '@/auth/AuthContext';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Plus } from '@phosphor-icons/react';
import { api } from '@/api/client';
import type { CustomPeer, NodeSummary, SharedPeerNode } from '@/api/schemas';
import { Button, RowAction } from '@/components/Button';
import { ConfirmModal } from '@/components/ConfirmModal';
import { HelpTip } from '@/components/HelpTip';
import { CountChip } from '@/components/ValueChip';
import { NodeLabel } from '@/components/NodeLabel';
import { Switch } from '@/components/Switches';
import { PeerAddForm } from '@/components/PeerAddForm';
import { Pill, type Tone } from '@/components/Pill';
import { PubkeyShort } from '@/components/PubkeyShort';
import { SkeletonRows, ErrorState } from '@/components/States';
import { GoLink } from '@/components/TextLinks';
import { WayHint, WaySeg, snippetOf, type Way } from '@/components/BesuRequirements';
import { CopyCmd } from '@/components/CopyButton';
import { GuideButton, LiveNodeStates, StepWizard, type NodeState } from '@/components/StepWizard';
import { apiState } from '@/lib/capabilities';
import { errMsg } from '@/lib/errors';
import { enodeHostPort as hostOf } from '@/lib/format';
import { CARD, CARD_HEAD } from '@/components/Card';
import { presenceRef, usePresence, type PresenceRow } from '@/hooks/usePresence';


export type PeerUsage = SharedPeerNode;
const labelOf = (p: CustomPeer) => p.nodeId ?? p.name ?? p.pubkey.slice(0, 12) + '…';

/**
 * A node's peering status (the status dot on the tab and the status card label use the same set).
 * Order: isolated -> ADMIN not enabled (cannot push) -> neither side configured (unused) -> no report -> some not connected -> all connected.
 */
/** Node offline or the platform cannot reach its RPC: then peers can neither be read nor pushed */
export const isOffline = (n: NodeSummary | undefined) => !!n && (n.status === 'offline' || n.status === 'rpc_unreachable' || n.capabilities?.agent === false);

export function peerNodeState(t: TFunction, u: PeerUsage | undefined, node: NodeSummary | undefined): { tone: 'healthy' | 'syncing' | 'neutral'; label: string; pill: Tone } {
  if (!u) return { tone: 'neutral', label: t('extraPeers.stateUnreported'), pill: 'neutral' };
  if (u.quarantined) return { tone: 'neutral', label: t('peerSource.quarantined'), pill: 'neutral' };
  // Offline (or RPC unreachable): connections can neither be seen nor changed, so it is a state of its own and says nothing about applied or not
  if (isOffline(node)) return { tone: 'neutral', label: t('extraPeers.stateOffline'), pill: 'neutral' };
  // ADMIN not enabled is a configuration issue, not a fault: gray, with the enable-setup guide next to it
  if (apiState(node?.capabilities, 'ADMIN') === 'off') return { tone: 'neutral', label: t('extraPeers.stateNoAdmin'), pill: 'neutral' };
  // This card only covers "is the shared list applied": switched off means not applied; the self-added state is on the card below
  if (!u.useShared) return { tone: 'neutral', label: t('extraPeers.stateOff'), pill: 'neutral' };
  if (u.missing == null) return { tone: 'neutral', label: t('extraPeers.stateUnreported'), pill: 'neutral' };
  // Normal states just use a gray label (same as authorized transaction addresses); only not-connected gets color
  return u.missing > 0
    ? { tone: 'syncing', label: t('extraPeers.stateMissing', { count: u.missing }), pill: 'warn' }
    : { tone: 'healthy', label: t('extraPeers.stateAllConnected'), pill: 'neutral' };
}

/** What to refetch after toggling the switch or adding/removing self-added peers: this node's settings, shared list usage, connections, node list ("Custom peers" marker) */
function useInvalidatePeers(nodeId: string) {
  const qc = useQueryClient();
  return () => {
    for (const k of [['customPeers', nodeId], ['sharedPeers'], ['nodePeers', nodeId], ['nodes']]) void qc.invalidateQueries({ queryKey: k });
  };
}

/**
 * First card on the node tab: how well this node is connected + the "Use shared list" switch.
 * To connect = (on ? shared list minus itself : []) ∪ self-added; each one not connected is listed below on its own line (in practice there can be several).
 * Changing the switch asks for confirmation first and takes effect immediately after confirming (the platform issues admin_addPeer / admin_removePeer to this node).
 */
export function NodePeerStatusCard({ node, usage, onViewShared }: { node: NodeSummary; usage: PeerUsage | undefined; onViewShared?: () => void }) {
  // Viewers are read-only: the apply-shared-list switch is disabled
  const canWrite = useAuth().can('nodes:onboard');
  const { t } = useTranslation();
  const id = node.id;
  const adminOff = apiState(node.capabilities, 'ADMIN') === 'off';
  const cfg = useQuery({ queryKey: ['customPeers', id], queryFn: () => api.getNodeCustomPeers(id), refetchInterval: 15_000 });
  const shared = useQuery({ queryKey: ['sharedPeers'], queryFn: api.listSharedPeers, refetchInterval: 15_000 });
  const seen = useQuery({ queryKey: ['nodePeers', id], queryFn: () => api.listNodePeers(id), refetchInterval: 15_000 });
  const invalidate = useInvalidatePeers(id);
  const [ask, setAsk] = useState<boolean | null>(null);
  const toggle = useMutation({
    mutationFn: (on: boolean) => api.setNodePeerShared(id, on),
    onSuccess: () => { setAsk(null); invalidate(); },
  });

  const useShared = cfg.data?.useShared ?? usage?.useShared ?? false;
  const own = cfg.data?.peers ?? [];
  const sharedList = (shared.data?.peers ?? []).filter((p) => p.nodeId !== id);
  // Everything to connect (deduplicated): shared list first, self-added after
  const targets = new Map<string, { pubkey: string; enode: string; nodeId?: string | null; name?: string | null; quarantined?: boolean }>();
  if (useShared) for (const p of sharedList) targets.set(p.pubkey, p);
  for (const p of own) if (!targets.has(p.pubkey)) targets.set(p.pubkey, p);
  const st = peerNodeState(t, usage, node);
  // Offline: the status label says "Offline", the switch cannot be pressed (cannot push), only one sentence below; applied-or-not is not shown
  const offline = isOffline(node);
  // Online but not reporting admin_peers means connection state is unknown; ADMIN-not-enabled is already covered by the status label, do not repeat
  const unknown = !offline && !!seen.data && !seen.data.reported && !adminOff && targets.size > 0;
  const showShared = !offline && useShared && !adminOff && sharedList.length > 0;
  // Shared list not applied: one sentence on what that means
  const idle = !offline && !adminOff && !useShared && !!cfg.data;
  const hasBody = showShared || unknown || seen.isError || adminOff || idle || offline;
  const [guide, setGuide] = useState(false);

  return (
    <section className={CARD}>
      <div className={`flex flex-wrap items-center gap-2 px-4 py-3 text-xs ${hasBody ? 'border-b border-hair' : ''}`}>
        <NodeLabel id={id} name={node.name} />
        <Pill tone={st.pill}>{st.label}</Pill>
        {/* Not connected is not the same as not applied: the platform resends every round and the node also retries on its own; explain common causes so people do not think they need to press something again */}
        {useShared && (usage?.missing ?? 0) > 0 && <HelpTip title={st.label}>{t('extraPeers.missingHelp')}</HelpTip>}
        {/* ADMIN not enabled: the guide sits right next to the status (same as not-enabled in transaction address authorization) */}
        {adminOff && <GuideButton onClick={() => setGuide(true)} label={t('allowlist.enableGuide')} />}
        {useShared && (onViewShared ? <GoLink onClick={onViewShared}>{t('extraPeers.viewShared')}</GoLink> : <GoLink to="/node-join/peers">{t('extraPeers.viewShared')}</GoLink>)}
        {/* Whether this node applies the shared list: confirm before changing; without ADMIN the platform cannot push, so the switch is disabled */}
        {/* When not enabled or offline the switch cannot be pressed and is meaningless, so it is not shown at all (transaction address authorization likewise only has a switch when enabled) */}
        {!offline && !adminOff && <span className="ml-auto flex items-center gap-2 text-ink-2">
          {t('extraPeers.useShared')}
          <Switch on={useShared} disabled={!canWrite || offline || adminOff || !cfg.data || (!!seen.data && !seen.data.reported)} onChange={(on) => { toggle.reset(); setAsk(on); }} aria-label={t('extraPeers.useShared')} />
        </span>}
      </div>
      {seen.isError && (
        <p role="alert" className="px-4 py-3 text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>{t('peerSource.stateError')}{errMsg(seen.error) ? ` (${errMsg(seen.error)})` : ''}</p>
      )}
      {unknown && <p className="px-4 py-3 text-xs text-ink-3">{t('peerSource.noState')}</p>}
      {/* How to fix it (follow the enable-setup guide) is for operators only: viewers do not see the guide button */}
      {adminOff && <p className="px-4 py-3 text-xs text-ink-3">{t('extraPeers.noAdminBody')}{canWrite && t('extraPeers.noAdminFix')}</p>}
      {offline && <p className="px-4 py-3 text-xs text-ink-3">{t('extraPeers.offlineBody')}</p>}
      {idle && <p className="whitespace-pre-line px-4 py-3 text-xs leading-relaxed text-ink-3">{t('extraPeers.idleBody')}</p>}
      {guide && <AdminGuide nodes={[node]} onClose={() => setGuide(false)} />}
      {/* Nodes the shared list pushes to this node: same table as "Peers added here", showing per entry whether it is connected */}
      {showShared && <div className="px-4 py-3"><PeerTable nodeId={id} rows={sharedList} /></div>}
      {ask !== null && (
        <ConfirmModal
          title={t(ask ? 'extraPeers.sharedOnTitle' : 'extraPeers.sharedOffTitle', { node: id })}
          body={
            <div className="flex flex-col gap-1.5">
              <p>{ask
                ? t('extraPeers.sharedOnBody', { shared: sharedList.length, count: own.length })
                : t(own.length ? 'extraPeers.sharedOffBody' : 'extraPeers.sharedOffBodyNone', { count: own.length })}</p>
              {toggle.isError && <p role="alert" style={{ color: 'var(--bsos-status-error-fg)' }}>{errMsg(toggle.error) || t('peerSource.switchError')}</p>}
            </div>
          }
          confirmLabel={t('common.confirm')}
          busy={toggle.isPending}
          onConfirm={() => toggle.mutate(ask)}
          onCancel={() => setAsk(null)}
        />
      )}
    </section>
  );
}

/**
 * Second card on the node tab: peers self-added on this node. Can be added whether or not the switch is on; takes effect immediately on add (one at a time, no separate apply).
 * The field for pasting an enode appears only after pressing "Add a peer"; removal asks for confirmation (a peer that is only in this node's static-nodes.json stays disconnected until Besu restarts).
 */
export function NodePeerAdditionsCard({ nodeId, adminOff }: { nodeId: string; adminOff: boolean }) {
  // Viewers are read-only: neither add nor remove appears
  const canWrite = useAuth().can('nodes:onboard');
  const { t } = useTranslation();
  const q = useQuery({ queryKey: ['customPeers', nodeId], queryFn: () => api.getNodeCustomPeers(nodeId), refetchInterval: 15_000 });
  const invalidate = useInvalidatePeers(nodeId);
  const [adding, setAdding] = useState(false);
  const [delTarget, setDelTarget] = useState<CustomPeer | null>(null);
  const add = useMutation({ mutationFn: (enode: string) => api.createNodeCustomPeer(nodeId, enode), onSuccess: invalidate });
  const del = useMutation({
    mutationFn: (pubkey: string) => api.deleteNodeCustomPeer(nodeId, pubkey),
    onSuccess: () => { setDelTarget(null); invalidate(); },
  });
  const peers = q.data?.peers ?? [];
  // On add / remove the row expands and fades in, or fades out and collapses; no animation on first load. When removing the last entry, the table stays until that row has collapsed before switching to the empty state
  const shown = usePresence(peers, (p) => p.pubkey, !!q.data);

  return (
    <section className={CARD}>
      <div className={CARD_HEAD}>
        <h2 className="flex items-center gap-2 text-sm font-semibold text-ink">
          {t('extraPeers.ownTitle')}
        </h2>
        <HelpTip title={t('extraPeers.ownTitle')}>{t('extraPeers.ownHelp')}</HelpTip>
        {/* Right side: the add button, with the count at the far right (one rule site-wide) */}
        <span className="ml-auto flex items-center gap-2">
        {canWrite && !adding && !adminOff && (
            <Button onClick={() => setAdding(true)} className="inline-flex items-center gap-1"><Plus size={13} aria-hidden="true" />{t('extraPeers.ownAdd')}</Button>
        )}
          <CountChip unit={t('chain.countUnit')}>{peers.length}</CountChip>
        </span>
      </div>
      <div className="flex min-w-0 flex-col gap-3 px-4 py-3">
        {q.isLoading ? <SkeletonRows rows={2} />
          : q.isError ? <ErrorState inCard onRetry={() => void q.refetch()} />
          : (
            <>
              {adding && <PeerAddForm idPrefix={`own-peer-${nodeId}`} pending={add.isPending} error={add.error} onAdd={(enode) => add.mutateAsync(enode)} />}
              {shown.length > 0
                ? <PeerTable nodeId={nodeId} rows={shown.map((r) => r.item)} presence={new Map(shown.map((r) => [r.key, r.state]))} action={canWrite ? (p) => (
                    <RowAction danger className="-my-1" onClick={() => { del.reset(); setDelTarget(p); }} aria-label={`${t('manualPeers.remove')}: ${labelOf(p)}`}>
                      {t('manualPeers.remove')}
                    </RowAction>
                  ) : undefined} />
                : <p className="text-xs text-ink-3">{t('extraPeers.ownEmpty')}</p>}
            </>
          )}
      </div>
      {delTarget && (
        <ConfirmModal
          title={t('peerSource.removeTitle')}
          body={del.isError ? errMsg(del.error) || t('manualPeers.removeError') : t('extraPeers.removeBody', { peer: labelOf(delTarget) })}
          confirmLabel={t('manualPeers.remove')}
          danger
          busy={del.isPending}
          onConfirm={() => del.mutate(delTarget.pubkey)}
          onCancel={() => setDelTarget(null)}
        />
      )}
    </section>
  );
}

type PeerRow = { pubkey: string; enode: string; nodeId?: string | null; name?: string | null; quarantined?: boolean };
/**
 * Peer table (shared list on the status card, self-added on the second card, same table): name, address, public key, connection status
 * (from this node's admin_peers; if unreadable the status column is not drawn). Connected is a green dot, not connected an orange pill (same color as the status card label).
 */
function PeerTable<R extends PeerRow>({ nodeId, rows, action, presence }: { nodeId: string; rows: R[]; action?: (r: R) => ReactNode; presence?: ReadonlyMap<string, PresenceRow<R>['state']> }) {
  const { t } = useTranslation();
  const seen = useQuery({ queryKey: ['nodePeers', nodeId], queryFn: () => api.listNodePeers(nodeId), refetchInterval: 15_000 });
  const connected = seen.data?.reported ? new Set(seen.data.peers.map((p) => p.pubkey)) : null;
  return (
    <div className="overflow-x-auto rounded-md border border-hair">
      <table className="w-full min-w-[36rem] table-fixed text-sm">
        <colgroup>
          <col className="w-[28%]" /><col className="w-[24%]" /><col />
          {connected && <col className="w-[16%]" />}
          {action && <col className="w-20" />}
        </colgroup>
        <thead>
          <tr className="table-head">
            <th className="px-3">{t('sharedPeers.colPeer')}</th>
            <th className="px-3">{t('sharedPeers.colAddr')}</th>
            <th className="px-3">{t('sharedPeers.colPubkey')}</th>
            {connected && <th className="px-3">{t('peerSource.colState')}</th>}
            {action && <th aria-hidden="true" />}
          </tr>
        </thead>
        <tbody>
          {rows.map((p) => (
            // Each cell's content is wrapped in .row-cell: when a row is added / removed (presence), this is the layer the height animation runs on
            <tr key={p.pubkey} ref={presenceRef(presence?.get(p.pubkey) ?? null)} className={`border-b border-hair align-top last:border-0 ${p.quarantined ? 'text-ink-3' : ''}`}>
              <td className="px-3 py-2"><div className="row-cell truncate">{p.nodeId ? <NodeLabel id={p.nodeId} name={p.name ?? undefined} /> : <span className={p.name ? 'text-ink' : 'text-ink-3'}>{p.name ?? t('peers.unnamed')}</span>}</div></td>
              <td className="px-3 py-2 font-mono text-xs text-ink-2"><div className="row-cell">{hostOf(p.enode)}</div></td>
              <td className="whitespace-nowrap px-3 py-2"><div className="row-cell"><PubkeyShort pubkey={p.pubkey} copy={p.enode} /></div></td>
              {connected && (
                <td className="whitespace-nowrap px-3 py-2 text-xs">
                  <div className="row-cell">
                  {p.quarantined ? (
                    <span title={t('peerSource.quarantinedHint')}>{t('peerSource.quarantined')}</span>
                  ) : connected.has(p.pubkey) ? (
                    <span className="inline-flex items-center gap-1.5 text-ink-2">
                      <span className="size-1.5 rounded-full" style={{ background: 'var(--bsos-status-healthy-fg)' }} />
                      {t('peerSource.connected')}
                    </span>
                  ) : <Pill tone="warn">{t('peerSource.notConnected')}</Pill>}
                  </div>
                </td>
              )}
              {action && <td className="px-3 py-2 text-right"><div className="row-cell">{action(p)}</div></td>}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** Setup guide for enabling ADMIN: add ADMIN to rpc-http-api and restart Besu. Same layout as the transaction address authorization guide. */
function AdminGuide({ nodes, onClose }: { nodes: NodeSummary[]; onClose: () => void }) {
  const { t } = useTranslation();
  const [way, setWay] = useState<Way>('toml');
  const lines = [`rpc-http-api=["${t('allowlist.existingApis')}","ADMIN"]`];
  const stateOf = (n: NodeSummary): NodeState => apiState(n.capabilities, 'ADMIN') === 'off'
    ? { tone: 'neutral', label: t('extraPeers.stateNoAdmin') }
    : { tone: 'healthy', label: t('extraPeers.adminOn') };
  return (
    <StepWizard
      title={t('extraPeers.adminGuideTitle')}
      onClose={onClose}
      steps={[
        { title: t('rpcAuth.stepConfig'), body: <><WaySeg way={way} onChange={setWay} /><WayHint way={way} existing="ADMIN" /><CopyCmd value={snippetOf(lines, way)} /></> },
        { title: t('rpcAuth.stepRestart'), body: <><p className="text-xs text-ink-3">{t('rpcAuth.restartNote')}</p><LiveNodeStates ids={nodes.map((n) => n.id)} stateOf={stateOf} label={t('rpcAuth.listLabel')} /></> },
      ]}
    />
  );
}
