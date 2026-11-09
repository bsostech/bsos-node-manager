import { useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowCounterClockwise, PencilSimple, Plus, UploadSimple, Wrench } from '@phosphor-icons/react';
import { api, HttpError } from '@/api/client';
import { errMsg, itemMsg } from '@/lib/errors';
import type { NodeSummary, PermissioningPolicy, PermissioningSaveResult, PolicyApplyNodeResult } from '@/api/schemas';
import { Switch } from '@/components/Switches';
import { useUnsavedGuard } from '@/hooks/useUnsavedGuard';
import { useUrlParam } from '@/hooks/useUrlParam';
import { AccessTabs } from '@/components/AccessTabs';
import { ALLOWLIST_PATH, PERM_LINES, WayHint, WaySeg, reqStatus, snippetOf, type Way } from '@/components/BesuRequirements';
import { Button, RowAction } from '@/components/Button';
import { ConfirmModal } from '@/components/ConfirmModal';
import { Modal, ModalTitle } from '@/components/Modal';
import { CopyCmd } from '@/components/CopyButton';
import { HelpTip } from '@/components/HelpTip';
import { CountChip } from '@/components/ValueChip';
import { NameEditor } from '@/components/NameEditor';
import { NodeLabel } from '@/components/NodeLabel';
import { GuideButton, LiveNodeStates, StepWizard, type NodeState } from '@/components/StepWizard';
import { GoLink } from '@/components/TextLinks';
import { BESU_DOCS } from '@/lib/besuDocs';
import { Pill } from '@/components/Pill';
import { ErrorState, Notice, Skeleton } from '@/components/States';
import { EnterpriseTeaser } from '@/components/EnterpriseTeaser';
import { isEnterprise } from '@/edition';
import { CARD, CARD_HEAD } from '@/components/Card';
import { useAuth } from '@/auth/AuthContext';
import { useSavedFlash } from '@/hooks/useSavedFlash';
import { presenceRef, usePresence } from '@/hooks/usePresence';

// Account address (Ethereum 0x + 40 hex).
const ADDR_RE = /^0x[0-9a-fA-F]{40}$/;
// The backend lowercases, dedupes and sorts on save; compare drafts in the same shape, or the next GET after saving shows "unsaved" again
const norm = (list: string[]) => JSON.stringify([...new Set(list.map((a) => a.toLowerCase()))].sort());
/** Union (lowercase) of every node's current list, used for the first-time import. */
const unionOf = (rs: PolicyApplyNodeResult[]) => [...new Set(rs.flatMap((r) => r.currentAllowlist ?? []).map((a) => a.toLowerCase()))];
/** Tab key: the shared list; other tabs use the node id */
const SHARED = '*';
const POLICY_KEY = ['permissioningPolicy'];
const NAMES_KEY = ['accountNames'];

// Enabled / list mismatch (not reverted automatically, waits for someone to save) / not enabled (no allowlist file or PERM off; reason not listed) / unreachable / node rejected (the last two carry a reason).
// An empty list on the node is labelled "Not applied": Besu rejects every transaction with an empty list, so it must not show as enabled or merely mismatched (freshly joined nodes look like this).
/**
 * How the node's actual list differs from what the platform wants on it (shared list + node-specific additions):
 * missing = not yet written to the node (the last apply failed, or someone deleted it directly on the node);
 * extra = neither in the shared list nor added for this node. The platform remembers each node's own additions, so these must be removed
 *   (the removal never synced, or someone added it directly on the node); node-specific additions count as wanted and are never treated as extra.
 */
type Gap = { missing: string[]; extra: string[] };
const gapOf = (r: PolicyApplyNodeResult | undefined, want: string[]): Gap | null => {
  if (r?.state !== 'drift' || !r.currentAllowlist) return null;
  const have = new Set(r.currentAllowlist.map((a) => a.toLowerCase()));
  const wanted = new Set(want.map((a) => a.toLowerCase()));
  return { missing: want.filter((a) => !have.has(a.toLowerCase())), extra: r.currentAllowlist.filter((a) => !wanted.has(a.toLowerCase())) };
};
const VIEW: Record<PolicyApplyNodeResult['state'], NodeState['tone']> = { in_sync: 'healthy', drift: 'syncing', disabled: 'neutral', unreachable: 'neutral', rejected: 'error' };
const resultState = (r: PolicyApplyNodeResult | undefined, t: TFunction): NodeState => {
  if (!r) return { tone: 'neutral', label: t('allowlist.state.unreachable') };
  if (r.currentAllowlist?.length === 0) return { tone: 'syncing', label: t('allowlist.state.notApplied'), detail: t(r.state === 'drift' ? 'allowlist.notAppliedHint' : 'allowlist.emptyHint') };
  // Mismatch states the conclusion (latest shared list not applied); the differing addresses are listed under the status row
  return { tone: VIEW[r.state], label: t(`allowlist.state.${r.state}`), ...(r.state === 'unreachable' || r.state === 'rejected' ? { detail: itemMsg(r), hint: r.error ?? undefined } : {}) };
};

/**
 * Authorized transaction addresses: a "Shared list" card on top, and a per-node lists card below.
 * Every node must honor the shared list (always applied); a node may only add extra addresses on top of it, never remove any.
 * So changing the shared list covers every node and no node misses addresses through a custom list; extra addresses are edited in a modal.
 * List changes don't take effect immediately: an apply bar appears when there are changes, and "Apply" first confirms which nodes it targets.
 * Whether a node has it enabled is decided by its startup flags; the platform only detects it. Applying issues perm_* to the target nodes, no restart.
 */
