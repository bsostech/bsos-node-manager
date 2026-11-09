import { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Plus } from '@phosphor-icons/react';
import { api } from '@/api/client';
import type { AlertLocale, AlertSettings, AlertWebhookUrl, SlackSettings, SmtpSecurity, SmtpSettings, TelegramSettings } from '@/api/schemas';
import { Field, inputCls } from '@/components/Field';
import { Listbox } from '@/components/Listbox';
import { NOTIFY_LANGUAGES, timeZones } from '@/lib/notifyLocale';
import { Button } from '@/components/Button';
import { ConnTestModal } from '@/components/ConnTestModal';
import { ConfirmModal } from '@/components/ConfirmModal';
import { LEAVE_WITHOUT_ASKING, useUnsavedGuard } from '@/hooks/useUnsavedGuard';
import { errMsg } from '@/lib/errors';
import { useAuth } from '@/auth/AuthContext';
import { BackLink } from '@/components/TextLinks';
import { Pill } from '@/components/Pill';
import { SaveButton, SavedNote } from '@/components/SaveButton';
import { presenceRef, usePresence } from '@/hooks/usePresence';

/**
 * Second level of notification channels: one form page per channel. The first level (/alerts?tab=notify) only lists categories + current summary.
 *
 * Why one page per channel instead of all on one page: flattened, the page had a single save button, while the backend used to overwrite everything +
 * require the SMTP password, meaning "adding a webhook" also required retyping the SMTP password. Once the endpoints split into
 * /alert-settings/smtp and /alert-settings/webhooks, each side saves independently.
 */

export const NOTIFY_QK = ['alertSettings'] as const;

const emptySmtp: SmtpSettings = { host: '', port: 587, security: 'none', username: '', password: '', from: '', to: [] };

/** Port 465 is implicit TLS, so picking it selects TLS; leaving 465 while TLS is selected goes back to None (the default). */
function withPort(s: SmtpSettings, port: number): SmtpSettings {
  if (port === 465) return { ...s, port, security: 'tls' };
  if (s.port === 465 && s.security === 'tls') return { ...s, port, security: 'none' };
  return { ...s, port };
}

/** Shell shared by both pages: back to the upper level + title + description. Back goes to the category list, not the browser's previous page. */
function NotifyShell({ title, desc, howTo, children }: { title: string; desc: string; /** how to obtain it: placed under the title with the description, not inside the form card */ howTo?: string; children: React.ReactNode }) {
  const { t } = useTranslation();
  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-4">
      <BackLink to="/alerts?tab=notify">{t('alertSettings.back')}</BackLink>
      <div className="flex flex-col gap-0.5">
        <h1 className="text-lg font-semibold text-ink">{title}</h1>
        <span className="text-xs text-ink-3">{desc}</span>
        {/* How-to-obtain gets its own paragraph, led by a small blue tag (like "Condition / Likely cause" in alert notifications) */}
        {howTo && <p className="mt-2 flex items-start gap-2 text-xs leading-relaxed text-ink-3"><Pill tone="info" className="shrink-0">{t('alertSettings.howToLabel')}</Pill><span>{howTo}</span></p>}
      </div>
      {children}
    </div>
  );
}

/** After a successful save, overwrite the cache with the response: all three write endpoints return the full updated settings, saving a refetch. */
function useNotifyMutation(fn: () => Promise<AlertSettings>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: (data) => qc.setQueryData(NOTIFY_QK, data),
  });
}

// ── Email(SMTP)───────────────────────────────────────────────

