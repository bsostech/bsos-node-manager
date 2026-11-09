import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Check } from '@phosphor-icons/react';
import { Button } from '@/components/Button';

/**
 * Form save button: shows "Saving…" while saving. Fixed width that fits the longer label, so neither the button nor its neighbors move when the text changes;
 * while saving it keeps the primary color and cannot be pressed again (no flipping between gray and green). Saved feedback is the adjacent <SavedNote>.
 */
export function SaveButton({ onClick, pending, disabled = false, label, pendingLabel }: {
  onClick: () => void;
  pending: boolean;
  disabled?: boolean;
  label: string;
  pendingLabel: string;
}) {
  return (
    <Button
      variant="primary"
      onClick={pending ? undefined : onClick}
      disabled={!pending && disabled}
      aria-disabled={pending || undefined}
      className={`inline-flex min-w-[5.25rem] justify-center ${pending ? 'cursor-default' : ''}`}
    >
      {pending ? pendingLabel : label}
    </Button>
  );
}

/**
 * Saved: "✓ Saved" appears next to the save button and disappears after 2 seconds; no animation (fade in/out does not look smooth). Screen readers announce it.
 * at = the time it was saved (or when that submit was sent); it appears once for each new value, and old times over 5 seconds are ignored -- so if the component remounts after saving
 * and the parent passes the time back it still appears, but a later remount for some other reason does not make it pop up again.
 * Place it so that appearing / disappearing does not push the button: in a right-aligned row, to the left of that group of buttons.
 */
export function SavedNote({ at }: { at: number }) {
  const { t } = useTranslation();
  const [shown, setShown] = useState(false);
  useEffect(() => {
    if (!at || Date.now() - at > 5_000) return;
    setShown(true);
    const id = setTimeout(() => setShown(false), 2_000);
    return () => clearTimeout(id);
  }, [at]);
  return (
    <span role="status" className="inline-flex items-center">
      {shown && (
        <span className="inline-flex items-center gap-1 whitespace-nowrap text-xs text-ink-2">
          <Check size={14} weight="bold" aria-hidden style={{ color: 'var(--bsos-status-healthy-fg)' }} />
          {t('common.saved')}
        </span>
      )}
    </span>
  );
}
