import { useState } from 'react';
import { PencilSimple } from '@phosphor-icons/react';
import { useMutation } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { inputCls } from '@/components/Field';

/** Small pencil button for inline value editing (placed after the value): click to switch to an input; Enter or blur saves, Esc cancels. */
export function EditPencil({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} aria-label={label} title={label} className="rounded p-0.5 text-ink-faint transition-colors hover:bg-elevated hover:text-ink">
      <PencilSimple size={13} aria-hidden="true" />
    </button>
  );
}

/**
 * Rename directly in the row: selects all on entry, Enter or blur saves, Esc cancels, empty = remove the name.
 * Where it is stored is up to the caller (platform node name, peer name ...); this only handles input. Inside a clickable row it stops propagation so a click does not navigate away.
 * onDone(saved): true when saved, false when cancelled with Esc; the caller uses it to decide whether to flash the name (useSavedFlash).
 * Switching to the input does not shift the layout: the input's vertical padding is reduced and offset with negative margins, so it takes about one line of text in the row;
 * fill: inside a table cell, fill that column (column width fixed by the caller) instead of growing wider, so columns to the right are not pushed over.
 */
export function NameEditor({ value, placeholder, label, onSave, onDone, fill = false }: {
  value: string | null | undefined;
  placeholder?: string;
  label: string;
  onSave: (name: string | null) => Promise<unknown>;
  onDone: (saved: boolean) => void;
  fill?: boolean;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState(value ?? '');
  const save = useMutation({ mutationFn: () => onSave(name.trim() || null), onSuccess: () => onDone(true) });
  return (
    <form className={`flex items-center gap-2 ${fill ? 'w-full min-w-0' : ''}`} onClick={(e) => e.stopPropagation()} onSubmit={(e) => { e.preventDefault(); save.mutate(); }}>
      <input
        autoFocus
        value={name}
        onChange={(e) => setName(e.target.value)}
        onFocus={(e) => e.currentTarget.select()}
        onKeyDown={(e) => { if (e.key === 'Escape') onDone(false); }}
        onBlur={() => { if (!save.isPending) save.mutate(); }}
        maxLength={64}
        placeholder={placeholder}
        aria-label={label}
        className={`${inputCls.replace('py-1.5', 'py-1')} -my-[0.3125rem] ${fill ? 'w-full min-w-0' : 'w-48'}`}
      />
      {save.isError && <span className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>{t('error.title')}</span>}
    </form>
  );
}
