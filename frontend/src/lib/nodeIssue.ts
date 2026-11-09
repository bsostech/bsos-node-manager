import type { TFunction } from 'i18next';
import type { NodeSummary } from '@/api/schemas';
import { requiredMissing, requiredTone } from '@/lib/capabilities';
import type { Tone } from '@/lib/status';

/**
 * The node list "Details" column and the detail page status bar: pick only the single most urgent issue. Red = the platform cannot get this node's data or is rejected, yellow = data available but a piece is missing.
 * Decided only from the capability checklist, the quarantine flag and agent version rejection (what the platform actually measured), not sync progress, which the status column already shows. Optional items being off is not an issue (see lib/capabilities.ts).
 */
export function nodeIssue(n: Pick<NodeSummary, 'quarantinedAt' | 'agentOutdatedAt' | 'capabilities'>, t: TFunction): { tone: Tone; text: string } | null {
  const c = n.capabilities;
  if (n.quarantinedAt) return { tone: 'error', text: t('nodesPage.issue.quarantined') };
  // When the platform rejects an agent for being too old it is also not connected; state the reason (upgrade needed) first, not just "not connected"
  if (n.agentOutdatedAt) return { tone: 'error', text: t('nodesPage.issue.agentOutdated') };
  if (!c?.agent) return { tone: 'error', text: t('nodesPage.issue.agentDown') };
  // Color comes from the same source as the capability checklist and the settings page dots; here we only pick the most urgent one and phrase it
  const tone = requiredTone(c);
  if (!tone) return null;
  const text = c.rpcUnauthorized ? t('nodesPage.issue.rpc401')
    : c.rpcReachable === false ? t('nodesPage.issue.rpcDown')
    : c.metricsReachable === false ? t('nodesPage.issue.metricsDown')
    : t('nodesPage.issue.missing', { apis: requiredMissing(c).join(t('common.listSep')) });
  return { tone, text };
}
