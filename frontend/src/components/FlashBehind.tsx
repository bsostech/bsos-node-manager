import type { CSSProperties } from 'react';
import type { Tone } from '@/lib/status';

/**
 * A background layer inside a cell, underneath its content, that flashes once whenever token changes (.flash-once, fades over 1.5s).
 * A separate layer instead of remounting the cell, so an open ⓘ or input inside isn't closed. The parent needs `relative isolate`.
 * tone sets a status color (e.g. the light error background when worsening to "critical"); omitted, it is the light primary background.
 */
export function FlashBehind({ token, tone }: { token: number; tone?: Tone }) {
  if (!token) return null;
  return (
    <span
      key={token}
      aria-hidden
      className="flash-once pointer-events-none absolute inset-0 -z-10"
      style={tone ? ({ '--flash': `var(--bsos-status-${tone}-bg)` } as CSSProperties) : undefined}
    />
  );
}
