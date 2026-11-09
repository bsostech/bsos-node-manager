import type { CSSProperties } from 'react';
import { useTranslation } from 'react-i18next';
import { Warning, ArrowClockwise, CheckCircle, Tray } from '@phosphor-icons/react';
import { Button } from '@/components/Button';

/** Loading uses a skeleton (not a spinner). */
export function Skeleton({ className = '', style }: { className?: string; style?: CSSProperties }) {
  return <div className={`animate-pulse rounded-md bg-elevated ${className}`} style={style} />;
}

export function SkeletonRows({ rows = 4 }: { rows?: number }) {
  return (
    <div className="flex flex-col gap-px">
      {Array.from({ length: rows }).map((_, i) => (
        <Skeleton key={i} className="h-12 w-full" />
      ))}
    </div>
  );
}

export function EmptyState({ message, action }: { message: string; action?: React.ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-3 py-16 text-ink-3">
      <Tray size={24} />
      <p className="text-xs">{message}</p>
      {action}
    </div>
  );
}

/**
 * Load failure. Full-page uses the default (with its own frame); inside a card use inCard (no extra frame, a bit shorter, to avoid a frame within the card).
 * Both have the same type size and button (message text-xs, retry is a small button); only the frame and vertical spacing differ.
 * The text is always "Failed to load" site-wide: unreadable is unreadable, and the reason (permissions, node offline...) is explained elsewhere on the page.
 */
export function ErrorState({ onRetry, inCard }: { onRetry?: () => void; inCard?: boolean }) {
  const { t } = useTranslation();
  return (
    <div className={`flex flex-col items-center text-center ${inCard ? 'gap-2 py-8' : 'gap-3 rounded-lg border border-hair bg-surface py-12'}`}>
      <Warning size={22} style={{ color: 'var(--bsos-status-error-fg)' }} />
      <p className="max-w-sm text-xs text-ink-2">{t('error.title')}</p>
      {onRetry && (
        <Button onClick={onRetry} className="inline-flex items-center gap-1">
          <ArrowClockwise size={13} aria-hidden="true" /> {t('common.retry')}
        </Button>
      )}
    </div>
  );
}

/**
 * A one-line hint / warning inside a page or card (stale data, list will become empty...): light background, rounded, small text, optional icon.
 * One kind site-wide; do not hand-write rounded-md px-3 py-2 + inline style each time.
 */
export function Notice({ tone = 'syncing', action, children }: { tone?: 'syncing' | 'error' | 'healthy' | 'info'; action?: React.ReactNode; children: React.ReactNode }) {
  const bg = tone === 'info' ? 'var(--bsos-accent-soft)' : `var(--bsos-status-${tone}-bg)`;
  const fg = tone === 'info' ? 'var(--bsos-text-2)' : `var(--bsos-status-${tone}-fg)`;
  const Icon = tone === 'healthy' ? CheckCircle : Warning;
  return (
    <div role={tone === 'error' ? 'alert' : 'status'} className="flex items-start gap-2 rounded-md px-3 py-2 text-xs leading-relaxed" style={{ background: bg, color: fg }}>
      <Icon size={14} aria-hidden="true" className="mt-0.5 shrink-0" />
      <div className="min-w-0 flex-1">{children}</div>
      {action}
    </div>
  );
}
