import { useEffect, useRef, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { CaretLeft, CaretRight, CircleNotch, DownloadSimple } from '@phosphor-icons/react';
import { AGENT_BUNDLE_URL, api, HttpError } from '@/api/client';
import type { AgentRuntime, JoinTokenCreated, JoinTokenTtl } from '@/api/schemas';
import { BesuRequirements, RunbookButton, type Way } from '@/components/BesuRequirements';
import { Button } from '@/components/Button';
import { ConfirmModal } from '@/components/ConfirmModal';
import { ErrorState } from '@/components/States';
import { CopyButton, CopyCmd } from '@/components/CopyButton';
import { inputCls } from '@/components/Field';
import { HelpTip } from '@/components/HelpTip';
import { isHostPort, isIpOrCidr } from '@/lib/net';
import { Listbox } from '@/components/Listbox';
import { PortField, portValid } from '@/components/PortField';
import { Stepper } from '@/components/Stepper';
import { errMsg } from '@/lib/errors';
import { fmtDateTimeShort, fmtHM } from '@/lib/format';
import { usePlatformAddress } from '@/hooks/usePlatformAddress';
import { installCommand, offlineInstallCommand } from '@/lib/runbook';
import { BackLink } from '@/components/TextLinks';

// Same as the backend nodeIDPattern; input is already lowercased, this only checks the format
const idOk = (v: string) => /^[a-z0-9][a-z0-9-]{0,62}$/.test(v);
// Source IP allowlist: space- or comma-separated; each entry must be an IP or CIDR (the backend validates and normalizes again)
const splitSources = (v: string) => v.split(/[\s,]+/).filter(Boolean);
const sourcesOk = (v: string) => splitSources(v).every(isIpOrCidr);
// The inbound address is put into an install command pasted into a shell, so only IP / [IPv6] / hostname plus port is allowed
const inboundOk = (v: string) => isHostPort(v.trim());
const TTLS: JoinTokenTtl[] = ['15m', '1h', '24h'];
const short = (h: string) => `${h.slice(0, 4)}…${h.slice(-4)}`;

/** One install step (number + title + content) */
function Step({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  return (
    <li className="flex gap-3">
      <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-elevated font-mono text-2xs text-ink-2">{n}</span>
      <div className="flex min-w-0 flex-1 flex-col gap-1.5">
        <span className="text-xs font-medium text-ink">{title}</span>
        {children}
      </div>
    </li>
  );
}

/** Help text under a field; on invalid format it becomes a red error (same as node id), so a greyed-out button shows why */
function FieldHint({ err, hint }: { err: string; hint: string }) {
  return (
    <span className={`text-2xs ${err ? '' : 'text-ink-3'}`} style={err ? { color: 'var(--bsos-status-error-fg)' } : undefined}>
      {err || hint}
    </span>
  );
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex items-center gap-2.5 pt-1">
      <span className="text-xs font-semibold uppercase tracking-wider text-ink-3">{children}</span>
      <span className="h-px flex-1 bg-hair" />
    </div>
  );
}

/**
 * Register a node (/node-add) in three steps: join settings -> adjust Besu settings -> install the Agent.
 * The wizard doesn't wait for the join: "Done" returns to the list with that row highlighted; once joined, the node settings capability list shows each item's status.
 * The join code is only generated at step ③: the first two steps may be abandoned at any time, and an early code would just become an orphan needing manual revocation.
 * After step ③ generates it, changing node info or the TTL generates a new code and the old one stops working: the code is bound to the node id and ports, so an old one would join as the wrong node.
 */
export function NodeAdd() {
  const { t } = useTranslation();
  const nav = useNavigate();
  const qc = useQueryClient();
  const [step, setStep] = useState(1);
  // Coming from a list row marked "The join code has expired": ?node=&name=&src= carry that node's registration data, and a new join code is generated directly
  const [params] = useSearchParams();
  const [name, setName] = useState(params.get('name') ?? '');
  const [nodeId, setNodeId] = useState(params.get('node') ?? '');
  const [sources, setSources] = useState(params.get('src') ?? '');
  const [inbound, setInbound] = useState<string | null>(null); // null = use the platform's default setting
  const [env, setEnv] = useState<AgentRuntime>('systemd');
  const [rpcPort, setRpcPort] = useState(''); // empty = default
  const [metricsPort, setMetricsPort] = useState('');
  const [portsEditing, setPortsEditing] = useState(false);
  const rpcEff = +(rpcPort || 8545);
  const metricsEff = +(metricsPort || 9545);
  const [ttl, setTtl] = useState<JoinTokenTtl>('1h');
  const [ttlAsk, setTtlAsk] = useState<JoinTokenTtl | null>(null); // TTL change pending confirmation: only after confirming is a new code generated (which voids the old one)
  const [token, setToken] = useState<{ created: JoinTokenCreated; key: string } | null>(null);
  const hadToken = useRef(false);
  const [cmdFlash, setCmdFlash] = useState(0);
  const [copied, setCopied] = useState(false);
  const [way, setWay] = useState<Way | null>(null);
  const [method, setMethod] = useState<'online' | 'offline'>('online');
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => { if (step > 1) heading.current?.focus(); }, [step]); // the button unmounts on step change, so move focus to the step heading

  // A node with this id already exists: the backend will reject the join
  const nodesQ = useQuery({ queryKey: ['nodes'], queryFn: () => api.listNodes(), select: (r) => r.nodes });
  const taken = !!nodeId && !!nodesQ.data?.some((n) => n.id === nodeId);
  const defaultMtls = usePlatformAddress().mtls;
  const inboundAddr = (inbound ?? defaultMtls).trim();
  const addr = usePlatformAddress(inboundAddr);
  const canNext1 = idOk(nodeId) && !taken && portValid(rpcPort) && portValid(metricsPort) && sourcesOk(sources) && inboundOk(inboundAddr);
  // The inbound address only goes into the install command, not the join code, so it isn't in the key: changing it needs no new code
  const keyOf = (ttlNow: JoinTokenTtl) => JSON.stringify([name.trim(), nodeId, splitSources(sources), rpcEff, metricsEff, ttlNow]);

  // Generate: the backend revokes this node id's earlier unused codes itself. A code made for a different node id (changed in step ①) isn't
  // covered by that, so revoke it first; if that fails, don't generate, so no stray usable code is left behind
  const issue = useMutation({
    mutationFn: async (ttlNow: JoinTokenTtl) => {
      if (token && token.created.nodeId !== nodeId) {
        await api.revokeJoinToken(token.created.id).catch((e) => { if (!(e instanceof HttpError && e.status === 409)) throw e; });
      }
      const created = await api.createJoinToken({
        nodeId, rpcPort: rpcEff, metricsPort: metricsEff, ttl: ttlNow,
        ...(name.trim() ? { nodeName: name.trim() } : {}),
        allowedSources: splitSources(sources),
      });
      return { created, key: keyOf(ttlNow) };
    },
    onSuccess: (tk) => {
      // Not the first code (TTL changed, regenerated): the old command is void, so flash the command box as a reminder to copy again
      if (hadToken.current) setCmdFlash((n) => n + 1);
      hadToken.current = true;
      setToken(tk); setCopied(false); void qc.invalidateQueries({ queryKey: ['joinTokens'] });
    },
    // The old code may already be revoked: on failure it must not stay around for the user to proceed with. While generating, keep showing the old code (dimmed) so the layout doesn't jump
    onError: () => setToken(null),
  });

  // Don't send a second request while generating: the backend keeps only the later of two codes, and the screen could end up showing the revoked one
  const reissue = (ttlNow: JoinTokenTtl) => { if (!issue.isPending) issue.mutate(ttlNow); };
  const goInstall = () => {
    setStep(3);
    if (!token || token.key !== keyOf(ttl)) reissue(ttl);
  };

  // Checksums and file names of install.sh and the offline bundle come from the platform (install.sh is embedded in the platform, the agent lives in AGENT_BINARY_DIR)
  const bundleQ = useQuery({ queryKey: ['agentBundle'], queryFn: api.getAgentBundle, enabled: step === 3 });
  const bundle = bundleQ.data?.bundle ?? null;
  const installCmd = token && bundleQ.data ? installCommand({ origin: addr.http, inbound: inboundAddr, token: token.created.token, docker: env === 'docker', sha: bundleQ.data.installShSha256 }) : '';
  const offlineCmd = token && bundle ? offlineInstallCommand({ file: bundle.file, sha: bundle.sha256, inbound: inboundAddr, token: token.created.token, docker: env === 'docker' }) : '';
  // Expiring today shows only the time; crossing midnight (24 hours) needs the date, or an early-morning time tomorrow looks already expired
  const expiresIso = token?.created.expiresAt;
  const expiresText = expiresIso ? t('addNode.expiresAt', { time: new Date(expiresIso).toDateString() === new Date().toDateString() ? fmtHM(Date.parse(expiresIso)) : fmtDateTimeShort(expiresIso) }) : '';
  const runbook = { nodeId, nodeName: name.trim() || undefined, docker: env === 'docker', inbound: inboundAddr };

  const steps = [t('addNode.step1'), t('addNode.step2'), t('addNode.step3')];
  const back = (
    <button type="button" onClick={() => setStep((s) => s - 1)} disabled={issue.isPending} className="inline-flex items-center gap-1 rounded-md px-3 py-1.5 text-xs text-ink-2 transition-colors hover:bg-elevated disabled:cursor-not-allowed disabled:opacity-60">
      <CaretLeft size={14} aria-hidden="true" />
      {t('common.prev')}
    </button>
  );
  const nextBtn = (onClick: () => void, disabled = false, label = t('common.next')) => (
    <Button variant="primary" className="inline-flex items-center gap-1" disabled={disabled} onClick={onClick}>
      {label}
      <CaretRight size={14} aria-hidden="true" />
    </Button>
  );

  let body: React.ReactNode;
  let foot: React.ReactNode;
  if (step === 1) {
    const idBad = !!nodeId && !idOk(nodeId);
    const idErr = idBad ? t('addNode.nodeIdBad') : taken ? t('addNode.nodeIdTaken') : '';
    body = (
      <form id="add-node-1" className="flex flex-col gap-4" onSubmit={(e) => { e.preventDefault(); if (canNext1) setStep(2); }}>
        <fieldset className="flex flex-col gap-3">
          <SectionLabel>{t('addNode.secIdentity')}</SectionLabel>
          <div className="grid gap-3 sm:grid-cols-2">
            {/* Node id first: required, it identifies this node; the note is an optional annotation, so it comes after */}
            <div className="flex flex-col gap-1 text-xs text-ink-3">
              <span className="inline-flex items-center gap-1">
                <label htmlFor="add-node-id">
                  {t('addNode.nodeId')}
                  <span className="ml-0.5 text-accent">*</span>
                </label>
                <HelpTip title={t('addNode.nodeId')}>{t('addNode.nodeIdHelp')}</HelpTip>
              </span>
              <input
                id="add-node-id"
                value={nodeId}
                onChange={(e) => setNodeId(e.target.value.toLowerCase())}
                placeholder="vn-01"
                spellCheck={false}
                autoFocus
                autoComplete="off"
                required
                aria-invalid={!!idErr}
                aria-describedby="add-node-id-hint"
                className={`${inputCls} font-mono ${idErr ? 'border-[var(--bsos-status-error-fg)]' : ''}`}
              />
              <span id="add-node-id-hint" className={`text-2xs ${idErr ? '' : 'text-ink-3'}`} style={idErr ? { color: 'var(--bsos-status-error-fg)' } : undefined}>
                {idErr || t('addNode.nodeIdFixed')}
              </span>
            </div>
            <label className="flex flex-col gap-1 text-xs text-ink-3">
              {t('addNode.name')}
              <input value={name} onChange={(e) => setName(e.target.value)} maxLength={64} className={inputCls} />
              <span className="text-2xs text-ink-3">{t('addNode.nameHint')}</span>
            </label>
          </div>
        </fieldset>
        <fieldset className="flex flex-col gap-3">
          <SectionLabel>{t('addNode.secConn')}</SectionLabel>
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="flex flex-col gap-1 text-xs text-ink-3">
              {t('addNode.inbound')}
              <input value={inbound ?? defaultMtls} onChange={(e) => setInbound(e.target.value)} spellCheck={false} autoComplete="off" aria-invalid={!inboundOk(inboundAddr)} className={`${inputCls} font-mono ${inboundOk(inboundAddr) ? '' : 'border-[var(--bsos-status-error-fg)]'}`} />
              <FieldHint err={inboundOk(inboundAddr) ? '' : t('addNode.inboundBad')} hint={t('addNode.inboundHint')} />
            </label>
            {/* Join code protection: the source IP is only checked at join time. The ⓘ sits outside the label so clicking it doesn't jump to the input */}
            <div className="flex flex-col gap-1 text-xs text-ink-3">
              <span className="inline-flex items-center gap-1">
                <label htmlFor="add-node-sources">{t('addNode.sources')}</label>
                <HelpTip title={t('addNode.sources')}>{t('addNode.sourcesHelp')}</HelpTip>
              </span>
              <input id="add-node-sources" value={sources} onChange={(e) => setSources(e.target.value)} placeholder="10.0.0.31" spellCheck={false} autoComplete="off" aria-invalid={!sourcesOk(sources)} className={`${inputCls} font-mono ${sourcesOk(sources) ? '' : 'border-[var(--bsos-status-error-fg)]'}`} />
              <FieldHint err={sourcesOk(sources) ? '' : t('addNode.sourcesBad')} hint={t('addNode.sourcesHint')} />
            </div>
          </div>
        </fieldset>
        <fieldset className="flex flex-col gap-3">
          <SectionLabel>{t('join.formEnv')}</SectionLabel>
          <div>
            <Listbox width="w-36"
              aria-label={t('join.formEnv')}
              value={env}
              onChange={setEnv}
              options={[{ value: 'systemd', label: t('join.envSystemd') }, { value: 'docker', label: t('join.envDocker') }]}
            />
          </div>
        </fieldset>
        <fieldset className="flex flex-col gap-3">
          <SectionLabel>{t('addNode.secPorts')}</SectionLabel>
          {/* The platform forwards to these two ports on the node's localhost via the agent (the agent doesn't know where Besu listens); they can also be changed in node settings after joining */}
          <div className="flex flex-wrap items-center gap-x-6 gap-y-2">
            {/* Usually the defaults: read-only until "Edit" is clicked; the wizard is still being filled in, so no separate save */}
            <PortField label={t('join.formRpcPort')} value={rpcPort} onChange={setRpcPort} def={8545} readOnly={!portsEditing} autoFocus={portsEditing} />
            <PortField label={t('join.formMetricsPort')} value={metricsPort} onChange={setMetricsPort} def={9545} readOnly={!portsEditing} />
            {!portsEditing && <Button onClick={() => setPortsEditing(true)}>{t('common.edit')}</Button>}
          </div>
        </fieldset>
      </form>
    );
    foot = (
      <>
        {/* No cancel on the first step: use the back link at top left to leave; the empty span keeps "next" right-aligned (same as the setup guide) */}
        <span />
        <Button type="submit" form="add-node-1" variant="primary" className="inline-flex items-center gap-1" disabled={!canNext1}>
          {t('common.next')}
          <CaretRight size={14} aria-hidden="true" />
        </Button>
      </>
    );
  } else if (step === 2) {
    // At this step the node hasn't joined, so no per-item status; Docker setups mostly configure Besu via environment variables
    body = <BesuRequirements rpcPort={rpcEff} metricsPort={metricsEff} way={way ?? (env === 'docker' ? 'env' : 'toml')} onWayChange={setWay} runbook={runbook} />;
    foot = <>{back}{nextBtn(goInstall)}</>;
  } else if (step === 3) {
    const busy = issue.isPending;
    body = issue.isPending && !token ? (
      <p className="flex items-center gap-2 text-xs text-ink-3"><CircleNotch size={14} className="animate-spin" aria-hidden="true" />{t('addNode.generating')}</p>
    ) : issue.isError && !issue.isPending ? (
      <div className="flex flex-col items-start gap-2">
        <p className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>{errMsg(issue.error) || t('join.createError')}</p>
        <Button onClick={() => reissue(ttl)}>{t('common.retry')}</Button>
      </div>
    ) : token ? (
      <>
        {/* Join code: changing the TTL regenerates it and the command changes with it, so it goes before the command */}
        <div className="flex flex-col gap-2 rounded-md border border-hair px-4 py-3">
          <div className="flex flex-wrap items-baseline gap-x-2">
            <span className="text-xs font-medium text-ink">{t('addNode.code')}</span>
            <span className="text-2xs text-ink-3">{t('addNode.codeInCmd')}</span>
          </div>
          <div className={`transition-opacity ${busy ? 'pointer-events-none opacity-40' : ''}`} aria-busy={busy}>
            <CopyCmd value={token.created.token} flash={cmdFlash} />
          </div>
          <div className="flex flex-wrap items-center gap-3 text-xs text-ink-3">
            {t('join.formTtl')}
            <Listbox width="w-28" aria-label={t('join.formTtl')} value={ttl}
              onChange={(v) => { if (v !== ttl && !busy) setTtlAsk(v); }}
              options={TTLS.map((v) => ({ value: v, label: t(`join.ttl${v}`) }))} />
            {busy
              ? <span className="flex items-center gap-1.5 text-ink-2"><CircleNotch size={12} className="animate-spin" aria-hidden="true" />{t('addNode.regenerating')}</span>
              : <span className="font-mono text-ink-2">{expiresText}</span>}
          </div>
          {method === 'offline' && ttl !== '24h' && (
            <span className="text-2xs" style={{ color: 'var(--bsos-status-syncing-fg)' }}>{t('addNode.offlineTtl')}</span>
          )}
        </div>

        <div className="flex flex-col gap-1.5">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="flex items-center gap-3">
              <span className="text-xs text-ink-3">{t('addNode.method')}</span>
              <Listbox width="w-36" aria-label={t('addNode.method')} value={method} onChange={(v) => { setMethod(v); setCopied(false); }}
                options={[{ value: 'online', label: t('addNode.methodOnline') }, { value: 'offline', label: t('addNode.methodOffline') }]} />
            </div>
            <RunbookButton rpcPort={rpcEff} metricsPort={metricsEff} {...runbook} />
          </div>
          <span className="text-2xs text-ink-3">{t(method === 'online' ? 'addNode.methodOnlineHint' : 'addNode.methodOfflineHint')}</span>
        </div>

        {bundleQ.isError ? (
          <ErrorState onRetry={() => void bundleQ.refetch()} />
        ) : !bundleQ.data ? (
          <p className="flex items-center gap-2 text-xs text-ink-3"><CircleNotch size={14} className="animate-spin" aria-hidden="true" />{t('common.loading')}</p>
        ) : method === 'offline' && !bundle ? (
          <p className="text-xs" style={{ color: 'var(--bsos-status-syncing-fg)' }}>{t('addNode.noBundle')}</p>
        ) : (
          <ol className="flex flex-col gap-5">
            {method === 'offline' && bundle && (
              <>
                <Step n={1} title={t('addNode.stepDownload')}>
                  <div className="flex flex-wrap items-center gap-3">
                    {/* Same origin with the session cookie; the file name comes from the backend's Content-Disposition */}
                    <a href={AGENT_BUNDLE_URL} download={bundle.file}
                      className="inline-flex items-center gap-1.5 rounded-md border border-strong px-3 py-1.5 text-xs text-ink transition-colors hover:bg-elevated">
                      <DownloadSimple size={14} aria-hidden="true" />{bundle.file}
                    </a>
                    <span className="inline-flex items-center gap-1 text-2xs text-ink-3">
                      sha256 <span className="font-mono text-ink-2">{short(bundle.sha256)}</span>
                      <CopyButton value={bundle.sha256} label={t('addNode.copySha')} />
                    </span>
                  </div>
                  <span className="text-2xs text-ink-3">
                    {t('addNode.bundleContents', { version: bundleQ.data.agentVersion ?? '', arches: bundle.arches.map((a) => (a === 'amd64' ? 'x86_64' : 'ARM64')).join(t('addNode.listSep')) })}
                  </span>
                </Step>
                <Step n={2} title={t('addNode.stepTransfer')}>
                  <span className="text-2xs text-ink-3">{t('addNode.transferHint')}</span>
                </Step>
              </>
            )}
            <Step n={method === 'offline' ? 3 : 1} title={t('addNode.runOnHost')}>
              {/* Generating a new code: the old command is void, dimmed and not copyable, then flashes once replaced */}
              <div className={`transition-opacity ${busy ? 'pointer-events-none opacity-40' : ''}`} aria-busy={busy}>
                <CopyCmd value={method === 'offline' ? offlineCmd : installCmd} onCopied={() => setCopied(true)} flash={cmdFlash} />
              </div>
              {method === 'online' && <span className="text-2xs text-ink-3">{t('addNode.verifyFirst')}</span>}
            </Step>
            <Step n={method === 'offline' ? 4 : 2} title={t('addNode.stepConfirm')}>
              <span className="text-2xs text-ink-3">{t('addNode.confirmHint')}</span>
            </Step>
          </ol>
        )}
      </>
    ) : null;
    foot = (
      <>
        {back}
        <div className="flex items-center gap-2">
          {token && !copied && !issue.isPending && <span className="text-xs text-ink-3">{t('join.copyFirst')}</span>}
          {/* Return to the list and highlight this row; join status is taken over by the list and the capability list.
              Don't block on "Copied": if copying fails the user can only select manually, which the wizard can't see, and being stuck is worse than not copying (the join code can be regenerated) */}
          <Button variant="primary" disabled={!token || issue.isPending} onClick={() => nav('/node-join', { state: { added: nodeId } })}>{t('common.done')}</Button>
        </div>
      </>
    );
  }

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-4">
      <BackLink to="/node-join">{t('join.title')}</BackLink>
      <header className="flex flex-col gap-0.5">
        <h1 className="text-lg font-semibold text-ink">{t('join.addNode')}</h1>
        <span className="text-xs text-ink-3">{t('addNode.subtitle')}</span>
      </header>
      <Stepper step={step} labels={steps} />
      <section className="min-w-0 overflow-hidden rounded-lg border border-hair bg-surface">
        <h2 ref={heading} tabIndex={-1} className="flex min-h-[54px] items-center gap-2 border-b border-hair px-4 py-2 text-sm font-semibold text-ink focus:outline-none">{steps[step - 1]}</h2>
        <div className="flex flex-col gap-4 px-5 py-4">{body}</div>
        <div className="flex flex-wrap items-center justify-between gap-2 border-t border-hair px-5 py-3">{foot}</div>
      </section>
      {ttlAsk && (
        <ConfirmModal title={t('addNode.ttlAskTitle')} body={t('addNode.ttlAskBody')}
          confirmLabel={t('addNode.ttlAskConfirm', { ttl: t(`join.ttl${ttlAsk}`) })}
          onCancel={() => setTtlAsk(null)}
          onConfirm={() => { setTtl(ttlAsk); reissue(ttlAsk); setTtlAsk(null); }} />
      )}
    </div>
  );
}
