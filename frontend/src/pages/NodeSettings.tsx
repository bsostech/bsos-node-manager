import { useState } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { IdentificationCard, Terminal, Warning, Wrench } from '@phosphor-icons/react';
import { api, HttpError } from '@/api/client';
import { errMsg } from '@/lib/errors';
import { isEnterprise } from '@/edition';
import type { NodeDetail as TNodeDetail, JoinTokenCreated, AgentRuntime } from '@/api/schemas';
import { ErrorState, Skeleton } from '@/components/States';
import { CopyButton, CopyCmd } from '@/components/CopyButton';
import { Listbox } from '@/components/Listbox';
import { HelpTip } from '@/components/HelpTip';
import { useUrlParam } from '@/hooks/useUrlParam';
import { useAuth } from '@/auth/AuthContext';
import { PortsEditor } from '@/components/PortsEditor';
import { CapabilityChecklist } from '@/components/CapabilityChecklist';
import { olderThan, requiredTone } from '@/lib/capabilities';
import { installCommand } from '@/lib/runbook';
import { BesuRequirements } from '@/components/BesuRequirements';
import { ValidatorTag } from '@/components/ValidatorTag';
import { Card } from '@/components/Card';
import { Button } from '@/components/Button';
import { inputCls } from '@/components/Field';
import { EditPencil, NameEditor } from '@/components/NameEditor';
import { Modal, ModalTitle } from '@/components/Modal';
import { fmtDateTimeShort, shortVersion } from '@/lib/format';
import { toneVars } from '@/lib/status';
import { LEAVE_WITHOUT_ASKING } from '@/hooks/useUnsavedGuard';
import { usePlatformAddress } from '@/hooks/usePlatformAddress';
import { BackLink, GoLink } from '@/components/TextLinks';
import { useSavedFlash } from '@/hooks/useSavedFlash';

type TabId = 'conn' | 'besu' | 'ops';
const TAB_IDS: TabId[] = ['conn', 'besu', 'ops'];

/**
 * Node settings (per node): header + capability list + three tabs (basic info / Besu setup guide / node ops).
 * This node's peers are managed in the node tab of "Node peering settings" (same two cards), not repeated here.
 * Node detail is only for monitoring; settings and troubleshooting live here. The platform doesn't change node startup flags such as P2P / tx pool.
 */
