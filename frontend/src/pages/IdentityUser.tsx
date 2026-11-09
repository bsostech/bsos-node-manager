import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Key, Prohibit, Trash } from '@phosphor-icons/react';
import { api } from '@/api/client';
import type { LocalUser } from '@/api/schemas';
import { roleDesc, roleLabel } from '@/auth/permissions';
import { useAuth } from '@/auth/AuthContext';
import { Button } from '@/components/Button';
import { ConfirmModal } from '@/components/ConfirmModal';
import { ShownPasswordModal, UserFormModal } from '@/components/LocalUserModals';
import { EmptyState, ErrorState, Notice, Skeleton } from '@/components/States';
import { errMsg } from '@/lib/errors';
import { fmtDateTimeShort } from '@/lib/format';
import { UserState } from './Identity';
import { BackLink } from '@/components/TextLinks';
import { Card } from '@/components/Card';


function BackToIdentity() {
  const { t } = useTranslation();
  return (
    <BackLink to="/identity">{t('nav.identity')}</BackLink>
  );
}

type Pending = { kind: 'disable' } | { kind: 'enable' } | { kind: 'delete' };

/** A single local user: role (decided at creation, display only), reset password, disable / enable, delete. Changing your own account is never allowed (the backend blocks it too). */
export function IdentityUser() {
  const { t } = useTranslation();
  const { username = '' } = useParams();
  const nav = useNavigate();
  const qc = useQueryClient();
  const { me } = useAuth();
  const q = useQuery({ queryKey: ['localUsers'], queryFn: api.listLocalUsers });
  const [pending, setPending] = useState<Pending | null>(null);
  const [resetting, setResetting] = useState(false);
  const [shown, setShown] = useState<{ username: string; password: string; mailError?: string | null } | null>(null);
  const [sentTo, setSentTo] = useState<string | null>(null); // which email the temporary password went to after a reset
  const u = q.data?.find((x) => x.username === username);
  const invalidate = () => void qc.invalidateQueries({ queryKey: ['localUsers'] });
  const act = useMutation({
    mutationFn: async (p: Pending) => {
      if (p.kind === 'delete') return api.deleteLocalUser(username);
      await api.updateLocalUser(username, { disabled: p.kind === 'disable' });
    },
    onSuccess: (_, p) => {
      setPending(null);
      invalidate();
      if (p.kind === 'delete') nav('/identity');
    },
  });

  if (q.isLoading) return <div className="mx-auto flex w-full max-w-5xl flex-col gap-4"><BackToIdentity /><Skeleton className="h-10 w-72" /></div>;
  if (q.isError || !u) {
    return (
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-4">
        <BackToIdentity />
      {sentTo && (
        <Notice tone="healthy" action={<button type="button" onClick={() => setSentTo(null)} className="shrink-0 rounded px-1.5 hover:bg-surface">{t('common.close')}</button>}>{t('users.resetSent', { email: sentTo })}</Notice>
      )}
        {q.isError ? <ErrorState onRetry={() => void q.refetch()} /> : <EmptyState message={t('users.notFound')} />}
      </div>
    );
  }
  const self = u.username === me?.user.id;
  const open = (p: Pending) => { act.reset(); setPending(p); };

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-4">
      <BackToIdentity />
      <header className="flex flex-wrap items-center gap-3">
        <div className="flex min-w-0 flex-col gap-0.5">
          <h1 className="break-all font-mono text-lg font-semibold text-ink">
            {u.username}{self && <span className="ml-2 font-sans text-xs font-normal text-ink-3">{t('users.you')}</span>}
          </h1>
          {/* Same subtitle as other detail pages: regular font, text-xs (dates don't need monospace) */}
          <span className="text-xs tabular-nums text-ink-3">
            {t('users.created')} {fmtDateTimeShort(u.createdAt)} · {t('users.colLastLogin')} {u.lastLoginAt ? fmtDateTimeShort(u.lastLoginAt) : '—'}
          </span>
        </div>
        <span className="ml-auto"><UserState u={u} /></span>
      </header>

      {/* Card body uses the site-wide text-xs: role name text-ink, what it can do text-ink-3 */}
      <Card title={t('users.colRole')} bodyClass="">
        <div className="flex flex-col items-start gap-1 px-4 py-3">
          <span className="text-xs font-medium text-ink">{roleLabel(t, u.role)}</span>
          <span className="text-xs text-ink-3">{roleDesc(t, u.role)}</span>
        </div>
      </Card>

      {!self && (
        <Card title={t('users.account')} bodyClass="">
          <ActionRow icon={<Key size={15} aria-hidden />} title={t('users.password')} hint={t('users.resetHint')}>
            <Button onClick={() => setResetting(true)}>{t('users.reset')}</Button>
          </ActionRow>
          <ActionRow icon={<Prohibit size={15} aria-hidden />} title={u.disabled ? t('users.disabled') : t('users.disableTitle')} hint={u.disabled ? t('users.disabledHint') : t('users.disableHint')}>
            <Button onClick={() => open({ kind: u.disabled ? 'enable' : 'disable' })}>{u.disabled ? t('users.enable') : t('users.disable')}</Button>
          </ActionRow>
          <ActionRow icon={<Trash size={15} aria-hidden />} title={t('users.deleteTitle')} hint={t('users.deleteHint')}>
            <Button variant="dangerOutline" onClick={() => open({ kind: 'delete' })}>{t('users.delete')}</Button>
          </ActionRow>
        </Card>
      )}

      {pending && <PendingModal u={u} p={pending} busy={act.isPending} error={act.isError ? errMsg(act.error) || t('error.title') : null} onConfirm={() => act.mutate(pending)} onCancel={() => setPending(null)} />}
      {resetting && (
        <UserFormModal
          mode="reset"
          username={u.username}
          userEmail={u.email}
          onClose={() => setResetting(false)}
          onDone={(r) => { setResetting(false); invalidate(); if (r.delivery === 'email') setSentTo(r.email); else if (r.password) setShown({ username: r.username, password: r.password, mailError: r.mailError }); }}
        />
      )}
      {shown && <ShownPasswordModal {...shown} onClose={() => setShown(null)} />}
    </div>
  );
}

function ActionRow({ icon, title, hint, children }: { icon: React.ReactNode; title: string; hint: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-center gap-3 border-b border-hair px-4 py-3 last:border-0">
      <span className="text-ink-2">{icon}</span>
      <span className="text-xs font-medium text-ink">{title}</span>
      <span className="ml-auto flex items-center gap-3">
        <span className="text-xs text-ink-3">{hint}</span>
        {children}
      </span>
    </div>
  );
}

/** Disable and delete both immediately affect the user's logged-in sessions, so confirm before sending. Errors show inside the confirm dialog. */
function PendingModal({ u, p, busy, error, onConfirm, onCancel }: { u: LocalUser; p: Pending; busy: boolean; error: string | null; onConfirm: () => void; onCancel: () => void }) {
  const { t } = useTranslation();
  const user = u.username;
  const m = {
    disable: { title: t('users.disableTitle'), body: t('users.disableBody', { user }), label: t('users.disable'), danger: true },
    enable: { title: t('users.enableTitle'), body: t('users.enableBody', { user }), label: t('users.enable'), danger: false },
    delete: { title: t('users.deleteTitle'), body: t('users.deleteBody', { user }), label: t('users.delete'), danger: true },
  }[p.kind];
  return <ConfirmModal title={m.title} body={error ?? m.body} confirmLabel={m.label} danger={m.danger} busy={busy} onConfirm={onConfirm} onCancel={onCancel} />;
}
