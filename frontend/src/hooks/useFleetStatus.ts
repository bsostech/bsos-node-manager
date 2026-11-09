import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import { refreshMs } from '@/lib/refreshInterval';
import type { FleetNodeStatus } from '@/api/schemas';

/** Binary model: a node is either unreachable (banner + legend ⊘ greyed out) or not listed, with no in-between.
 *  The backend lists a node only after its agent has been unreachable for 10 minutes, so short gaps stay silent. */
export type FleetUiStatusKind = FleetNodeStatus['status'];
export interface FleetUiStatus {
  kind: FleetUiStatusKind;
  since: string;
  /** Set only for unreachable, where the backend's sole reason is "node agent not responding"; the UI shows overview.srcAgentNoResponse instead of this English text. */
  reason?: string | null;
}

/** Group order in the status bar. */
export const GROUP_ORDER: FleetUiStatusKind[] = ['unreachable'];

/**
 * Fleet source status (per node, one per page): shared by the status bar and the cross-node trend legend.
 * Polling follows the "live" cadence; pausing live freezes the status too (so the charts are never stale while the status is fresh).
 */
export function useFleetStatus(autoRefresh: boolean) {
  const q = useQuery({
    queryKey: ['fleet-status'],
    queryFn: api.getFleetStatus,
    refetchInterval: autoRefresh ? refreshMs : false,
  });

  const byId = new Map<string, FleetUiStatus>();
  for (const s of q.data ?? []) {
    byId.set(s.nodeId, { kind: s.status, since: s.since, reason: s.reason });
  }

  /** Listed nodes grouped by status; StatusBar renders them directly. */
  const groups = GROUP_ORDER.map((kind) => ({
    kind,
    nodes: [...byId.entries()].filter(([, v]) => v.kind === kind).map(([id, v]) => ({ id, ...v })),
  })).filter((g) => g.nodes.length > 0);

  return { byId, groups, isLoading: q.isLoading };
}
