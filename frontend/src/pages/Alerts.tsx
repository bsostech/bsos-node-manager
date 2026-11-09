import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ClockCounterClockwise, Plus, SlidersHorizontal, EnvelopeSimple, Plugs, CaretRight, MagnifyingGlass, ArrowCounterClockwise, SlackLogo, TelegramLogo, Globe } from '@phosphor-icons/react';
import { api } from '@/api/client';
import type { AlertRule } from '@/api/schemas';
import { SkeletonRows, ErrorState } from '@/components/States';
import { Card } from '@/components/Card';
import { Listbox } from '@/components/Listbox';
import { CountChip } from '@/components/ValueChip';
import { Pill } from '@/components/Pill';
import { useUrlParam } from '@/hooks/useUrlParam';
import { useAuth } from '@/auth/AuthContext';
import { ConfirmModal } from '@/components/ConfirmModal';
import { Button, ButtonLink } from '@/components/Button';
import { errMsg } from '@/lib/errors';
import { forPhrase, ruleCondition, ruleTitle } from '@/lib/alertRules';
import { LockedTab } from '@/components/PageTabs';
import { isEnterprise } from '@/edition';
import { notifyLanguageLabel } from '@/lib/notifyLocale';

type TabId = 'rules' | 'notify';
type StatusTab = 'all' | 'on' | 'off';
const STATUS_TABS: StatusTab[] = ['all', 'on', 'off'];

/**
 * Alerts home: one rule table + notification channels. Clicking a rule opens its settings page (/alerts/rules/:id); firing status and history are on the alert notifications page.
 * Default and user-added rules are listed together, not split into built-in/custom.
 */
export function Alerts() {
  const { t } = useTranslation();
  // tab goes into the URL: kept across reload / share / back
  const [tab, setTab] = useUrlParam<TabId>('tab', 'rules', (v) => v === 'notify');
  const qc = useQueryClient();
  const { can } = useAuth();
  const canWrite = can('nodes:onboard');
  const [restoring, setRestoring] = useState(false);
  const restore = useMutation({
    mutationFn: () => api.restoreDefaultAlertRules(),
    onSuccess: (rules) => {
      qc.setQueryData(['alertRules'], rules);
      void qc.invalidateQueries({ queryKey: ['alerts'] }); // a restored rule may be firing right now
      setRestoring(false);
    },
  });

  const tabs: { id: TabId; label: string; icon: typeof SlidersHorizontal }[] = [
    { id: 'rules', label: t('alerts.tabRules'), icon: SlidersHorizontal },
    { id: 'notify', label: t('alerts.tabNotify'), icon: EnvelopeSimple },
  ];

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-4">
      <header className="flex flex-wrap items-center gap-3">
        <h1 className="text-lg font-semibold text-ink">{t('nav.alertSettings')}</h1>
        {canWrite && tab === 'rules' && (
          <Button onClick={() => { restore.reset(); setRestoring(true); }} className="ml-auto inline-flex shrink-0 items-center gap-1.5">
            <ArrowCounterClockwise size={15} aria-hidden />
            {t('alerts.restoreDefaults')}
          </Button>
        )}
        {canWrite && tab === 'rules' && (
          <ButtonLink to="/alerts/rules/new" className="shrink-0 gap-1.5">
            <Plus size={15} aria-hidden />
            {t('alerts.rules.add')}
          </ButtonLink>
        )}
      </header>

      <div role="tablist" className="flex gap-1 border-b border-hair">
        {tabs.map((tb) => {
          const active = tab === tb.id;
          return (
            <button
              key={tb.id}
              role="tab"
              aria-selected={active}
              onClick={() => setTab(tb.id)}
              className={`-mb-px flex items-center gap-1.5 border-b-2 px-3 py-2 text-sm transition-colors focus:outline-none focus-visible:ring-1 focus-visible:ring-accent ${
                active ? 'border-accent text-accent font-medium' : 'border-transparent text-ink-2 hover:text-ink'
              }`}
            >
              <tb.icon size={16} weight={active ? 'fill' : 'regular'} aria-hidden />
              {tb.label}
            </button>
          );
        })}
        {/* Core: who changed which rule or channel is part of Enterprise auditing; a locked tab that opens an explanation when clicked */}
        {!isEnterprise && <LockedTab tab={{ label: t('edition.config-history.tab'), icon: ClockCounterClockwise }} slug="config-history" />}
      </div>

      {tab === 'rules' ? <RuleTable /> : <AlertNotifySettings />}
      {restoring && (
        <ConfirmModal
          title={t('alerts.restoreDefaults')}
          body={restore.isError ? errMsg(restore.error) || t('alerts.rules.saveError') : t('alerts.restoreBody')}
          confirmLabel={restore.isPending ? t('alerts.rules.saving') : t('alerts.restoreDefaults')}
          busy={restore.isPending}
          onConfirm={() => restore.mutate()}
          onCancel={() => setRestoring(false)}
        />
      )}
    </div>
  );
}

