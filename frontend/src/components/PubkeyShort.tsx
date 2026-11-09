import { useTranslation } from 'react-i18next';
import { Tooltip } from '@/components/Tooltip';
import { CopyButton } from '@/components/CopyButton';

/**
 * Public key in tables: only the first 8 and last 6 kept (middle truncated), full key on hover; the button beside it copies copy (the full enode by default).
 * Where it must be compared character by character (adding, editing a peer), show the full key with CopyCmd wrap.
 */
export function PubkeyShort({ pubkey, copy }: { pubkey: string; copy: string }) {
  const { t } = useTranslation();
  return (
    <span className="inline-flex items-center gap-1 whitespace-nowrap font-mono text-xs text-ink-2">
      <Tooltip label={<span className="break-all font-mono">{pubkey}</span>}>
        <span tabIndex={0} className="rounded-sm focus:outline-none focus-visible:ring-1 focus-visible:ring-accent">
          {pubkey.slice(0, 8)}…{pubkey.slice(-6)}
        </span>
      </Tooltip>
      <CopyButton value={copy} variant="cell" label={t('common.copyEnode')} />
    </span>
  );
}
