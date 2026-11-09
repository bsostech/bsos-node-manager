import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { CaretRight, Copy, DownloadSimple } from '@phosphor-icons/react';
import { api } from '@/api/client';
import type { NodeDetail } from '@/api/schemas';
import { CopyCmd } from '@/components/CopyButton';
import { Listbox } from '@/components/Listbox';
import { LogSetup } from '@/components/LogSetup';
import { ExtLink } from '@/components/TextLinks';
import { BESU_DOCS } from '@/lib/besuDocs';
import { copyToClipboard } from '@/lib/clipboard';
import { downloadRunbook } from '@/lib/runbook';
import { allowlistState, observeLogsState, optionalOff, requiredMissing } from '@/lib/capabilities';
import type { Tone } from '@/lib/status';
import { usePlatformAddress } from '@/hooks/usePlatformAddress';
import { Expand } from '@/components/Expand';

// Example path; the node operator can put it anywhere as long as the Besu config points to it.
export const PUBKEY_PATH = '/var/lib/besu/platform-rpc.pub';
export const ALLOWLIST_PATH = '/var/lib/besu/accounts-allowlist.toml';

export type Way = 'toml' | 'cli' | 'env';
type Key = 'rpc' | 'metrics' | 'auth' | 'perm' | 'logs';
export const PERM_LINES = ['permissions-accounts-config-file-enabled=true', `permissions-accounts-config-file="${ALLOWLIST_PATH}"`];
export const rpcLines = (rpcPort: number) => ['rpc-http-enabled=true', `rpc-http-port=${rpcPort}`, 'rpc-http-api=["ETH","NET","WEB3","ADMIN","PERM","IBFT","QBFT","TXPOOL"]'];
export const metricsLines = (metricsPort: number) => ['metrics-enabled=true', `metrics-port=${metricsPort}`];
export const AUTH_LINES = ['rpc-http-authentication-enabled=true', `rpc-http-authentication-jwt-public-key-file="${PUBKEY_PATH}"`, 'rpc-http-authentication-jwt-algorithm="RS256"'];

/** Convert a config.toml line to a CLI flag or environment variable; array brackets and string quotes are dropped (rpc-http-api=["ETH","NET"] -> ETH,NET). */
export function tomlTo(line: string, way: Way): string {
  if (way === 'toml') return line;
  const i = line.indexOf('=');
  const k = line.slice(0, i);
  const v = line.slice(i + 1).replace(/^\[|\]$/g, '').replace(/"/g, '');
  return way === 'cli' ? `--${k}=${v}` : `BESU_${k.toUpperCase().replace(/-/g, '_')}=${v}`;
}
export const snippetOf = (lines: string[], way: Way) => lines.map((l) => tomlTo(l, way)).join('\n');

/** Format switch (config.toml / CLI flags / environment variables); shared with the setup wizard.
 *  A dropdown rather than a segmented control: the three are alternative formats, not steps to do all of, so the label says "pick one" */
export function WaySeg({ way, onChange }: { way: Way; onChange: (w: Way) => void }) {
  const { t } = useTranslation();
  return (
    <div className="flex items-center gap-2 text-xs text-ink-3">
      <span>{t('besuReq.wayPick')}</span>
      <Listbox
        aria-label={t('besuReq.way')}
        value={way}
        onChange={onChange}
        width="w-36"
        options={[
          { value: 'toml', label: 'config.toml' },
          { value: 'cli', label: t('besuReq.wayCli') },
          { value: 'env', label: t('besuReq.wayEnv') },
        ]}
      />
    </div>
  );
}

/** For the chosen format, two sentences on where to add it and what to do if a setting of the same name exists (placed below WaySeg) */
export function WayHint({ way, existing }: { way: Way; existing?: string }) {
  const { t } = useTranslation();
  // existing: when the command contains an "<existing APIs>" placeholder, the set to add to rpc-http-api (PERM, ADMIN...), plus one sentence on how to fill it
  return <p className="text-2xs text-ink-3">{t(`besuReq.wayHint.${way}`)}{existing && ` ${t('besuReq.wayHintExisting', { api: existing })}`}</p>;
}

/** Platform public key: download button, fingerprint and the curl to place the file; this "RPC auth" item is shared with the setup wizard */
export function PubkeyFetch({ inbound }: { inbound?: string }) {
  const { t } = useTranslation();
  const key = useQuery({ queryKey: ['rpcPublicKey'], queryFn: api.getPlatformRpcPublicKey, staleTime: Infinity });
  const pemUrl = `${usePlatformAddress(inbound).http}/api/v1/platform/rpc-public-key.pem`;
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <a href="/api/v1/platform/rpc-public-key.pem" download="platform-rpc.pub" className="inline-flex items-center gap-1.5 rounded-md border border-strong px-3 py-1.5 text-xs text-ink transition-colors hover:bg-elevated">
          <DownloadSimple size={15} aria-hidden="true" />
          {t('besuReq.downloadPubkey')}
        </a>
        {key.data && <span className="text-2xs text-ink-3">{key.data.algorithm} · sha256 <span className="font-mono">{key.data.sha256.slice(0, 4)}…{key.data.sha256.slice(-4)}</span></span>}
      </div>
      <CopyCmd value={`sudo curl -fsSL ${pemUrl} \\\n  -o ${PUBKEY_PATH}`} />
      <p className="text-2xs text-ink-3">{t('besuReq.pathNote')}</p>
    </>
  );
}

