import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation } from '@tanstack/react-query';
import { api, HttpError } from '@/api/client';
import { errMsg } from '@/lib/errors';
import type { NodeDetail, NodeLogLevelRequest } from '@/api/schemas';
import { Button } from '@/components/Button';
import { HelpTip } from '@/components/HelpTip';
import { Listbox } from '@/components/Listbox';
import { ExtLink } from '@/components/TextLinks';
import { BESU_DOCS } from '@/lib/besuDocs';
import { apiState } from '@/lib/capabilities';

const LEVELS: NodeLogLevelRequest['level'][] = ['ERROR', 'WARN', 'INFO', 'DEBUG', 'TRACE'];

/**
 * Node log level: the platform calls admin_changeLogLevel on the node directly. Takes effect immediately but doesn't persist -
 * a Besu restart reverts to the logging in config.toml, so no "current level" is shown here (the platform can't read it, and guessing would mislead).
 * Placed above the logs tab of node health (like the chart's range toolbar, aligned right): switch to DEBUG, watch the logs, switch back.
 * ADMIN is an optional namespace: without it, pressing would just get a 502, so the control is disabled with the reason shown.
 */
export function LogLevelControl({ node }: { node: Pick<NodeDetail, 'id' | 'capabilities' | 'quarantinedAt'> }) {
  const { t } = useTranslation();
  const [level, setLevel] = useState<NodeLogLevelRequest['level']>('INFO');
  const apply = useMutation({ mutationFn: (l: NodeLogLevelRequest['level']) => api.setNodeLogLevel(node.id, { level: l }) });
  const c = node.capabilities;
  const noRpc = !c?.agent || !!c.rpcUnauthorized || c.rpcReachable === false || !!node.quarantinedAt;
  const noAdmin = apiState(c, 'ADMIN') === 'off';
  return (
    <div className="flex flex-wrap items-center justify-end gap-x-3 gap-y-2">
      {/* Result and disabled reason go to the left of the control, not on a separate row */}
      <span role="status" className="text-xs">
        {noRpc ? <span className="text-ink-3">{t('nodeSet.logNoRpc')}</span>
          : noAdmin ? <span className="text-ink-3">{t('nodeSet.logNoAdmin')}</span>
          : apply.isSuccess ? <span className="text-ink-3">{t('detail.logLevelApplied', { level: apply.variables })}</span>
          : apply.isError ? <span style={{ color: 'var(--bsos-status-error-fg)' }}>{apply.error instanceof HttpError ? errMsg(apply.error) : t('detail.logLevelError')}</span> : null}
      </span>
      <span className="flex items-center gap-2">
        <span className="inline-flex items-center gap-1 text-xs text-ink-3">
          {t('detail.logLevel')}
          {/* First say what changes (only the running Besu), then describe each level from least to most verbose (same order as the dropdown), and finally warn that DEBUG / TRACE are high volume and should be reverted after investigating */}
          <HelpTip title={t('detail.logLevel')}>
            <p className="mb-2">{t('nodeSet.logHint')}</p>
            <dl className="flex flex-col gap-1.5">
              {LEVELS.map((l) => <div key={l}><dt className="font-medium text-ink">{l}</dt><dd>{t(`nodeSet.logLevelHelp.${l}`)}</dd></div>)}
              <p className="border-t border-hair pt-1.5 text-ink-3">{t('nodeSet.logLevelHelp.note')}</p>
            </dl>
            <span className="-ml-1.5 mt-1 flex"><ExtLink inTip href={BESU_DOCS.logging}>{t('besuDoc.logs')}</ExtLink></span>
          </HelpTip>
        </span>
        <Listbox width="w-28" aria-label={t('detail.logLevel')} value={level} onChange={(l) => { setLevel(l); apply.reset(); }} options={LEVELS.map((l) => ({ value: l, label: l }))} />
        <Button onClick={() => apply.mutate(level)} disabled={noRpc || noAdmin || apply.isPending}>{t('detail.logLevelApply')}</Button>
      </span>
    </div>
  );
}