function RuleTable() {
  const { t } = useTranslation();
  const nav = useNavigate();
  // Viewers only look: rule rows don't open the edit page (condition, severity and state are all visible in the row)
  const canWrite = useAuth().can('nodes:onboard');
  const rules = useQuery<AlertRule[]>({ queryKey: ['alertRules'], queryFn: () => api.listAlertRules() });
  // This is a settings page: it only shows whether a rule is on or disabled; firing status is in alert notifications
  const [picked, setPicked] = useUrlParam<StatusTab>('st', 'all', (v) => (STATUS_TABS as string[]).includes(v));
  const [q, setQ] = useState('');

  if (rules.isLoading) return <SkeletonRows rows={4} />;
  if (rules.isError) return <ErrorState onRetry={() => void rules.refetch()} />;

  const rows = (rules.data ?? []).map((r) => ({ r, state: (r.enabled ? 'on' : 'off') as 'on' | 'off' }));
  const count = (s: StatusTab) => (s === 'all' ? rows.length : rows.filter((x) => x.state === s).length);
  const st = picked;
  const needle = q.trim().toLowerCase();
  const shown = rows
    .filter((x) => st === 'all' || x.state === st)
    .filter((x) => !needle || `${ruleTitle(t, x.r)} ${ruleCondition(t, x.r)}`.toLowerCase().includes(needle))
    .sort((a, b) => ruleTitle(t, a.r).localeCompare(ruleTitle(t, b.r)));

  // Right side of the title row: search -> status dropdown -> count, same order as other cards
  const aside = (
    <div className="flex flex-wrap items-center justify-end gap-3">
      <label className="flex h-8 w-52 items-center gap-2 rounded-md border border-hair bg-canvas px-2.5 text-ink-3 focus-within:ring-1 focus-within:ring-accent">
        <MagnifyingGlass size={13} aria-hidden />
        <input type="search" value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('alerts.search')} aria-label={t('alerts.search')}
          className="w-full bg-transparent text-xs text-ink outline-none placeholder:text-ink-3" />
      </label>
      <label className="flex items-center gap-2">
        {t('alerts.statusShow')}
        <Listbox width="w-auto" aria-label={t('alerts.statusShow')} value={st} onChange={(v) => setPicked(v)}
          options={STATUS_TABS.map((s) => ({ value: s, label: t('alerts.stateCount', { label: t(`alerts.state.${s}`), n: count(s) }) }))} />
      </label>
      <CountChip unit={t('alerts.unitRule')}>{shown.length}</CountChip>
    </div>
  );
  return (
    <Card title={<span className="whitespace-nowrap">{t('alerts.rulesTitle')}</span>} aside={aside} bodyClass="flex flex-col">
      <div className="overflow-x-auto">
        <table className="w-full text-xs">
          <thead>
            <tr className="table-head">
              <th className="px-4">{t('alerts.colRule')}</th>
              <th className="whitespace-nowrap px-4">{t('alerts.rules.colSeverity')}</th>
              <th className="whitespace-nowrap px-4">{t('alerts.colStatus')}</th>
              <th className="w-8" />
            </tr>
          </thead>
          <tbody>
            {shown.length === 0 ? (
              <tr>
                <td colSpan={4} className="px-4 py-8 text-center text-xs text-ink-3">{t('alerts.noMatch')}</td>
              </tr>
            ) : shown.map(({ r, state }) => {
              const href = `/alerts/rules/${encodeURIComponent(r.id)}`;
              return (
                <tr key={r.id} onClick={canWrite ? () => nav(href) : undefined} className={`group border-b border-hair last:border-0 ${canWrite ? 'cursor-pointer hover:bg-elevated' : ''}`}>
                  <td className="px-4 py-2.5">
                    {/* Name on one line, condition on another: the condition is what the rule really compares, and the numbers share the page's color source */}
                    <span className="flex flex-col gap-0.5">
                      {canWrite ? (
                        <Link to={href} onClick={(e) => e.stopPropagation()} className={`rounded-sm font-medium focus:outline-none focus-visible:ring-1 focus-visible:ring-accent ${r.enabled ? 'text-ink' : 'text-ink-3'}`}>
                          {ruleTitle(t, r)}
                        </Link>
                      ) : (
                        <span className={`font-medium ${r.enabled ? 'text-ink' : 'text-ink-3'}`}>{ruleTitle(t, r)}</span>
                      )}
                      <span className="text-2xs text-ink-3">{ruleCondition(t, r)}{forPhrase(t, r.forDuration)}</span>
                    </span>
                  </td>
                  <td className="whitespace-nowrap px-4 py-2.5"><Pill tone={r.severity === 'critical' ? 'err' : 'warn'} className="-ml-1.5">{t(`overview.level.${r.severity === 'critical' ? 'err' : 'warn'}`)}</Pill></td>
                  <td className="whitespace-nowrap px-4 py-2.5">
                    {/* Running is faint text; disabled is a grey pill */}
                    {state === 'off' ? <Pill tone="neutral" className="-ml-1.5">{t('alerts.state.off')}</Pill> : <span className="text-ink-3">{t('alerts.state.on')}</span>}
                  </td>
                  <td className="px-2 text-ink-3">
                    {canWrite && <CaretRight size={14} className="opacity-0 group-hover:opacity-100" aria-hidden />}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

/**
 * First level of notification settings: only channel categories + current summary; forms open on click (/alerts/notify/*).
 *
 * Forms aren't expanded at this level: expanded, a disabled channel becomes a big empty box holding one checkbox,
 * and the page looks unfinished; also each channel saves differently (SMTP needs a password, webhooks overwrite the whole set),
 * which is easy to misread under a single save button.
 */
function AlertNotifySettings() {
  const { t } = useTranslation();
  const nav = useNavigate();
  // Viewers only see each channel's status: rows don't open the settings page (it is a form, and holds usable values such as webhook URLs)
  const canWrite = useAuth().can('nodes:onboard');
  const Row = canWrite ? 'button' : 'div';
  const { data: settings, isLoading, isError, refetch } = useQuery({
    queryKey: ['alertSettings'],
    queryFn: () => api.getAlertSettings(),
  });

  if (isLoading) return <SkeletonRows rows={2} />;
  if (isError) return <ErrorState onRetry={() => void refetch()} />;

  const webhookCount = settings?.webhookUrls.length ?? 0;
  const channels = [
    {
      to: '/alerts/notify/email',
      icon: EnvelopeSimple,
      title: t('alertSettings.smtpTitle'),
      desc: t('alertSettings.smtpSummary'),
      // The summary shows "what state it is in now" so this level can be scanned without clicking in.
      status: settings?.smtp ? settings.smtp.host : t('alertSettings.statusOff'),
      muted: !settings?.smtp,
    },
    {
      to: '/alerts/notify/telegram',
      icon: TelegramLogo,
      title: t('alertSettings.telegramTitle'),
      desc: t('alertSettings.telegramSummary'),
      status: settings?.telegram ? t('alertSettings.statusOn') : t('alertSettings.statusOff'),
      muted: !settings?.telegram,
    },
    {
      to: '/alerts/notify/slack',
      icon: SlackLogo,
      title: t('alertSettings.slackTitle'),
      desc: t('alertSettings.slackSummary'),
      status: settings?.slack ? t('alertSettings.statusOn') : t('alertSettings.statusOff'),
      muted: !settings?.slack,
    },
    {
      to: '/alerts/notify/webhooks',
      icon: Plugs,
      title: t('alertSettings.webhookTitle'),
      desc: t('alertSettings.webhookSummary'),
      status: webhookCount > 0 ? t('alertSettings.webhookCount', { count: webhookCount }) : t('alertSettings.statusOff'),
      muted: webhookCount === 0,
    },
    {
      to: '/alerts/notify/locale',
      icon: Globe,
      title: t('alertSettings.localeTitle'),
      desc: t('alertSettings.localeSummary'),
      status: settings ? `${notifyLanguageLabel(settings.locale.language)} · ${settings.locale.effectiveTimezone}` : '',
      muted: false,
    },
  ];

  return (
    <div className="overflow-hidden rounded-lg border border-hair bg-surface">
      {channels.map((c) => {
        const Icon = c.icon;
        return (
          <Row
            key={c.to}
            {...(canWrite ? { type: 'button' as const, onClick: () => nav(c.to) } : {})}
            className={`flex w-full items-center gap-4 border-b border-hair px-5 py-4 text-left last:border-0 ${canWrite ? 'transition-colors hover:bg-elevated focus:outline-none focus-visible:ring-1 focus-visible:ring-accent' : ''}`}
          >
            <Icon size={18} className="shrink-0 text-ink-3" aria-hidden />
            {/* Titles use the same size as the "Alert rules" table body (text-xs), set apart from descriptions by weight and color; not the card-title size */}
            <span className="flex min-w-0 flex-col gap-0.5">
              <span className="text-xs font-medium text-ink">{c.title}</span>
              <span className="truncate text-xs text-ink-3">{c.desc}</span>
            </span>
            <span className={`ml-auto shrink-0 text-xs ${c.muted ? 'text-ink-3' : 'text-ink-2'}`}>{c.status}</span>
            {canWrite && <CaretRight size={14} className="shrink-0 text-ink-3" aria-hidden />}
          </Row>
        );
      })}
    </div>
  );
}
