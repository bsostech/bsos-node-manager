import type { ReactNode } from 'react';

export type Tone = 'neutral' | 'slate' | 'accent' | 'info' | 'ok' | 'warn' | 'err';

/* The color chip uses a status bg/fg pair, the same set as the node status lights; no invented colors. */
const TONES: Record<Tone, { background: string; color: string }> = {
  neutral: { background: 'var(--bsos-surface-elevated)', color: 'var(--bsos-status-neutral-fg)' },
  /* Blue-gray (the identity palette's slate shifted slightly toward blue): for when same-kind labels need to stand apart from plain gray (validator vs ordinary node).
     Pure slate gray is too much and looks lifeless; a touch of blue livens it up while staying a low-key identity color that does not compete with status colors */
  slate: { background: 'color-mix(in srgb, var(--bsos-id-blue-bg) 50%, var(--bsos-id-slate-bg))', color: 'color-mix(in srgb, var(--bsos-id-blue-fg) 35%, var(--bsos-id-slate-fg))' },
  accent: { background: 'var(--bsos-accent-soft)', color: 'var(--bsos-accent)' },
  /* Blue: marks "what this is" (diagnostic, informational), not a severity level */
  info: { background: 'var(--bsos-status-info-bg)', color: 'var(--bsos-status-info-fg)' },
  ok: { background: 'var(--bsos-status-healthy-bg)', color: 'var(--bsos-status-healthy-fg)' },
  warn: { background: 'var(--bsos-status-syncing-bg)', color: 'var(--bsos-status-syncing-fg)' },
  err: { background: 'var(--bsos-status-error-bg)', color: 'var(--bsos-status-error-fg)' },
};

/**
 * Small pill label: tags one fact (our org, validator, not participating in consensus, critical / warning).
 * Uses a color chip instead of coloring the whole sentence -- text stays ink and color lands only on a small label, so the screen does not get busy.
 */
export function Pill({ tone = 'neutral', children, className = '' }: { tone?: Tone; children: ReactNode; className?: string }) {
  return (
    <span className={`inline-flex shrink-0 items-center whitespace-nowrap rounded-full px-1.5 py-px text-2xs font-medium leading-4 ${className}`} style={TONES[tone]}>
      {children}
    </span>
  );
}
