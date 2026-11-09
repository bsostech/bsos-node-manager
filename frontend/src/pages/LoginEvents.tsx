import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import type { LoginEvent } from '@/api/schemas';
import { Card } from '@/components/Card';
import { HelpTip } from '@/components/HelpTip';
import { Listbox } from '@/components/Listbox';
import { Pill } from '@/components/Pill';
import { ErrorState, Notice, SkeletonRows } from '@/components/States';
import { CountChip } from '@/components/ValueChip';
import { fmtDateTimeSec } from '@/lib/format';

type Range = '24h' | '7d' | '30d';
const RANGE_MS: Record<Range, number> = { '24h': 86_400_000, '7d': 7 * 86_400_000, '30d': 30 * 86_400_000 };

/**
 * Login history (admin): who logged in when, success or failure, from which IP.
 * Only the act of logging in, not auditing (change history of what admins did belongs to Enterprise auditing).
 * Failures for nonexistent accounts don't record what was typed: people sometimes type their password into the username field.
 */
export function LoginEvents() {
  const { t } = useTranslation();
  const [range, setRange] = useState<Range>('7d');
  const [user, setUser] = useState('');
  const [result, setResult] = useState<'' | 'success' | 'failure'>('');
  const usersQ = useQuery({ queryKey: ['localUsers'], queryFn: api.listLocalUsers });
  // since is computed at fetch time, so it moves forward on range change or refresh. The unfiltered list feeds the dropdown counts
  const since = () => new Date(Date.now() - RANGE_MS[range]).toISOString();
  const q = useQuery({ queryKey: ['loginEvents', range], queryFn: () => api.listLoginEvents({ since: since() }) });
  // With a filter the rows come from the backend: the unfiltered list stops at the newest 500 entries and would miss older matches
  const filtered = !!(user || result);
  const fq = useQuery({
    queryKey: ['loginEvents', range, user, result],
    queryFn: () => api.listLoginEvents({ since: since(), username: user || undefined, result: result || undefined }),
    enabled: filtered,
  });
  const events = q.data?.events ?? [];
  const userOk = (e: LoginEvent, u: string) => !u || e.username === u;
  const resultOk = (e: LoginEvent, r: typeof result) => !r || e.result === r;
  const shown = filtered ? fq : q;
  const rows = shown.data?.events ?? [];
  const truncated = shown.data?.truncated;
  const failed = q.isError || shown.isError;
  const all = t('loginEvents.all');
  // Each dropdown option shows a count, computed under the other dropdown's current choice; a truncated list would undercount, so then the counts are left out
  const counted = (label: string, n: number) => (q.data?.truncated ? label : t('common.withCount', { label, n }));
  const userOpt = (value: string, label: string) => ({ value, label: counted(label, events.filter((e) => userOk(e, value) && resultOk(e, result)).length) });
  const resultOpt = (value: typeof result, label: string) => ({ value, label: counted(label, events.filter((e) => userOk(e, user) && resultOk(e, value)).length) });
  // Local accounts plus any account name seen in the history (LDAP / OIDC sign-ins have no local account)
  const accounts = [...new Set([...(usersQ.data ?? []).map((u) => u.username), ...events.flatMap((e) => (e.username ? [e.username] : []))])];

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-4">
      <div className="flex flex-col gap-0.5">
        <h1 className="text-lg font-semibold text-ink">{t('loginEvents.title')}</h1>
        <span className="whitespace-nowrap text-xs text-ink-3">{t('loginEvents.subtitle')}</span>
      </div>

      <Card
        title={<>{t('loginEvents.listTitle')}<HelpTip title={t('loginEvents.listTitle')}>{t('loginEvents.help')}</HelpTip></>}
        aside={(
          <>
            <span className="whitespace-nowrap text-xs text-ink-3">{t('loginEvents.filterUser')}</span>
            <Listbox width="w-auto" aria-label={t('loginEvents.filterUser')} value={user} onChange={setUser}
              options={[userOpt('', all), ...accounts.map((u) => userOpt(u, u))]} />
            <span className="whitespace-nowrap text-xs text-ink-3">{t('loginEvents.filterResult')}</span>
            <Listbox width="w-auto" aria-label={t('loginEvents.filterResult')} value={result} onChange={setResult}
              options={[resultOpt('', all), resultOpt('success', t('loginEvents.success')), resultOpt('failure', t('loginEvents.failure'))]} />
            <span className="whitespace-nowrap text-xs text-ink-3">{t('loginEvents.range')}</span>
            <Listbox width="w-28" aria-label={t('loginEvents.range')} value={range} onChange={setRange}
              options={(['24h', '7d', '30d'] as const).map((r) => ({ value: r, label: t(`loginEvents.range_${r}`) }))} />
            {q.data && <CountChip unit={t('loginEvents.unit')}>{rows.length}</CountChip>}
          </>
        )}
        bodyClass=""
      >
        {truncated && <div className="px-4 pt-3"><Notice>{t('loginEvents.truncated')}</Notice></div>}
        {failed ? <ErrorState inCard onRetry={() => { void q.refetch(); if (filtered) void fq.refetch(); }} />
          : shown.isLoading ? <div className="p-3"><SkeletonRows rows={5} /></div>
          : rows.length === 0 ? <p className="px-4 py-8 text-center text-xs text-ink-3">{t('loginEvents.empty')}</p>
          : (
            // No separate height or scrollbar: the page only scrolls with the browser's outer scrollbar
            <div>
              <table className="w-full text-xs">
                <thead>
                  <tr className="table-head">
                    <th className="whitespace-nowrap px-4">{t('loginEvents.colTime')}</th>
                    <th className="whitespace-nowrap px-4">{t('loginEvents.colUser')}</th>
                    <th className="whitespace-nowrap px-4">{t('loginEvents.colResult')}</th>
                    <th className="px-4">{t('loginEvents.colReason')}</th>
                    <th className="whitespace-nowrap px-4">{t('loginEvents.colIp')}</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((e) => <Row key={e.id} e={e} />)}
                </tbody>
              </table>
            </div>
          )}
      </Card>
    </div>
  );
}

function Row({ e }: { e: LoginEvent }) {
  const { t } = useTranslation();
  const ok = e.result === 'success';
  return (
    <tr className="border-b border-hair last:border-0">
      <td className="whitespace-nowrap px-4 py-2.5 tabular-nums text-ink-2">{fmtDateTimeSec(e.at)}</td>
      <td className="whitespace-nowrap px-4 py-2.5">{e.username ? <span className="font-mono text-ink">{e.username}</span> : <span className="text-ink-3">{t('loginEvents.unknownUser')}</span>}</td>
      <td className="whitespace-nowrap px-4 py-2.5"><Pill tone={ok ? 'ok' : 'err'} className="-ml-1.5">{t(ok ? 'loginEvents.success' : 'loginEvents.failure')}</Pill></td>
      <td className="px-4 py-2.5 text-ink-2">{e.reason ? t(`loginEvents.reason.${e.reason}`) : <span className="text-ink-3">—</span>}</td>
      <td className="whitespace-nowrap px-4 py-2.5 font-mono text-ink-2">{e.sourceIp}</td>
    </tr>
  );
}