export function AlertNotifyEmail() {
  const { t } = useTranslation();
  const nav = useNavigate();
  const { can } = useAuth();
  const canWrite = can('nodes:onboard');
  const { data: settings } = useQuery({ queryKey: NOTIFY_QK, queryFn: () => api.getAlertSettings() });

  const [smtp, setSmtp] = useState<SmtpSettings>(emptySmtp);
  const [toText, setToText] = useState('');
  const configured = !!settings?.smtp;

  useEffect(() => {
    if (!settings?.smtp) return;
    // The password always loads empty: GET never returns it, and the backend doesn't accept "leave blank to keep".
    setSmtp({ ...emptySmtp, ...settings.smtp, password: '' });
    setToText(settings.smtp.to.join(', '));
  }, [settings]);

  const toList = () => toText.split(',').map((s) => s.trim()).filter(Boolean);
  const save = useNotifyMutation(() => api.putAlertSettingsSmtp({ ...smtp, to: toList() }));
  const disable = useNotifyMutation(() => api.deleteAlertSettingsSmtp());

  const valid = !!(smtp.host.trim() && smtp.from.trim() && (!smtp.username || smtp.password) && toText.trim());

  const [showTest, setShowTest] = useState(false);
  const test = useMutation({ mutationFn: () => api.testAlertSettingsSmtp({ ...smtp, to: toList() }) });
  const runTest = () => {
    setShowTest(true);
    test.reset();
    test.mutate();
  };

  const [confirmDisable, setConfirmDisable] = useState(false);

  const baseline = JSON.stringify(
    settings?.smtp
      ? { smtp: { ...emptySmtp, ...settings.smtp, password: '' }, toText: settings.smtp.to.join(', ') }
      : { smtp: emptySmtp, toText: '' },
  );
  useUnsavedGuard(baseline !== JSON.stringify({ smtp, toText }));

  return (
    <NotifyShell title={t('alertSettings.smtpTitle')} desc={t('alertSettings.smtpDesc')}>
      <fieldset disabled={!canWrite} className="flex flex-col gap-4">
        {/* Two sections: which SMTP server to connect to, and who to send to */}
        <section className="overflow-hidden rounded-lg border border-hair bg-surface">
          <h2 className="flex min-h-[54px] items-center gap-2 border-b border-hair px-4 py-2 text-sm font-semibold text-ink">{t('alertSettings.smtpServerSection')}</h2>
          <div className="grid gap-3 px-4 py-3 md:grid-cols-2">
            <Field label={t('alertSettings.smtpHost')} value={smtp.host} onChange={(v) => setSmtp({ ...smtp, host: v })} placeholder="smtp.org-a.internal" />
            <Field label={t('alertSettings.smtpPort')} type="number" value={String(smtp.port)} onChange={(v) => setSmtp(withPort(smtp, Number(v) || 0))} />
            <div className="flex flex-col gap-1">
              <label className="flex flex-col gap-1 text-xs text-ink-3">
                {t('alertSettings.smtpSecurity')}
                <Listbox<SmtpSecurity> width="w-full" aria-label={t('alertSettings.smtpSecurity')} value={smtp.security ?? 'none'}
                  options={[{ value: 'none', label: t('alertSettings.smtpSecurityNone') }, { value: 'starttls', label: 'STARTTLS' }, { value: 'tls', label: 'TLS' }]}
                  onChange={(security) => setSmtp({ ...smtp, security })} disabled={!canWrite} />
              </label>
              <p className="text-2xs text-ink-3">{t(settings?.customCa ? 'alertSettings.smtpSecurityHintCa' : 'alertSettings.smtpSecurityHint')}</p>
            </div>
            <Field label={t('alertSettings.smtpUsername')} value={smtp.username ?? ''} onChange={(v) => setSmtp({ ...smtp, username: v })} />
            <Field
              label={t('alertSettings.smtpPassword')}
              type="password"
              autoComplete="new-password"
              value={smtp.password ?? ''}
              onChange={(v) => setSmtp({ ...smtp, password: v })}
            />
          </div>
        </section>
        <section className="overflow-hidden rounded-lg border border-hair bg-surface">
          <h2 className="flex min-h-[54px] items-center gap-2 border-b border-hair px-4 py-2 text-sm font-semibold text-ink">{t('alertSettings.smtpMailSection')}</h2>
          <div className="grid gap-3 px-4 py-3 md:grid-cols-2">
            <Field label={t('alertSettings.smtpFrom')} value={smtp.from} onChange={(v) => setSmtp({ ...smtp, from: v })} placeholder="alerts@org-a.example" />
            <Field label={t('alertSettings.smtpTo')} value={toText} onChange={setToText} placeholder="ops@org-a.example, oncall@org-a.example" />
          </div>
        </section>

        <div className="flex items-center justify-end gap-3">
          {save.isError && (
            <p role="alert" className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>
              {errMsg(save.error) || t('alertSettings.saveError')}
            </p>
          )}
          {configured && (
            <Button variant="dangerOutline" onClick={() => setConfirmDisable(true)} className="mr-auto">
              {t('alertSettings.disableEmail')}
            </Button>
          )}
          {/* "Saved" sits left of the test button so neither button moves when it appears / disappears */}
          <SavedNote at={save.isSuccess ? save.submittedAt : 0} />
          <Button variant="secondary" onClick={runTest} disabled={!valid}>
            {t('alertSettings.testSmtp')}
          </Button>
          <SaveButton onClick={() => save.mutate()} pending={save.isPending} disabled={!valid} label={t('alertSettings.save')} pendingLabel={t('alertSettings.saving')} />
        </div>
      </fieldset>

      {showTest && (
        <ConnTestModal
          title={t('alertSettings.testSmtpTitle')}
          runningText={t('alertSettings.testSmtpRunning')}
          okText={t(smtp.username ? 'alertSettings.testSmtpOk' : 'alertSettings.testSmtpOkNoAuth')}
          failText={t('alertSettings.testSmtpFail')}
          test={test}
          onRetry={runTest}
          onClose={() => setShowTest(false)}
        />
      )}
      {confirmDisable && (
        <ConfirmModal
          title={t('alertSettings.disableEmail')}
          body={t('alertSettings.disableEmailBody')}
          confirmLabel={t('alertSettings.disableEmail')}
          danger
          onConfirm={() => {
            setConfirmDisable(false);
            disable.mutate(undefined, { onSuccess: () => nav('/alerts?tab=notify', LEAVE_WITHOUT_ASKING) });
          }}
          onCancel={() => setConfirmDisable(false)}
        />
      )}
    </NotifyShell>
  );
}