/**
 * Current status of each item (only on the node settings page; when registering a node it isn't connected yet, so without node it isn't shown).
 * Judged only from what the platform actually measured: the capability list and the RPC auth probe; when the agent is offline it is always "unknown".
 */
type ReqState = { tone: Tone; code: string; apis?: string[] };

export function reqStatus(node: Pick<NodeDetail, 'id' | 'capabilities' | 'rpcAuthEnabled'>): Record<Key, ReqState> {
  const c = node.capabilities;
  const st = (tone: Tone, code: string, apis?: string[]): ReqState => ({ tone, code, apis });
  const unknown = st('neutral', 'unknown');
  if (!c?.agent) return { rpc: unknown, metrics: unknown, auth: unknown, perm: unknown, logs: unknown };
  // Yellow only when a required namespace is missing; missing only optional ones (PERM is covered by the account allowlist item) still counts as enabled, with a note listing which are off
  const missing = requiredMissing(c);
  const off = optionalOff(c).filter((ns) => ns !== 'PERM');
  const perm = allowlistState(c);
  const logs = observeLogsState(node.id, c);
  return {
    // 401: RPC is listening but the platform can't get in, and namespaces can't be queried either, so it can't count as "enabled"
    rpc: c.rpcUnauthorized ? st('error', 'rejected')
      : c.rpcReachable ? (missing.length ? st('syncing', 'missing', missing) : off.length ? st('healthy', 'onWithout', off) : st('healthy', 'on'))
      : c.rpcReachable === false ? st('error', 'unreachable') : unknown,
    auth: c.rpcUnauthorized ? st('error', 'keyMismatch') : node.rpcAuthEnabled ? st('healthy', 'on') : node.rpcAuthEnabled === false ? st('neutral', 'off') : unknown,
    // metrics unreachable uses the same color as the capability list: yellow (the node is visible, but charts and alerts are missing)
    metrics: c.metricsReachable ? st('healthy', 'on') : c.metricsReachable === false ? st('syncing', 'unreachable') : unknown,
    perm: perm === 'on' ? st('healthy', 'on') : perm === 'off' ? st('neutral', 'off') : unknown,
    logs: logs === 'on' ? st('healthy', 'connected') : logs === 'lost' ? st('neutral', 'lost') : logs === 'off' ? st('neutral', 'notConnected') : unknown,
  };
}

