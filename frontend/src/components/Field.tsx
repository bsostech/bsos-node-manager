/** Site-wide standard single-line input style (labeled layouts go through Field); pages with inline layout use it directly. Font size and padding match Listbox fields so they line up at equal height. */
export const inputCls =
  'rounded-md border border-hair bg-canvas px-2.5 py-1.5 text-xs text-ink placeholder:text-ink-3 focus:border-accent focus:outline-none';

export function Field({ label, value, onChange, type = 'text', placeholder, autoComplete, spellCheck }: { label: string; value: string; onChange: (v: string) => void; type?: string; placeholder?: string; autoComplete?: string; spellCheck?: boolean }) {
  return (
    <label className="flex flex-col gap-1 text-xs text-ink-3">
      {label}
      <input type={type} value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} autoComplete={autoComplete} spellCheck={spellCheck} className={inputCls} />
    </label>
  );
}
