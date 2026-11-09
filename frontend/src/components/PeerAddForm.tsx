import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/Button';
import { EnodeInput } from '@/components/EnodeInput';
import { errMsg } from '@/lib/errors';

/** Row for adding by pasting an enode (shared by the shared list and node self-added peers). Clears the field only when onAdd succeeds. */
export function PeerAddForm({ idPrefix, pending, error, onAdd }: {
  idPrefix: string;
  pending: boolean;
  error: unknown;
  onAdd: (enode: string) => Promise<unknown>;
}) {
  const { t } = useTranslation();
  const [enode, setEnode] = useState('');
  return (
    <>
      <form
        className="flex flex-col gap-2 sm:flex-row sm:items-start"
        onSubmit={(e) => {
          e.preventDefault();
          void onAdd(enode.trim()).then(() => setEnode(''), () => {});
        }}
      >
        <EnodeInput id={`${idPrefix}-enode`} value={enode} onChange={setEnode} aria-label={t('manualPeers.enode')} />
        <Button type="submit" variant="primary" className="shrink-0 whitespace-nowrap" disabled={!enode.trim() || pending}>
          {pending ? t('manualPeers.adding') : t('manualPeers.add')}
        </Button>
      </form>
      {!!error && (
        <span role="alert" className="break-all text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>
          {errMsg(error) || t('error.title')}
        </span>
      )}
    </>
  );
}
