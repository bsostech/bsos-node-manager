import { useTranslation } from 'react-i18next';
import { CircleNotch, CheckCircle, XCircle } from '@phosphor-icons/react';
import { Modal, ModalTitle } from './Modal';
import { Button } from './Button';
import { errMsg } from '@/lib/errors';

/** The three connection tests (SMTP/LDAP-OIDC/Webhook) share one spinner -> success/failure modal skeleton; only the copy differs. */
export function ConnTestModal({
  title,
  runningText,
  okText,
  failText,
  test,
  onRetry,
  onClose,
}: {
  title: string;
  runningText: string;
  okText: string;
  failText: string;
  test: { isPending: boolean; isIdle: boolean; isSuccess: boolean; isError: boolean; error: unknown };
  onRetry: () => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  return (
    <Modal width="max-w-sm" contentClass="gap-4 rounded-lg border border-hair bg-surface p-5 shadow-xl" onClose={onClose}>
      <ModalTitle>{title}</ModalTitle>
      {test.isPending || test.isIdle ? (
        <div className="flex flex-col items-center gap-3 py-4">
          <CircleNotch size={22} className="animate-spin text-ink-3" aria-hidden />
          <p className="text-xs leading-relaxed text-ink-2">{runningText}</p>
        </div>
      ) : test.isSuccess ? (
        <div className="flex flex-col items-center gap-3 py-4">
          <CheckCircle size={22} weight="fill" style={{ color: 'var(--bsos-status-healthy-fg)' }} aria-hidden />
          <p className="text-xs leading-relaxed text-ink-2">{okText}</p>
        </div>
      ) : (
        <div className="flex flex-col items-center gap-3 py-4 text-center">
          <XCircle size={22} weight="fill" style={{ color: 'var(--bsos-status-error-fg)' }} aria-hidden />
          <p role="alert" className="text-xs leading-relaxed text-ink-2">{errMsg(test.error) || failText}</p>
        </div>
      )}
      <div className="flex justify-end gap-2">
        {test.isError && (
          <Button variant="secondary" onClick={onRetry}>{t('common.retry')}</Button>
        )}
        <Button variant="primary" onClick={onClose}>{t('common.close')}</Button>
      </div>
    </Modal>
  );
}
