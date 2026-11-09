// Toggle controls: a switch for on/off settings and a button-style toggle for picking RPC namespaces.

/** Custom switch (replaces the native checkbox); on = accent track, off = plain track. */
export function Switch({ on, onChange, disabled, 'aria-label': ariaLabel }: { on: boolean; onChange: (v: boolean) => void; disabled?: boolean; 'aria-label'?: string }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={on}
      aria-label={ariaLabel}
      disabled={disabled}
      onClick={() => onChange(!on)}
      className={`relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors disabled:cursor-not-allowed disabled:opacity-50 ${on ? 'bg-accent' : 'border border-hair bg-elevated'}`}
    >
      <span className={`inline-block size-4 transform rounded-full bg-white shadow transition-transform ${on ? 'translate-x-[18px]' : 'translate-x-0.5'}`} />
    </button>
  );
}

/** Namespace toggle button: selected = accent border + solid text; unselected = plain border + secondary text (the border carries the state, no fill). */
export function NsToggle({ label, on, onClick, className = '' }: { label: string; on: boolean; onClick: () => void; className?: string }) {
  return (
    <button
      type="button"
      aria-pressed={on}
      onClick={onClick}
      className={`rounded-md border px-2 py-1 font-mono text-xs transition-colors ${className} ${
        on ? 'border-accent text-ink' : 'border-hair text-ink-3 hover:text-ink-2'
      }`}
    >
      {label}
    </button>
  );
}
