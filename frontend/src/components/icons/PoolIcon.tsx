import type { SVGProps } from 'react';

/**
 * Transaction pool: an above-ground round pool (cylinder + a ladder) with a light fill for the water surface. The icon library only has the in-ground pool, so this one is drawn here;
 * stroke width close to phosphor bold, compatible with the Icon type (size / weight props accepted but unused), so it can go straight into CAT_ICON.
 */
export function PoolIcon({ size = 12, weight: _w, ...rest }: { size?: number | string; weight?: string } & SVGProps<SVGSVGElement>) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth={1.25} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...rest}>
      {/* water surface */}
      <ellipse cx="8.5" cy="9" rx="5.5" ry="1.9" fill="currentColor" fillOpacity="0.28" stroke="none" />
      {/* pool rim + cylinder */}
      <path d="M3 9a5.5 1.9 0 0 1 11 0" />
      <path d="M3 9v3a5.5 1.9 0 0 0 11 0V9" />
      <path d="M3 9a5.5 1.9 0 0 0 11 0" />
      {/* ladder: two handrail hooks + one rung */}
      <path d="M5.2 9.6V5.3a1.1 1.1 0 0 1 2.2 0" />
      <path d="M8.2 9.4V5.1a1.1 1.1 0 0 1 2.2 0" />
      <path d="M5.2 7.2h3" />
    </svg>
  );
}
