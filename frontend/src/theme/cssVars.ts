/**
 * Turn the brand.ts tokens into CSS variables injected into <head>.
 * Light goes on :root, dark on [data-theme="dark"]. brand.ts is the single source of truth.
 * Imported early in main.tsx so the variables exist before first paint (avoids a flash).
 */
import { brand, brandDark } from './brand';

type Mode = typeof brand | typeof brandDark;

function vars(m: Mode): Record<string, string> {
  return {
    '--bsos-canvas': m.canvas,
    '--bsos-surface': m.surface,
    '--bsos-surface-elevated': m.surfaceElevated,
    '--bsos-border-hair': m.borderHair,
    '--bsos-border-strong': m.borderStrong,
    '--bsos-border-active': m.borderActive,
    '--bsos-text': m.textPrimary,
    '--bsos-text-2': m.textSecondary,
    '--bsos-text-3': m.textTertiary,
    '--bsos-text-faint': m.textFaint,
    '--bsos-accent': m.accent,
    '--bsos-accent-hover': m.accentHover,
    '--bsos-accent-active': m.accentActive,
    '--bsos-accent-bright': m.accentBright,
    '--bsos-accent-soft': m.accentSoft,
    '--bsos-status-healthy-fg': m.status.healthy.fg,
    '--bsos-status-healthy-bg': m.status.healthy.bg,
    '--bsos-status-syncing-fg': m.status.syncing.fg,
    '--bsos-status-syncing-bg': m.status.syncing.bg,
    '--bsos-status-error-fg': m.status.error.fg,
    '--bsos-status-error-bg': m.status.error.bg,
    '--bsos-status-info-fg': m.status.info.fg,
    '--bsos-status-info-bg': m.status.info.bg,
    '--bsos-status-neutral-fg': m.status.neutral.fg,
    '--bsos-status-neutral-bg': m.status.neutral.bg,
    '--bsos-id-indigo-fg': m.identity.indigo.fg,
    '--bsos-id-indigo-bg': m.identity.indigo.bg,
    '--bsos-id-violet-fg': m.identity.violet.fg,
    '--bsos-id-violet-bg': m.identity.violet.bg,
    '--bsos-id-blue-fg': m.identity.blue.fg,
    '--bsos-id-blue-bg': m.identity.blue.bg,
    '--bsos-id-slate-fg': m.identity.slate.fg,
    '--bsos-id-slate-bg': m.identity.slate.bg,
  };
}

function block(selector: string, map: Record<string, string>): string {
  const body = Object.entries(map)
    .map(([k, v]) => `  ${k}: ${v};`)
    .join('\n');
  return `${selector} {\n${body}\n}`;
}

function buildStylesheet(): string {
  return [block(':root', vars(brand)), block('[data-theme="dark"]', vars(brandDark))].join('\n\n');
}

const STYLE_ID = 'bsos-theme-vars';

export function injectThemeVars(): void {
  if (typeof document === 'undefined') return;
  if (document.getElementById(STYLE_ID)) return;
  const el = document.createElement('style');
  el.id = STYLE_ID;
  el.textContent = buildStylesheet();
  document.head.appendChild(el);
}

// module side effect: importing injects
injectThemeVars();
