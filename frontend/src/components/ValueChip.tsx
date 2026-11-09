import type { ReactNode } from 'react';

type Tone = 'dark' | 'warn' | 'err';
type Size = 'md' | 'sm';

/* Dark chip is the default; deviation uses an orange background, disconnected a red one, text always reversed out */
const TONES: Record<Tone, { background: string; color: string }> = {
  // Iron gray: a step back from the plain text color so the chip is not heavier than the title (white text contrast about 8.7:1; one step back in the dark theme too)
  dark: { background: 'color-mix(in srgb, var(--bsos-text) 78%, var(--bsos-surface))', color: 'var(--bsos-canvas)' },
  warn: { background: 'var(--bsos-status-syncing-fg)', color: '#fff' },
  err: { background: 'var(--bsos-status-error-fg)', color: '#fff' },
};

/**
 * Rounded reversed-out chip for the "current value": a chart's latest reading, a list's count.
 * md for large charts and counts; sm for separately shown small cards (large chips across a row of small cards would be too heavy). The unit shrinks to small semi-transparent text inside the chip.
 */
export function ValueChip({ children, unit, tone = 'dark', size = 'md', className = '' }: { children: ReactNode; unit?: ReactNode; tone?: Tone; size?: Size; className?: string }) {
  const sz = size === 'sm' ? 'px-1.5 py-px text-xs leading-5' : 'px-1.5 py-0.5 text-sm leading-5';
  return (
    <span className={`inline-flex shrink-0 items-baseline gap-1 whitespace-nowrap rounded-md font-mono font-medium tabular-nums ${sz} ${className}`} style={TONES[tone]}>
      {children}
      {unit != null && unit !== '' && <span className="font-sans text-2xs font-normal opacity-70">{unit}</span>}
    </span>
  );
}

/**
 * Count at the far right of a card title bar (one rule site-wide: count at the far right, dark chip with unit; tried tight against the title, but far right felt more natural).
 * One size smaller than chart readings: the count is secondary information and the chip must not be heavier than the title.
 */
export function CountChip({ children, unit, className }: { children: ReactNode; unit: ReactNode; className?: string }) {
  // Reserve at least two digits of width (monospace, right-aligned): when filtering moves the count between 1 and 2 digits, the chip and the dropdown beside it do not jump
  return <ValueChip size="sm" unit={unit} className={className}><span className="inline-block min-w-[2ch] text-right">{children}</span></ValueChip>;
}