/** Footer shared by the Telegram and Slack pages: error, disable, test, save, plus the test and disable dialogs. */
function NotifyActions({ valid, configured, save, disable, test, runTest, showTest, setShowTest, k }: {
  valid: boolean;
  configured: boolean;
  save: ReturnType<typeof useNotifyMutation>;
  disable: ReturnType<typeof useNotifyMutation>;
  test: React.ComponentProps<typeof ConnTestModal>['test'];
  runTest: () => void;
  showTest: boolean;
  setShowTest: (v: boolean) => void;
  k: 'telegram' | 'slack';
}) {
  const { t } = useTranslation();
  const nav = useNavigate();
  const [confirmDisable, setConfirmDisable] = useState(false);
  return (
    <>
      <div className="flex items-center justify-end gap-3">
        {save.isError && (
          <p role="alert" className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>
            {errMsg(save.error) || t('alertSettings.saveError')}
          </p>
        )}
        {configured && (
          <Button variant="dangerOutline" onClick={() => setConfirmDisable(true)} className="mr-auto">
            {t(`alertSettings.${k}Disable`)}
          </Button>
        )}
        {/* "Saved" sits left of the test button so neither button moves when it appears / disappears */}
        <SavedNote at={save.isSuccess ? save.submittedAt : 0} />
        <Button variant="secondary" onClick={runTest} disabled={!valid}>
          {t('alertSettings.testSend')}
        </Button>
        <SaveButton onClick={() => save.mutate()} pending={save.isPending} disabled={!valid} label={t('alertSettings.save')} pendingLabel={t('alertSettings.saving')} />
      </div>
      {showTest && (
        <ConnTestModal
          title={t('alertSettings.testSendTitle')}
          runningText={t('alertSettings.testSendRunning')}
          okText={t('alertSettings.testSendOk')}
          failText={t('alertSettings.testSendFail')}
          test={test}
          onRetry={runTest}
          onClose={() => setShowTest(false)}
        />
      )}
      {confirmDisable && (
        <ConfirmModal
          title={t(`alertSettings.${k}Disable`)}
          body={t('alertSettings.disableChannelBody')}
          confirmLabel={t(`alertSettings.${k}Disable`)}
          danger
          onConfirm={() => {
            setConfirmDisable(false);
            disable.mutate(undefined, { onSuccess: () => nav('/alerts?tab=notify', LEAVE_WITHOUT_ASKING) });
          }}
          onCancel={() => setConfirmDisable(false)}
        />
      )}
    </>
  );
}

/** Explains how to obtain the settings: a paragraph of grey text below the form. */

// ── Telegram ──────────────────────────────────────────────────