/**
 * Besu setup guide. The install command only installs the agent and doesn't touch Besu; this is where users learn
 * "what the platform needs Besu to enable". Five items, each expandable; snippets omit host - binding 127.0.0.1 or 0.0.0.0
 * depends on the deployment, the platform only needs it reachable via local loopback.
 */
export function BesuRequirements({ rpcPort, metricsPort, node, way: wayProp, onWayChange, runbook }: {
  rpcPort: number; metricsPort: number; node?: Pick<NodeDetail, 'id' | 'capabilities' | 'rpcAuthEnabled'>;
  /** "Runbook" appears only when given: the runbook needs this node's id; docker decides whether the install command includes --runtime docker */
  runbook?: { nodeId: string; nodeName?: string; docker?: boolean; inbound?: string };
  /** Pass when the caller needs the format kept across unmounts (switching back and forth between steps while registering a node); otherwise the component remembers it itself */
  way?: Way; onWayChange?: (w: Way) => void;
}) {
  const { t } = useTranslation();
  const [ownWay, setOwnWay] = useState<Way>('toml');
  const way = wayProp ?? ownWay;
  const setWay = onWayChange ?? setOwnWay;
  const [open, setOpen] = useState<Set<Key>>(new Set());
  const [copied, setCopied] = useState(false);
  const status = node ? reqStatus(node) : null;

  // doc: the official Besu docs link at the bottom when expanded
  const items: { k: Key; level: 'required' | 'optional'; lines?: string[]; detail?: React.ReactNode; doc: string }[] = [
    {
      k: 'rpc', level: 'required', doc: BESU_DOCS.jsonRpc,
      lines: rpcLines(rpcPort),
      detail: (
        <dl className="grid grid-cols-[7rem_1fr] gap-x-3 gap-y-1 text-xs">
          {(['ethNetWeb3', 'admin', 'ibftQbft', 'txpool', 'perm'] as const).map((ns) => (
            <div key={ns} className="contents">
              <dt className="font-mono text-ink-2">{t(`besuReq.ns.${ns}`)}</dt>
              <dd className="text-ink-3">{t(`besuReq.nsUse.${ns}`)}</dd>
            </div>
          ))}
        </dl>
      ),
    },
    { k: 'metrics', level: 'required', lines: metricsLines(metricsPort), doc: BESU_DOCS.metrics },
    { k: 'logs', level: 'optional', detail: <LogSetup inbound={runbook?.inbound} />, doc: BESU_DOCS.logging },
  ];
  const snippet = (lines: string[]) => snippetOf(lines, way);
  const full = fullConfig(rpcPort, metricsPort, way, t);
  const toggle = (k: Key) => setOpen((s) => { const n = new Set(s); if (!n.delete(k)) n.add(k); return n; });

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <WaySeg way={way} onChange={setWay} />
        <div className="flex flex-wrap items-center gap-2">
          <button
            type="button"
            onClick={async () => { const ok = await copyToClipboard(full); setCopied(ok); if (ok) setTimeout(() => setCopied(false), 1500); }}
            className="inline-flex items-center gap-1.5 rounded-md border border-strong px-3 py-1.5 text-xs text-ink transition-colors hover:bg-elevated"
          >
            <Copy size={15} aria-hidden="true" />
            {copied ? t('common.copied') : t('besuReq.copyAll')}
          </button>
          {runbook && <RunbookButton rpcPort={rpcPort} metricsPort={metricsPort} {...runbook} />}
        </div>
      </div>
      <WayHint way={way} />
      <div className="overflow-hidden rounded-md border border-hair">
        {items.map((it) => {
          const on = open.has(it.k);
          const st = status?.[it.k];
          return (
            <div key={it.k} className="border-b border-hair last:border-0">
              <button
                type="button"
                onClick={() => toggle(it.k)}
                aria-expanded={on}
                className="grid w-full grid-cols-[1rem_7rem_3rem_1fr_auto] items-center gap-3 px-3 py-2.5 text-left transition-colors hover:bg-elevated"
              >
                <CaretRight size={12} aria-hidden="true" className={`text-ink-3 transition-transform ${on ? 'rotate-90' : ''}`} />
                <span className="text-xs text-ink">{t(`besuReq.item.${it.k}`)}</span>
                <span className={`text-xs ${it.level === 'optional' ? 'text-ink-3' : 'text-ink-2'}`}>{t(`besuReq.level.${it.level}`)}</span>
                <span className="truncate text-xs text-ink-3">{t(`besuReq.use.${it.k}`)}</span>
                <span className="text-xs">{st && <StateLabel tone={st.tone} label={t(`besuReq.state.${st.code}`, { apis: st.apis?.join(' ') })} />}</span>
              </button>
              {on && (
                <Expand>
                <div className="flex flex-col gap-2 border-t border-hair bg-canvas px-3 py-3 pl-10">
                  {it.detail}
                  {it.lines && <CopyCmd value={snippet(it.lines)} />}
                  <div className="-ml-1.5"><ExtLink href={it.doc}>{t(`besuDoc.${it.k}`)}</ExtLink></div>
                </div>
                </Expand>
              )}
            </div>
          );
        })}
      </div>
      {/* Official docs for the three formats go at the bottom of the list; -ml-1.5 cancels the link padding.
          RPC auth and transaction address authorization aren't pointed to here: they're visible in the sidebar, and each feature page has its own enablement guide */}
      <div className="-ml-1.5"><ExtLink href={BESU_DOCS.configure}>{t('besuDoc.configure')}</ExtLink></div>
    </div>
  );
}

