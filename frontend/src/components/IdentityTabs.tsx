import { useTranslation } from 'react-i18next';
import { ClockCounterClockwise, User } from '@phosphor-icons/react';
import { PageTabs } from '@/components/PageTabs';
import { enterprise, isEnterprise } from '@/edition';

/**
 * Header for account and permission management: the account list, plus Enterprise's "Connect company accounts" tab. In the open-source
 * edition, the same item is a commercial-edition entry under the sidebar's "User management",
 * not here. The open-source edition also shows a locked "Change history" tab (what admins changed,
 * which belongs to auditing); clicking it opens the commercial-edition popup without switching tabs.
 */
export function IdentityTabs({ action }: { action?: React.ReactNode }) {
  const { t } = useTranslation();
  const hasAuth = enterprise.identityTabs.length > 0;
  return (
    <PageTabs
      title={t('nav.identity')}
      subtitle={t(hasAuth ? 'users.subtitleWithAuth' : 'users.subtitle')}
      action={action}
      tabs={[
        { to: '/identity', label: t('users.tab'), icon: User },
        ...enterprise.identityTabs.map((tb) => ({ to: tb.to, label: t(tb.labelKey), icon: tb.icon })),
        ...(isEnterprise ? [] : [{ to: '/identity/changes', label: t('users.tabChanges'), icon: ClockCounterClockwise, locked: 'audit' as const }]),
      ]}
    />
  );
}