export function AlertNotifyTelegram() {
  const { t } = useTranslation();
  const { can } = useAuth();
  const canWrite = can('nodes:onboard');
  const { data: settings } = useQuery({ queryKey: NOTIFY_QK, queryFn: () => api.getAlertSettings() });

  // chat id is edited as a string (groups are negative, so a lone "-" appears mid-typing)
  const [token, setToken] = useState('');
  const [chat, setChat] = useState('');
  const [thread, setThread] = useState('');
  // Only seed on the first load: later background refetches (e.g. window refocus) must not overwrite what is being edited
  const seeded = useRef(false);
  useEffect(() => {
    if (seeded.current || !settings) return;
    seeded.current = true;
    if (!settings.telegram) return;
    setChat(String(settings.telegram.chatId));
    setThread(settings.telegram.messageThreadId != null ? String(settings.telegram.messageThreadId) : '');
  }, [settings]);

  const body = (): TelegramSettings => ({
    botToken: token.trim(),
    chatId: Number(chat),
    messageThreadId: thread.trim() ? Number(thread) : null,
  });
  const valid = !!token.trim() && /^-?\d+$/.test(chat.trim()) && Number(chat) !== 0 && (!thread.trim() || /^\d+$/.test(thread.trim()));
  // Clear the token field after saving: GET never returns the token, and keeping it would make the page think there are unsaved changes forever
  const save = useNotifyMutation(async () => {
    const r = await api.putAlertSettingsTelegram(body());
    setToken('');
    return r;
  });
  const disable = useNotifyMutation(() => api.deleteAlertSettingsTelegram());
  const [showTest, setShowTest] = useState(false);
  const test = useMutation({ mutationFn: () => api.testAlertSettingsTelegram(body()) });
  const runTest = () => {
    setShowTest(true);
    test.reset();
    test.mutate();
  };

  const tg = settings?.telegram;
  useUnsavedGuard(!!token || chat !== (tg ? String(tg.chatId) : '') || thread !== (tg?.messageThreadId != null ? String(tg.messageThreadId) : ''));

  return (
    <NotifyShell title={t('alertSettings.telegramTitle')} desc={t('alertSettings.telegramDesc')} howTo={t('alertSettings.telegramHowTo')}>
      <fieldset disabled={!canWrite} className="flex flex-col gap-4">
        <section className="overflow-hidden rounded-lg border border-hair bg-surface">
          <div className="grid gap-3 px-4 py-3 md:grid-cols-2">
            <div className="md:col-span-2">
              <Field label={t('alertSettings.telegramToken')} type="password" autoComplete="new-password" value={token} onChange={setToken} placeholder="123456789:AA…" />
            </div>
            <Field label={t('alertSettings.telegramChat')} value={chat} onChange={setChat} placeholder="-1001234567890" />
            <Field label={t('alertSettings.telegramThread')} value={thread} onChange={setThread} />
          </div>
        </section>
        <NotifyActions k="telegram" valid={valid} configured={!!tg} save={save} disable={disable} test={test} runTest={runTest} showTest={showTest} setShowTest={setShowTest} />
      </fieldset>
    </NotifyShell>
  );
}

// ── Slack ─────────────────────────────────────────────────────

export function AlertNotifySlack() {
  const { t } = useTranslation();
  const { can } = useAuth();
  const canWrite = can('nodes:onboard');
  const { data: settings } = useQuery({ queryKey: NOTIFY_QK, queryFn: () => api.getAlertSettings() });

  const [url, setUrl] = useState('');
  const body = (): SlackSettings => ({ webhookUrl: url.trim() });
  const valid = /^https:\/\/hooks\.slack(-gov)?\.com\/\S+$/i.test(url.trim());
  const save = useNotifyMutation(async () => {
    const r = await api.putAlertSettingsSlack(body());
    setUrl('');
    return r;
  });
  const disable = useNotifyMutation(() => api.deleteAlertSettingsSlack());
  const [showTest, setShowTest] = useState(false);
  const test = useMutation({ mutationFn: () => api.testAlertSettingsSlack(body()) });
  const runTest = () => {
    setShowTest(true);
    test.reset();
    test.mutate();
  };
  useUnsavedGuard(!!url);

  return (
    <NotifyShell title={t('alertSettings.slackTitle')} desc={t('alertSettings.slackDesc')} howTo={t('alertSettings.slackHowTo')}>
      <fieldset disabled={!canWrite} className="flex flex-col gap-4">
        <section className="overflow-hidden rounded-lg border border-hair bg-surface">
          <div className="px-4 py-3">
            <Field
              label={t('alertSettings.slackUrl')}
              type="password"
              autoComplete="off"
              value={url}
              onChange={setUrl}
              placeholder={settings?.slack ? t('alertSettings.slackUrlSet') : 'https://hooks.slack.com/services/…'}
            />
          </div>
        </section>
        <NotifyActions k="slack" valid={valid} configured={!!settings?.slack} save={save} disable={disable} test={test} runTest={runTest} showTest={showTest} setShowTest={setShowTest} />
      </fieldset>
    </NotifyShell>
  );
}

