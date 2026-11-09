import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { Modal, ModalTitle } from '@/components/Modal';
import { Button } from '@/components/Button';

/** Confirmation dialog for destructive/important actions (name the target + one sentence on consequences; confirm/cancel same size, distinguished by color).
 *  Focus defaults to "Cancel" (Radix picks the first focusable element in the dialog), so a destructive action can't happen with a single Enter. */
export function ConfirmModal({
  title, body, confirmLabel, danger, busy, onConfirm, onCancel,
}: {
  title: string; body: ReactNode; confirmLabel: string; danger?: boolean; busy?: boolean; onConfirm: () => void; onCancel: () => void;
}) {
  const { t } = useTranslation();
  return (
    <Modal width="max-w-sm" onClose={onCancel}>
      <ModalTitle>{title}</ModalTitle>
      {/* Keep line breaks in the copy: long text is split into paragraphs by blank lines */}
      <div className="whitespace-pre-line text-xs leading-relaxed text-ink-2">{body}</div>
      <div className="flex justify-end gap-2 pt-1">
        <Button onClick={onCancel}>{t('common.cancel')}</Button>
        <Button variant={danger ? 'danger' : 'primary'} onClick={onConfirm} disabled={busy}>
          {confirmLabel}
        </Button>
      </div>
    </Modal>
  );
}
