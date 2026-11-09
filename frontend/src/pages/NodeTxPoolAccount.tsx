import { useLocation, useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { api, HttpError } from '@/api/client';
import type { NodeTxPool } from '@/api/schemas';
import { CopyButton } from '@/components/CopyButton';
import { SkeletonRows, EmptyState, ErrorState } from '@/components/States';
import { Band, Box, LaneDetail, POLL_MS, txpoolErrMsg } from '@/components/NodeTxPoolPanel';
import { fmtTime } from '@/lib/format';
import { analyzeLane, fmtAge, fmtRanges, isAddress, sourceKey } from '@/lib/txpool';
import { BackLink } from '@/components/TextLinks';
import { Pill } from '@/components/Pill';

/**
 * A single account in the tx pool: summary + nonce track + transaction details. Only requests ?from=<address>,
 * so the node returns just this account and the pool-wide account cap doesn't apply; the response still flags truncated when one account has too many transactions.
 */
export function NodeTxPoolAccount() {
  const { t } = useTranslation();
  const { id = '', address: raw = '' } = useParams();
  const address = raw.toLowerCase();
  const valid = isAddress(address);
  const q = useQuery<NodeTxPool, HttpError>({
    queryKey: ['txpool', id, address],
    queryFn: () => api.getNodeTxPool(id, address),
    enabled: valid,
    refetchInterval: POLL_MS,
    retry: false,
  });
  const now = Date.now();
  const sender = q.data?.senders.find((s) => s.address === address);

  // Same order of checks as the tx pool tab: unreadable -> syncing -> empty
  const body = !valid ? <EmptyState message={t('txpool.badAddress')} />
    : q.isLoading ? <div className="p-4"><SkeletonRows rows={2} /></div>
    : q.isError ? (q.error.status === 404 ? <EmptyState message={t('txpool.nodeNotFound')} />
      : q.error.status === 502 ? <EmptyState message={txpoolErrMsg(q.error)} />
      : <ErrorState onRetry={() => void q.refetch()} />)
    : q.data?.syncing ? <EmptyState message={t('txpool.syncing')} />
    : !sender ? <EmptyState message={t('txpool.accountEmpty')} />
    : null;

  const lane = sender && analyzeLane(sender, now);
  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-4">
      <BackToPool nodeId={id} />
      <header className="flex flex-wrap items-end gap-x-3 gap-y-1">
        {/* A small line above the address says who it is: this page shows this sender account's transactions left in the tx pool */}
        <div className="flex min-w-0 flex-col gap-0.5">
          <span className="text-xs text-ink-3">{t('txpool.accountKicker')}</span>
          <h1 className="inline-flex min-w-0 items-center gap-2 break-all font-mono text-lg font-semibold text-ink">
            {valid ? address : raw} {valid && <CopyButton value={address} variant="cell" />}
          </h1>
        </div>
        {q.data?.truncated && <Pill tone="warn">{t('txpool.accountTruncated')}</Pill>}
        {q.data && <span className="ml-auto text-xs text-ink-3">{t('txpool.snapshot')} <span className="font-mono tabular-nums text-ink-2">{fmtTime(q.data.asOf)}</span></span>}
      </header>
      {body ? <Box>{body}</Box> : lane && (
        <>
          <Band cells={[
            { label: t('txpool.colTxs'), value: lane.sender.txs.length },
            // pending / queued are Besu's terms (the status column below uses them too); the ⓘ explains in plain words and maps to the track legend
            { label: 'pending', value: lane.ready.length, help: t('txpool.acctHelpPending') },
            // queued is orange (same as "Waiting" on the track and the queued pill in the table); only gaps are red (same as the track's red dashed line)
            { label: 'queued', value: <span style={lane.blocked.length > 0 ? { color: 'var(--bsos-status-syncing-fg)' } : undefined}>{lane.blocked.length}</span>, help: t('txpool.acctHelpQueued') },
            { label: t('txpool.colMissing'), value: lane.missing.length ? fmtRanges(lane.missing) : '—', loud: lane.missing.length > 0, help: t('txpool.helpMissing') },
            { label: t('txpool.colAge'), value: fmtAge(now - lane.since), help: t('txpool.helpAge') },
            { label: t('txpool.colSource'), value: <span className="font-sans text-base">{t(sourceKey(lane))}</span>, help: t('txpool.helpSource') },
          ]} />
          <LaneDetail lane={lane} />
        </>
      )}
    </div>
  );
}

/** Back: coming from the account table, go back one page (filters, search and page are in the URL, so they return intact); a direct link has no previous page, so go to the tx pool tab */
function BackToPool({ nodeId }: { nodeId: string }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const back = () => (location.key === 'default' ? navigate(`/nodes/${encodeURIComponent(nodeId)}?v=txpool`) : navigate(-1));
  return (
    <BackLink onClick={back}>{t('txpool.back')}</BackLink>
  );
}
