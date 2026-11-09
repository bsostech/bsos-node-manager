import { useTranslation } from 'react-i18next';
import type { NodeSummary, Alert } from '@/api/schemas';
import { statusTone, alertTone, shownStatus, statusLabel, besuStopped } from '@/lib/status';
import { Pill } from '@/components/Pill';

/**
 * Unified status vocabulary (no filled pills): colored dot + text. Normal = plain text; abnormal (error tone) = red text carries the emphasis.
 * Besu stopped but agent online: the main status still says "Offline", followed by a light red tag giving the reason
 * (the same kind of tag as RPC auth's "Key mismatch").
 */
export function NodeStatusBadge({ node }: { node: Pick<NodeSummary, 'status' | 'besuRunning'> }) {
  const { t } = useTranslation();
  const tone = statusTone(shownStatus(node));
  const loud = tone === 'error';
  const stopped = besuStopped(node);
  return (
    <span
      className={`inline-flex items-center gap-1.5 whitespace-nowrap text-xs font-medium ${loud ? '' : 'text-ink-2'}`}
      style={loud ? { color: 'var(--bsos-status-error-fg)' } : undefined}
    >
      <span
        className="size-1.5 shrink-0 rounded-full"
        style={{ background: `var(--bsos-status-${tone}-fg)` }}
      />
      {stopped ? t('status.offline') : statusLabel(t, node)}
      {stopped && <Pill tone="err">{t('status.besuStoppedNote')}</Pill>}
    </span>
  );
}

/** Status of an alert rule / a node under a rule: firing is red text, normal is a green dot with plain text, disabled is a gray dot. suffix follows the text (e.g. how long it has lasted). */

/** Alert state (resolved = quiet green, recovered; firing by severity: critical is loud red, warning is quiet amber). */
export function AlertStatusBadge({ alert }: { alert: Pick<Alert, 'status' | 'severity'> }) {
  const { t } = useTranslation();
  const tone = alertTone(alert);
  const loud = alert.status === 'firing' && alert.severity === 'critical';
  return (
    <span
      className={`inline-flex items-center gap-1.5 text-xs font-medium ${loud ? '' : 'text-ink-2'}`}
      style={loud ? { color: 'var(--bsos-status-error-fg)' } : undefined}
    >
      <span className="size-1.5 shrink-0 rounded-full" style={{ background: `var(--bsos-status-${tone}-fg)` }} />
      {t(`alerts.status.${alert.status}`)}
    </span>
  );
}


/** "Ours" marker: the only identity marker on the platform that uses accent (the one exception to the color rules); other orgs are always plain text. Only a small tag, never coloring the whole row (it would clash with the selected state). */
/** "Ours" marker: one style site-wide (accent capsule), used in org columns, node lists and vote details. */
export function OwnOrgTag({ children }: { children: React.ReactNode }) {
  return <Pill tone="accent">{children}</Pill>;
}

/** Legend swatch: solid (className sets the fill) or hollow (outline). */
export function Swatch({ className = '', outline = false }: { className?: string; outline?: boolean }) {
  return <span aria-hidden className={`inline-block size-2 shrink-0 rounded-[2px] ${outline ? 'shadow-[inset_0_0_0_1.5px_var(--color-accent)]' : className}`} />;
}