export function NodeSettings() {
  const { id = '' } = useParams();
  const { t } = useTranslation();
  const { can } = useAuth();
  const canOnboard = can('nodes:onboard');
  // tab goes into the URL (?tab=ops; the default conn adds no param) so reload / shared links keep the tab
  const [tab, setTab] = useUrlParam<TabId>('tab', 'conn', (v) => TAB_IDS.includes(v as TabId));

  const { data, isLoading, isError, refetch } = useQuery<TNodeDetail, HttpError>({
    queryKey: ['node', id],
    queryFn: () => api.getNode(id),
    retry: false,
    refetchInterval: 10_000, // the capability list follows node settings changes (no manual reload after changing Besu settings)
  });
  const certQ = useQuery({ queryKey: ['nodeCert', id], queryFn: () => api.getNodeCert(id), enabled: !!data, refetchInterval: 15_000 });

  if (isLoading) {
    return (
      <div className="mx-auto flex max-w-5xl flex-col gap-3">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-32" />
      </div>
    );
  }
  if (isError || !data) {
    return (
      <div className="mx-auto flex max-w-5xl flex-col gap-4">
        <BackToNodes />
        <ErrorState onRetry={() => void refetch()} />
      </div>
    );
  }

  const c = data.capabilities;
  // Tab dots use the capability list's levels: red = broken, yellow = missing a required piece; optional items not on stay unlit
  const besuTone = requiredTone(c);
  const opsBad = !!data.quarantinedAt || certQ.data?.cert === 'untrusted';
  const marker = (tone: 'error' | 'syncing') => <span role="img" className="size-1.5 rounded-full" style={{ background: `var(--bsos-status-${tone}-fg)` }} aria-label={t('nodeSet.hasIssue')} />;
  // help: the ⓘ right of the tab name (outside the tab button, so clicking ⓘ doesn't switch tabs)
  const tabs: { id: TabId; label: string; icon: typeof Wrench; mark?: React.ReactNode; help?: React.ReactNode }[] = [
    { id: 'conn', label: t('nodeSet.tabConn'), icon: IdentificationCard },
    { id: 'besu', label: t('besuReq.title'), icon: Terminal, mark: besuTone ? marker(besuTone) : null },
    {
      id: 'ops', label: t('detail.opsTitle'), icon: Wrench, mark: opsBad ? marker('error') : null,
      // What the Agent is, what it does, where it is installed: every card in this tab deals with it, so introduce it first
      help: (
        <HelpTip title={t('nodeSet.agentHelpTitle')}>
          <dl className="flex flex-col gap-1.5">
            {(['what', 'use', 'where'] as const).map((k) => (
              <div key={k}><dt className="font-medium text-ink">{t(`nodeSet.agentHelp.${k}.term`)}</dt><dd>{t(`nodeSet.agentHelp.${k}.text`)}</dd></div>
            ))}
          </dl>
        </HelpTip>
      ),
    },
  ];

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-4">
      <BackToNodes />
      <header className="flex flex-wrap items-center gap-3">
        <div className="flex flex-col gap-0.5">
          <h1 className="inline-flex items-baseline gap-2 text-lg font-semibold text-ink">
            <span className="font-mono">{data.id}</span>
            {data.name && <span className="text-sm font-normal text-ink-3">· {data.name}</span>}
            {data.consensus?.isValidator && <span className="self-center"><ValidatorTag /></span>}
          </h1>
          {/* Last report is shown in the node registration & settings list column, not repeated here */}
          {isEnterprise && <span className="text-xs text-ink-3">{data.org}</span>}
        </div>
        {/* The chain health node list and node detail both show health, so only keep a link to detail here */}
        <span className="ml-auto"><GoLink to={`/nodes/${encodeURIComponent(data.id)}`}>{t('nodeSet.toDetail')}</GoLink></span>
      </header>

      {/* Viewers can see the whole page (capability list, connection, Besu settings, Agent); editing the note or ports, rejoining and unjoining are operator-only */}
      {(
        <>
          {data.quarantinedAt && (
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-hair px-4 py-2.5 text-xs" style={toneVars('error')}>
              <Warning size={14} weight="fill" aria-hidden="true" />
              <span className="font-medium">{t('nodeSet.quarantined', { time: fmtDateTimeShort(data.quarantinedAt) })}</span>
              <span className="text-ink-2">{t('nodeSet.quarantinedHint')}</span>
              {canOnboard && <span className="ml-auto"><GoLink onClick={() => setTab('ops')}>{t('detail.rejoinTitle')}</GoLink></span>}
            </div>
          )}

          <CapabilityChecklist node={data} cert={certQ.data} onFix={canOnboard ? () => setTab('besu') : undefined} onAgentTab={canOnboard ? () => setTab('ops') : undefined} />

          <div role="tablist" className="flex gap-1 overflow-x-auto border-b border-hair">
            {/* The Besu setup guide, Agent and join are for people doing the work (running commands on the node host), so viewers only get basic info; the Agent version is visible in the capability list above */}
            {tabs.filter((tb) => canOnboard || tb.id === 'conn').map((tb) => {
              const active = tab === tb.id || (tb.id === 'conn' && tab !== 'conn' && !canOnboard);
              return (
                <span key={tb.id} className="flex items-center">
                <button
                  role="tab"
                  aria-selected={active}
                  onClick={() => setTab(tb.id)}
                  className={`-mb-px flex items-center gap-1.5 whitespace-nowrap border-b-2 px-3 py-2 text-sm transition-colors ${
                    active ? 'border-accent font-medium text-accent' : 'border-transparent text-ink-2 hover:text-ink'
                  }`}
                >
                  <tb.icon size={16} weight={active ? 'fill' : 'regular'} aria-hidden="true" />
                  {tb.label}
                  {tb.mark}
                </button>
                {tb.help && <span className="-ml-1.5 flex items-center pr-1.5">{tb.help}</span>}
                </span>
              );
            })}
          </div>

          <div className="flex flex-col gap-4">
            {(tab === 'conn' || !canOnboard) && <ConnTab node={data} />}
            {tab === 'besu' && canOnboard && (
              <Card title={t('besuReq.title')}>
                <p className="text-xs text-ink-3">{t('nodeSet.besuHint')}</p>
                <BesuRequirements rpcPort={data.rpcPort ?? 8545} metricsPort={data.metricsPort ?? 9545} node={data} runbook={{ nodeId: data.id, nodeName: data.name ?? undefined }} />
              </Card>
            )}
            {tab === 'ops' && canOnboard && <OpsTab key={data.id} node={data} />}
          </div>
        </>
      )}
    </div>
  );
}

