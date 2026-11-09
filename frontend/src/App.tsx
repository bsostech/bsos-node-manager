import { createBrowserRouter, RouterProvider, Routes, Route, Outlet, Navigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { AppShell } from '@/components/AppShell';
import { AuthProvider, useAuth } from '@/auth/AuthContext';
import { UnsavedBlocker } from '@/hooks/useUnsavedGuard';
import { TooltipProvider } from '@/components/Tooltip';
import { Login } from '@/pages/Login';
import { EmptyState, ErrorState } from '@/components/States';
import type { Permission } from '@/auth/permissions';
import { ChangePassword } from '@/pages/ChangePassword';
import { Overview } from '@/pages/Overview';
import { NodeDetail } from '@/pages/NodeDetail';
import { NodeTxPoolAccount } from '@/pages/NodeTxPoolAccount';
import { NodeJoin } from '@/pages/NodeJoin';
import { NodeAdd } from '@/pages/NodeAdd';
import { Validators } from '@/pages/Validators';
import { ValidatorDetail } from '@/pages/ValidatorDetail';
import { ValidatorProposal } from '@/pages/ValidatorProposal';
import { ValidatorPropose } from '@/pages/ValidatorPropose';
import { Identity } from '@/pages/Identity';
import { IdentityUser } from '@/pages/IdentityUser';
import { LoginEvents } from '@/pages/LoginEvents';
import { Alerts } from '@/pages/Alerts';
import { AlertRulePage } from '@/pages/AlertRule';
import { AlertRuleNew } from '@/pages/AlertRuleNew';
import { AlertNotifyEmail, AlertNotifyLocale, AlertNotifySlack, AlertNotifyTelegram, AlertNotifyWebhooks } from '@/pages/AlertNotify';
import { AccessOverview } from '@/pages/AccessOverview';
import { AccessRpcUser } from '@/pages/AccessRpcUser';
import { NodeSettings } from '@/pages/NodeSettings';
import { AccessPermissioning } from '@/pages/AccessPermissioning';
import { EnterpriseInfo, ENTERPRISE_INFO_PATH } from '@/pages/EnterpriseInfo';
import { EnterpriseFeature } from '@/pages/EnterpriseFeature';
import { ChainHealth } from '@/pages/ChainHealth';
import { Notifications } from '@/pages/Notifications';
import { navGroups } from '@/nav';
import { enterprise, isEnterprise } from '@/edition';
import { GoLink } from '@/components/TextLinks';

const spinner = <div className="flex h-full items-center justify-center text-sm text-ink-3">…</div>;

/** Route-level permission check: without permission, render "No permission" instead of the page itself (otherwise the page would draw its action buttons first and data would spin forever).
 *  The sidebar uses perm in nav.ts to decide visibility; this covers typing the URL directly. The real enforcement is still on the backend. */
function Need({ perm }: { perm: Permission | Permission[] }) {
  const { can } = useAuth();
  const { t } = useTranslation();
  return can(perm) ? <Outlet /> : <EmptyState message={t('error.forbidden')} />;
}

function NotFound() {
  const { t } = useTranslation();
  return <EmptyState message={t('error.notFound')} action={<GoLink to="/">{t('nav.overview')}</GoLink>} />;
}

/** Home: land on the first visible sidebar item (operator / viewer -> chain health; admin -> account and permission management). "My nodes" lives at /nodes. */
function Home() {
  const { can } = useAuth();
  const first = navGroups.flatMap((g) => g.items).find((i) => i.status === 'live' && (!i.perm || can(i.perm)));
  return first ? <Navigate to={first.to} replace /> : <Need perm="monitor:view" />;
}

/** Login gate: not logged in -> Login; first login must change password -> ChangePassword; otherwise AppShell + routes. */
function Gate() {
  const { me, isLoading, unavailable, retry } = useAuth();
  if (isLoading) return spinner;
  if (unavailable) return <div className="mx-auto max-w-md p-8"><ErrorState onRetry={retry} /></div>;
  if (!me) return <Login />;
  if (me.mustChangePassword) return <ChangePassword />;
  return (
    <AppShell>
      <Routes>
        {/* Implemented */}
        <Route path="/" element={<Home />} />
        <Route element={<Need perm="monitor:view" />}>
          <Route path="/chain" element={<ChainHealth />} />
          <Route path="/nodes" element={<Overview />} />
          <Route path="/notifications" element={<Notifications />} />
          {/* Viewers can also see these platform settings pages (read-only; writes are checked separately in the page against nodes:onboard) */}
          <Route path="/node-join" element={<NodeJoin />} />
          <Route path="/node-join/peers" element={<NodeJoin />} />
          <Route path="/access" element={<AccessOverview />} />
          <Route path="/access/permissioning" element={<AccessPermissioning />} />
          <Route path="/access/rpc-users/:id" element={<AccessRpcUser />} />
          <Route path="/alerts" element={<Alerts />} />
          <Route path="/nodes/:id/settings" element={<NodeSettings />} />
        </Route>
        <Route path="/nodes/:id" element={<NodeDetail />} />
        <Route path="/nodes/:id/txpool/:address" element={<NodeTxPoolAccount />} />
        {/* Only pages that "do things" are restricted to operators: the add-node wizard, proposal voting, and the alert rule and notification channel edit pages
            (all forms, and notification channels hold values like webhook URLs that are usable as soon as someone sees them; viewers see rule and channel status on the alert settings overview) */}
        <Route element={<Need perm="nodes:onboard" />}>
          <Route path="/node-add" element={<NodeAdd />} />
          <Route path="/validators/propose" element={<ValidatorPropose />} />
          <Route path="/alerts/rules/new" element={<AlertRuleNew />} />
          <Route path="/alerts/rules/:id" element={<AlertRulePage />} />
          <Route path="/alerts/notify/email" element={<AlertNotifyEmail />} />
          <Route path="/alerts/notify/telegram" element={<AlertNotifyTelegram />} />
          <Route path="/alerts/notify/slack" element={<AlertNotifySlack />} />
          <Route path="/alerts/notify/webhooks" element={<AlertNotifyWebhooks />} />
          <Route path="/alerts/notify/locale" element={<AlertNotifyLocale />} />
        </Route>
        <Route element={<Need perm="validators:view" />}>
          <Route path="/validators" element={<Validators />} />
          <Route path="/validators/proposals/:key" element={<ValidatorProposal />} />
          <Route path="/validators/:address" element={<ValidatorDetail />} />
        </Route>
        <Route element={<Need perm="members:manage" />}>
          <Route path="/identity" element={<Identity />} />
          <Route path="/identity/users/:username" element={<IdentityUser />} />
          <Route path="/login-history" element={<LoginEvents />} />
        </Route>

        {/* Core edition: the Enterprise info page (not needed in the Enterprise build) */}
        {!isEnterprise && <Route path={ENTERPRISE_INFO_PATH} element={<EnterpriseInfo />} />}
        {/* Open-source edition: intro pages for each locked commercial item in the sidebar */}
        {!isEnterprise && <Route path={`${ENTERPRISE_INFO_PATH}/:slug`} element={<EnterpriseFeature />} />}

        {/* Enterprise pages (absent in the open-source edition, see src/edition.ts) */}
        {enterprise.routes.map((r) =>
          r.perm ? (
            <Route key={r.path} element={<Need perm={r.perm} />}>
              <Route path={r.path} element={r.element} />
            </Route>
          ) : (
            <Route key={r.path} path={r.path} element={r.element} />
          ),
        )}

        {/* Any other URL (including Enterprise paths hit on Core): page not found. Core does not need to know which pages Enterprise has (edition.ts) */}
        <Route path="*" element={<NotFound />} />
      </Routes>
    </AppShell>
  );
}

// The data router exists only for useBlocker (warn about unsaved changes before in-app navigation): one catch-all route takes every URL, and the original <Routes> still runs inside Gate.
const router = createBrowserRouter([
  {
    path: '*',
    element: (
      <UnsavedBlocker>
        <Gate />
      </UnsavedBlocker>
    ),
  },
]);

export function App() {
  return (
    <AuthProvider>
      <TooltipProvider>
        <RouterProvider router={router} />
      </TooltipProvider>
    </AuthProvider>
  );
}
