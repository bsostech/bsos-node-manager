import { useTranslation } from 'react-i18next';
import type { useFleetStatus } from '@/hooks/useFleetStatus';
import { fmtDur } from '@/lib/format';
import { Tooltip } from '@/components/Tooltip';
import { Pill } from '@/components/Pill';

/**
 * Nodes whose time series can't be fetched, one line per cause: a red capsule gives the cause, followed by the nodes,
 * how long it has lasted, and that this period has no data. Sits above the trend chart and is hidden entirely when all is well.
 */
export function FleetStatusBar({ groups }: { groups: ReturnType<typeof useFleetStatus>['groups'] }) {
  const { t } = useTranslation();
  if (groups.length === 0) return null;

  return (
    <div className="flex flex-wrap items-center gap-x-5 gap-y-1">
      {groups.map((g) => {
        const names = g.nodes.map((n) => n.id);
        const shown = names.length > 3 ? t('overview.srcEtAl', { name: names[0], n: names.length }) : names.join('、');
        return (
          // When srcEtAl folds the list, the full list exists only in the tooltip - hence tabIndex so keyboard users can reach it
          <Tooltip key={g.kind} label={names.join('、')}>
            <span tabIndex={0} className="inline-flex items-center gap-1.5 text-xs focus:outline-none focus-visible:ring-1 focus-visible:ring-accent">
              <Pill tone="err">{t(`overview.src_${g.kind}`)}</Pill>
              <span className="text-ink-2">{shown}</span>
              <span className="text-ink-3">{t('overview.srcSince', { dur: fmtDur(g.nodes[0].since) })}</span>
            </span>
          </Tooltip>
        );
      })}
    </div>
  );
}