// ── Custom webhook ──────────────────────────────────────────────

export function AlertNotifyWebhooks() {
  const { t } = useTranslation();
  const { can } = useAuth();
  const canWrite = can('nodes:onboard');
  const { data: settings } = useQuery({ queryKey: NOTIFY_QK, queryFn: () => api.getAlertSettings() });

  // Each row carries a UI-only id (never sent to the backend): deleting a middle row won't make later rows look like the same row, so add / remove animations line up
  const [rows, setRows] = useState<{ id: number; w: AlertWebhookUrl }[]>([]);
  const nextId = useRef(0);
  const [seeded, setSeeded] = useState(false); // settings loaded and rows laid out; only later adds / removes animate
  const withIds = (l: AlertWebhookUrl[]) => l.map((w) => ({ id: nextId.current++, w }));
  // Re-read after saving: rows with identical content keep their id (otherwise every row looks deleted and re-added and the whole card flashes); only an unsaved blank row collapses
  useEffect(() => {
    if (!settings) return;
    setRows((cur) => {
      const free = [...cur];
      return settings.webhookUrls.map((w) => {
        const i = free.findIndex((r) => r.w.label === w.label && r.w.url === w.url);
        return i >= 0 ? { id: free.splice(i, 1)[0].id, w } : withIds([w])[0];
      });
    });
    setSeeded(true);
  }, [settings]); // eslint-disable-line react-hooks/exhaustive-deps
  const webhooks = rows.map((r) => r.w);
  // Rows expand and fade in on add, fade out and collapse on removal; no animation when settings first load
  const shown = usePresence(rows, (r) => String(r.id), seeded);

  // Don't submit blank rows: saving a new row before filling it in shouldn't write half an entry (the backend rejects it too; this just cuts the noise).
  const save = useNotifyMutation(() =>
    api.putAlertSettingsWebhooks(webhooks.filter((w) => w.label.trim() && w.url.trim())),
  );

  const [testing, setTesting] = useState<AlertWebhookUrl | null>(null);
  const test = useMutation({ mutationFn: (w: AlertWebhookUrl) => api.testAlertSettingsWebhook(w) });
  const runTest = (w: AlertWebhookUrl) => {
    setTesting(w);
    test.reset();
    test.mutate(w);
  };

  useUnsavedGuard(JSON.stringify(settings?.webhookUrls ?? []) !== JSON.stringify(webhooks));

  const add = () => setRows((l) => [...l, ...withIds([{ label: '', url: '' }])]);
  const update = (id: number, patch: Partial<AlertWebhookUrl>) =>
    setRows((l) => l.map((r) => (r.id === id ? { ...r, w: { ...r.w, ...patch } } : r)));
  const remove = (id: number) => setRows((l) => l.filter((r) => r.id !== id));

  return (
    <NotifyShell title={t('alertSettings.webhookTitle')} desc={t('alertSettings.webhookDesc')}>
      <fieldset disabled={!canWrite} className="flex flex-col gap-4">
        <div className="flex flex-col gap-3 rounded-lg border border-hair bg-surface px-4 py-3">
          {/* One row each: name, URL, test, remove. The grid has fixed column widths so long URLs don't squash the name column.
              Row spacing comes from each row's own vertical padding (not gap): when a deleted row collapses its padding goes with it, so rows below don't jump at the end */}
          <div className="-my-1.5 flex flex-col">
          {shown.map(({ key, item: { id, w }, state }) => (
            <div key={key} ref={presenceRef(state)} className="grid grid-cols-1 items-center gap-2 py-1.5 sm:grid-cols-[minmax(8rem,14rem)_minmax(0,1fr)_auto]">
              <input
                value={w.label}
                onChange={(e) => update(id, { label: e.target.value })}
                placeholder={t('alertSettings.webhookLabel')}
                aria-label={t('alertSettings.webhookLabel')}
                className={inputCls}
              />
              <input
                value={w.url}
                onChange={(e) => update(id, { url: e.target.value })}
                placeholder="https://monitor.org-a.internal/alertmanager"
                aria-label={t('alertSettings.webhookUrl')}
                className={`min-w-0 font-mono ${inputCls}`}
              />
              <span className="flex gap-1">
                <Button onClick={() => runTest(w)} disabled={!w.url.trim()}>{t('alertSettings.webhookTest')}</Button>
                <Button variant="dangerOutline" onClick={() => remove(id)}>{t('alertSettings.webhookRemove')}</Button>
              </span>
            </div>
          ))}
          </div>
          <Button onClick={add} className="inline-flex w-fit items-center gap-1">
            <Plus size={13} aria-hidden="true" />
            {t('alertSettings.webhookAdd')}
          </Button>
        </div>

        <div className="flex items-center justify-end gap-3">
          {save.isError && (
            <p role="alert" className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>
              {errMsg(save.error) || t('alertSettings.saveError')}
            </p>
          )}
          <SavedNote at={save.isSuccess ? save.submittedAt : 0} />
          <SaveButton onClick={() => save.mutate()} pending={save.isPending} label={t('alertSettings.save')} pendingLabel={t('alertSettings.saving')} />
        </div>
      </fieldset>

      {testing && (
        <ConnTestModal
          title={t('alertSettings.testWebhookTitle')}
          runningText={t('alertSettings.testWebhookRunning')}
          okText={t('alertSettings.testWebhookOk')}
          failText={t('alertSettings.testWebhookFail')}
          test={test}
          onRetry={() => runTest(testing)}
          onClose={() => setTesting(null)}
        />
      )}
    </NotifyShell>
  );
}

