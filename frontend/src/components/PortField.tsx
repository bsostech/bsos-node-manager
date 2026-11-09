import { useTranslation } from 'react-i18next';
import { TextButton } from '@/components/TextLinks';

export const portOk = (v: string) => /^\d+$/.test(v) && +v >= 1 && +v <= 65535;
/** empty string = use the default */
export const portValid = (v: string) => v === '' || portOk(v);

/**
 * Port input: empty means the default (shown as a gray placeholder); when filled it is dark and a "Reset" button appears to clear back to the default.
 * Default vs custom is told by text color, not by border or background (a gray background already means read-only or disabled). readOnly only displays, with no reset.
 */
export function PortField({ label, value, onChange, def, readOnly = false, autoFocus = false }: {
  label: string; value: string; onChange: (v: string) => void; def: number; readOnly?: boolean; autoFocus?: boolean;
}) {
  const { t } = useTranslation();
  const bad = !portValid(value);
  return (
    <label className="flex items-center gap-2 text-xs text-ink-3">
      {label}
      <input
        value={value}
        onChange={(e) => onChange(e.target.value.trim())}
        placeholder={String(def)}
        readOnly={readOnly}
        inputMode="numeric"
        autoFocus={autoFocus}
        aria-invalid={bad}
        className={`w-24 rounded-md border bg-canvas px-2.5 py-1.5 font-mono text-xs text-ink placeholder:text-ink-3 focus:outline-none ${readOnly ? 'border-transparent' : 'focus:border-accent'} ${bad ? 'border-[var(--bsos-status-error-fg)]' : readOnly ? '' : 'border-hair'}`}
      />
      {!readOnly && value !== '' && (
        <TextButton onClick={() => onChange('')}>{t('common.useDefault')}</TextButton>
      )}
    </label>
  );
}
