import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router-dom';
import { CaretRight, Warning } from '@phosphor-icons/react';
import type { JoinToken, NodeSummary } from '@/api/schemas';
import { NodeStatusBadge } from '@/components/Badges';
import { ValidatorTag } from '@/components/ValidatorTag';
import { NodeLabel } from '@/components/NodeLabel';
import { nodeIssue } from '@/lib/nodeIssue';
import { fmtAddr, fmtDateTimeShort, fmtSources } from '@/lib/format';
import { CopyButton } from '@/components/CopyButton';
import type { Tone } from '@/lib/status';
import { CARD, CARD_HEAD, CARD_TITLE } from '@/components/Card';
import { CountChip } from '@/components/ValueChip';
import { RowAction } from '@/components/Button';
import { presenceRef, usePresence } from '@/hooks/usePresence';

const dot = (tone: Tone) => <span className="size-1.5 shrink-0 rounded-full" style={{ background: `var(--bsos-status-${tone}-fg)` }} />;

/**
 * List for node registration and settings: our nodes + join codes waiting to connect.
 * canAdd: only roles that can register nodes can click a "join code expired" row, which goes to node registration with that node's data prefilled to regenerate the join code.
 * onRemove: "Revoke" on a "Waiting to join" row, "Remove" on a "join code expired" row; both revoke the code and drop the registration. expired tells the caller which one, for the confirm dialog it owns.
 * highlight: the node id just returned from node registration; that row (pending or connected) lights up for 1.5 s then fades (.flash-hold).
 * When a pending row becomes a connected node row it is a new row and lights up again -- which marks exactly the moment it "connected" (no separate status bar at the top of the page).
 */
