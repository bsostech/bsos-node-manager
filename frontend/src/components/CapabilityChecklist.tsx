import { useState } from 'react';
import { useAuth } from '@/auth/AuthContext';
import { useQuery } from '@tanstack/react-query';
import { Trans, useTranslation } from 'react-i18next';
import { CaretRight, Warning } from '@phosphor-icons/react';
import { api } from '@/api/client';
import type { NodeCertStatus, NodeDetail } from '@/api/schemas';
import type { Tone } from '@/lib/status';
import { allowlistState, apiState, observeLogsState, olderThan, requiredMissing, rpcUp } from '@/lib/capabilities';
import { fmtDateTimeShort } from '@/lib/format';
import { GoLink } from '@/components/TextLinks';
import { Expand } from '@/components/Expand';

type Group = 'required' | 'optional';
/** fix: links to the Besu setup guide; go: links to the feature page that handles it (RPC auth, transaction address authorization) */
/** dash: agent online but the value can't be read (e.g. RPC unreachable); status shows "—" with no explanation (the reason is on the RPC row); same format as other rows (gray dot + text). rejoin: links to "Re-join" */
type Item = { key: string; label: string; group: Group; tone: Tone; state: string; hint?: React.ReactNode; fix?: boolean; upgrade?: boolean; go?: { to: string; label: string }; dash?: boolean; rejoin?: boolean };

/** Required namespace -> which features are lost without it (ones not listed show only the name). Optional ADMIN, TXPOOL, PERM each get their own row, see below. */
const API_IMPACT: Record<string, string> = {
  'IBFT/QBFT': 'caps.impactConsensus',
  NET: 'caps.impactNet',
};

/**
 * Capability list (required/optional are defined in lib/capabilities.ts): whether the prerequisites for each layer of platform
 * features are met. Collapsed to one line when everything is fine; expanded only when something is wrong.
 * Two sections: required (Agent, Agent version, RPC, metrics, and the certificate when it needs attention) and optional
 * (ADMIN, TXPOOL, transaction address authorization, RPC auth, logs).
 * Red = the platform can't see this node or is rejected; yellow = a required piece is missing; gray = optional item not enabled,
 * agent upgradable, or unknown because the agent is offline (none of these count as needing attention).
 * The certificate appears only when someone must act: agent offline (renewal has stopped; once expired it must rejoin) or not trusted by the platform.
 */
