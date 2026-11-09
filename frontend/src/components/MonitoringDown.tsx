import { Warning } from '@phosphor-icons/react';
import { useTranslation } from 'react-i18next';
import { toneVars } from '@/lib/status';

/** Notice that the monitoring source is down: alerts are still listed, but no new firings or resolutions arrive, so updates are paused. The bell, the alert-notification page and node detail all use the same sentence. */
export function MonitoringDownNote() {
  const { t } = useTranslation();
  const tone = toneVars('syncing');
  return (
    <div role="status" className="flex items-center gap-3 rounded-lg border border-hair px-4 py-2.5 text-xs" style={{ background: tone.background, color: tone.color }}>
      <Warning size={14} weight="bold" aria-hidden />
      <span>{t('monitoring.downStale')}</span>
    </div>
  );
}