export function NodesTable({ nodes, pending, highlight, canAdd = false, onRemove }: { nodes: NodeSummary[]; pending: JoinToken[]; highlight?: string; canAdd?: boolean; onRemove?: (tk: JoinToken, expired: boolean) => void }) {
  const { t } = useTranslation();
  const nav = useNavigate();
  // Sort: not yet connected (pending below, newest on top) -> those with problems -> the rest by node id A->Z (numbers by value, node-a2 before node-a10).
  // A node that just connected stays on top for this visit (the highlighted row); after leaving and coming back it returns to its A->Z position
  const rows = nodes.map((n) => ({ n, issue: nodeIssue(n, t) }))
    .sort((a, b) => Number(b.n.id === highlight) - Number(a.n.id === highlight)
      || Number(!!b.issue) - Number(!!a.issue)
      || a.n.id.localeCompare(b.n.id, undefined, { numeric: true }));
  const waiting = [...pending].sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt));
  // On register, remove or connect the row expands and fades in / fades out and collapses; no animation the one time the table mounts (when both data sets have arrived). Each cell's content is wrapped in .row-cell, which is the layer that animates
  const waitingRows = usePresence(waiting, (tk) => tk.id);
  const nodeRows = usePresence(rows, (r) => r.n.id);

  return (
    <section className={`overflow-hidden ${CARD}`}>
      {/* Few nodes, so no filter or search: list everything, with problems first and pending last */}
      <div className={CARD_HEAD}>
        <h2 className={CARD_TITLE}>{t('join.listTitle')}</h2>
        {/* Count at the far right of the title bar (one rule site-wide); pending ones count as a node too */}
        <CountChip unit={t('chain.countUnit')} className="ml-auto">{nodes.length + pending.length}</CountChip>
      </div>
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="table-head">
              <th className="px-4">{t('join.thNode')}</th>
              <th className="px-4">{t('join.colAddress')}</th>
              <th className="px-4">{t('join.colStatus')}</th>
              <th className="px-4">{t('join.colLastSeen')}</th>
              {/* Description: the node's problem ("Agent not connected"...), or when the pending join code expires */}
              <th className="px-4">{t('join.colNote')}</th>
              <th className="w-8" aria-hidden="true" />
            </tr>
          </thead>
          <tbody>
            {/* Not-yet-connected go on top: just added, waiting for it to connect. One line only, with the join code expiry in the "Details" column.
                Rows whose code expired before connecting are still listed: status says "Not joined", and the description says expired in the warning color, regenerate to connect */}
            {waitingRows.map(({ key, item: tk, state }) => {
              const expired = tk.status === 'expired' || Date.parse(tk.expiresAt) <= Date.now();
              // Clicking an expired row goes to node registration with this node's id / name / sources prefilled to regenerate the join code (otherwise the row is stuck with no way out)
              const redo = canAdd && expired && tk.nodeId
                ? `/node-add?${new URLSearchParams({ node: tk.nodeId, ...(tk.nodeName ? { name: tk.nodeName } : {}), ...(tk.allowedSources.length ? { src: fmtSources(tk.allowedSources) } : {}) })}`
                : null;
              return (
              <tr key={key} onClick={redo ? () => nav(redo) : undefined}
                ref={presenceRef(state)} className={`border-b border-hair last:border-0 ${redo ? 'group cursor-pointer transition-colors hover:bg-elevated' : ''} ${tk.nodeId === highlight ? 'flash-hold' : ''}`}>
                <td className="px-4 py-3">
                  <div className="row-cell"><span className="text-ink">{tk.nodeId ? <NodeLabel id={tk.nodeId} name={tk.nodeName} /> : tk.nodeName}</span></div>
                </td>
                <td className="px-4 py-3" />
                <td className="whitespace-nowrap px-4 py-3">
                  <div className="row-cell">
                    <span className="inline-flex items-center gap-1.5 text-xs font-medium text-ink-2">
                      {dot(expired ? 'neutral' : 'syncing')}
                      {t(expired ? 'join.notJoined' : 'join.waiting')}
                    </span>
                  </div>
                </td>
                <td className="px-4 py-3" />
                {expired ? (
                  <td className="whitespace-nowrap px-4 py-3 text-xs">
                    <div className="row-cell">
                      <span className="inline-flex items-center gap-1.5 font-medium" style={{ color: 'var(--bsos-status-syncing-fg)' }}>
                        <Warning size={13} aria-hidden="true" />
                        {t('join.pendingExpired', { time: fmtDateTimeShort(tk.expiresAt) })}
                      </span>
                    </div>
                  </td>
                ) : (
                  <td className="whitespace-nowrap px-4 py-3 text-xs text-ink-3">
                    <div className="row-cell">
                      {t('join.pendingSub', { time: fmtDateTimeShort(tk.expiresAt) })}
                      {tk.allowedSources.length ? ` · ${fmtSources(tk.allowedSources)}` : ''}
                    </div>
                  </td>
                )}
                <td className="whitespace-nowrap px-2 text-right text-ink-3">
                  <div className="row-cell">
                    <span className="inline-flex items-center gap-2">
                      {/* Not needed anymore: revoke the code and drop this registration (clicking this does not go to node registration) */}
                      {onRemove && <RowAction danger onClick={(e) => { e.stopPropagation(); onRemove(tk, expired); }}>{t(expired ? 'join.remove' : 'join.revoke')}</RowAction>}
                      {redo && <CaretRight size={14} aria-hidden="true" className="opacity-0 transition-opacity group-hover:opacity-100" />}
                    </span>
                  </div>
                </td>
              </tr>
              );
            })}
            {nodeRows.map(({ key, item: { n, issue }, state }) => {
              const to = `/nodes/${encodeURIComponent(n.id)}/settings`;
              return (
                <tr key={key} onClick={() => nav(to)} ref={presenceRef(state)} className={`group cursor-pointer border-b border-hair transition-colors last:border-0 hover:bg-elevated ${n.id === highlight ? 'flash-hold' : ''}`}>
                  <td className="px-4 py-3">
                    <div className="row-cell">
                      <span className="inline-flex flex-wrap items-center gap-2">
                        <Link to={to} onClick={(e) => e.stopPropagation()} className="rounded-sm text-ink focus:outline-none focus-visible:ring-1 focus-visible:ring-accent">
                          <NodeLabel id={n.id} name={n.name} />
                        </Link>
                        {n.isValidator && <ValidatorTag />}
                        {n.peerMode === 'custom' && <span className="rounded-md bg-elevated px-1.5 py-0.5 text-2xs text-ink-2">{t('peerSource.badge')}</span>}
                      </span>
                    </div>
                  </td>
                  {/* Node address (0x...): truncated at both ends, full value on hover, copy button beside it (same as overall chain health); shows — until read */}
                  <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-ink-2" onClick={(e) => e.stopPropagation()}>
                    <div className="row-cell">
                      {n.address ? <span className="inline-flex items-center gap-1" title={n.address}>{fmtAddr(n.address)}<CopyButton value={n.address} variant="cell" /></span> : <span className="text-ink-3">—</span>}
                    </div>
                  </td>
                  <td className="whitespace-nowrap px-4 py-3">
                    <div className="row-cell"><NodeStatusBadge node={n} /></div>
                  </td>
                  <td className="whitespace-nowrap px-4 py-3 text-xs tabular-nums text-ink-2">
                    <div className="row-cell">{n.lastSeen ? fmtDateTimeShort(n.lastSeen) : <span className="text-ink-3">—</span>}</div>
                  </td>
                  <td className="px-4 py-3 text-xs">
                    <div className="row-cell">
                      {issue ? (
                        <span className="inline-flex items-center gap-1.5 font-medium" style={{ color: `var(--bsos-status-${issue.tone}-fg)` }}>
                          <Warning size={13} aria-hidden="true" />
                          {issue.text}
                        </span>
                      ) : (
                        <span className="text-ink-3">—</span>
                      )}
                    </div>
                  </td>
                  <td className="px-2 text-ink-3">
                    <div className="row-cell"><CaretRight size={14} aria-hidden="true" className="opacity-0 transition-opacity group-hover:opacity-100" /></div>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}
