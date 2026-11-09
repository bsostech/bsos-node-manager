import { useEffect, useState } from 'react';
import { Trans, useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api, HttpError, type ErrorReason } from '@/api/client';
import { useAuth } from '@/auth/AuthContext';
import { Logo } from '@/components/Logo';
import { ThemeToggle, LangToggle } from '@/components/Toggles';
import { inputCls } from '@/components/Field';
import { Button } from '@/components/Button';
import { Modal, ModalTitle } from '@/components/Modal';
import { TextButton } from '@/components/TextLinks';
import { DEMO_PASSWORD, demoScenario, freshProgress, switchDemoScenario, type DemoScenario } from '@/mocks/scenario';

/** Reasons a failed OIDC sign-in sends back in ?login_error=; anything else in the URL is ignored. Typed against
 *  ErrorReason so a renamed code breaks the build. Shown with login.oidcError.*, worded for someone who just failed
 *  to sign in rather than the admin-screen apiError.* text. */
const LOGIN_ERRORS: ReadonlySet<string> = new Set([
  'oidc_not_enabled', 'oidc_flow_expired', 'oidc_state_mismatch', 'oidc_verify_failed', 'no_role', 'role_mapping_changed',
  'db_unavailable', 'internal_error', 'audit_unavailable',
] satisfies ErrorReason[]);

export function Login() {
  const { t } = useTranslation();
  const { login } = useAuth();
  const nav = useNavigate();
  const { data: config } = useQuery({ queryKey: ['authConfig'], queryFn: () => api.getAuthConfig() });

  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  // Holds an i18n key, so switching language on this page re-translates the message
  const [error, setError] = useState<string | null>(() => {
    const reason = new URLSearchParams(window.location.search).get('login_error');
    return reason && LOGIN_ERRORS.has(reason) ? `login.oidcError.${reason}` : null;
  });
  // Drop the param once read, so a refresh does not show the failure again
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    if (!params.has('login_error')) return;
    params.delete('login_error');
    const q = params.toString();
    window.history.replaceState(window.history.state, '', window.location.pathname + (q ? `?${q}` : '') + window.location.hash);
  }, []);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await login({ username: username.trim(), password });
      // The URL may still be the previous user's page at logout (e.g. an admin's /identity); a new login always starts from home, and Home routes by role
      nav('/', { replace: true });
    } catch (err) {
      // Wrong credentials always get the same message, not revealing whether the account exists; 5xx means the platform is unreachable, not a credentials problem.
      // With LDAP behind the form, a known user whose groups map to no role is refused the same way, so the message says so.
      setError(err instanceof HttpError && err.status >= 500 ? 'error.unavailable' : config?.ldapLogin ? 'login.errorLdap' : 'login.error');
    } finally {
      setBusy(false);
    }
  };

  // The backend decides what to show: credential form (local accounts, or password-based SSO), SSO button (redirect SSO), or both.
  const ssoUrl = config?.ssoLoginUrl;
  const hasSso = !!ssoUrl;
  const showCredentials = config?.passwordLogin ?? false;
  // The credential form explains itself, so no subtitle; only hint when "SSO only"
  const hint = hasSso && !showCredentials ? t('login.hintOidc') : null;
  const errorLine = error && (
    <p role="alert" className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>
      {t(error)}
    </p>
  );

  return (
    <div className="relative flex h-full items-center justify-center bg-canvas p-4">
      <div className="absolute right-4 top-4 flex items-center gap-1">
        <LangToggle />
        <ThemeToggle />
      </div>
      <div className="flex w-full max-w-sm flex-col gap-5 rounded-lg border border-hair bg-surface p-6">
        <Logo className="h-6 w-auto self-start" />
        <div>
          <h1 className="text-lg font-semibold text-ink">{t('login.title')}</h1>
          {hint && <p className="text-sm text-ink-3">{hint}</p>}
        </div>

        {hasSso && (
          <button
            onClick={() => {
              if (ssoUrl) window.location.href = ssoUrl;
            }}
            className="rounded-md bg-accent px-3 py-2 text-sm font-medium text-white hover:bg-accent-hover transition-colors"
          >
            {t('login.sso')}
          </button>
        )}

        {/* Without the credential form (SSO only), an OIDC failure still needs a place to show */}
        {!showCredentials && errorLine}

        {hasSso && showCredentials && (
          <div className="flex items-center gap-3 text-xs text-ink-3">
            <span className="h-px flex-1 bg-hair" />
            {t('login.or')}
            <span className="h-px flex-1 bg-hair" />
          </div>
        )}

        {config && !hasSso && !showCredentials && (
          <p role="alert" className="text-sm text-ink-2">{t('login.unavailable')}</p>
        )}

        {showCredentials && (
          <form className="flex flex-col gap-3" onSubmit={submit}>
            <label className="flex flex-col gap-1 text-xs text-ink-3">
              {t('login.username')}
              <input value={username} onChange={(e) => setUsername(e.target.value)} className={inputCls} autoFocus autoComplete="username" spellCheck={false} />
            </label>
            <label className="flex flex-col gap-1 text-xs text-ink-3">
              {t('login.password')}
              <input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className={inputCls}
                autoComplete="current-password"
              />
            </label>
            {errorLine}
            <button
              type="submit"
              disabled={!username.trim() || !password || busy}
              className="rounded-md bg-accent px-3 py-2 text-sm font-medium text-white hover:bg-accent-hover transition-colors disabled:opacity-60"
            >
              {busy ? t('login.signingIn') : t('login.signIn')}
            </button>
            {/* For walkthroughs: "Where is the initial password?" only shows on a fresh deployment before admin finishes first login (dev mode only, not a real feature) */}
            {import.meta.env.DEV && firstLoginPending() && <FirstPasswordHelp />}
          </form>
        )}

        {/* Dev hints: demo scenario switch + demo accounts. Only shown in dev mode (MSW), absent in production */}
        {import.meta.env.DEV && showCredentials && <DevHints />}
      </div>
    </div>
  );
}