function BackToNodes() {
  const { t } = useTranslation();
  return (
    <BackLink to="/node-join">{t('join.title')}</BackLink>
  );
}

/** Basic info tab: node identity on top (read-only basic facts, often copied), editable note and local ports side by side below. Consensus algorithm and validator count are chain-wide, not here. */
function ConnTab({ node }: { node: TNodeDetail }) {
  const { t } = useTranslation();
  const version = node.clientVersion ? shortVersion(node.clientVersion) : null;
  const noRpc = !node.capabilities?.agent || !!node.capabilities.rpcUnauthorized || node.capabilities.rpcReachable === false;
  const enode = node.reportedEnode ?? node.p2p?.enode;
  const rows: [string, React.ReactNode][] = [
    [t('settings.role'), node.consensus?.isValidator ? t('common.validator') : t('common.regularNode')],
  ];
  if (node.validatorAddress) rows.push([t('detail.validatorAddress'), <CopyableMono value={node.validatorAddress} />]);
  if (enode) rows.push([t('nodeSet.ownEnode'), <CopyableMono value={enode} />]);
  if (version) rows.push([t('nodeSet.besuVersion'), <span className="font-mono">{version}</span>]);
  return (
    <>
      <Card title={t('settings.consensusSection')}>
        {noRpc && <p className="text-xs text-ink-3">{t('nodeSet.lastRead')}</p>}
        <dl className="grid grid-cols-[6rem_1fr] gap-x-3 gap-y-2.5 text-xs">
          {rows.map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="text-ink-3">{k}</dt>
              <dd className="min-w-0 break-all text-ink-2">{v}</dd>
            </div>
          ))}
        </dl>
      </Card>
      {/* key includes the name: when renamed elsewhere the input resets, so a stale draft won't overwrite it */}
      <div className="grid gap-4 lg:grid-cols-2">
        <NameCard key={`name-${node.id}:${node.name ?? ''}`} node={node} />
        <PortsEditor key={`ports-${node.id}`} node={node} />
      </div>
    </>
  );
}

function CopyableMono({ value }: { value: string }) {
  return (
    <span className="inline-flex items-start gap-1 font-mono text-xs">
      <span className="break-all">{value}</span>
      <CopyButton value={value} variant="cell" />
    </span>
  );
}

/** Note (nodes.name): a description for yourself, not a second name; empty = no note. Edited the same way as the note under the node page title (pencil -> inline input). */
function NameCard({ node }: { node: TNodeDetail }) {
  // Viewers only see the note, no editing
  const canWrite = useAuth().can('nodes:onboard');
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [editing, setEditing] = useState(false);
  const savedFlash = useSavedFlash();
  const done = () => {
    setEditing(false);
    for (const k of [['node', node.id], ['nodes'], ['sharedPeers'], ['nodePeers'], ['validators']]) void qc.invalidateQueries({ queryKey: k });
  };
  return (
    <Card title={t('nodeSet.nameTitle')}>
      <p className="text-xs text-ink-3">{t('nodeSet.nameHint')}</p>
      {editing ? (
        <NameEditor value={node.name} label={t('overview.editNote')} placeholder={t('overview.notePlaceholder')} onSave={(v) => api.setNodeName(node.id, { name: v })} onDone={(saved) => { done(); if (saved) savedFlash.mark('name'); }} />
      ) : (
        <span className="inline-flex items-center gap-1.5 text-xs">
          {node.name ? <span key={savedFlash.key('name')} className={`text-ink ${savedFlash.cls('name')}`}>{node.name}</span> : <span className="text-ink-faint">{t('overview.addNote')}</span>}
          {canWrite && <EditPencil label={t('overview.editNote')} onClick={() => setEditing(true)} />}
        </span>
      )}
    </Card>
  );
}

