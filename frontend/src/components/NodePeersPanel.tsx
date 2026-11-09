import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { ArrowDownLeft, ArrowUpRight } from '@phosphor-icons/react';
import { api } from '@/api/client';
import type { PeerInfo } from '@/api/schemas';
import { enterprise, isEnterprise } from '@/edition';
import { OrgFilter, type OrgSel } from '@/components/OrgFilter';
import { SkeletonRows, EmptyState, ErrorState } from '@/components/States';
import { Card } from '@/components/Card';
import { Listbox } from '@/components/Listbox';
import { CountChip } from '@/components/ValueChip';
import { OwnOrgTag } from '@/components/Badges';
import { Pill } from '@/components/Pill';
import { HelpTip } from '@/components/HelpTip';
import { ValidatorTag } from '@/components/ValidatorTag';
import { CopyButton } from '@/components/CopyButton';
import { useAuth } from '@/auth/AuthContext';
import { fmtAddr } from '@/lib/format';
import { GoLink } from '@/components/TextLinks';

type RoleF = 'all' | 'val' | 'nonval';
// Organization filter: Core only splits our org / others (whether there is a node id); Enterprise uses the overview's organization dropdown
type UnitSel = OrgSel | { kind: 'other' };

/** One row of the card. Peers of our own nodes are PeerInfo directly; external organizations' are converted by Enterprise, with org = which organization the data source says it belongs to. */
export type PeerRow = Pick<PeerInfo, 'address' | 'inbound' | 'nodeId' | 'name' | 'validatorAddress' | 'remoteAddress'> & { org?: string };
/** For a node that is not ours (external organization), the caller supplies the data source; same as fetchSeries for charts. */
export type PeerSource = { queryKey: unknown[]; fetch: () => Promise<{ reported: boolean; peers: PeerRow[] }> };

const noGroups = () => undefined;

/**
 * Peers the node is currently connected to (admin_peers), for monitoring; any logged-in user can view.
 * Ours <=> has a node id; validator <=> has a validator address -- the two are judged separately, and our own validators have both.
 * External organizations' peers have no id or name, so they are filled in here by matching address against our node list; the organization column (Enterprise) then matches address against external organizations' node lists.
 * The peer's network address needs monitor:view (cross-organization relays do not carry it, see backend publicPeer); without it that column is not drawn. Naming is in node settings; which peers to connect is in "Node peering settings".
 */
