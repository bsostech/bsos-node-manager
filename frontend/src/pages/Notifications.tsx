import { useEffect, useState } from 'react';
import { MonitoringDownNote } from '@/components/MonitoringDown';
import { useTranslation } from 'react-i18next';
import { useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import { forPhrase, ruleCondition } from '@/lib/alertRules';
import { CaretDown } from '@phosphor-icons/react';
import { useNotifications, markRead, markAllRead, type Notification, type NotifScope } from '@/lib/notifications';
import { Listbox } from '@/components/Listbox';
import { CountChip } from '@/components/ValueChip';
import { Pill } from '@/components/Pill';
import { SkeletonRows, EmptyState, ErrorState } from '@/components/States';
import { fmtDateTimeSec } from '@/lib/format';
import type { TFunction } from 'i18next';
import { GoLink } from '@/components/TextLinks';
import { useAuth } from '@/auth/AuthContext';
import { Expand } from '@/components/Expand';

const PAGE = 30;

/** Duration in words: 25 min / 1 h 20 min / 2 d 3 h */
function spanText(t: TFunction, ms: number): string {
  const m = Math.max(1, Math.round(ms / 60_000));
  if (m < 60) return t('notif.spanM', { m });
  const h = Math.floor(m / 60), rm = m % 60;
  if (h < 24) return rm ? t('notif.spanHM', { h, m: rm }) : t('notif.spanH', { h });
  const d = Math.floor(h / 24), rh = h % 24;
  return rh ? t('notif.spanDH', { d, h: rh }) : t('notif.spanD', { d });
}

/** Target: always a pill. Own nodes show the node name (no node means this platform's own services); chain-wide ones get a blue-grey "Whole chain" */
function ScopeCell({ n }: { n: Notification }) {
  const { t } = useTranslation();
  if (n.scope === 'chain') return <Pill tone="slate" className="-ml-1.5">{t('notif.scope_chain')}</Pill>;
  return n.nodeId
    ? <Pill tone="neutral" className="-ml-1.5 font-mono">{n.nodeId}</Pill>
    : <Pill tone="neutral" className="-ml-1.5">{t('notif.platformService')}</Pill>;
}

/** Alert notifications: listed one by one like an inbox; expand for the explanation and where to go, expanding marks it read. Alert settings live in platform settings. */
export function Notifications() {
  const { t } = useTranslation();
  const { q, items, unread, isRead, monitoringDown } = useNotifications();
  const canEditRules = useAuth().can('nodes:onboard');
  // Trigger conditions are read from alert settings (same source as the settings page and page colors), not from the alert's own text
  const rules = useQuery({ queryKey: ['alertRules'], queryFn: () => api.listAlertRules(), staleTime: 60_000 });
  const condOf = (alertname: string) => { const r = rules.data?.find((x) => x.alertname === alertname); return r ? `${ruleCondition(t, r)}${forPhrase(t, r.forDuration)}` : null; };
  const [show, setShow] = useState<'all' | 'unread'>('all');
  const [level, setLevel] = useState<'all' | 'err' | 'warn' | 'ok'>('all');
  const [scope, setScope] = useState<'all' | NotifScope>('all');
  // Coming from the bell: ?open=<id> expands that notification, scrolls to it and marks it read
  const [params, setParams] = useSearchParams();
  const [open, setOpen] = useState<string | null>(params.get('open'));
  useEffect(() => {
    const id = params.get('open');
    if (!id || !q.data) return;
    const idx = items.findIndex((n) => n.id === id);
    setOpen(id); setShow('all'); setLevel('all'); setScope('all'); markRead([id]);
    if (idx >= 0) setShown((v) => Math.max(v, idx + 1));
    requestAnimationFrame(() => document.getElementById(`notif-${id}`)?.scrollIntoView({ block: 'center' }));
    setParams({}, { replace: true });
  }, [params, setParams, q.data, items]);
  const [shown, setShown] = useState(PAGE);
  const base = items.filter((n) => show === 'all' || !isRead(n.id));
  const byLevel = (n: Notification, l: typeof level) => l === 'all' || n.level === l;
  const byScope = (n: Notification, s: typeof scope) => s === 'all' || n.scope === s;
  const rows = base.filter((n) => byLevel(n, level) && byScope(n, scope));
  // Each dropdown option shows a count, computed under the other dropdown's (and "Unread only") current choice
  const scopeOpt = (value: typeof scope, label: string) => ({ value, label: t('common.withCount', { label, n: base.filter((n) => byLevel(n, level) && byScope(n, value)).length }) });
  const levelOpt = (value: typeof level, label: string) => ({ value, label: t('common.withCount', { label, n: base.filter((n) => byLevel(n, value) && byScope(n, scope)).length }) });
  const toggle = (n: Notification) => { setOpen((o) => (o === n.id ? null : n.id)); markRead([n.id]); };

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-4">
      {/* An inbox needs no refresh rate or refresh button: it updates in the background (same data as the bell), and new notifications appear at the top on their own */}
      <h1 className="text-lg font-semibold text-ink">{t('notif.title')}</h1>
      {monitoringDown && <MonitoringDownNote />}

      <section className="overflow-hidden rounded-lg border border-hair bg-surface">
        <div className="flex min-h-[54px] flex-wrap items-center gap-2 border-b border-hair px-4 py-2">
          <h2 className="text-sm font-semibold text-ink">{t('notif.listTitle')}</h2>
          {unread.length > 0 && (
            <button type="button" onClick={() => markAllRead(unread.map((n) => n.id))} className="rounded px-1.5 py-0.5 text-xs text-ink-3 transition-colors hover:bg-elevated hover:text-ink">
              {t('notif.markAll')}
            </button>
          )}
          {/* Only two states, so a checkbox will do; unchecked by default (show all) */}
          <label className="ml-auto flex items-center gap-2 text-xs text-ink-2">
            <input type="checkbox" checked={show === 'unread'} onChange={(e) => setShow(e.target.checked ? 'unread' : 'all')} className="accent-accent" />
            {t('notif.onlyUnread')}
          </label>
          <label className="flex items-center gap-2 text-xs text-ink-3">
            {t('notif.scope')}
            <Listbox width="w-auto" aria-label={t('notif.scope')} value={scope} onChange={setScope}
              options={[scopeOpt('all', t('notif.levelAll')), scopeOpt('own', t('notif.scope_own')), scopeOpt('chain', t('notif.scope_chain'))]} />
          </label>
          <label className="flex items-center gap-2 text-xs text-ink-3">
            {t('notif.level')}
            <Listbox width="w-auto" aria-label={t('notif.level')} value={level} onChange={setLevel}
              options={[levelOpt('all', t('notif.levelAll')), levelOpt('err', t('overview.level.err')), levelOpt('warn', t('overview.level.warn')), levelOpt('ok', t('notif.resolved'))]} />
          </label>
          <CountChip unit={t('notif.unit')}>{rows.length}</CountChip>
        </div>

        {q.isLoading ? (
          <div className="p-4"><SkeletonRows rows={5} /></div>
        ) : q.isError ? (
          <ErrorState onRetry={() => void q.refetch()} />
        ) : rows.length === 0 ? (
          <EmptyState message={show === 'unread' ? t('notif.noUnread') : t('notif.empty')} />
        ) : (
          <>
          {/* Header: column widths use the same classes as each row, so text aligns naturally */}
          <div className="flex items-center gap-3 border-b border-strong bg-elevated px-4 py-2 text-xs font-semibold tracking-[0.05em] text-ink-2" aria-hidden="true">
            <span className="size-1.5 shrink-0" />
            <span className="w-14 shrink-0">{t('notif.colStatus')}</span>
            <span className="min-w-0 flex-1">{t('notif.colAlert')}</span>
            <span className="hidden w-24 shrink-0 sm:inline">{t('notif.colTarget')}</span>
            <span className="w-[19ch] shrink-0 text-right">{t('notif.colTime')}</span>
            <span className="w-3 shrink-0" />
          </div>
          <ul>
            {rows.slice(0, shown).map((n) => {
              const unreadRow = !isRead(n.id);
              const expanded = open === n.id;
              return (
                <li key={n.id} id={`notif-${n.id}`} className="border-b border-hair last:border-0">
                  <button type="button" onClick={() => toggle(n)} aria-expanded={expanded}
                    className={`flex w-full items-center gap-3 px-4 py-2.5 text-left text-xs transition-colors hover:bg-elevated ${unreadRow ? 'bg-accent-soft/40' : ''}`}>
                    {/* Unread: a primary-color dot on the left + bold */}
                    <span className="size-1.5 shrink-0 rounded-full" style={{ background: unreadRow ? 'var(--bsos-accent)' : 'transparent' }} aria-hidden="true" />
                    <span className="w-14 shrink-0"><Pill tone={n.status === 'resolved' ? 'neutral' : n.level} className="-ml-1.5">{n.status === 'resolved' ? t('notif.resolved') : t(`overview.level.${n.level}`)}</Pill></span>
                    <span className={`min-w-0 flex-1 truncate ${unreadRow ? 'font-semibold text-ink' : 'text-ink-2'}`}>
                      {n.title}
                      {/* After recovery: append the duration on the same row, not a separate one */}
                      {n.endsAt && <span className="ml-2 font-normal text-ink-3">{t('notif.lasted', { d: spanText(t, Date.parse(n.endsAt) - Date.parse(n.startsAt)) })}</span>}
                    </span>
                    <span className="hidden w-24 shrink-0 sm:inline"><ScopeCell n={n} /></span>
                    <span className="w-[19ch] shrink-0 text-right font-mono tabular-nums text-ink-3">{fmtDateTimeSec(n.at)}</span>
                    <CaretDown size={12} className={`shrink-0 text-ink-3 transition-transform ${expanded ? 'rotate-180' : ''}`} aria-hidden="true" />
                  </button>
                  {expanded && (
                    // Indent is made by the same columns as the row above (dot + pill column), no hardcoded pixels: the content then aligns with the title's left edge
                    <Expand>
                    <div className="flex items-start gap-3 bg-canvas px-4 py-3 text-xs text-ink-2">
                      <span className="size-1.5 shrink-0" aria-hidden="true" />
                      <span className="w-14 shrink-0" aria-hidden="true" />
                      <span className="flex min-w-0 flex-1 flex-col gap-2.5">
                        {/* First line: the alert's start and end are the conclusion (black bold); the go-to link sits on the right of the same line. While firing only the start; after recovery start, recovery and duration */}
                        <span className="flex flex-wrap items-center gap-x-4 gap-y-1">
                          <span className="min-w-0 font-mono font-semibold tabular-nums text-ink">
                            {n.endsAt
                              ? t('notif.timeline', { from: fmtDateTimeSec(n.startsAt), to: fmtDateTimeSec(n.endsAt), d: spanText(t, Date.parse(n.endsAt) - Date.parse(n.startsAt)) })
                              : t('notif.timelineOpen', { from: fmtDateTimeSec(n.startsAt), d: spanText(t, Date.now() - Date.parse(n.startsAt)) })}
                          </span>
                          <span className="ml-auto flex items-center gap-4">
                            {n.nodeId && <GoLink to={`/nodes/${encodeURIComponent(n.nodeId)}`}>{t('overview.noticeSeeNode')}</GoLink>}
                            {n.scope === 'chain' && <GoLink to="/chain">{t('overview.noticeSeeChain')}</GoLink>}
                          </span>
                        </span>
                        {/* Trigger condition (the rule's threshold and duration) and explanation: one size smaller, as background; placed under the conclusion */}
                        {(condOf(n.alertname) || n.detail) && (
                          // Labels use a blue pill (same as "Diagnosis"), content follows
                          <span className="flex flex-col gap-1.5 text-2xs">
                            {condOf(n.alertname) && <span className="flex items-center gap-2"><Pill tone="info" className="shrink-0">{t('notif.condLabel')}</Pill>{condOf(n.alertname)}</span>}
                            {n.detail && <span className="flex items-center gap-2"><Pill tone="info" className="shrink-0">{t('notif.causeLabel')}</Pill>{n.detail}</span>}
                          </span>
                        )}
                      </span>
                    </div>
                    </Expand>
                  )}
                </li>
              );
            })}
          </ul>
          </>
        )}
      </section>
      {/* Alert settings get a single link at the card's bottom right (same as the connection card), not one per notification, so it doesn't look like encouraging frequent threshold changes; hidden for those without permission */}
      {canEditRules && <div className="flex justify-end"><GoLink to="/alerts">{t('notif.ruleLink')}</GoLink></div>}
      {rows.length > shown && (
        <button type="button" onClick={() => setShown((s) => s + PAGE)} className="self-center rounded px-2 py-1 text-xs text-ink-3 transition-colors hover:bg-elevated hover:text-ink">
          {t('notif.more', { n: Math.min(PAGE, rows.length - shown), left: rows.length - shown })}
        </button>
      )}
    </div>
  );
}
