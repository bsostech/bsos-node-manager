import * as Popover from '@radix-ui/react-popover';
import { Siren } from '@phosphor-icons/react';
import { useTranslation } from 'react-i18next';
import { GoLink } from '@/components/TextLinks';
import { useNotifications } from '@/lib/notifications';
import { fmtDateTimeShort } from '@/lib/format';
import { useState } from 'react';
import { Link } from 'react-router-dom';
import { Pill } from '@/components/Pill';
import { toneVars } from '@/lib/status';

/**
 * Top alert bell (not a notification bell): its color depends only on unresolved alerts in "Alert notifications" -
 * red if any is critical, otherwise orange if any is a warning, back to the gray outline once all have resolved.
 * It does not judge page state on its own: if the bell is red, alert notifications must contain an unresolved critical alert.
 * The red dot is separate: it lights only while there are unread alert notifications. The dropdown lists only the latest
 * 5 unread; clicking one opens the alert notifications page with it expanded (only expanding counts as read).
 * Alerts are events (sent to email / Telegram); this only lets people see "is anything wrong right now" from any page, no stats.
 */

export function AlertBell() {
  const { t } = useTranslation();
  // Color: the most severe unresolved alert notification (red / orange / outline); list: latest 5 unread; red dot: unread remain
  const { q, items, unread, monitoringDown } = useNotifications();
  // The query itself failed (network, platform): don't present stale pre-outage data as current
  const failed = q.isError;
  const firing = items.filter((n) => n.status === 'firing');
  const worst = firing.some((n) => n.level === 'err') ? 'err' : firing.length ? 'warn' : null;
  const tone = failed ? undefined
    : worst === 'err' ? 'var(--bsos-status-error-fg)' : worst === 'warn' || monitoringDown ? 'var(--bsos-status-syncing-fg)' : undefined;
  const latest = failed ? [] : unread.slice(0, 5);
  const [open, setOpen] = useState(false);
  const count = failed ? t('bell.loadError') : unread.length ? t('bell.unread', { n: unread.length }) : t('bell.none');
  // Monitoring source down: still list alerts (must not hide ones that really fired), and mark that the state is frozen at the outage
  const label = monitoringDown && !failed ? `${t('error.monitoringDown')} · ${count}` : count;
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger asChild>
        <button
          type="button"
          aria-label={label}
          title={label}
          className="relative inline-flex size-9 items-center justify-center rounded-md text-ink-2 transition-colors hover:bg-elevated hover:text-ink focus:outline-none focus-visible:ring-1 focus-visible:ring-accent data-[state=open]:bg-elevated"
          style={tone ? { color: tone } : undefined}
        >
          <Siren size={22} weight={worst ? 'fill' : 'regular'} aria-hidden="true" />
          {!failed && unread.length > 0 && <span className="absolute right-1.5 top-1.5 size-2 rounded-full ring-2 ring-surface" style={{ background: 'var(--bsos-status-error-fg)' }} aria-hidden="true" />}
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          align="end"
          side="bottom"
          sideOffset={6}
          collisionPadding={16}
          onOpenAutoFocus={(e) => e.preventDefault()}
          className="pop-in z-50 flex w-96 max-w-[calc(100vw-2rem)] flex-col gap-2 rounded-lg border border-hair bg-surface p-3 text-xs shadow-lg focus:outline-none"
        >
          <span className="text-xs font-semibold text-ink">{count}</span>
          {monitoringDown && !failed && (
            <p className="rounded px-2 py-1.5" style={{ background: toneVars('syncing').background, color: toneVars('syncing').color }}>{t('monitoring.downStale')}</p>
          )}
          {latest.length > 0 && (
            <ul className="flex flex-col gap-1.5">
              {latest.map((n) => (
                <li key={n.id}>
                  {/* Clicking one opens it expanded on the alert notifications page (expanding marks it read) */}
                  <Link to={`/notifications?open=${encodeURIComponent(n.id)}`} onClick={() => setOpen(false)}
                    className="-mx-1.5 flex items-center gap-2 rounded px-1.5 py-1 text-xs text-ink transition-colors hover:bg-elevated">
                    <span className="w-14 shrink-0"><Pill tone={n.status === 'resolved' ? 'neutral' : n.level}>{n.status === 'resolved' ? t('notif.resolved') : t(`overview.level.${n.level}`)}</Pill></span>
                    <span className="min-w-0 flex-1 truncate">{n.title}</span>
                    <span className="shrink-0 font-mono tabular-nums text-ink-3">{fmtDateTimeShort(n.at)}</span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
          {/* Marking as read happens only on the alert notifications page: the bell only answers "any unread, and what are the latest 5" */}
          <div className="border-t border-hair pt-2">
            <span onClick={() => setOpen(false)}><GoLink to="/notifications">{t('bell.all')}</GoLink></span>
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
