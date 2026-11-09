import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { CopyCmd } from '@/components/CopyButton';
import { Listbox } from '@/components/Listbox';
import { usePlatformAddress } from '@/hooks/usePlatformAddress';

// Example path; it can go anywhere as long as LOG4J_CONFIGURATION_FILE points to it.
const CFG_PATH = '/etc/besu/log4j2.xml';
// Default sink address per run mode; the agent listens on 127.0.0.1:5170 by default, bridge networking uses docker0's IP instead. The address is editable.
const DEFAULT_SINK = { host: '127.0.0.1:5170', bridge: '172.17.0.1:5170' } as const;
// Format check: IPv4 (each octet 0-255) plus a port 1-65535, flagged immediately when wrong. Whether it is loopback or a
// local internal bridge interface can only be checked by the agent on the node, which sees the interfaces; it enforces that at startup.
const sinkOk = (v: string) => {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3}):(\d{1,5})$/.exec(v);
  if (!m) return false;
  const port = Number(m[5]);
  return m.slice(1, 5).every((o) => Number(o) <= 255) && port >= 1 && port <= 65535;
};

/**
 * Log setup steps: Besu uses the platform-generated log4j2 config to send logs to the agent on the same host.
 * The address depends on how Besu runs: installed directly on the host or a container on host networking sends to 127.0.0.1;
 * a container on bridge networking sends to docker0's IP, and the agent must use the same address (--log-sink).
 * The download link carries the address, so users don't have to edit the XML.
 */
export function LogSetup({ inbound }: { inbound?: string }) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<'host' | 'bridge'>('host');
  const [addr, setAddr] = useState<string>(DEFAULT_SINK.host);
  const sink = addr.trim();
  const valid = sinkOk(sink);
  const isDefault = sink === DEFAULT_SINK.host;
  const url = `${usePlatformAddress(inbound).http}/api/v1/platform/besu-log4j2.xml?sink=${encodeURIComponent(sink)}`;
  const fetchCmd = `sudo mkdir -p /etc/besu && sudo curl -fsSL "${url}" -o ${CFG_PATH}`;
  const systemdLine = `Environment=LOG4J_CONFIGURATION_FILE=${CFG_PATH}`;
  const dockerFlags = `-e LOG4J_CONFIGURATION_FILE=${CFG_PATH} -v ${CFG_PATH}:${CFG_PATH}:ro`;
  const input = 'w-44 rounded-md border border-hair bg-canvas px-2.5 py-1.5 font-mono text-xs text-ink focus:border-accent focus:outline-none';
  return (
    <div className="flex flex-col gap-3 text-xs text-ink-2">
      <p className="text-ink-3">{t('logs.setupIntro')}</p>
      <ol className="flex flex-col gap-3">
        <li className="flex flex-col gap-1.5">
          <span>{t('logs.step1')}</span>
          <div className="flex flex-wrap items-center gap-3">
            <Listbox width="w-auto"
              aria-label={t('logs.step1')}
              value={mode}
              options={[
                { value: 'host' as const, label: t('logs.modeHost') },
                { value: 'bridge' as const, label: t('logs.modeBridge') },
              ]}
              onChange={(m) => { setMode(m); setAddr(DEFAULT_SINK[m]); }}
            />
            <label className="flex items-center gap-2 text-ink-3">
              {t('logs.sinkAddr')}
              <input value={addr} onChange={(e) => setAddr(e.target.value)} aria-invalid={!valid} className={input} />
            </label>
          </div>
          {mode === 'bridge' && <span className="text-2xs text-ink-3">{t('logs.bridgeHint')}</span>}
        </li>
        <li className="flex flex-col gap-1.5">
          <span>{t('logs.step2')}</span>
          {valid ? <CopyCmd value={fetchCmd} /> : <span style={{ color: 'var(--bsos-status-error-fg)' }}>{t('logs.ipInvalid')}</span>}
        </li>
        <li className="flex flex-col gap-1.5">
          <span>{t('logs.step3')}</span>
          <span className="text-2xs text-ink-3">systemd</span>
          <CopyCmd value={systemdLine} />
          <span className="text-2xs text-ink-3">{t('logs.container')}</span>
          <CopyCmd value={dockerFlags} />
        </li>
        <li>{t('logs.step4')}</li>
        {!isDefault && (
          <li className="flex flex-col gap-1.5">
            <span>{t('logs.step5Bridge')}</span>
            <CopyCmd value={`--log-sink ${sink}`} />
          </li>
        )}
      </ol>
    </div>
  );
}