export function AccessPermissioning() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  // Viewers only look: lists and per-node status still show, but add / remove / rename / apply / reapply / shared-list toggle are all hidden
  const canWrite = useAuth().can('nodes:onboard');
  // Refetch every 10s: nodes (per-node comparison) comes from the platform's latest collection round
  const policyQ = useQuery({ queryKey: POLICY_KEY, queryFn: api.getPermissioningPolicy, refetchInterval: 10000 });
  const nodesQ = useQuery({ queryKey: ['nodes'], queryFn: () => api.listNodes(), refetchInterval: 15_000 });
  // Tab goes into the URL (?node=node-a1; the shared list adds no param) so links from the node settings page open straight to that node
  const [tab, setTab] = useUrlParam<string>('node', SHARED);
  // Setup guide: opened from a node's status row it targets that node (its file includes the node's own additions); opened from the card center it covers every node not yet enabled
  const [guide, setGuide] = useState<{ nodeId?: string } | null>(null);
  // While applying: the nodes being pushed to show a spinner, then flash once the result arrives
  const [pushing, setPushing] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [flash, setFlash] = useState(false);
  const names = useQuery({ queryKey: NAMES_KEY, queryFn: api.listAccountNames }).data ?? {};
  // Node tab "Re-apply": push the platform's copy (shared list + this node's own additions) to this node again, adding what is missing and removing extras.
  // Declared before the early return (hook order); begin / finish are defined below and only called on click
  const resync = useMutation({
    mutationFn: (id: string) => { begin([id]); return api.syncNodeAllowlist(id); },
    onSuccess: (res) => { setAskResync(null); finish(res); },
    onError: () => finish(null),
  });
  // Reapply: confirm first (stating how many will be added and removed), push to this node only after confirmation
  const [askResync, setAskResync] = useState<string | null>(null);
  // "Use shared list" toggle: confirm first (turning it off accepts only this node's own additions, and an empty list rejects every transaction), then push to this node immediately
  const [askShared, setAskShared] = useState<{ nodeId: string; on: boolean } | null>(null);
  const toggleShared = useMutation({
    mutationFn: ({ nodeId, on }: { nodeId: string; on: boolean }) => { begin([nodeId]); return api.setNodeAllowlistShared(nodeId, on); },
    onSuccess: (res) => { setAskShared(null); finish(res); void qc.invalidateQueries({ queryKey: POLICY_KEY }); },
    onError: () => finish(null),
  });

  if (!policyQ.data || !nodesQ.data) {
    const err = (policyQ.isError && !policyQ.data) || (nodesQ.isError && !nodesQ.data);
    return (
      <div className="mx-auto flex max-w-5xl flex-col gap-4">
        <AccessTabs page="allowlist" />
        {err ? <ErrorState onRetry={() => { void policyQ.refetch(); void nodesQ.refetch(); }} />
          : <div className="flex flex-col gap-3"><Skeleton className="h-8 w-56" /><Skeleton className="h-40" /></div>}
      </div>
    );
  }

  const policy = policyQ.data;
  const custom = new Map((policy.custom ?? []).map((c) => [c.nodeId, c]));
  // Whether this node applies the shared list (unset = applies)
  const usesShared = (id: string) => custom.get(id)?.useShared !== false;
  const results = policy.nodes ?? [];
  const byId = new Map(results.map((r) => [r.nodeId, r]));
  const nodes = nodesQ.data.nodes;
  const on = results.filter((r) => r.state === 'in_sync' || r.state === 'drift' || r.state === 'rejected'); // a node rejection also means the allowlist is enabled
  const noneOn = on.length === 0 && results.some((r) => r.state === 'disabled');
  const onIds = new Set(on.map((r) => r.nodeId));

  // Each node should have = shared list + its own additions; for mismatched nodes work out what is missing / extra
  const gaps = new Map(results.flatMap((r) => {
    const g = gapOf(r, [...(usesShared(r.nodeId) ? policy.accountsAllowlist : []), ...(custom.get(r.nodeId)?.accountsAllowlist ?? [])]);
    return g ? [[r.nodeId, g] as const] : [];
  }));
  const begin = (ids: string[]) => { setPushing(new Set(ids)); setBusy(true); };
  // Seed with the save response first (read back on the spot); the 10s GET loop takes over afterwards. The response only covers the nodes pushed this time, so merge per node
  const finish = (res: PermissioningSaveResult | null, patch?: (p: PermissioningPolicy) => PermissioningPolicy) => {
    setBusy(false);
    if (!res) { setPushing(new Set()); return; }
    // A GET already in flight before saving brings back the old version and overwrites the new one written to the cache below
    void qc.cancelQueries({ queryKey: POLICY_KEY });
    const got = new Map(res.nodes.map((r) => [r.nodeId, r]));
    qc.setQueryData<PermissioningPolicy>(POLICY_KEY, (p) => p && (patch ?? ((x) => x))({ ...p, nodes: (p.nodes ?? []).map((r) => got.get(r.nodeId) ?? r) }));
    setFlash(true);
    setTimeout(() => setFlash(false), 1500);
  };
  const stateOf = (id: string): NodeState => !pushing.has(id) ? resultState(byId.get(id), t)
    : busy ? { tone: 'neutral', label: t('allowlist.applying'), busy: true }
    : { ...resultState(byId.get(id), t), flash };
  const PILL: Partial<Record<NodeState['tone'], 'ok' | 'warn' | 'neutral' | 'err' | 'info'>> = { healthy: 'ok', syncing: 'warn', neutral: 'neutral', error: 'err', info: 'info' };
  const active = tab === SHARED || nodes.some((n) => n.id === tab) ? tab : SHARED; // fall back to the shared list when the node is removed
  const DOT: Partial<Record<NodeState['tone'], string>> = { healthy: 'var(--bsos-status-healthy-fg)', syncing: 'var(--bsos-status-syncing-fg)', error: 'var(--bsos-status-error-fg)', neutral: 'var(--bsos-text-faint)' };

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-4">
      {/* The setup guide only appears where needed: the status row of non-enabled nodes and the card center when none is enabled; not in the page header (useless once enabled) */}
      <AccessTabs page="allowlist" />
      {policyQ.isError && <Notice>{t('access.stale')}</Notice>}

      {/* Tabs sit outside the card (same style as other pages' tabs): "Shared list" first, then one tab per node. Each tab's draft stays mounted, so switching tabs loses nothing.
          Each tab's content is its own card: one for the shared list; for a node, one status card + one "Node-specific allowlist" card */}
      {noneOn ? (
        <section className={`${CARD} flex flex-col items-start gap-3 px-4 py-6 text-sm text-ink-2`}>
          <p>{t('allowlist.noneOn')}</p>
          {canWrite && <Button onClick={() => setGuide({})} className="inline-flex items-center gap-1.5"><Wrench size={14} aria-hidden="true" />{t('allowlist.enableGuide')}</Button>}
        </section>
      ) : (<>
        <div role="tablist" aria-label={t('access.permTitle')} className="flex items-end gap-1 overflow-x-auto border-b border-hair">
          {[SHARED, ...nodes.map((n) => n.id)].map((id) => {
            const sel = id === active;
            const st = id === SHARED ? null : stateOf(id);
            const extra = custom.get(id)?.accountsAllowlist.length ?? 0;
            return (
              <button key={id} type="button" role="tab" aria-selected={sel} onClick={() => setTab(id)}
                className={`flex items-center gap-1.5 whitespace-nowrap border-b-2 px-3 py-2 text-sm transition-colors ${sel ? 'border-accent font-medium text-accent' : 'border-transparent text-ink-2 hover:text-ink'}`}>
                {/* Node tab: status dot (applied green, mismatch orange, not enabled / unreachable grey) + "+N" when the node has its own additions */}
                {st && <span aria-hidden="true" className="size-1.5 shrink-0 rounded-full" style={{ background: DOT[st.tone] ?? DOT.neutral }} />}
                {id === SHARED ? t('allowlist.tabShared') : <span className="font-mono">{id}</span>}
                {id === SHARED ? <span className="font-mono text-2xs text-ink-3">{policy.accountsAllowlist.length}</span>
                  : extra > 0 && <span className="font-mono text-2xs text-ink-3">+{extra}</span>}
              </button>
            );
          })}
        </div>
        {/* Shared list tab: one card */}
        <div role="tabpanel" hidden={active !== SHARED}>
          <ListEditor
            scope={SHARED}
            saved={policy.accountsAllowlist}
            revision={policy.revision}
            isNew={false}
            shared={policy.accountsAllowlist}
            targets={results}
            customIds={[...custom.keys()]}
            notSharedIds={nodes.map((n) => n.id).filter((id) => !usesShared(id))}
            readOnly={!canWrite}
            onBegin={begin}
            onFinish={finish}
          />
        </div>
        {/* Per-node tab: one card for "how this node's apply is going"; enabled nodes get another for "Node-specific allowlist" */}
        {nodes.map((n) => {
          const st = stateOf(n.id);
          const own = custom.get(n.id);
          const enabled = onIds.has(n.id);
          const gap = gaps.get(n.id);
          const showGap = !!gap && !st.busy;
          const useShared = usesShared(n.id);
          return (
            <div key={n.id} role="tabpanel" hidden={active !== n.id} className={active === n.id ? 'flex flex-col gap-4' : 'hidden'}>
              <section className={CARD}>
                <div className={`flex flex-wrap items-center gap-2 px-4 py-3 text-xs ${showGap || !enabled ? 'border-b border-hair' : ''}`}>
                  <NodeLabel id={n.id} name={n.name} />
                  {/* When in sync, state "what was applied" in grey (normal state needs no color); other states keep their usual colors */}
                  {byId.get(n.id)?.state === 'in_sync' && !st.busy
                    ? <Pill tone="neutral" className={st.flash ? 'ring-2 ring-accent/40' : ''}>{t(!useShared ? 'allowlist.appliedOwnOnly' : own?.accountsAllowlist.length ? 'allowlist.appliedWithOwn' : 'allowlist.appliedShared')}</Pill>
                    : <Pill tone={PILL[st.tone] ?? 'neutral'} className={st.flash ? 'ring-2 ring-accent/40' : ''}>{st.label}</Pill>}
                  {/* Not enabled: the guide sits right next to the status so the next step is obvious */}
                  {byId.get(n.id)?.state === 'disabled' && <GuideButton onClick={() => setGuide({ nodeId: n.id })} label={t('allowlist.enableGuide')} />}
                  {st.detail && <span className="text-ink-3" title={st.hint && st.hint !== st.detail ? `${st.detail}\n${st.hint}` : undefined}>{st.detail}</span>}
                  {/* The shared list is always applied; the full list lives in the "Shared list" tab, so only link to it here */}
                  {enabled && useShared && <GoLink onClick={() => setTab(SHARED)}>{t('allowlist.viewShared')}</GoLink>}
                  {enabled && (
                    <span className="ml-auto flex items-center gap-3">
                      {/* Missing addresses or last write failed: reapply from this node's status card, kept apart from editing its own additions */}
                      {resync.isError && resync.variables === n.id && <span role="alert" style={{ color: 'var(--bsos-status-error-fg)' }}>{resync.error instanceof HttpError ? errMsg(resync.error) : t('error.title')}</span>}
                      {canWrite && (byId.get(n.id)?.state === 'drift' || byId.get(n.id)?.state === 'rejected') && !st.busy && (
                        <Button onClick={() => { resync.reset(); setAskResync(n.id); }} disabled={resync.isPending}>{t('allowlist.resync')}</Button>
                      )}
                      {/* Whether this node applies the shared list: confirm on change, then push to the node immediately */}
                      <span className="flex items-center gap-2 text-ink-2">
                        {t('allowlist.useShared')}
                        <Switch on={useShared} disabled={!canWrite} onChange={(on) => { toggleShared.reset(); setAskShared({ nodeId: n.id, on }); }} aria-label={t('allowlist.useShared')} />
                      </span>
                    </span>
                  )}
                </div>
                {/* The differences go under the status: there may be several in practice, one address per line */}
                {showGap && <GapList gap={gap} names={names} />}
                {/* Not enabled and unreachable are told apart: not enabled means changing startup flags (setup guide), unreachable means waiting for it to come back */}
                {!enabled && <p className="px-4 py-4 text-xs text-ink-3">{t(byId.get(n.id)?.state === 'disabled' ? 'allowlist.nodeOff' : 'allowlist.nodeUnreachable')}{canWrite && byId.get(n.id)?.state === 'disabled' && t('allowlist.nodeOffFix')}</p>}
              </section>
              {enabled && (
                <ListEditor
                  scope={n.id}
                  saved={own?.accountsAllowlist ?? []}
                  revision={own?.revision ?? 0}
                  isNew={!own}
                  shared={useShared ? policy.accountsAllowlist : []}
                  targets={results.filter((r) => r.nodeId === n.id)}
                  customIds={[]}
                  readOnly={!canWrite}
                  onBegin={begin}
                  onFinish={finish}
                />
              )}
            </div>
          );
        })}
      </>)}
      {askResync && (() => {
        const g = gaps.get(askResync);
        return (
          <ConfirmModal
            title={t('allowlist.resyncTitle', { node: askResync })}
            body={
              <div className="flex flex-col gap-1.5">
                <p>{t(usesShared(askResync) ? 'allowlist.resyncBody' : 'allowlist.resyncBodyOwnOnly', { node: askResync })}</p>
                {g && (g.missing.length > 0 || g.extra.length > 0) && (
                  <p className="text-ink-3">{[
                    g.missing.length ? t('allowlist.resyncAdd', { count: g.missing.length }) : '',
                    g.extra.length ? t('allowlist.resyncRemove', { count: g.extra.length }) : '',
                  ].filter(Boolean).join(t('allowlist.sep'))}</p>
                )}
                {resync.isError && <p role="alert" style={{ color: 'var(--bsos-status-error-fg)' }}>{resync.error instanceof HttpError ? errMsg(resync.error) : t('error.title')}</p>}
              </div>
            }
            confirmLabel={t('common.confirm')}
            busy={resync.isPending}
            onConfirm={() => resync.mutate(askResync)}
            onCancel={() => setAskResync(null)}
          />
        );
      })()}
      {askShared && (() => {
        const ownCount = custom.get(askShared.nodeId)?.accountsAllowlist.length ?? 0;
        return (
          <ConfirmModal
            title={t(askShared.on ? 'allowlist.sharedOnTitle' : 'allowlist.sharedOffTitle', { node: askShared.nodeId })}
            body={
              <div className="flex flex-col gap-1.5">
                <p>{askShared.on
                  ? t('allowlist.sharedOnBody', { node: askShared.nodeId, shared: policy.accountsAllowlist.length, count: ownCount })
                  : t(ownCount ? 'allowlist.sharedOffBody' : 'allowlist.sharedOffBodyNone', { node: askShared.nodeId, count: ownCount })}</p>
                {!askShared.on && ownCount === 0 && <p style={{ color: 'var(--bsos-status-syncing-fg)' }}>{t('allowlist.sharedOffEmpty')}</p>}
                {toggleShared.isError && <p role="alert" style={{ color: 'var(--bsos-status-error-fg)' }}>{toggleShared.error instanceof HttpError ? errMsg(toggleShared.error) : t('error.title')}</p>}
              </div>
            }
            confirmLabel={t('common.confirm')}
            busy={toggleShared.isPending}
            onConfirm={() => toggleShared.mutate(askShared)}
            onCancel={() => setAskShared(null)}
          />
        );
      })()}
      {guide && <AllowlistGuide nodes={nodes} nodeId={guide.nodeId} shared={policy.accountsAllowlist} own={guide.nodeId ? custom.get(guide.nodeId)?.accountsAllowlist ?? [] : []} onClose={() => setGuide(null)} />}
    </div>
  );
}

