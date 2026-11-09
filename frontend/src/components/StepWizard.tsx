import { useState } from 'react';
import { useAuth } from '@/auth/AuthContext';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { CircleNotch, Wrench, X } from '@phosphor-icons/react';
import { api } from '@/api/client';
import type { NodeSummary } from '@/api/schemas';
import { Button } from '@/components/Button';
import { Modal, ModalTitle } from '@/components/Modal';
import { NodeLabel } from '@/components/NodeLabel';
import { SegmentedProgress } from '@/components/SegmentedProgress';
import { ErrorState, SkeletonRows } from '@/components/States';
import { StateLabel } from '@/components/BesuRequirements';
import type { Tone } from '@/lib/status';
import { ExtLink, TextButton } from '@/components/TextLinks';

/** doc: the official Besu docs for this step, appended at the bottom of the description (link text reads "Besu docs: topic") */
export type WizardStep = { title: string; body: React.ReactNode; doc?: { href: string; label: string } };
/** detail: the reason after the status (e.g. an error returned by the node), truncated when long with full text on hover; hint: original text added on hover. busy: spinner (in progress); flash: just updated, the row briefly lights up. */
export type NodeState = { tone: Tone; label: string; detail?: string; hint?: string; busy?: boolean; flash?: boolean };

/** Step popup for setup guides: one step at a time, previous / next, and "Done" on the last step closes it. */
export function StepWizard({ title, steps, onClose }: { title: string; steps: WizardStep[]; onClose: () => void }) {
  const { t } = useTranslation();
  const [i, setI] = useState(0);
  const last = i === steps.length - 1;
  return (
    <Modal onClose={onClose} width="max-w-2xl" contentClass="rounded-lg border border-hair bg-surface">
      <div className="flex items-center gap-3 border-b border-hair px-5 py-3">
        <ModalTitle>{title}</ModalTitle>
        {/* Progress on the right, tight against the close button: the title is its own block, and progress shows at a glance which step you are on */}
        <span className="ml-auto flex items-center gap-2">
          <SegmentedProgress segments={steps.map((_, k) => (k <= i ? 'done' : 'pending'))} />
          <span className="font-mono text-2xs text-ink-3">{i + 1} / {steps.length}</span>
        </span>
        <button type="button" onClick={onClose} aria-label={t('common.close')} className="rounded p-1 text-ink-3 transition-colors hover:bg-elevated hover:text-ink">
          <X size={16} aria-hidden="true" />
        </button>
      </div>
      <div className="flex flex-col gap-3 px-5 py-4">
        <h3 className="text-sm font-medium text-ink">{steps[i].title}</h3>
        {steps[i].body}
        {/* -ml-1.5 cancels the link's padding so the text aligns with the description above */}
        {steps[i].doc && <div className="-ml-1.5"><ExtLink href={steps[i].doc.href}>{steps[i].doc.label}</ExtLink></div>}
      </div>
      <div className="flex items-center justify-between border-t border-hair px-5 py-3">
        {/* The first step has no previous (no disabled button); an empty span keeps next right-aligned */}
        {i === 0 ? <span /> : <Button onClick={() => setI(i - 1)}>{t('common.prev')}</Button>}
        {last
          ? <Button variant="primary" onClick={onClose}>{t('common.done')}</Button>
          : <Button variant="primary" onClick={() => setI(i + 1)}>{t('common.next')}</Button>}
      </div>
    </Modal>
  );
}

/** One row per node: node + status; a long list scrolls inside the box instead of stretching the outer one. */
export function NodeStateList({ nodes, stateOf, label, className = 'max-h-[32rem]', extra }: {
  nodes: NodeSummary[]; stateOf: (n: NodeSummary) => NodeState; label?: string; className?: string;
  /** Extra marker next to the node name (e.g. "Custom" in the allowlist) */
  extra?: (n: NodeSummary) => React.ReactNode;
}) {
  return (
    <ul aria-label={label} className={`flex flex-col overflow-y-auto overscroll-contain ${className}`}>
      {nodes.map((n) => {
        const s = stateOf(n);
        return (
          <li key={n.id} className={`flex items-center justify-between gap-3 border-b border-hair px-4 py-2 text-sm transition-colors duration-700 last:border-0 ${s.flash ? 'bg-accent/10' : ''}`}>
            <span className="flex min-w-0 max-w-[60%] shrink-0 items-center gap-1.5"><span className="truncate"><NodeLabel id={n.id} name={n.name} /></span>{extra?.(n)}</span>
            <span className="flex min-w-0 items-center gap-1 text-xs">
              {s.busy
                ? <span className="inline-flex items-center gap-1.5 whitespace-nowrap text-ink-3"><CircleNotch size={12} className="animate-spin" aria-hidden="true" />{s.label}</span>
                : <StateLabel tone={s.tone} label={s.label} />}
              {s.detail && <span className="truncate text-ink-3" title={s.hint && s.hint !== s.detail ? `${s.detail}\n${s.hint}` : s.detail}>· {s.detail}</span>}
            </span>
          </li>
        );
      })}
    </ul>
  );
}

/** Last step of a guide: lists the current status of the given nodes, refetching the node list every 5 seconds, so no check button is needed. */
export function LiveNodeStates({ ids, stateOf, label }: { ids: string[]; stateOf: (n: NodeSummary) => NodeState; label: string }) {
  const q = useQuery({ queryKey: ['nodes'], queryFn: () => api.listNodes(), refetchInterval: 10_000 });
  if (!q.data) return q.isError ? <ErrorState onRetry={() => void q.refetch()} /> : <SkeletonRows rows={3} />;
  const nodes = ids.flatMap((id) => q.data.nodes.find((n) => n.id === id) ?? []);
  // Not listed when there is only one node (opened from that node's tab): its status is already visible on that tab, so another row here would be a duplicate
  if (nodes.length === 1) return null;
  return (
    <div className="overflow-hidden rounded-md border border-hair">
      <NodeStateList nodes={nodes} stateOf={stateOf} label={label} className="max-h-[18rem]" />
    </div>
  );
}

/** "Setup guide" link (blue text + wrench, same as TextButton); label can be a more specific text (e.g. "Enable setup guide"). */
export function GuideButton({ onClick, label }: { onClick: () => void; label?: string }) {
  const { t } = useTranslation();
  // Guides are for people who will change node settings (operators); viewers only see status, so no "Enable setup guide" is shown
  const { can } = useAuth();
  if (!can('nodes:onboard')) return null;
  return (
    <TextButton onClick={onClick}>
      <Wrench size={13} aria-hidden="true" />
      {label ?? t('rpcAuth.guide')}
    </TextButton>
  );
}
