import { useSyncExternalStore } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { api } from '@/api/client';
import type { Alert } from '@/api/schemas';
import { alertText } from '@/lib/alertRules';
import { refreshMs } from '@/lib/refreshInterval';

/**
 * Alert notifications = an inbox: one alert occurrence (fire to resolve) takes a single row whose state changes: while firing it shows critical / warning, after resolving it shows resolved and how long it lasted.
 * Resolving is news too: on resolve the row becomes unread again and moves to the top. When a value hovers around the threshold, the list is not flooded with fire / resolve pairs.
 * Read state is stored on the backend, per user (Alert.readFire / readResolve), so it is the same across browsers and machines.
 */
/** Our org (our own nodes, or our own platform service itself) / the whole chain. Alerts come only from this org's own platform, so there is no "someone else's platform" category */
export type NotifScope = 'own' | 'chain';
export type Notification = {
  /** Display identifier: one alert occurrence has two, one while firing and one after resolving (so it becomes unread again on resolve; the bell's ?open= link uses it too) */
  id: string;
  /** For marking read: the same for one alert occurrence from fire to resolve (Alert.id); the backend marks its current entry as read */
  alertId: string;
  /** Whether the current user has read this entry (readFire while firing, readResolve once resolved) */
  read: boolean;
  status: 'firing' | 'resolved';
  /** Row pill: by severity while firing, ok after resolving */
  level: 'err' | 'warn' | 'ok';
  startsAt: string;
  endsAt: string | null;
  /** Time of the last change (fire or resolve), for sorting and the row timestamp */
  at: string;
  title: string;
  detail: string;
  nodeId: string | null;
  alertname: string;
  /** Our org's issue (handle it ourselves) / a whole-chain issue (may need to contact other orgs): handled differently, so labelled separately on screen */
  scope: NotifScope;
};

/** Alerts that apply once to the whole chain, not per node (other node-less alerts are about our own platform service, e.g. a platform outage) */
const CHAIN_ALERTS = new Set(['ChainSlow', 'ChainSlowSevere', 'ValidatorsMissing', 'QuorumLost']);

// Mark read: record in memory first so the UI updates immediately, then send to the backend; refetch afterwards and trust the backend, reverting to unread if the send fails
let pending = new Set<string>();
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());
// The most recent notifications on screen (Notification.id -> its alertId and read flag), used when marking read to map to Alert.id and skip already-read ones
let shown = new Map<string, { alertId: string; read: boolean }>();
let refresh: (() => void) | null = null;
function optimistic(ids: string[]): string[] {
  const fresh = ids.filter((id) => !pending.has(id) && shown.get(id)?.read === false);
  if (fresh.length) { pending = new Set([...pending, ...fresh]); emit(); }
  return fresh;
}
function settle(ids: string[], p: Promise<unknown>) {
  void p.catch(() => { pending = new Set([...pending].filter((id) => !ids.includes(id))); emit(); }).finally(() => refresh?.());
}
/** Mark as read (takes a Notification.id) */
export function markRead(ids: string[]) {
  const fresh = optimistic(ids);
  if (fresh.length) settle(fresh, api.markAlertsRead([...new Set(fresh.map((id) => shown.get(id)!.alertId))]));
}
/** Mark all as read (ids are the currently unread ones on screen, so the UI updates immediately) */
export function markAllRead(ids: string[]) {
  settle(optimistic(ids), api.markAllAlertsRead());
}
function usePending(): Set<string> {
  return useSyncExternalStore((cb) => { listeners.add(cb); return () => { listeners.delete(cb); }; }, () => pending, () => pending);
}

function toNotifications(t: TFunction, alerts: Alert[]): Notification[] {
  return alerts.map((a): Notification => {
    const resolved = !!a.endsAt;
    // Title and likely cause follow the UI language (default rules are built from the locale files, see alertText)
    const text = alertText(t, a);
    return {
      id: `${a.id}|${resolved ? 'resolve' : 'fire'}`,
      alertId: a.id,
      read: resolved ? a.readResolve : a.readFire,
      status: resolved ? 'resolved' : 'firing',
      level: resolved ? 'ok' : a.severity === 'critical' ? 'err' : 'warn',
      startsAt: a.startsAt,
      endsAt: a.endsAt ?? null,
      at: a.endsAt ?? a.startsAt,
      title: text.summary,
      detail: text.cause,
      nodeId: a.nodeId ?? null,
      alertname: a.alertname,
      scope: !a.nodeId && CHAIN_ALERTS.has(a.alertname) ? 'chain' : 'own',
    };
  }).sort((x, y) => Date.parse(y.at) - Date.parse(x.at));
}

export function useNotifications() {
  const q = useQuery({ queryKey: ['alerts', 'all'], queryFn: () => api.listAlerts(), refetchInterval: refreshMs });
  const qc = useQueryClient();
  refresh = () => { void qc.invalidateQueries({ queryKey: ['alerts'] }); };
  const local = usePending();
  const { t } = useTranslation();
  const items = toNotifications(t, q.data?.alerts ?? []);
  shown = new Map(items.map((n) => [n.id, { alertId: n.alertId, read: n.read }]));
  const readIds = new Set(items.filter((n) => n.read || local.has(n.id)).map((n) => n.id));
  const unread = items.filter((n) => !readIds.has(n.id));
  // Monitoring source down: alerts are still listed but no new fires or resolves arrive, so state is frozen as of before the outage
  const monitoringDown = q.data?.monitoringAvailable === false;
  return { q, items, unread, isRead: (id: string) => readIds.has(id), monitoringDown };
}
