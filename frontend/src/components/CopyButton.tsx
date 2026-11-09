import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Check, Copy, Warning } from '@phosphor-icons/react';
import { copyToClipboard } from '@/lib/clipboard';

const variantCls = {
  cmd: 'inline-flex shrink-0 items-center gap-1.5 rounded-md border border-strong px-3 py-2 text-xs text-ink transition-colors hover:bg-elevated',
  cell: 'shrink-0 text-ink-3 transition-colors hover:text-ink',
  corner: 'absolute right-1.5 top-1.5 inline-flex rounded-md border border-hair bg-surface p-1.5 text-ink-3 transition-colors hover:text-ink focus:outline-none focus-visible:ring-1 focus-visible:ring-accent',
} as const;
const iconSize = { cmd: 14, cell: 12, corner: 14 } as const;

/**
 * Shared icon-only copy button (consolidates duplicate implementations formerly spread over 5 files). Default cell: a small icon after the text, no frame.
 * aria-label + sr-only role=status: keyboard and screen reader users also get the "Copied" feedback.
 * stop: stops propagation when the table row itself is clickable.
 */
export function CopyButton({ value, variant = 'cell', stop = false, label, onCopied }: { value: string; variant?: keyof typeof variantCls; stop?: boolean; label?: string; onCopied?: () => void }) {
  const { t } = useTranslation();
  // null = not pressed yet; false = copy failed (must be visible, otherwise users assume it was copied)
  const [done, setDone] = useState<boolean | null>(null);
  return (
    <button
      type="button"
      aria-label={label ?? t('common.copy')}
      onClick={async (e) => {
        if (stop) e.stopPropagation();
        // Show the check only when the copy really succeeded: a check on failure would send users off to paste an empty clipboard
        const ok = await copyToClipboard(value);
        setDone(ok);
        if (ok) { onCopied?.(); setTimeout(() => setDone(null), 1500); }
      }}
      title={done === false ? t('common.copyFailed') : undefined}
      className={variantCls[variant]}
      style={done === false ? { color: 'var(--bsos-status-error-fg)' } : undefined}
    >
      {done ? <Check size={iconSize[variant]} aria-hidden="true" /> : done === false ? <Warning size={iconSize[variant]} aria-hidden="true" /> : <Copy size={iconSize[variant]} aria-hidden="true" />}
      <span className="sr-only" role="status">{done ? t('common.copied') : done === false ? t('common.copyFailed') : ''}</span>
    </button>
  );
}

/**
 * Code block with a copy button at the top right. Wraps by default with no horizontal scroll: breaks at whitespace first, and only
 * single long strings (URLs, public keys) break mid-string; existing line breaks are kept; the copied content is unchanged.
 * wrap={false} switches back to single-line horizontal scrolling.
 */
/** flash: when the number changes, the command box background flashes once (e.g. regenerating the join code: the old command is invalid, reminding users to copy again). */
export function CopyCmd({ value, wrap = true, size = 'xs', onCopied, flash = 0 }: { value: string; wrap?: boolean; size?: 'xs' | 'sm'; onCopied?: () => void; flash?: number }) {
  return (
    <div className="relative min-w-0">
      <code
        key={flash}
        className={`block rounded-md border border-hair bg-elevated py-2 pl-3 pr-11 font-mono text-ink [--flash-to:var(--color-elevated)] ${flash ? 'flash-once' : ''} ${size === 'sm' ? 'text-sm' : 'text-xs'} ${
          wrap ? 'whitespace-pre-wrap leading-relaxed [overflow-wrap:anywhere]' : 'overflow-x-auto whitespace-pre'
        }`}
      >
        {value}
      </code>
      <CopyButton value={value} variant="corner" onCopied={onCopied} />
    </div>
  );
}
