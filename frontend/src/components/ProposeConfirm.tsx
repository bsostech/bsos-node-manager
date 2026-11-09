import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import type { ValidatorVoteResult } from '@/api/schemas';
import { ConfirmModal } from '@/components/ConfirmModal';
import { errMsg, itemMsg } from '@/lib/errors';
import { sameAddr, setAfter, validatorName } from '@/lib/validators';

/** Per-node result: success says "Sent", failure shows the reason in red. */
export function VoteResults({ results }: { results: ValidatorVoteResult['results'] }) {
  const { t } = useTranslation();
  return (
    <ul className="flex flex-col gap-1.5 text-sm">
      {results.map((r) => (
        <li key={r.nodeId} className="flex flex-wrap items-baseline gap-3">
          <span className="font-mono text-ink">{r.nodeId}</span>
          {r.ok
            ? <span className="text-xs text-ink-2">{t('validators.sent')}</span>
            : <span className="text-xs" title={r.error ?? undefined} style={{ color: 'var(--bsos-status-error-fg)' }}>{itemMsg(r) ?? t('error.title')}</span>}
        </li>
      ))}
    </ul>
  );
}

/** Confirmation before propose/approve/resend: the action, the target (name first if there is one), and the validator count and block-production threshold once it takes effect. Pass mutation.error as error. */
export function ProposeConfirm({ address, add, confirmLabel, busy, error, onConfirm, onCancel }: {
  address: string; add: boolean; confirmLabel: string; busy: boolean; error: unknown; onConfirm: () => void; onCancel: () => void;
}) {
  const { t } = useTranslation();
  const set = useQuery({ queryKey: ['validators'], queryFn: api.listValidatorSet });
  // The target being added is not yet in the validator set: if it is one of our nodes, use the node name
  const nodes = useQuery({ queryKey: ['nodes'], queryFn: () => api.listNodes() });
  const node = nodes.data?.nodes.find((n) => sameAddr(n.address, address));
  const name = validatorName(set.data?.find((v) => sameAddr(v.address, address))) ?? node?.name ?? node?.id;
  const s = set.data && setAfter(set.data.length, add);
  return (
    <ConfirmModal
      title={t(add ? 'validators.confirmAdd' : 'validators.confirmRemove')}
      body={
        <div className="flex flex-col gap-1.5">
          {name && <span className="text-ink">{name}</span>}
          <span className="break-all font-mono text-xs">{address}</span>
          {/* Keep a little space between the post-change result and the target above (name, address) */}
          {s && <span className="mt-2">{t('validators.confirmSet', { from: s.from, to: s.next, q: s.quorum, spare: s.spare })}</span>}
          {error != null && <span role="alert" className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>{errMsg(error) || t('error.title')}</span>}
        </div>
      }
      confirmLabel={confirmLabel}
      busy={busy}
      onConfirm={onConfirm}
      onCancel={onCancel}
    />
  );
}
