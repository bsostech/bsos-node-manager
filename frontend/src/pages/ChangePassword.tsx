import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api, HttpError } from '@/api/client';
import { errMsg } from '@/lib/errors';
import { useAuth } from '@/auth/AuthContext';
import { Logo } from '@/components/Logo';
import { ThemeToggle, LangToggle } from '@/components/Toggles';
import { inputCls } from '@/components/Field';
import { useClickLock } from '@/components/Button';

/** First-login forced password change gate: blocks the app while me.mustChangePassword is true. */
export function ChangePassword() {
  const { t } = useTranslation();
  const { logout } = useAuth();
  const qc = useQueryClient();

  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [error, setError] = useState<string | null>(null);

  const lock = useClickLock();
  const change = useMutation({
    mutationFn: () => api.changePassword({ currentPassword: current, newPassword: next }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['me'] }),
    onError: (err) => setError(err instanceof HttpError && err.status === 400 ? errMsg(err) : t('changePw.error')),
  });

  const tooShort = next.length > 0 && [...next].length < 12;
  const mismatch = confirm.length > 0 && next !== confirm;
  const valid = current && [...next].length >= 12 && next === confirm;

  return (
    <div className="relative flex h-full items-center justify-center bg-canvas p-4">
      <div className="absolute right-4 top-4 flex items-center gap-1">
        <LangToggle />
        <ThemeToggle />
      </div>
      <div className="flex w-full max-w-sm flex-col gap-5 rounded-lg border border-hair bg-surface p-6">
        <Logo className="h-6 w-auto self-start" />
        <div>
          <h1 className="text-lg font-semibold text-ink">{t('changePw.title')}</h1>
          <p className="text-sm text-ink-3">{t('changePw.hint')}</p>
        </div>

        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            setError(null);
            if (valid) change.mutate();
          }}
        >
          <label className="flex flex-col gap-1 text-xs text-ink-3">
            {t('changePw.current')}
            <input type="password" value={current} onChange={(e) => setCurrent(e.target.value)} className={inputCls} autoFocus autoComplete="current-password" />
          </label>
          <label className="flex flex-col gap-1 text-xs text-ink-3">
            {t('changePw.next')}
            <input type="password" value={next} onChange={(e) => setNext(e.target.value)} className={inputCls} autoComplete="new-password" />
          </label>
          <label className="flex flex-col gap-1 text-xs text-ink-3">
            {t('changePw.confirm')}
            <input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} className={inputCls} autoComplete="new-password" />
          </label>
          {tooShort && <p className="text-xs text-ink-3">{t('changePw.tooShort')}</p>}
          {mismatch && <p className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>{t('changePw.mismatch')}</p>}
          <p className="text-xs text-ink-3">{t('password.rules')}</p>
          {error && <p role="alert" className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>{error}</p>}
          <button
            type="submit"
            disabled={!valid || change.isPending}
            onClick={lock()}
            className="rounded-md bg-accent px-3 py-2 text-sm font-medium text-white hover:bg-accent-hover transition-colors disabled:opacity-60"
          >
            {change.isPending ? t('changePw.saving') : t('changePw.submit')}
          </button>
        </form>

        <button onClick={() => logout()} className="self-start text-xs text-ink-3 hover:text-ink">
          {t('common.logout')}
        </button>
      </div>
    </div>
  );
}
