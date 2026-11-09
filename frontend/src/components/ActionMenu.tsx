import { useEffect, useRef, useState } from 'react';
import { DotsThree } from '@phosphor-icons/react';

export type ActionMenuItem = {
  key: string;
  label: string;
  /** Destructive action: text turns error color (no filled pill) */
  danger?: boolean;
  onSelect: () => void;
};

/**
 * Shared row action menu (⋯): destructive or rarely used actions are folded away instead of taking permanent space.
 * Keyboard/focus handling lives here (dropdown keyboard behavior belongs to shared components, not rewritten per page):
 * focus the first item on open, ArrowUp/Down/Home/End to move, Escape closes and restores focus, Tab leaves naturally, outside click closes.
 * onSelect always fires after the menu has closed and focus is restored (so a following modal starts from a clean focus point).
 */
export function ActionMenu({ triggerLabel, items, label }: {
  triggerLabel: string;
  items: ActionMenuItem[];
  /** If given, render as a labeled button (for toolbars, same gray boxed style as filters); otherwise it is the row-end ⋯ */
  label?: React.ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    // Focus the first item on open (menu pattern: focus moves into the menu, arrow keys navigate)
    ref.current?.querySelector<HTMLElement>('[role="menuitem"]')?.focus();
    return () => document.removeEventListener('mousedown', onDown);
  }, [open]);
  const close = () => {
    setOpen(false);
    triggerRef.current?.focus(); // after closing, return focus to the trigger
  };
  const onMenuKeys = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') { e.stopPropagation(); close(); return; }
    if (e.key === 'Tab') { setOpen(false); return; } // Tab leaves naturally: close the menu without grabbing focus
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(e.key)) return;
    const nodes = Array.from(ref.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? []);
    if (nodes.length === 0) return;
    e.preventDefault();
    const cur = nodes.indexOf(document.activeElement as HTMLElement);
    const next =
      e.key === 'Home' ? 0 :
      e.key === 'End' ? nodes.length - 1 :
      e.key === 'ArrowDown' ? (cur + 1) % nodes.length :
      (cur - 1 + nodes.length) % nodes.length;
    nodes[next].focus();
  };
  return (
    <div ref={ref} className="relative" onKeyDown={onMenuKeys}>
      <button
        ref={triggerRef}
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={triggerLabel}
        onClick={() => setOpen((o) => !o)}
        className={label
          ? 'inline-flex items-center gap-1.5 rounded-md border border-hair bg-elevated px-2.5 py-1.5 text-xs text-ink-2 transition-colors hover:text-ink focus:outline-none focus-visible:ring-1 focus-visible:ring-accent'
          : 'rounded-md p-1 text-ink-3 transition-colors hover:bg-elevated hover:text-ink focus:outline-none focus-visible:ring-1 focus-visible:ring-accent'}
      >
        {label ?? <DotsThree size={18} weight="bold" aria-hidden="true" />}
      </button>
      {open && (
        <div role="menu" className="absolute right-0 top-full z-20 mt-1 w-40 rounded-md border border-hair bg-surface py-1 shadow-lg">
          {items.map((it) => (
            <button
              key={it.key}
              type="button"
              role="menuitem"
              onClick={() => { close(); it.onSelect(); }}
              className={
                it.danger
                  ? 'w-full px-3 py-1.5 text-left text-xs transition-colors hover:bg-elevated'
                  : 'w-full px-3 py-1.5 text-left text-xs text-ink-2 transition-colors hover:bg-elevated hover:text-ink'
              }
              style={it.danger ? { color: 'var(--bsos-status-error-fg)' } : undefined}
            >
              {it.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