/** Agent & join tab: upgrade Agent, rejoin, unjoin, all about this node's relationship with the platform. Log level changes Besu itself, so it is in the logs tab of node status. */
function OpsTab({ node }: { node: TNodeDetail }) {
  const { t } = useTranslation();
  const canWrite = useAuth().can('nodes:onboard');
  const nav = useNavigate();
  const qc = useQueryClient();
  const [rejoinTok, setRejoinTok] = useState<JoinTokenCreated | null>(null);
  // Same as "Run the agent as" in node registration: choosing Docker adds --runtime docker to the command automatically, so nobody has to edit it by hand.
  // Starts at the runtime the agent reports, so a re-join keeps the node running the same way unless someone picks otherwise.
  const [env, setEnv] = useState<AgentRuntime>(node.capabilities?.agentRuntime ?? 'systemd');
  const rejoin = useMutation({ mutationFn: api.createJoinToken, onSuccess: setRejoinTok });
  const [askDel, setAskDel] = useState(false);
  const [typed, setTyped] = useState('');
  const del = useMutation({
    mutationFn: () => api.deleteNode(node.id),
    onSuccess: () => { void qc.invalidateQueries({ queryKey: ['nodes'] }); nav('/node-join', LEAVE_WITHOUT_ASKING); },
  });
  // The upgrade card's text answers "should I upgrade": compare the agent version this node reports with the one the platform ships (same as "Agent version" in the capability list);
  // if the version can't be read (agent offline, or not asked this round) only say what to do. What the command does isn't explained here, so nobody thinks those steps are manual
  const agentVer = node.capabilities?.agentVersion;
  const latestVer = useQuery({ queryKey: ['agentBundle'], queryFn: api.getAgentBundle, staleTime: 5 * 60_000 }).data?.agentVersion;
  const upgradeLine = !agentVer || !latestVer ? t('nodeSet.upgradeRun')
    : olderThan(agentVer, latestVer) ? t('nodeSet.upgradeOld', { v: agentVer, latest: latestVer })
    : t('nodeSet.upgradeLatest', { v: agentVer });
  const { http: origin } = usePlatformAddress();
  // Rejoining also downloads install.sh and runs it as root: like node registration, verify it first against the platform's checksum
  const installSha = useQuery({ queryKey: ['agentBundle'], queryFn: api.getAgentBundle, enabled: !!rejoinTok }).data?.installShSha256;

  return (
    <>
      {/* Upgrade and rejoin side by side: both are a command run on the node host; prerequisites and timing go in the ⓘ */}
      <div className="grid gap-4 lg:grid-cols-2">
      <Card
        title={(
          <>
            {t('detail.upgradeTitle')}
            {/* First why and when to upgrade (platform updates bring a new version, the one on the node won't update itself), then how and what the impact is */}
            <HelpTip title={t('detail.upgradeTitle')}>
              <dl className="flex flex-col gap-1.5">
                {(['when', 'how', 'effect'] as const).map((k) => (
                  <div key={k}><dt className="font-medium text-ink">{t(`nodeSet.upgradeHelp.${k}.term`)}</dt><dd>{t(`nodeSet.upgradeHelp.${k}.text`)}</dd></div>
                ))}
              </dl>
            </HelpTip>
          </>
        )}
      >
        <p className="text-xs text-ink-3">{upgradeLine}</p>
        {/* No --platform / --state-dir: upgrade-agent.sh reads them from the agent's own config, which knows a per-node address or a custom --state-dir */}
        <CopyCmd value={`curl -fsSL ${origin}/upgrade-agent.sh -o upgrade-agent.sh && sudo bash upgrade-agent.sh`} />
      </Card>
      {/* Cards only say what they do; when to use them and what is kept go in the ⓘ. Rejoin and unjoin are actions, hidden from viewers */}
      {canWrite && <Card title={<>{t('detail.rejoinTitle')}<HelpTip title={t('detail.rejoinTitle')}>{t('nodeSet.rejoinHelp')}</HelpTip></>} alert={!!node.quarantinedAt}>
        <p className="text-xs text-ink-3">{t('nodeSet.rejoinHint')}</p>
        <div className="flex items-center gap-2 text-xs text-ink-3">
          <span>{t('join.formEnv')}</span>
          <Listbox width="w-36"
            aria-label={t('join.formEnv')}
            value={env}
            onChange={setEnv}
            options={[{ value: 'systemd', label: t('join.envSystemd') }, { value: 'docker', label: t('join.envDocker') }]}
          />
        </div>
        {rejoinTok ? (
          <>
            {installSha
              ? <CopyCmd value={installCommand({ origin, inbound: rejoinTok.platformAddr, token: rejoinTok.token, docker: env === 'docker', sha: installSha, reEnroll: true })} />
              : <Skeleton className="h-16" />}
            <span className="text-xs text-ink-3">{t('nodeSet.rejoinOnce')}</span>
          </>
        ) : (
          <div>
            <Button onClick={() => rejoin.mutate({ nodeId: node.id, rpcPort: node.rpcPort ?? 8545, metricsPort: node.metricsPort ?? 9545, ttl: '1h' })} disabled={rejoin.isPending}>
              {rejoin.isPending ? t('detail.rejoinGenerating') : t('detail.rejoinGen')}
            </Button>
          </div>
        )}
        {rejoin.isError && (
          <span role="alert" className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>
            {rejoin.error instanceof HttpError ? errMsg(rejoin.error) : t('error.title')}
          </span>
        )}
      </Card>}
      </div>
      {canWrite && <Card title={t('settings.deleteTitle')} danger>
        <p className="whitespace-pre-line text-xs text-ink-3">{t('nodeSet.deleteHint')}</p>
        <div>
          <Button variant="dangerOutline" onClick={() => { setTyped(''); del.reset(); setAskDel(true); }}>
            {t('nodeSet.deleteButton')}
          </Button>
        </div>
      </Card>}

      {askDel && (
        <Modal width="max-w-sm" onClose={() => setAskDel(false)} dismissOnOutside={false} autoFocusChild>
          <ModalTitle className="text-base font-semibold" style={{ color: 'var(--bsos-status-error-fg)' }}>{t('settings.deleteTitle')}</ModalTitle>
          <p className="text-xs text-ink-2">{t('nodeSet.deleteBody', { id: node.id })}</p>
          <form className="flex flex-col gap-3" onSubmit={(e) => { e.preventDefault(); if (typed === node.id) del.mutate(); }}>
            <label className="flex flex-col gap-1 text-xs text-ink-3">
              <span>{t('nodeSet.deleteType')} <span className="font-mono text-ink-2">{node.id}</span></span>
              <input value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" spellCheck={false} autoFocus className={`${inputCls} font-mono`} />
            </label>
            {del.isError && (
              <span role="alert" className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>
                {del.error instanceof HttpError ? errMsg(del.error) : t('settings.deleteError')}
              </span>
            )}
            <div className="flex justify-end gap-2">
              <Button onClick={() => setAskDel(false)}>{t('common.cancel')}</Button>
              <Button type="submit" variant="danger" disabled={typed !== node.id || del.isPending}>
                {del.isPending ? t('settings.deleting') : t('settings.deleteConfirmButton')}
              </Button>
            </div>
          </form>
        </Modal>
      )}
    </>
  );
}

