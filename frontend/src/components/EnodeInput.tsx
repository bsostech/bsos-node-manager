import { forwardRef } from 'react';
import { useTranslation } from 'react-i18next';
import { inputCls } from '@/components/Field';

const PREFIX = 'enode://';

/**
 * enode input: "enode://" is fixed inside the box; users only paste the <pubkey>@<IP>:<port> part.
 * Pasting the whole string (including enode://) also works, the prefix is stripped; onChange always returns the full enode://... string ('' when empty).
 */
export const EnodeInput = forwardRef<HTMLInputElement, {
  value: string;
  onChange: (enode: string) => void;
  invalid?: boolean;
  autoFocus?: boolean;
  id?: string;
  'aria-label'?: string;
  /** On Enter: e.g. add this entry to the list and move on to the next node */
  onEnter?: () => void;
}>(function EnodeInput({ value, onChange, invalid, autoFocus, id, 'aria-label': ariaLabel, onEnter }, ref) {
  const { t } = useTranslation();
  const rest = value.startsWith(PREFIX) ? value.slice(PREFIX.length) : value;
  return (
    <div className={`${inputCls} flex w-full items-center gap-0 font-mono text-xs focus-within:border-accent ${invalid ? 'border-[var(--bsos-status-error-fg)]' : ''}`}>
      <span aria-hidden="true" className="shrink-0 select-none text-ink-3">{PREFIX}</span>
      <input
        ref={ref}
        id={id}
        value={rest}
        onChange={(e) => {
          const v = e.target.value.trim().replace(/^enode:\/\//i, '');
          onChange(v ? PREFIX + v : '');
        }}
        placeholder={t('manualPeers.enodePlaceholder')}
        aria-label={ariaLabel}
        aria-invalid={invalid}
        spellCheck={false}
        autoComplete="off"
        autoFocus={autoFocus}
        onKeyDown={onEnter ? (e) => { if (e.key === 'Enter') { e.preventDefault(); onEnter(); } } : undefined}
        className="min-w-0 flex-1 bg-transparent font-mono text-xs text-ink outline-none placeholder:font-sans placeholder:text-ink-3"
      />
    </div>
  );
});
