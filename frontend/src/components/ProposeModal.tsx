import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import { Button } from '@/components/Button';
import { inputCls } from '@/components/Field';
import { Listbox } from '@/components/Listbox';
import { Modal, MODAL_SHELL, ModalFoot, ModalHead } from '@/components/Modal';
import { ProposeConfirm, VoteResults } from '@/components/ProposeConfirm';
import { fmtAddr } from '@/lib/format';
import { isAddress, sameAddr, setAfter, validatorName } from '@/lib/validators';

/**
 * Propose a vote (modal): enter a new address to add, or pick from current validators to remove; casts one vote from each platform-managed validator (POST /validator-votes).
 * Pressing "Send" first opens a confirm dialog (target, validator count and block-production threshold after it passes) and only sends after confirming; changing the validator set is a big deal, so it is confirmed first just like "Vote".
 * Closes once every node has sent, and the new proposal appears under "Voting"; only if some validators failed to send are the reasons listed per node in the modal.
 */
export function ProposeModal({ initialAdd = true, initialAddress = '', onClose }: { initialAdd?: boolean; initialAddress?: string; onClose: () => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [add, setAdd] = useState(initialAdd);
  const [addr, setAddr] = useState(initialAddress);
  const [confirm, setConfirm] = useState(false);
  const set = useQuery({ queryKey: ['validators'], queryFn: api.listValidatorSet });
  // The target being added is not yet in the validator set: if it is one of our nodes, show the node name to help confirm
  const nodes = useQuery({ queryKey: ['nodes'], queryFn: () => api.listNodes() });
  const send = useMutation({
    mutationFn: () => api.proposeValidatorVote(addr.trim(), add),
    onSuccess: async (r) => {
      await Promise.all([qc.invalidateQueries({ queryKey: ['validator-votes'] }), qc.invalidateQueries({ queryKey: ['validators'] })]);
      setConfirm(false);
      if (r.results.every((x) => x.ok)) onClose();
    },
  });

  const failed = send.data?.results.filter((x) => !x.ok).length ?? 0;
  if (send.data && failed > 0) {
    return (
      <Modal onClose={onClose} contentClass={MODAL_SHELL}>
        <ModalHead onClose={onClose}>{t('validators.sentFailed', { n: failed })}</ModalHead>
        <div className="px-5 py-4"><VoteResults results={send.data.results} /></div>
        <ModalFoot><Button variant="primary" onClick={onClose}>{t('common.close')}</Button></ModalFoot>
      </Modal>
    );
  }

  const a = addr.trim();
  const ok = isAddress(a);
  const hit = set.data?.find((v) => sameAddr(v.address, a));
  const node = add && ok ? nodes.data?.nodes.find((n) => sameAddr(n.address, a)) : undefined;
  let hint: { err: boolean; text: string } | null = null;
  if (a && !ok) hint = { err: true, text: t('validators.badAddress') };
  else if (ok && add && hit) hint = { err: true, text: t('validators.alreadyValidator') };
  else if (ok && !add && set.data && !hit) hint = { err: true, text: t('validators.notInSet') };
  // When removing, the name is already in the dropdown option, so do not repeat it
  else if (node) hint = { err: false, text: node.name ?? node.id };
  const canSend = ok && !hint?.err && !send.isPending;
  const after = canSend && set.data ? setAfter(set.data.length, add) : null;

  return (
    <Modal onClose={onClose} dismissOnOutside={false} contentClass={MODAL_SHELL}>
      <ModalHead onClose={onClose}>{t('validators.propose')}</ModalHead>
      <form className="flex flex-col" onSubmit={(e) => { e.preventDefault(); if (canSend) { send.reset(); setConfirm(true); } }}>
        <div className="flex flex-col gap-4 px-5 py-4">
          <div className="flex flex-col items-start gap-1.5">
            <span className="text-xs font-medium text-ink">{t('validators.action')}</span>
            <Listbox
              width="w-auto"
              aria-label={t('validators.action')}
              value={add ? 'add' : 'remove'}
              options={[{ value: 'add', label: t('validators.optAdd') }, { value: 'remove', label: t('validators.optRemove') }]}
              onChange={(x) => { setAdd(x === 'add'); setAddr(''); }}
            />
          </div>
          {/* Remove: pick from current validators (name + shortened address) instead of pasting a 42-char address; add: a new address can only be typed */}
          {!add && set.data ? (
            <div className="flex flex-col items-start gap-1.5">
              <span className="text-xs font-medium text-ink">{t('validators.pickToRemove')}</span>
              <Listbox
                width="w-auto"
                aria-label={t('validators.pickToRemove')}
                value={addr}
                placeholder={t('validators.pickPlaceholder')}
                options={set.data.map((v) => ({ value: v.address, label: `${validatorName(v) ?? t('chain.unnamed')} · ${fmtAddr(v.address)}` }))}
                onChange={setAddr}
              />
            </div>
          ) : (
            <label className="flex flex-col gap-1.5">
              <span className="text-xs font-medium text-ink">{t('validators.address')}</span>
              <input value={addr} onChange={(e) => setAddr(e.target.value)} placeholder="0x…" spellCheck={false} autoComplete="off" autoFocus className={`${inputCls} w-full font-mono`} />
            </label>
          )}
          {/* Hints (bad format, already a validator, our node's name) and the validator count after it passes; the space is reserved so the buttons below do not jump while typing. The confirm dialog repeats it */}
          <div className="flex min-h-5 flex-col gap-1 text-xs" aria-live="polite">
            {hint && <span className={hint.err ? '' : 'text-ink-2'} style={hint.err ? { color: 'var(--bsos-status-error-fg)' } : undefined}>{hint.text}</span>}
            {after && <span className="text-ink-2">{t('validators.confirmSet', { from: after.from, to: after.next, q: after.quorum, spare: after.spare })}</span>}
          </div>
        </div>
        <ModalFoot>
          <Button onClick={onClose}>{t('common.cancel')}</Button>
          {/* This only "confirms" the form is filled in; the next confirm dialog is what "sends" */}
          <Button type="submit" variant="primary" disabled={!canSend}>{t('common.confirm')}</Button>
        </ModalFoot>
      </form>
      {confirm && (
        <ProposeConfirm
          address={a}
          add={add}
          confirmLabel={t('validators.send')}
          busy={send.isPending}
          error={send.error}
          onConfirm={() => send.mutate()}
          onCancel={() => setConfirm(false)}
        />
      )}
    </Modal>
  );
}