/** Fresh deployment where admin hasn't finished first login: only then does someone walking through it need to know where the initial password comes from. */
const firstLoginPending = () => demoScenario() === 'fresh' && !freshProgress.adminReady;

/** "Where is the initial password?" link + explanation dialog: covers the two sources of the initial password in a real deployment and what follows, without revealing any password. */
function FirstPasswordHelp() {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  return (
    <div className="text-xs">
      <TextButton onClick={() => setOpen(true)}>{t('login.firstTime')}</TextButton>
      {open && (
        <Modal width="max-w-sm" contentClass="gap-3 rounded-lg border border-hair bg-surface p-5 shadow-xl" onClose={() => setOpen(false)}>
          <ModalTitle>{t('login.firstTimeTitle')}</ModalTitle>
          <div className="flex flex-col gap-2 text-xs leading-relaxed text-ink-2">
            <p>{t('login.firstTimeIntro')}</p>
            <ol className="flex list-decimal flex-col gap-1 pl-4">
              <li><Trans i18nKey="login.firstTimeEnv" components={{ c: <code className="font-mono text-ink" /> }} /></li>
              <li>{t('login.firstTimeLog')}</li>
            </ol>
            <p>{t('login.firstTimeAfter')}</p>
          </div>
          <div className="flex justify-end">
            <Button variant="primary" onClick={() => setOpen(false)}>{t('login.firstTimeClose')}</Button>
          </div>
        </Modal>
      )}
    </div>
  );
}

/** Dev-mode hint area: switch demo scenario (existing / fresh deployment, see mocks/scenario.ts) and the demo accounts for that scenario. */
function DevHints() {
  const { t } = useTranslation();
  const scenario = demoScenario();
  const option = (s: DemoScenario) => (
    <button
      type="button"
      aria-pressed={s === scenario}
      onClick={() => { if (s !== scenario) switchDemoScenario(s); }}
      className={`rounded px-2 py-0.5 transition-colors ${s === scenario ? 'bg-elevated font-medium text-ink' : 'text-ink-3 hover:text-ink'}`}
    >
      {t(`login.scenario.${s}`)}
    </button>
  );
  return (
    <div className="flex flex-col gap-2 border-t border-hair pt-4 text-xs leading-relaxed text-ink-3">
      <div className="flex flex-wrap items-center gap-1">
        <span>{t('login.scenario.label')}</span>
        {option('existing')}
        {option('fresh')}
      </div>
      <p>{t(scenario !== 'fresh' ? 'login.devAccounts' : firstLoginPending() ? 'login.devAccountsFresh' : 'login.devAccountsFreshDone', { password: DEMO_PASSWORD })}</p>
    </div>
  );
}