export function NodePeersPanel({ nodeId, canOnboard, source }: { nodeId: string; canOnboard: boolean; source?: PeerSource }) {
  const { t } = useTranslation();
  const { me } = useAuth();
  const q = useQuery({
    queryKey: source?.queryKey ?? ['nodePeers', nodeId],
    queryFn: source?.fetch ?? (() => api.listNodePeers(nodeId)),
    refetchInterval: 15_000,
  });
  const ownNodes = useQuery({ queryKey: ['nodes'], queryFn: () => api.listNodes(), enabled: !!source });
  // Read edition only at render time: the Enterprise external node page also imports this card, and reading at module load would hit a circular import
  const groups = (enterprise.overviewOrgs?.useGroups ?? noGroups)();
  const [role, setRole] = useState<RoleF>('all');
  const [unit, setUnit] = useState<UnitSel>({ kind: 'all' });

  // Peer address: shown whenever it comes back (viewers get it too); external organizations' data sources do not provide this column
  const showRemote = (q.data?.peers ?? []).some((p) => p.remoteAddress != null);
  const ownByAddr = new Map((ownNodes.data?.nodes ?? []).flatMap((n) => (n.address ? [[n.address.toLowerCase(), n] as const] : [])));
  const rows: PeerRow[] = (q.data?.peers ?? []).map((p) => {
    const n = p.nodeId ? undefined : ownByAddr.get(p.address.toLowerCase());
    return n ? { ...p, nodeId: n.id, name: n.name } : p;
  });
  // Organization: our nodes go by id; if the data source says, follow it; otherwise match address against external organizations' lists; if nothing matches it is unknown
  const orgOf = (p: PeerRow) => (p.nodeId ? me?.org : p.org ?? groups?.find((g) => g.nodes.some((n) => n.address?.toLowerCase() === p.address.toLowerCase()))?.org);
  const unitOk = (p: PeerRow, u: UnitSel) =>
    u.kind === 'all' || (u.kind === 'org' ? orgOf(p) === u.org : (u.kind === 'own') === !!p.nodeId);
  const roleOk = (p: PeerRow, r: RoleF) => r === 'all' || (r === 'val') === !!p.validatorAddress;
  const peers = rows.filter((p) => roleOk(p, role) && unitOk(p, unit));
  // Every dropdown option is labeled with a count, computed under the other dropdown's current selection
  const roleOpt = (value: RoleF, label: string) => ({ value, label: t('common.withCount', { label, n: rows.filter((p) => roleOk(p, value) && unitOk(p, unit)).length }) });
  const scopeOpt = (value: 'all' | 'own', label: string) => ({ value, label: t('common.withCount', { label, n: rows.filter((p) => roleOk(p, role) && unitOk(p, { kind: value } as UnitSel)).length }) });
  // All connections in one direction: only inbound = cannot dial out; only outbound = others cannot connect in (judged only with at least 2 connections, 1 means nothing)
  const ins = rows.filter((p) => p.inbound).length;
  const oneWay: 'in' | 'out' | null = rows.length >= 2 ? (ins === rows.length ? 'in' : ins === 0 ? 'out' : null) : null;
  const unitLabel = (org?: string) => (!org ? null : org === me?.org ? <OwnOrgTag>{t('overview.orgOwn')}</OwnOrgTag> : org);

  const manage = canOnboard && (
    <GoLink to={`/node-join/peers?node=${encodeURIComponent(nodeId)}`}>{t('peers.manage')}</GoLink>
  );
  // Filters and count go on the right of the card title bar (site-wide rule); hidden when there is no data. "manage peers" moved to the bottom right of the card
  const filters = !!rows.length && q.data?.reported !== false && (
    <div className="flex flex-wrap items-center justify-end gap-2">
    {/* Filters are always "label + dropdown", right-aligned, with the count at the far right (one rule site-wide) */}
    <label className="flex items-center gap-2 text-xs text-ink-3">
      {t('peers.filterRole')}
      <Listbox width="w-auto" aria-label={t('peers.filterRole')} value={role} onChange={setRole}
        options={[roleOpt('all', t('peers.roleAll')), roleOpt('val', t('common.validator')), roleOpt('nonval', t('peers.roleNonVal'))]} />
    </label>
    {groups ? (
      <OrgFilter value={unit.kind === 'other' ? { kind: 'all' } : unit} onChange={setUnit} ownOrg={me?.org} groups={groups}
        ownCount={rows.filter((p) => p.nodeId).length} countOf={(u) => rows.filter((p) => unitOk(p, u)).length} />
    ) : (
      // Each organization installs its own platform and cannot see whether others use one; the only split possible is "nodes registered by our org" vs "others"
      <label className="flex items-center gap-2 text-xs text-ink-3">
        {t('peers.filterScope')}
        <Listbox width="w-auto" aria-label={t('peers.filterScope')} value={unit.kind === 'own' ? 'own' : 'all'} onChange={(kind) => setUnit({ kind } as typeof unit)}
          options={[
            scopeOpt('all', t('peers.scopeAll')),
            scopeOpt('own', isEnterprise ? t('peers.unitOwn', { org: me?.org }) : t('peers.scopeOwn')),
          ]} />
      </label>
    )}
    {/* No filter by direction; only when lopsided (inbound only or outbound only) is a pill hint shown, since that is what needs attention */}
    {oneWay && <Pill tone="warn">{t(oneWay === 'in' ? 'peers.onlyIn' : 'peers.onlyOut')}</Pill>}
    <CountChip unit={t('chain.countUnit')}>{peers.length}</CountChip>
    </div>
  );
  const td = 'whitespace-nowrap px-4 py-2.5';
  const th = 'sticky top-0 px-4';
  return (
    <div className="flex flex-col gap-2">
    <Card title={t('peers.title')} aside={filters} bodyClass="@container flex flex-col">
      {q.isLoading ? (
        <div className="p-4"><SkeletonRows rows={3} /></div>
      ) : q.isError ? (
        <ErrorState inCard onRetry={() => void q.refetch()} />
      ) : q.data && !q.data.reported ? (
        <EmptyState message={t('peers.notReported', { node: nodeId })} />
      ) : !rows.length ? (
        <EmptyState message={t('peers.empty')} />
      ) : (
        <>
          {peers.length === 0 ? (
            <EmptyState message={t('peers.noMatch')} />
          ) : (
            <div className="max-h-[28rem] overflow-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="table-head">
                    {/* Same layout as the on-chain node table: name first, abbreviated address copyable, role as a pill */}
                    <th className={th}>{t('peers.colName')}</th>
                    {groups && <th className={th}>{t('peers.colUnit')}</th>}
                    <th className={th}>{t('peers.colAddr')}</th>
                    <th className={th}>{t('peers.colRole')}</th>
                    <th className={th}>
                      <span className="inline-flex items-center gap-1.5">{t('peers.colDir')}
                        <HelpTip title={t('peers.colDir')}>
                          <dl className="flex flex-col gap-1.5">
                            <div><dt className="font-medium text-ink">{t('peers.out')}</dt><dd>{t('peers.helpOut')}</dd></div>
                            <div><dt className="font-medium text-ink">{t('peers.in')}</dt><dd>{t('peers.helpIn')}</dd></div>
                            <p className="border-t border-hair pt-1.5 text-ink-3">{t('peers.helpOneLink')}</p>
                          </dl>
                        </HelpTip>
                      </span>
                    </th>
                    {showRemote && <th className={th}>{t('peers.colRemote')}</th>}
                  </tr>
                </thead>
                <tbody>
                  {peers.map((p) => (
                    <tr key={p.address} className="border-b border-hair last:border-0">
                      {/* Name: our node's name is its id + "Ours" (just a label, not a link); others show the name our org gave it, or "Unnamed" if none */}
                      <td className={td}>
                        {p.nodeId ? (
                          <span className="inline-flex items-center gap-1.5">
                            <span className="font-mono text-ink">{p.nodeId}</span>
                            <Pill tone="accent">{t('chain.ownTag')}</Pill>
                          </span>
                        ) : (
                          <span className={p.name ? 'text-ink' : 'text-ink-faint'}>{p.name ?? t('chain.unnamed')}</span>
                        )}
                      </td>
                      {groups && <td className={`${td} text-ink-2`}>{unitLabel(orgOf(p)) ?? <span className="text-ink-3">—</span>}</td>}
                      <td className={`${td} font-mono text-xs text-ink-2`}>
                        <span className="inline-flex items-center gap-1" title={p.address}>{fmtAddr(p.address)}<CopyButton value={p.address} variant="cell" /></span>
                      </td>
                      <td className={td}>{p.validatorAddress ? <ValidatorTag className="-ml-1.5" /> : <Pill className="-ml-1.5">{t('chain.regular')}</Pill>}</td>
                      <td className={`${td} text-xs text-ink-2`}>
                        <span className="inline-flex items-center gap-1">
                          {p.inbound ? <ArrowDownLeft size={12} aria-hidden="true" /> : <ArrowUpRight size={12} aria-hidden="true" />}
                          {t(p.inbound ? 'peers.in' : 'peers.out')}
                        </span>
                      </td>
                      {showRemote && <td className={`${td} font-mono text-xs text-ink-2`}>{p.remoteAddress ?? <span className="font-sans text-ink-3">—</span>}</td>}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </Card>
    {manage && <div className="flex justify-end">{manage}</div>}
    </div>
  );
}