/**
 * Full config (RPC, metrics, each with a one-line comment heading); shared by "Copy full config" and the runbook. Works as pasted:
 * RPC auth and transaction address authorization are left out (they need the public key / allowlist file in place first; without it
 * Besu won't start or rejects all transactions) - enable them via the guide on their own feature pages.
 */
export function fullConfig(rpcPort: number, metricsPort: number, way: Way, t: (k: string) => string) {
  const parts: [Key, string[]][] = [['rpc', rpcLines(rpcPort)], ['metrics', metricsLines(metricsPort)]];
  return parts.map(([k, lines]) => `# ${t(`besuReq.item.${k}`)}\n${snippetOf(lines, way)}`).join('\n\n');
}

/** Download this node's onboarding runbook (Chinese or English follows the UI language, see lib/runbook.ts) */
export function RunbookButton({ rpcPort, metricsPort, nodeId, nodeName, docker, inbound }: {
  rpcPort: number; metricsPort: number; nodeId: string; nodeName?: string; docker?: boolean; inbound?: string;
}) {
  const { t } = useTranslation();
  const addr = usePlatformAddress(inbound);
  const download = () => void downloadRunbook({
    nodeId, nodeName, platformUrl: addr.http, inbound: addr.mtls, docker: !!docker, rpcPort, metricsPort,
    configs: { toml: fullConfig(rpcPort, metricsPort, 'toml', t), cli: fullConfig(rpcPort, metricsPort, 'cli', t), env: fullConfig(rpcPort, metricsPort, 'env', t) },
  });
  return (
    <button type="button" onClick={download} className="inline-flex items-center gap-1.5 rounded-md border border-strong px-3 py-1.5 text-xs text-ink transition-colors hover:bg-elevated">
      <DownloadSimple size={15} aria-hidden="true" />
      {t('besuReq.runbook')}
    </button>
  );
}

export function StateLabel({ tone, label }: { tone: Tone; label: string }) {
  const err = tone === 'error';
  return (
    <span className={`inline-flex items-center gap-1.5 whitespace-nowrap ${err ? '' : 'text-ink-2'}`} style={err ? { color: 'var(--bsos-status-error-fg)' } : undefined}>
      <span className="size-1.5 shrink-0 rounded-full" style={{ background: `var(--bsos-status-${tone}-fg)` }} />
      {label}
    </span>
  );
}
