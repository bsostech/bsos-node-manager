import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate } from 'react-router-dom';
import { Button } from '@/components/Button';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { CaretRight, Plus } from '@phosphor-icons/react';
import { api } from '@/api/client';
import type { LocalUser } from '@/api/schemas';
import type { UserCreated } from '@/components/LocalUserModals';
import { useAuth } from '@/auth/AuthContext';
import { IdentityTabs } from '@/components/IdentityTabs';
import { ShownPasswordModal, UserFormModal } from '@/components/LocalUserModals';
import { EmptyState, ErrorState, Notice, SkeletonRows } from '@/components/States';
import { fmtDateTimeShort } from '@/lib/format';
import { roleKnown, roleLabel } from '@/auth/permissions';

const FOCUS = 'focus:outline-none focus-visible:ring-1 focus-visible:ring-accent';

/** Account status: dot + text (Disabled / Cannot sign in / Must change password / Active). Cannot log in: the role doesn't exist in this edition, so the backend refuses login. */
export function UserState({ u }: { u: LocalUser }) {
  const { t } = useTranslation();
  const [color, key] = u.disabled
    ? ['var(--bsos-status-neutral-fg)', 'users.disabled']
    : !roleKnown(u.role)
    ? ['var(--bsos-status-error-fg)', 'users.noLogin']
    : u.mustChangePassword
      ? ['var(--bsos-status-syncing-fg)', 'users.mustChange']
      : ['var(--bsos-status-healthy-fg)', 'users.active'];
  return (
    <span className="inline-flex items-center gap-1.5 text-xs text-ink-2">
      <span className="size-1.5 rounded-full" style={{ background: color }} />{t(key)}
    </span>
  );
}

/**
 * Account permissions > Users: the local user list; click a row for the user page (change role, reset password, disable, delete all live there).
 * Admin only (nav + route gated by members:manage); SSO users aren't here, the backend only records local accounts.
 */
export function Identity() {
  const { t } = useTranslation();
  const nav = useNavigate();
  const qc = useQueryClient();
  const { me } = useAuth();
  const q = useQuery({ queryKey: ['localUsers'], queryFn: api.listLocalUsers });
  const [creating, setCreating] = useState(false);
  const [shown, setShown] = useState<{ username: string; password: string; mailError?: string | null } | null>(null);
  const [sent, setSent] = useState<UserCreated | null>(null); // just added with the temporary password emailed: one-line notice above the list

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-4">
      <IdentityTabs
        action={
          <Button variant="primary" onClick={() => setCreating(true)} className="inline-flex shrink-0 items-center gap-1.5">
            <Plus size={15} aria-hidden /> {t('users.add')}
          </Button>
        }
      />

      {sent && (
        <Notice tone="healthy" action={<button type="button" onClick={() => setSent(null)} className="shrink-0 rounded px-1.5 hover:bg-surface">{t('common.close')}</button>}>{t('users.sent', { user: sent.username, email: sent.email })}</Notice>
      )}

      {q.isLoading ? (
        <SkeletonRows rows={3} />
      ) : q.isError ? (
        <ErrorState onRetry={() => void q.refetch()} />
      ) : !q.data?.length ? (
        <EmptyState message={t('users.empty')} />
      ) : (
        <section className="overflow-hidden rounded-lg border border-hair bg-surface">
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="table-head">
                  <th className="px-4">{t('users.colUser')}</th>
                  <th className="px-4">{t('users.colEmail')}</th>
                  <th className="px-4">{t('users.colRole')}</th>
                  <th className="px-4">{t('users.colState')}</th>
                  <th className="px-4">{t('users.colLastLogin')}</th>
                  <th aria-hidden="true" />
                </tr>
              </thead>
              <tbody>
                {q.data.map((u) => {
                  const href = `/identity/users/${encodeURIComponent(u.username)}`;
                  return (
                    <tr key={u.username} onClick={() => nav(href)} className={`group cursor-pointer border-b border-hair transition-colors last:border-0 hover:bg-elevated ${u.disabled ? 'text-ink-3' : 'text-ink'}`}>
                      <td className="whitespace-nowrap px-4 py-3">
                        {/* Real link: keyboard reachable + Cmd+click opens a tab; tr onClick is just a big mouse target */}
                        <Link to={href} onClick={(e) => e.stopPropagation()} className={`rounded-sm font-mono ${FOCUS}`}>{u.username}</Link>
                        {u.username === me?.user.id && <span className="ml-2 text-xs text-ink-3">{t('users.you')}</span>}
                      </td>
                      <td className="whitespace-nowrap px-4 py-3 text-ink-2">{u.email ?? '—'}</td>
                      <td className="whitespace-nowrap px-4 py-3">{roleLabel(t, u.role)}</td>
                      <td className="whitespace-nowrap px-4 py-3"><UserState u={u} /></td>
                      <td className="whitespace-nowrap px-4 py-3 font-mono text-xs text-ink-3">{u.lastLoginAt ? fmtDateTimeShort(u.lastLoginAt) : '—'}</td>
                      <td className="px-2 text-ink-3"><CaretRight size={14} className="opacity-0 group-hover:opacity-100" aria-hidden /></td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </section>
      )}


      {creating && (
        <UserFormModal
          mode="create"
          onClose={() => setCreating(false)}
          onDone={(r) => {
            setCreating(false);
            void qc.invalidateQueries({ queryKey: ['localUsers'] });
            // If emailed, show just one line; if not sent (or sending failed) we get the password, shown once for the admin to hand over
            if (r.delivery === 'email') setSent(r);
            else if (r.password) setShown({ username: r.username, password: r.password, mailError: r.mailError });
          }}
        />
      )}
      {shown && <ShownPasswordModal {...shown} onClose={() => setShown(null)} />}
    </div>
  );
}
