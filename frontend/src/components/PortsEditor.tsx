import { useState } from 'react';
import { useAuth } from '@/auth/AuthContext';
import { useTranslation } from 'react-i18next';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api, HttpError } from '@/api/client';
import { errMsg } from '@/lib/errors';
import type { NodeDetail } from '@/api/schemas';
import { Card } from '@/components/Card';
import { inputCls } from '@/components/Field';
import { EditPencil } from '@/components/NameEditor';
import { portValid } from '@/components/PortField';

const RPC_DEFAULT = 8545;
const METRICS_DEFAULT = 9545;
type Which = 'rpc' | 'metrics';

/**
 * The node's local RPC / metrics ports. The agent does not know which port Besu listens on; the platform sends X-Target: 127.0.0.1:<port> with every relay,
 * so if Besu's rpc-http-port / metrics-port change they must be changed here too, or node status cannot be read and chain metrics cannot be scraped.
 * Edited like the note: the pencil after the value -> inline input, Enter or blur saves, Esc cancels; empty = default port.
 */
export function PortsEditor({ node }: { node: NodeDetail }) {
  const { t } = useTranslation();
  // Viewers only see the port numbers and cannot edit
  const canWrite = useAuth().can('nodes:onboard');
  const qc = useQueryClient();
  const [editing, setEditing] = useState<Which | null>(null);
  const [draft, setDraft] = useState('');
  const current: Record<Which, number> = { rpc: node.rpcPort ?? RPC_DEFAULT, metrics: node.metricsPort ?? METRICS_DEFAULT };
  const def: Record<Which, number> = { rpc: RPC_DEFAULT, metrics: METRICS_DEFAULT };
  const save = useMutation({
    mutationFn: ({ which, port }: { which: Which; port: number }) =>
      api.setNodePorts(node.id, { rpcPort: which === 'rpc' ? port : current.rpc, metricsPort: which === 'metrics' ? port : current.metrics }),
    onSuccess: () => { setEditing(null); void qc.invalidateQueries({ queryKey: ['node', node.id] }); },
  });
  const valid = portValid(draft);
  const begin = (w: Which) => { save.reset(); setDraft(current[w] === def[w] ? '' : String(current[w])); setEditing(w); };
  const commit = () => {
    if (!editing || !valid || save.isPending) return;
    const port = draft === '' ? def[editing] : +draft;
    if (port === current[editing]) { setEditing(null); return; }
    save.mutate({ which: editing, port });
  };

  // No layout shift while editing: the input is shorter and offset with negative margins (about one line of text tall); the normal "number + pencil" takes the same width (w-24), so the port to the right is not pushed
  const item = (w: Which, label: string) => (
    <span className="inline-flex items-center gap-1.5 text-xs text-ink-3">
      {label}
      {editing === w ? (
        <input
          autoFocus
          value={draft}
          onChange={(e) => { setDraft(e.target.value.trim()); save.reset(); }}
          onFocus={(e) => e.currentTarget.select()}
          onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); commit(); } if (e.key === 'Escape') setEditing(null); }}
          onBlur={commit}
          placeholder={String(def[w])}
          inputMode="numeric"
          aria-label={label}
          aria-invalid={!valid}
          className={`${inputCls.replace('py-1.5', 'py-1')} -my-[0.3125rem] w-24 font-mono ${valid ? '' : 'border-[var(--bsos-status-error-fg)]'}`}
        />
      ) : (
        <span className="inline-flex w-24 items-center gap-1.5">
          <span className="font-mono text-ink">{current[w]}</span>
          {canWrite && <EditPencil label={t('common.edit')} onClick={() => begin(w)} />}
        </span>
      )}
    </span>
  );

  return (
    <Card title={t('settings.portsSection')}>
      <p className="text-xs text-ink-3">{t('nodeSet.portsHint')}</p>
      <div className="flex flex-wrap items-center gap-x-6 gap-y-2">
        {item('rpc', t('join.formRpcPort'))}
        {item('metrics', t('join.formMetricsPort'))}
      </div>
      {(!valid || save.isError) && (
        <span role="alert" className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>
          {!valid ? t('settings.portsInvalid') : save.error instanceof HttpError ? errMsg(save.error) : t('settings.portsError')}
        </span>
      )}
    </Card>
  );
}
