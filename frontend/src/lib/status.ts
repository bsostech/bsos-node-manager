/** NodeStatus -> color tone (maps to the brand.status CSS variables) + i18n key. */
import type { TFunction } from 'i18next';
import type { NodeStatus, NodeSummary, Alert } from '@/api/schemas';

export type Tone = 'healthy' | 'syncing' | 'error' | 'info' | 'neutral';

const STATUS_TONE: Record<NodeStatus, Tone> = {
  healthy: 'healthy',
  syncing: 'syncing',
  rpc_unreachable: 'syncing', // agent present, RPC not connected: missing config rather than broken, so quiet amber; see the capability checklist for why
  offline: 'error',
  unknown: 'error', // never connected: to the user it is just offline, not a separate state
};

export function statusTone(s: NodeStatus): Tone {
  return STATUS_TONE[s];
}

type StatusOf = Pick<NodeSummary, 'status' | 'besuRunning'>;

/** RPC unreachable and the platform confirms Besu is stopped (besuRunning === false): treated as offline in the UI (filters, counts, red). */
export const besuStopped = (n: StatusOf) => n.status === 'rpc_unreachable' && n.besuRunning === false;

/** Status for display: Besu stopped maps to offline. */
export const shownStatus = (n: StatusOf): NodeStatus => (besuStopped(n) ? 'offline' : n.status);

/** Status text: Besu stopped reads "Offline (Besu stopped)"; everything else follows the status value. */
export const statusLabel = (t: TFunction, n: StatusOf) => (besuStopped(n) ? t('status.besuStopped') : t(`status.${n.status}`));

/** Alert -> tone (resolved = recovered, quiet green; firing by severity: critical loud red, warning quiet amber). */
export function alertTone(a: Pick<Alert, 'status' | 'severity'>): Tone {
  if (a.status === 'resolved') return 'healthy';
  return a.severity === 'critical' ? 'error' : 'syncing';
}

/** Returns the foreground / background CSS variables for the tone (follows light / dark automatically). */
export function toneVars(tone: Tone): { color: string; background: string } {
  return {
    color: `var(--bsos-status-${tone}-fg)`,
    background: `var(--bsos-status-${tone}-bg)`,
  };
}