/**
 * Editor for one list (shared or a node's custom one). Draft, save and reverting to shared live here; push status is drawn by the parent in the right column.
 * isNew: "+ custom list" just opened and not created yet; saved is the shared list, revision 0, and the first save creates it.
 */
function ListEditor({ scope, saved, revision, isNew, shared, targets, customIds, notSharedIds = [], readOnly = false, onBegin, onFinish }: {
  scope: string; saved: string[]; revision: number; isNew: boolean; shared: string[];
  targets: PolicyApplyNodeResult[]; customIds: string[];
  /** Viewer: only list addresses and names, no editing at all */
  readOnly?: boolean;
  /** Shared list tab: nodes that don't apply the shared list (not pushed on apply, listed separately in the confirm dialog) */
  notSharedIds?: string[];
  onBegin: (ids: string[]) => void;
  onFinish: (res: PermissioningSaveResult | null, patch?: (p: PermissioningPolicy) => PermissioningPolicy) => void;
}) {
  const { t } = useTranslation();
  const isShared = scope === SHARED;
  const [draft, setDraft] = useState<string[] | null>(null);
  const [base, setBase] = useState<string[] | null>(null); // which version the draft started from
  const [baseRev, setBaseRev] = useState(0); // that version's revision, sent on save; if someone else saved first the backend returns 409 instead of overwriting
  const [imported, setImported] = useState(false);
  // Apply result: after success a result dialog says how many nodes were applied and which failed; closing it dismisses it, nothing stays on the page
  const [outcome, setOutcome] = useState<{ ok: number; total: number; failed: { nodeId: string; error: string; raw?: string }[] } | null>(null);
  const [confirmApply, setConfirmApply] = useState(false);
  const [adding, setAdding] = useState(false); // Node tab: the input only shows after clicking "Add to node allowlist"
  // Address names: the platform's own address book, saved immediately rather than waiting for "Apply" (names are never pushed to nodes)
  const qc = useQueryClient();
  const names = useQuery({ queryKey: NAMES_KEY, queryFn: api.listAccountNames }).data ?? {};
  const [naming, setNaming] = useState<string | null>(null);
  const savedFlash = useSavedFlash();

  // An untouched draft follows newer versions saved by others (by revision: someone saving the same list still counts as new, or the next save would 409);
  // an edited draft is kept and still carries the revision it started from, so a save after someone else's returns 409
  if (draft === null || (base && norm(draft) === norm(base) && revision !== baseRev)) { setDraft(saved); setBase(saved); setBaseRev(revision); }

  const save = useMutation({
    mutationFn: (list: string[]) => {
      onBegin(targets.filter((r) => r.state !== 'disabled' || !isShared).map((r) => r.nodeId));
      const body = { accountsAllowlist: list, revision: baseRev };
      if (!isShared && list.length === 0) return api.deleteNodeAllowlist(scope); // removing all own additions = this node applies only the shared list
      return isShared ? api.setPermissioningPolicy(body) : api.setNodeAllowlist(scope, body);
    },
    onSuccess: (res, list) => {
      setConfirmApply(false);
      const pushed = res.nodes.filter((r) => r.state !== 'disabled');
      setOutcome({
        ok: pushed.filter((r) => r.state === 'in_sync').length,
        total: pushed.length,
        failed: pushed.filter((r) => r.state !== 'in_sync').map((r) => ({ nodeId: r.nodeId, error: itemMsg(r) ?? t(`allowlist.state.${r.state}`), raw: r.error ?? undefined })),
      });
      setAdding(false);
      // Single node: the platform drops entries already in the shared list and the toggle lives in custom too; don't guess here, just refetch
      onFinish(res, isShared ? (p) => ({ ...p, accountsAllowlist: list, revision: res.policyRevision }) : undefined);
      if (!isShared) void qc.invalidateQueries({ queryKey: POLICY_KEY });
      setDraft(list);
      setBase(list);
      setBaseRev(list.length || isShared ? res.policyRevision : 0);
      setImported(false);
    },
    onError: () => onFinish(null),
  });
  const dirty = draft !== null && norm(draft) !== norm(saved);
  useUnsavedGuard(dirty);
  // Each row of the list: applied ones (kept / struck through pending removal) + newly added ones not yet applied
  const draftSet = new Set((draft ?? []).map((a) => a.toLowerCase()));
  const savedSet = new Set(saved.map((a) => a.toLowerCase()));
  const rows: { a: string; kind: 'kept' | 'added' | 'removed' }[] = [
    ...saved.map((a) => ({ a, kind: draftSet.has(a.toLowerCase()) ? 'kept' as const : 'removed' as const })),
    ...(draft ?? []).filter((a) => !savedSet.has(a.toLowerCase())).map((a) => ({ a, kind: 'added' as const })),
  ];
  // Rows expand and fade in on add, fade out and collapse on removal (strike-through doesn't count, the row stays); no animation on first render
  const shown = usePresence(rows, (r) => r.a, draft !== null);
  if (!draft) return null;

  // Nodes already have lists: offer a one-time import (merged across nodes) while the shared list is empty; also why saving an empty list is refused (same backend data, returns 409)
  const holders = targets.filter((r) => (r.currentAllowlist?.length ?? 0) > 0);
  const canImport = isShared && !saved.length && !draft.length && holders.length > 0;
  // The shared list cannot be saved empty (when nodes already have lists); an empty extra list means none, use "Remove all"
  // The shared list can be saved empty (both the page and the confirm dialog warn); for a node that never saved and is empty there is nothing to apply
  const emptyBlocked = !isShared && !draft.length && !saved.length;
  // The apply bar only covers "what changed in this list"; a node out of sync with the platform is always fixed with "Re-apply" on that node's tab (same as peer settings)
  const canSave = (dirty || isNew) && !emptyBlocked && !save.isPending;
  const enabled = targets.some((r) => r.state === 'in_sync' || r.state === 'drift' || r.state === 'rejected');
  // accounts this node loses / gains when reverting to shared
  const lower = (l: string[]) => new Set(l.map((a) => a.toLowerCase()));
  // diff between draft and applied (the apply bar summary)
  const draftAdded = draft.filter((a) => !lower(saved).has(a.toLowerCase())).length;
  const draftRemoved = saved.filter((a) => !lower(draft).has(a.toLowerCase())).length;
  // Nodes whose write failed are listed separately with the reason Besu returned (only after applying)
  const refused = targets.filter((r) => r.state === 'rejected');
  // Apply scope: nodes that will be pushed, nodes on a custom list (unaffected), nodes not enabled (skipped)
  const applyTo = targets.filter((r) => (r.state === 'in_sync' || r.state === 'drift' || r.state === 'rejected') && !notSharedIds.includes(r.nodeId)).map((r) => r.nodeId);
  const notSharedOn = targets.filter((r) => (r.state === 'in_sync' || r.state === 'drift' || r.state === 'rejected') && notSharedIds.includes(r.nodeId)).map((r) => r.nodeId);
  const offIds = targets.filter((r) => r.state === 'disabled' || r.state === 'unreachable').map((r) => r.nodeId);
  const sep = t('allowlist.sep');
  // Apply bar: only when there are changes, a just-opened custom list, or a node out of sync
  const showBar = canSave || save.isPending || save.isError;

  return (
    <section className={CARD}>
      {/* Card title row: shared list (count + rules ⓘ); for a node it is "Node-specific allowlist", with remove-all / add on the right */}
      <div className={CARD_HEAD}>
        <h2 className="flex items-center gap-2 text-sm font-semibold text-ink">
          {t(isShared ? 'allowlist.tabShared' : 'allowlist.ownTitle')}
        </h2>
        {/* The rules note belongs to the shared list: the shared list is always applied, nodes may only add their own */}
        {isShared && <HelpTip title={t('access.permTitle')}>{t('allowlist.help')}</HelpTip>}
        {/* Right side of the title row: for the shared list "Sync with consortium" (Enterprise) + add; for a node "Remove all" + add. The input only appears after clicking add, same on both */}
        <span className="ml-auto flex items-center gap-3">
          {/* Core: syncing the shared list with other consortium members is a small Enterprise tool, so use a text button that doesn't steal focus */}
          {isShared && !isEnterprise && <EnterpriseTeaser slug="consortium-sync" label={t('edition.consortium-sync.short')} />}
          {/* Remove all only strikes through this node's own additions; it takes effect on "Apply" */}
          {!readOnly && !isShared && draft.length > 0 && <RowAction danger onClick={() => setDraft([])}>{t('allowlist.toShared')}</RowAction>}
          {!readOnly && !adding && <Button onClick={() => setAdding(true)} className="inline-flex items-center gap-1"><Plus size={13} aria-hidden="true" />{t(isShared ? 'allowlist.sharedAdd' : 'allowlist.ownAdd')}</Button>}
          {/* count at the far right (site-wide rule) */}
          <CountChip unit={t('allowlist.unitAddr')}>{isShared ? saved.length : draft.length}</CountChip>
        </span>
      </div>
      <div className="flex min-w-0 flex-col gap-3 px-4 py-3">
      {!readOnly && canImport && <ImportBox holders={holders} onImport={() => { setDraft(unionOf(holders)); setImported(true); }} />}
      {adding && <AddAccount list={draft} exclude={isShared ? [] : shared} onAdd={(a) => setDraft([...draft, a])} onAddMany={(as) => as.length && setDraft([...draft, ...as])} />}
      {imported && <p className="text-xs text-ink-3">{t('allowlist.imported', { count: draft.length })}</p>}
      {/* Removing an applied address doesn't make it vanish: it gets a strike-through and the button turns into "Undo"; it is only removed on apply. Newly added, unapplied ones are tagged "New" and "Remove" drops them directly */}
      {shown.length > 0 ? (
        <ul aria-label={isShared ? t('allowlist.tabShared') : `${scope} ${t('allowlist.customSuffix')}`} className="max-h-[24rem] overflow-y-auto overscroll-contain rounded-md border border-hair">
          {shown.map(({ key, item: { a, kind }, state }) => (
            <li key={key} ref={presenceRef(state)} className="flex items-center gap-3 border-b border-hair px-3 py-2 last:border-0">
              <span className={`min-w-0 truncate font-mono text-xs ${kind === 'removed' ? 'text-ink-3 line-through' : 'text-ink-2'}`}>{a}</span>
              {naming === a ? (
                <NameEditor
                  value={names[a.toLowerCase()]}
                  label={t('allowlist.rename')}
                  placeholder={t('allowlist.namePlaceholder')}
                  onSave={(name) => (name ? api.setAccountName(a, name) : api.deleteAccountName(a))}
                  onDone={(saved) => { setNaming(null); void qc.invalidateQueries({ queryKey: NAMES_KEY }); if (saved) savedFlash.mark(a); }}
                />
              ) : (
                <span className="flex min-w-0 items-center gap-1">
                  {names[a.toLowerCase()] && <span key={savedFlash.key(a)} className={`truncate text-xs ${kind === 'removed' ? 'text-ink-3' : 'text-ink'} ${savedFlash.cls(a)}`}>{names[a.toLowerCase()]}</span>}
                  {!readOnly && kind !== 'removed' && (
                    <button type="button" onClick={() => setNaming(a)} aria-label={t('allowlist.renameOf', { address: a })} title={t('allowlist.rename')}
                      className="shrink-0 rounded p-0.5 text-ink-faint transition-colors hover:bg-elevated hover:text-ink">
                      <PencilSimple size={13} aria-hidden="true" />
                    </button>
                  )}
                </span>
              )}
              {kind === 'added' && <Pill tone="accent">{t('allowlist.tagAdded')}</Pill>}
              {kind === 'removed' && <Pill tone="warn">{t('allowlist.tagRemoving')}</Pill>}
              {!readOnly && <span className="ml-auto shrink-0">
                {kind === 'removed' ? (
                  <RowAction onClick={() => setDraft([...draft, a])} aria-label={t('allowlist.undoRemove', { address: a })}>
                    <ArrowCounterClockwise size={13} aria-hidden="true" />{t('allowlist.undo')}
                  </RowAction>
                ) : (
                  // Applied ones are struck through pending removal, new ones dropped directly; both are called "Remove" and look the same
                  <RowAction danger onClick={() => setDraft(draft.filter((x) => x !== a))} aria-label={t('allowlist.remove', { address: a })}>
                    {t('allowlist.removeBtn')}
                  </RowAction>
                )}
              </span>}
            </li>
          ))}
        </ul>
      ) : null}
      {/* Everything struck through (the shared list would become empty): still warn that applying rejects every transaction */}
      {rows.length > 0 && isShared && draft.length === 0 && enabled && (
        <Notice>{t('allowlist.emptyWarn')}</Notice>
      )}
      {shown.length > 0 ? null : !isShared ? (
        <p className="text-xs text-ink-3">{t('allowlist.noExtra')}</p>
      ) : !canImport && enabled && (
        <Notice>{t('allowlist.emptyWarn')}</Notice>
      )}
      {/* Apply bar: only shown when there is something to apply; not a card, just a row below the list (white background covers content scrolled under it).
          Sticks to the bottom of the viewport (sticky): with a long list, "Discard" and "Apply" stay visible while scrolling */}
      {!readOnly && showBar ? (
        <div className="sticky bottom-0 z-10 flex flex-wrap items-center gap-3 bg-surface py-1 text-xs">
          <span role="status" className="min-w-0 flex-1" style={save.isError ? { color: 'var(--bsos-status-error-fg)' } : { color: 'var(--bsos-text-2)' }}>
            {save.isError ? (save.error instanceof HttpError ? errMsg(save.error) : t('error.title'))
              : save.isPending ? t('allowlist.saving')
              : dirty ? t('allowlist.pendingSummary', { a: draftAdded, r: draftRemoved })
              : isNew ? t('allowlist.newDesc') : null}
            {!save.isError && !save.isPending && refused.map((r) => (
              <span key={r.nodeId} className="mt-0.5 block text-xs" title={r.error ?? undefined} style={{ color: 'var(--bsos-status-error-fg)' }}>{t('allowlist.refusedLine', { node: r.nodeId, error: itemMsg(r) ?? t('error.title') })}</span>
            ))}
          </span>
          {dirty && <Button onClick={() => { setDraft(saved); setImported(false); save.reset(); }} disabled={save.isPending} >{t('allowlist.discard')}</Button>}
          <Button variant="primary" disabled={!canSave} onClick={() => setConfirmApply(true)} >{t('allowlist.apply')}</Button>
        </div>
      ) : null}
      {outcome && (
        <Modal width="max-w-sm" onClose={() => setOutcome(null)}>
          <ModalTitle>{t(outcome.failed.length ? 'allowlist.resultPartialTitle' : 'allowlist.resultTitle')}</ModalTitle>
          <div className="flex flex-col gap-1.5 text-xs leading-relaxed text-ink-2">
            <p>{t('allowlist.appliedCount', outcome)}</p>
            {outcome.failed.map((f) => (
              <p key={f.nodeId} title={f.raw} style={{ color: 'var(--bsos-status-error-fg)' }}>{t('allowlist.resultFailLine', { node: f.nodeId, error: f.error })}</p>
            ))}
            {outcome.failed.length > 0 && <p className="text-ink-3">{t('allowlist.resultFailHint')}</p>}
          </div>
          <div className="flex justify-end pt-1">
            <Button onClick={() => setOutcome(null)}>{t('common.close')}</Button>
          </div>
        </Modal>
      )}
      {/* Confirm scope before applying: which nodes get it, which are on a custom list (unaffected), which are not enabled (skipped) */}
      {confirmApply && (
        <ConfirmModal
          title={t(isShared ? 'allowlist.applySharedTitle' : 'allowlist.applyCustomTitle', { node: scope })}
          body={
            <div className="flex flex-col gap-1.5">
              <p>{applyTo.length ? t('allowlist.applyTo', { nodes: applyTo.join(sep) }) : t('allowlist.applyNone')}</p>
              {isShared && draft.length === 0 && <Notice>{t('allowlist.emptyWarn')}</Notice>}
              {customIds.length > 0 && <p className="text-ink-3">{t('allowlist.applySkipCustom')}</p>}
              {notSharedOn.length > 0 && <p className="text-ink-3">{t('allowlist.applySkipNotShared', { nodes: notSharedOn.join(sep) })}</p>}
              {offIds.length > 0 && <p className="text-ink-3">{t('allowlist.applySkipOff', { nodes: offIds.join(sep) })}</p>}
            </div>
          }
          confirmLabel={save.isPending ? t('allowlist.saving') : t('allowlist.apply')}
          busy={save.isPending}
          onConfirm={() => save.mutate(draft)}
          onCancel={() => setConfirmApply(false)}
        />
      )}
      </div>
    </section>
  );
}