export function CapabilityChecklist({ node, cert, onFix, onAgentTab }: { node: NodeDetail; cert?: NodeCertStatus; /** When not given (viewer), the "Besu setup guide" link is hidden */ onFix?: () => void; /** When not given (viewer), the "Upgrade agent" and "Re-join" links are hidden */ onAgentTab?: () => void }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  // Viewers only see the current state: links to configure on feature pages (transaction address authorization, node access settings) are hidden
  const canWrite = useAuth().can('nodes:onboard');
  const latest = useQuery({ queryKey: ['agentBundle'], queryFn: api.getAgentBundle, staleTime: 5 * 60_000 }).data?.agentVersion;
  const c = node.capabilities;
  if (!c) return null;

  const items: Item[] = [];
  const unknown = (key: string, label: string, group: Group, impact: string): Item => ({ key, label, group, tone: 'neutral', state: t('caps.unknown'), hint: t(impact) });
  const none = (key: string, label: string, group: Group): Item => ({ key, label, group, tone: 'neutral', state: '—', dash: true });
  // Optional items: "On" when on; when off, gray "Off" plus a line on what enabling it allows; not counted as needing attention
  const optional = (key: string, label: string, on: boolean, gives: string, go?: Item['go']): Item =>
    on ? { key, label, group: 'optional', tone: 'healthy', state: t('caps.on') } : { key, label, group: 'optional', tone: 'neutral', state: t('caps.off'), hint: gives, ...(go ? { go } : { fix: true }) };
  const nodeQ = `?node=${encodeURIComponent(node.id)}`;
  if (!c.agent && node.agentOutdatedAt) {
    // The platform rejects the connection because the protocol version is too old: the cause is clear, so link straight to "Upgrade agent" (the upgrade script uses the agent's certificate and works even when the agent isn't connected)
    items.push({ key: 'agent', label: 'Agent', group: 'required', tone: 'error', state: t('caps.agentOutdated'), hint: t('caps.agentOutdatedHint'), upgrade: true });
  } else if (!c.agent) {
    // The explanation has two parts: what to check first, then what to do based on the result; the last resort is rejoining, linked to that card
    items.push({ key: 'agent', label: 'Agent', group: 'required', tone: 'error', state: t('caps.agentDown'), hint: <AgentDownSteps />, rejoin: true });
    // Agent not connected: nothing else can be read and none of it matters yet; keep only the Agent row (and the certificate expiry below) so people focus on getting the agent back first
  } else {
    items.push({ key: 'agent', label: 'Agent', group: 'required', tone: 'healthy', state: t('caps.ok') });
    // Agent version: if older than the platform's latest, a gray hint that it can be upgraded (not an error), linked to "Upgrade agent"; no version this round shows "—"
    if (c.agentVersion && latest) {
      items.push(olderThan(c.agentVersion, latest)
        ? { key: 'agentVersion', label: t('caps.agentVersion'), group: 'required', tone: 'neutral', state: t('caps.versionOld', { v: c.agentVersion, latest }), upgrade: true }
        : { key: 'agentVersion', label: t('caps.agentVersion'), group: 'required', tone: 'healthy', state: t('caps.versionLatest', { v: c.agentVersion }) });
    } else items.push(none('agentVersion', t('caps.agentVersion'), 'required'));
    const missing = requiredMissing(c);
    if (c.rpcUnauthorized) items.push({ key: 'rpc', label: 'RPC', group: 'required', tone: 'error', state: t('caps.rpcUnauthorized'), hint: t('caps.rpcUnauthorizedHint'), fix: true });
    else if (c.rpcReachable === false) items.push({ key: 'rpc', label: 'RPC', group: 'required', tone: 'error', state: t('caps.rpcDown', { port: node.rpcPort }), hint: t('caps.rpcDownHint'), fix: true });
    else if (c.rpcReachable == null) items.push(unknown('rpc', 'RPC', 'required', 'caps.impactRpc'));
    else if (missing.length) {
      items.push({
        key: 'rpc', label: 'RPC', group: 'required', tone: 'syncing', state: t('caps.apisMissing', { list: missing.join(t('common.listSep')) }),
        hint: missing.map((a) => (API_IMPACT[a] ? `${a}：${t(API_IMPACT[a])}` : a)).join(' · '), fix: true,
      });
    } else items.push({ key: 'rpc', label: 'RPC', group: 'required', tone: 'healthy', state: t('caps.ok') });
    if (c.metricsReachable === false) items.push({ key: 'metrics', label: 'metrics', group: 'required', tone: 'syncing', state: t('caps.metricsDown', { port: node.metricsPort }), hint: t('caps.metricsDownHint'), fix: true });
    else if (c.metricsReachable == null) items.push(unknown('metrics', 'metrics', 'required', 'caps.impactMetrics'));
    else items.push({ key: 'metrics', label: 'metrics', group: 'required', tone: 'healthy', state: t('caps.ok') });
    // Optional items depend on RPC: unreadable when RPC isn't connected, shown as "—" (the reason is on the RPC row)
    const admin = apiState(c, 'ADMIN'), txpool = apiState(c, 'TXPOOL'), allow = allowlistState(c);
    const enable = (k: string) => t('caps.enableGives', { list: t(k) });
    items.push(admin ? optional('admin', t('caps.labelAdmin'), admin === 'on', enable('caps.givesAdmin')) : none('admin', t('caps.labelAdmin'), 'optional'));
    items.push(txpool ? optional('txpool', t('caps.labelTxpool'), txpool === 'on', enable('caps.givesTxpool')) : none('txpool', t('caps.labelTxpool'), 'optional'));
    items.push(allow ? optional('allowlist', t('caps.allowlist'), allow === 'on', enable('caps.givesAllowlist'), { to: `/access/permissioning${nodeQ}`, label: t('caps.goAllowlist') }) : none('allowlist', t('caps.allowlist'), 'optional'));
    // RPC auth: on 401 the RPC row is already red (public key mismatch), so this shows only "—" instead of repeating it
    items.push(rpcUp(c) && node.rpcAuthEnabled != null
      ? optional('rpcAuth', t('caps.rpcAuth'), node.rpcAuthEnabled, enable('caps.givesRpcAuth'), { to: `/access${nodeQ}`, label: t('caps.goAccess') })
      : none('rpcAuth', t('caps.rpcAuth'), 'optional'));
    const logs = observeLogsState(node.id, c);
    if (logs === 'lost') items.push({ key: 'logs', label: t('caps.logs'), group: 'optional', tone: 'neutral', state: t('caps.logsLost'), hint: t('caps.logsLostHint'), fix: true });
    else if (logs) items.push(optional('logs', t('caps.logs'), logs === 'on', enable('caps.givesLogs')));
    else items.push(unknown('logs', t('caps.logs'), 'optional', 'caps.impactLogs'));
  }
  // Agent certificate: red when untrusted; when offline, warn about expiry (online agents renew automatically, offline ones don't - this is the deadline for fixing the agent); when online it is always listed, "OK" when fine, "—" when not yet obtained
  // The backend returns untrusted for expired, revoked and unknown alike; check expiry first, otherwise it reads as a CA reset and users won't know they should rejoin
  const left = cert?.notAfter ? Date.parse(cert.notAfter) - Date.now() : null;
  if (cert?.notAfter && left !== null && left <= 0 && (cert.cert === 'untrusted' || !c.agent))
    items.push({ key: 'cert', label: t('settings.cert'), group: 'required', tone: 'error', state: t('caps.certExpired', { time: fmtDateTimeShort(cert.notAfter) }), hint: t('caps.certExpiredHint'), rejoin: true });
  else if (cert?.cert === 'untrusted') items.push({ key: 'cert', label: t('settings.cert'), group: 'required', tone: 'error', state: t('settings.certUntrusted'), hint: t('settings.certUntrustedHint') });
  else if (cert?.notAfter && left !== null && !c.agent) {
    // No automatic renewal while offline: show how many days remain (the deadline); after that it becomes the red case above and can only rejoin
    const time = fmtDateTimeShort(cert.notAfter);
    items.push({ key: 'cert', label: t('settings.cert'), group: 'required', tone: 'syncing', state: left < 86_400_000 ? t('caps.certExpiresSoon', { time }) : t('caps.certExpiresIn', { time, count: Math.round(left / 86_400_000) }), hint: t('caps.certOfflineHint') });
  }
  else if (cert && c.agent) items.push({ key: 'cert', label: t('settings.cert'), group: 'required', tone: 'healthy', state: t('caps.ok') });
  else if (c.agent) items.push(none('cert', t('settings.cert'), 'required'));

  // Certificate expiry while offline is a reminder, not a failing item: what is actually failing is the Agent, and that row is already red
  const bad = items.filter((x) => x.tone === 'error' || (x.tone === 'syncing' && x.key !== 'cert'));
  const tone: Tone = bad.some((x) => x.tone === 'error') ? 'error' : 'syncing';
  const expanded = open || bad.length > 0;
  const fg = (tn: Tone) => `var(--bsos-status-${tn}-fg)`;
  const required = items.filter((x) => x.group === 'required');
  const optionals = items.filter((x) => x.group === 'optional');
  // Collapsed one-liner: how many required items are OK (version row excluded) · how many optional items are on · agent upgradable
  const reqOk = required.filter((x) => x.tone === 'healthy' && x.key !== 'agentVersion').length;
  const optKnown = optionals.filter((x) => !x.dash && x.state !== t('caps.unknown'));
  const outdated = items.some((x) => x.upgrade);

  const summary = bad.length ? (
    <>
      <span className="inline-flex items-center gap-1.5 text-xs font-medium" style={{ color: fg(tone) }}>
        <Warning size={15} weight="fill" aria-hidden="true" />
        {t(tone === 'error' ? 'caps.badCount' : 'caps.warnCount', { count: bad.length })}
      </span>
      <span className="text-xs text-ink-2">{bad.map((b) => `${b.label}：${b.state}`).join('；')}</span>
    </>
  ) : (
    <>
      <span className="inline-flex items-center gap-1.5 text-xs font-medium text-ink-2">
        <span className="size-1.5 rounded-full" style={{ background: fg('healthy') }} />
        {t('caps.requiredOk', { count: reqOk })}
      </span>
      <span className="text-xs text-ink-3">
        {[
          optKnown.length ? t('caps.optionalOn', { on: optKnown.filter((x) => x.tone === 'healthy').length, total: optKnown.length }) : '',
          outdated ? t('caps.upgradeShort') : '',
        ].filter(Boolean).join(' · ')}
      </span>
    </>
  );

  const row = (it: Item) => {
    const loud = it.tone === 'error' || it.tone === 'syncing';
    return (
      <li key={it.key} className="grid grid-cols-[9rem_1fr] items-baseline gap-3 py-1.5">
        <span className="text-xs text-ink-3">{it.label}</span>
        <div className="flex min-w-0 flex-col gap-0.5">
          {/* Links (upgrade agent, go to setup guide or feature page, rejoin) always follow the status text on the right; the explanation goes on the next line */}
          <span className="flex flex-wrap items-center gap-x-3">
            <span className={`inline-flex items-center gap-1.5 text-xs ${loud ? 'font-medium' : 'text-ink-2'}`} style={loud ? { color: fg(it.tone) } : undefined}>
              <span aria-hidden="true" className="size-1.5 shrink-0 rounded-full" style={{ background: fg(it.tone) }} />
              {it.state}
            </span>
            {it.fix && onFix && <GoLink onClick={onFix}>{t('caps.goBesu')}</GoLink>}
            {it.go && canWrite && <GoLink to={it.go.to}>{it.go.label}</GoLink>}
            {it.upgrade && onAgentTab && <GoLink onClick={onAgentTab}>{t('caps.upgrade')}</GoLink>}
            {it.rejoin && onAgentTab && <GoLink onClick={onAgentTab}>{t('detail.rejoinTitle')}</GoLink>}
          </span>
          {/* Indent the explanation past the dot (dot size-1.5 + gap-1.5 = pl-3) to align with the status text */}
          {it.hint && <div className="whitespace-pre-line pl-3 text-xs text-ink-3">{it.hint}</div>}
        </div>
      </li>
    );
  };
  // When the agent isn't connected only the Agent and certificate rows remain with no optional section, so no need to label it "Required"
  const group = (label: string, list: Item[]) => list.length > 0 && (
    <div className="flex flex-col">
      {c.agent && <span className="pt-1 text-2xs font-medium text-ink-3">{label}</span>}
      <ul className="flex flex-col">{list.map(row)}</ul>
    </div>
  );

  return (
    <section className="rounded-lg border border-hair bg-surface" style={bad.length ? { borderColor: fg(tone) } : undefined}>
      {bad.length ? (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3 text-sm">
          <span className="text-xs font-semibold uppercase tracking-wide text-ink-3">{t('caps.title')}</span>
          {summary}
        </div>
      ) : (
        <button type="button" onClick={() => setOpen((v) => !v)} aria-expanded={expanded} className="flex w-full flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3 text-left text-sm">
          <span className="text-xs font-semibold uppercase tracking-wide text-ink-3">{t('caps.title')}</span>
          {summary}
          <CaretRight size={14} aria-hidden="true" className={`ml-auto text-ink-3 transition-transform ${expanded ? 'rotate-90' : ''}`} />
        </button>
      )}
      {expanded && (
        <Expand>
          <div className="flex flex-col gap-2 border-t border-hair px-4 py-2">
            {group(t('caps.groupRequired'), required)}
            {group(t('caps.groupOptional'), optionals)}
          </div>
        </Expand>
      )}
    </section>
  );
}

/** Steps for an unconnected agent: check two things first, then act on the result. Commands use monospace for readability and copying */
function AgentDownSteps() {
  const { t } = useTranslation();
  const code = { c: <code className="font-mono text-ink-2" /> };
  return (
    <div className="flex flex-col gap-3 whitespace-normal">
      <div className="flex flex-col gap-1">
        <p>{t('caps.agentDownSteps.check')}</p>
        <ul className="list-disc pl-4">
          <li><Trans i18nKey="caps.agentDownSteps.checkRun" components={code} /></li>
          <li>{t('caps.agentDownSteps.checkPort')}</li>
        </ul>
      </div>
      <div className="flex flex-col gap-1">
        <p>{t('caps.agentDownSteps.then')}</p>
        <dl className="flex flex-col gap-2">
          {(['stopped', 'blocked', 'still'] as const).map((k) => (
            <div key={k}><dt className="font-medium text-ink-2">{t(`caps.agentDownSteps.${k}.term`)}</dt><dd><Trans i18nKey={`caps.agentDownSteps.${k}.text`} components={code} /></dd></div>
          ))}
        </dl>
      </div>
    </div>
  );
}
