import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ArrowClockwise } from '@phosphor-icons/react';
import { Listbox } from '@/components/Listbox';
import { Pill } from '@/components/Pill';
import { useNow } from '@/hooks/useNow';
import { COLLECT_SEC, REFRESH_STEPS, useRefreshSec } from '@/lib/refreshInterval';

/**
 * Refresh area on the right of the page header: "Updated Ns ago" + refresh-interval dropdown + manual refresh button; when the data has gone stale for too long, "Updated Ns ago" becomes "Ns behind".
 * "Updated Ns ago" is when the view last got a response, so it ranges from 0 to the chosen interval; the data itself is a bit older still (each backend collection round takes a few seconds to write).
 * Age of the data itself = the backend's dataAgeSec (collection to response) + seconds elapsed since the response arrived; each part is measured on one clock, so clock skew does not matter.
 * Normally it is at most about "interval + one round + write time"; beyond "interval + two rounds" = at least one full round with no new data (backend stuck or unreachable).
 * Every polling page uses this set, and the interval is one site-wide setting (stored in the browser), the same whichever page changes it.
 * Pressing "Refresh" spins the arrow once; on the first new data after that, "Updated Ns ago" darkens briefly then fades back -- so the press visibly did something even if the number did not change.
 */
export function RefreshControls({ updatedAt, ageSec, onRefresh }: { updatedAt: number | null | undefined; ageSec: number | null | undefined; onRefresh: () => void }) {
  const { t } = useTranslation();
  const now = useNow();
  const [sec, setSec] = useRefreshSec();
  const [turns, setTurns] = useState(0);
  const clickedAt = useRef<number | null>(null);
  const [freshAt, setFreshAt] = useState<number | null>(null);
  useEffect(() => {
    if (clickedAt.current != null && updatedAt && updatedAt >= clickedAt.current) {
      setFreshAt(updatedAt);
      clickedAt.current = null;
    }
  }, [updatedAt]);
  const age = updatedAt && ageSec != null ? Math.round(ageSec + (now - updatedAt) / 1000) : null;
  const stale = age != null && age > sec + 2 * COLLECT_SEC;
  return (
    <div className="ml-auto flex items-center gap-3">
      {stale ? <Pill tone="warn">{t('refresh.stale', { s: age })}</Pill> : updatedAt ? <span key={freshAt ?? 'idle'} className={`text-xs text-ink-3 ${freshAt ? 'ink-flash' : ''}`}>{t('refresh.updatedAgo', { s: Math.max(0, Math.round((now - updatedAt) / 1000)) })}</span> : null}
      <Listbox
        width="w-auto"
        aria-label={t('refresh.rate')}
        value={String(sec)}
        options={REFRESH_STEPS.map((v) => ({ value: String(v), label: v >= 60 ? t('refresh.everyMin', { m: v / 60 }) : t('refresh.every', { s: v }) }))}
        onChange={(v) => setSec(Number(v) as typeof sec)}
      />
      <button
        type="button"
        onClick={() => { clickedAt.current = Date.now(); setTurns((n) => n + 1); onRefresh(); }}
        className="inline-flex items-center gap-1.5 rounded-md border border-strong bg-surface px-3 py-1.5 text-xs text-ink transition-colors hover:bg-elevated active:bg-hair focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent"
      >
        <ArrowClockwise key={turns} size={14} aria-hidden className={turns ? 'turn-once' : undefined} />{t('refresh.now')}
      </button>
    </div>
  );
}