/**
 * Pick addresses out of CSV (or any plain text): commas, semicolons, newlines and whitespace all separate; non-addresses (header row, note columns) are skipped.
 * Returns the newly added addresses and how many were skipped (invalid or already in the list).
 */
function parseAddresses(text: string, existing: string[]): { add: string[]; skipped: number } {
  const seen = new Set(existing.map((a) => a.toLowerCase()));
  const add: string[] = [];
  let skipped = 0;
  for (const raw of text.split(/[\s,;]+/)) {
    const v = raw.trim().replace(/^["']|["']$/g, '');
    if (!v) continue;
    if (!ADDR_RE.test(v)) { if (/^0x/i.test(v)) skipped++; continue; } // only count things that "look like an address but are invalid" as skipped, not header words
    if (seen.has(v.toLowerCase())) { skipped++; continue; }
    seen.add(v.toLowerCase());
    add.push(v);
  }
  return { add, skipped };
}

/** Add one account address: invalid format and duplicates are blocked on the spot. A CSV import beside it adds many at once (still a draft, needs apply) */
function AddAccount({ list, exclude = [], onAdd, onAddMany }: { list: string[]; exclude?: string[]; onAdd: (a: string) => void; onAddMany: (a: string[]) => void }) {
  const { t } = useTranslation();
  const [raw, setRaw] = useState('');
  const [err, setErr] = useState<string | null>(null);
  const [csvMsg, setCsvMsg] = useState<string | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const importFile = async (f: File) => {
    const { add, skipped } = parseAddresses(await f.text(), [...list, ...exclude]);
    onAddMany(add);
    setCsvMsg(add.length ? t('allowlist.csvDone', { file: f.name, n: add.length, skipped }) : t('allowlist.csvNone', { file: f.name }));
  };
  const add = (e: React.FormEvent) => {
    e.preventDefault();
    const v = raw.trim();
    if (!ADDR_RE.test(v)) return setErr(t('access.permAccountInvalid'));
    if (list.some((a) => a.toLowerCase() === v.toLowerCase())) return setErr(t('access.permAccountDup'));
    if (exclude.some((a) => a.toLowerCase() === v.toLowerCase())) return setErr(t('allowlist.inShared'));
    onAdd(v);
    setRaw('');
    setErr(null);
  };
  return (
    <form onSubmit={add} className="flex flex-wrap items-start gap-2">
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <input
          value={raw}
          onChange={(e) => { setRaw(e.target.value); setErr(null); }}
          placeholder={t('access.permAccountPlaceholder')}
          aria-label={t('allowlist.address')}
          spellCheck={false}
          autoComplete="off"
          className="w-full rounded-md border border-hair bg-canvas px-2.5 py-1.5 font-mono text-xs text-ink placeholder:font-sans placeholder:text-ink-3 focus:border-accent focus:outline-none"
        />
        {err && <span role="alert" className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>{err}</span>}
      </div>
      <Button type="submit" className="inline-flex shrink-0 items-center gap-1"><Plus size={13} aria-hidden="true" />{t('access.permAdd')}</Button>
      <Button type="button" onClick={() => fileRef.current?.click()} className="inline-flex shrink-0 items-center gap-1"><UploadSimple size={13} aria-hidden="true" />{t('allowlist.csvImport')}</Button>
      <input ref={fileRef} type="file" accept=".csv,.txt,text/csv,text/plain" className="hidden" aria-hidden="true"
        onChange={(e) => { const f = e.target.files?.[0]; e.target.value = ''; if (f) void importFile(f); }} />
      {/* Import format in one sentence: addresses separated by commas or newlines; no template needed */}
      <p className="basis-full text-2xs text-ink-3">{csvMsg ? <span role="status">{csvMsg}</span> : t('allowlist.csvHint')}</p>
    </form>
  );
}

/** Differences between the node and the platform's copy, one address per line: missing first, to-remove after; scrolls inside the box when long */
function GapList({ gap, names }: { gap: Gap; names: Record<string, string> }) {
  const { t } = useTranslation();
  const rows = [
    ...gap.missing.map((a) => ({ a, kind: 'missing' as const })),
    ...gap.extra.map((a) => ({ a, kind: 'extra' as const })),
  ];
  return (
    <ul aria-label={t('allowlist.gapTitle')} className="max-h-[12rem] overflow-y-auto overscroll-contain rounded-b-lg bg-canvas px-4 py-1.5">
      {rows.map(({ a, kind }) => (
        <li key={`${kind}:${a}`} className="flex items-center gap-2 py-1 text-xs">
          <Pill tone="warn" className="w-14 justify-center">{t(kind === 'missing' ? 'allowlist.gapTagMissing' : 'allowlist.gapTagExtra')}</Pill>
          <span className="min-w-0 truncate font-mono text-ink-2">{a}</span>
          {names[a.toLowerCase()] && <span className="truncate text-ink">{names[a.toLowerCase()]}</span>}
        </li>
      ))}
    </ul>
  );
}

/** First use: the platform list is empty and nodes already have lists; merge-import them into a draft in one click (still needs saving to take effect). */
function ImportBox({ holders, onImport }: { holders: PolicyApplyNodeResult[]; onImport: () => void }) {
  const { t } = useTranslation();

  const mixed = new Set(holders.map((r) => r.currentAllowlist!.map((a) => a.toLowerCase()).sort().join())).size > 1;
  // only list the first three nodes when there are many
  const names = holders.slice(0, 3).map((r) => r.nodeId).join(t('allowlist.sep'));
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-md border border-hair bg-canvas px-3 py-2.5 text-xs text-ink-2">
      <span className="min-w-0 flex-1">
        {t('allowlist.holders', { nodes: holders.length > 3 ? t('allowlist.holdersMore', { names, count: holders.length }) : names, count: unionOf(holders).length })}
        {mixed && t('allowlist.holdersMixed')}
      </span>
      <Button onClick={onImport} >{t('allowlist.import')}</Button>
    </div>
  );
}

/** Three-step guide for enabling the account allowlist. The last step lists nodes not enabled when the guide was opened; if all are enabled, list all. */
function AllowlistGuide({ nodes, nodeId, shared, own, onClose }: { nodes: NodeSummary[]; nodeId?: string; shared: string[]; own: string[]; onClose: () => void }) {
  const { t } = useTranslation();
  const [way, setWay] = useState<Way>('toml');
  const [ids] = useState(() => {
    if (nodeId) return [nodeId];
    const todo = nodes.filter((n) => reqStatus(n).perm.code === 'off');
    return (todo.length ? todo : nodes).map((n) => n.id);
  });
  // The allowlist file is written with the full list (shared list + this node's own additions), so the list is right after restart.
  // No empty file: Besu rejects every transaction with an empty list, so this node would accept nothing between the restart and someone clicking "Re-apply"
  const accounts = [...new Set([...shared, ...own].map((a) => a.toLowerCase()))];
  const fileBody = accounts.length ? `accounts-allowlist=[\n${accounts.map((a) => `  "${a}"`).join(',\n')}\n]` : 'accounts-allowlist=[]';
  const ownCount = accounts.length - new Set(shared.map((a) => a.toLowerCase())).size;
  // Same four labels as the right column; the guide can only see "enabled or not", whether the list is in sync is checked back in the tabs
  const stateOf = (n: NodeSummary): NodeState => {
    const code = reqStatus(n).perm.code;
    return code === 'on' ? { tone: 'healthy', label: t('allowlist.state.in_sync') }
      : code === 'unknown' ? { tone: 'neutral', label: t('allowlist.state.unreachable') }
      : { tone: 'neutral', label: t('allowlist.state.disabled') };
  };
  const lines = [...PERM_LINES, `rpc-http-api=["${t('allowlist.existingApis')}","PERM"]`];
  return (
    <StepWizard
      title={t('allowlist.guideTitle')}
      onClose={onClose}
      steps={[
        {
          title: t('allowlist.stepFile'),
          body: (
            <>
              {accounts.length
                ? <p className="text-xs text-ink-2">{t(ownCount > 0 ? 'allowlist.fileFilledOwn' : 'allowlist.fileFilled', { count: accounts.length - ownCount, own: ownCount })}</p>
                : <Notice>{t('allowlist.fileEmpty')}</Notice>}
              <CopyCmd value={`sudo tee ${ALLOWLIST_PATH} > /dev/null <<'EOF'\n${fileBody}\nEOF\nsudo chown besu:besu ${ALLOWLIST_PATH}`} />
            </>
          ),
        },
        { title: t('rpcAuth.stepConfig'), body: <><WaySeg way={way} onChange={setWay} /><WayHint way={way} existing="PERM" /><CopyCmd value={snippetOf(lines, way)} /></>, doc: { href: BESU_DOCS.accountAllowlist, label: t('besuDoc.accountAllowlist') } },
        {
          title: t('rpcAuth.stepRestart'),
          body: <><p className="text-xs text-ink-3">{t('rpcAuth.restartNote')}</p><LiveNodeStates ids={ids} stateOf={stateOf} label={t('rpcAuth.listLabel')} /></>,
        },
      ]}
    />
  );
}

