import { useState } from 'react';
import { useAuth } from '@/auth/AuthContext';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import type { NodeSummary, RpcUser } from '@/api/schemas';
import { AUTH_LINES, PubkeyFetch, WayHint, WaySeg, reqStatus, snippetOf, type Way } from '@/components/BesuRequirements';
import { CARD, Card } from '@/components/Card';
import { EnterpriseTeaser } from '@/components/EnterpriseTeaser';
import { Switch } from '@/components/Switches';
import { PermChips } from '@/components/RpcUserModals';
import { CopyCmd } from '@/components/CopyButton';
import { GuideButton, LiveNodeStates, StepWizard, type NodeState } from '@/components/StepWizard';
import { NodeLabel } from '@/components/NodeLabel';
import { isOffline } from '@/components/NodePeerCards';
import { Pill } from '@/components/Pill';
import { GoLink } from '@/components/TextLinks';
import { BESU_DOCS } from '@/lib/besuDocs';

/**
 * Status card on the node tab: whether this node has RPC auth on. Whether it is on is the node's own startup flag; the platform only shows the probe result.
 * States: offline -> public key mismatch (red) -> auth on / auth off (gray; off is not a fault: a node that only produces blocks and does not expose RPC can reasonably leave it off).
 */
export type RpcAuthCode = 'offline' | 'on' | 'off' | 'unknown' | 'keyMismatch';
export const rpcAuthCode = (n: NodeSummary): RpcAuthCode => (isOffline(n) ? 'offline' : reqStatus(n).auth.code as RpcAuthCode);

// The last step of the guide should show "it got better": auth on is green
const authState = (n: NodeSummary, t: TFunction): NodeState => {
  const { tone, code } = reqStatus(n).auth;
  return { tone, label: t(`rpcAuth.state.${code}`) };
};

export function RpcAuthStatusCard({ node, onViewUsers }: { node: NodeSummary; onViewUsers: () => void }) {
  const { t } = useTranslation();
  // How to fix it (follow the enable-setup guide) is for operators only
  const canWrite = useAuth().can('nodes:onboard');
  const [guide, setGuide] = useState(false);
  const code = rpcAuthCode(node);
  return (
    <section className={CARD}>
      <div className="flex flex-wrap items-center gap-2 border-b border-hair px-4 py-3 text-xs">
        <NodeLabel id={node.id} name={node.name} />
        <Pill tone={code === 'keyMismatch' ? 'err' : 'neutral'}>{t(`rpcAuth.state.${code}`)}</Pill>
        {/* Auth off, public key mismatch: the guide sits right next to the status (same as not-enabled in transaction address authorization) */}
        {(code === 'off' || code === 'keyMismatch') && <GuideButton onClick={() => setGuide(true)} label={t('allowlist.enableGuide')} />}
        {code === 'on' && <GoLink onClick={onViewUsers}>{t('rpcAuth.viewUsers')}</GoLink>}
      </div>
      <p className="whitespace-pre-line px-4 py-3 text-xs leading-relaxed text-ink-3">{t(`rpcAuth.body.${code}`)}{code === 'keyMismatch' && canWrite && t('rpcAuth.keyMismatchFix')}</p>
      {guide && <RpcAuthGuide nodes={[node]} onClose={() => setGuide(false)} />}
    </section>
  );
}

/**
 * Second card on the node tab (commercial-edition preview): which access identities may connect to this node. In the open-source edition every identity can connect to every auth-enabled node of our org,
 * so all switches are on and cannot be operated; per-node assignment is the commercial edition's "Per-node RPC access".
 */
export function NodeIdentityScopeCard({ users }: { users: RpcUser[] }) {
  const { t } = useTranslation();
  return (
    <Card title={<>{t('rpcAuth.scopeTitle')}<EnterpriseTeaser slug="rpc-node-scope" tagOnly /></>} bodyClass="">
      <p className="border-b border-hair px-4 py-2.5 text-xs text-ink-3">{t('rpcAuth.scopeCore')}</p>
      {users.length === 0 ? (
        <p className="px-4 py-6 text-center text-xs text-ink-3">{t('rpcUsers.empty')}</p>
      ) : (
        <ul aria-label={t('rpcAuth.scopeTitle')}>
          {users.map((u) => (
            <li key={u.id} className="flex items-center gap-3 border-b border-hair px-4 py-2.5 last:border-0">
              <span className="w-40 shrink-0 truncate font-mono text-xs text-ink">{u.username}</span>
              <span className="min-w-0 flex-1"><PermChips perms={u.permissions} /></span>
              <Switch on disabled onChange={() => {}} aria-label={u.username} />
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

/** Three-step guide for enabling RPC auth. The last step lists the nodes that did not have auth on when the guide was opened; if all are on, list all of them. */
export function RpcAuthGuide({ nodes, onClose }: { nodes: NodeSummary[]; onClose: () => void }) {
  const { t } = useTranslation();
  const [way, setWay] = useState<Way>('toml');
  const [ids] = useState(() => {
    const todo = nodes.filter((n) => reqStatus(n).auth.code !== 'on');
    return (todo.length ? todo : nodes).map((n) => n.id);
  });
  const stateOf = (n: NodeSummary) => authState(n, t);
  return (
    <StepWizard
      title={t('rpcAuth.guideTitle')}
      onClose={onClose}
      steps={[
        { title: t('rpcAuth.stepPubkey'), body: <PubkeyFetch /> },
        { title: t('rpcAuth.stepConfig'), body: <><WaySeg way={way} onChange={setWay} /><WayHint way={way} /><CopyCmd value={snippetOf(AUTH_LINES, way)} /></>, doc: { href: BESU_DOCS.jwtAuth, label: t('besuDoc.jwtAuth') } },
        {
          title: t('rpcAuth.stepRestart'),
          body: <><p className="text-xs text-ink-3">{t('rpcAuth.restartNote')}</p><LiveNodeStates ids={ids} stateOf={stateOf} label={t('rpcAuth.listLabel')} /></>,
        },
      ]}
    />
  );
}