// ── Language and time zone ───────────────────────────────────────

export function AlertNotifyLocale() {
  const { t } = useTranslation();
  const { can } = useAuth();
  const canWrite = can('nodes:onboard');
  const { data: settings } = useQuery({ queryKey: NOTIFY_QK, queryFn: () => api.getAlertSettings() });

  // timezone '' = follow the platform host; kept as '' so saving only the language never pins a zone
  const [locale, setLocale] = useState<AlertLocale | null>(null);
  // Only seed on the first load: later background refetches must not overwrite what is being edited
  useEffect(() => {
    if (settings && !locale) setLocale({ language: settings.locale.language, timezone: settings.locale.timezone });
  }, [settings, locale]);
  const save = useNotifyMutation(() => api.putAlertSettingsLocale(locale!));
  useUnsavedGuard(!!locale && !!settings && (locale.language !== settings.locale.language || locale.timezone !== settings.locale.timezone));

  // The host option shows the zone it resolves to now; while a fixed zone is stored that is not known, so the label stays generic
  const hostZone = settings && !settings.locale.timezone ? settings.locale.effectiveTimezone : '';
  const zoneOptions = [
    { value: '', label: hostZone ? t('alertSettings.localeHostZoneNow', { zone: hostZone }) : t('alertSettings.localeHostZone') },
    ...timeZones(locale?.timezone ?? '').map((z) => ({ value: z, label: z })),
  ];

  return (
    <NotifyShell title={t('alertSettings.localeTitle')} desc={t('alertSettings.localeDesc')}>
      <fieldset disabled={!canWrite || !locale} className="flex flex-col gap-4">
        <section className="overflow-hidden rounded-lg border border-hair bg-surface">
          <div className="grid gap-3 px-4 py-3 md:grid-cols-2">
            <label className="flex flex-col gap-1 text-xs text-ink-3">
              {t('alertSettings.localeLanguage')}
              <Listbox width="w-full" aria-label={t('alertSettings.localeLanguage')} value={locale?.language ?? 'en'} options={NOTIFY_LANGUAGES}
                onChange={(language) => locale && setLocale({ ...locale, language })} disabled={!canWrite || !locale} />
            </label>
            <label className="flex flex-col gap-1 text-xs text-ink-3">
              {t('alertSettings.localeTimezone')}
              <Listbox width="w-full" aria-label={t('alertSettings.localeTimezone')} value={locale?.timezone ?? ''} options={zoneOptions}
                onChange={(timezone) => locale && setLocale({ ...locale, timezone })} disabled={!canWrite || !locale} />
            </label>
          </div>
        </section>
        <div className="flex items-center justify-end gap-3">
          {save.isError && (
            <p role="alert" className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>
              {errMsg(save.error) || t('alertSettings.saveError')}
            </p>
          )}
          <SavedNote at={save.isSuccess ? save.submittedAt : 0} />
          <SaveButton onClick={() => save.mutate()} pending={save.isPending} disabled={!locale} label={t('alertSettings.save')} pendingLabel={t('alertSettings.saving')} />
        </div>
      </fieldset>
    </NotifyShell>
  );
}
