import { useTranslation } from 'react-i18next';
import { Key, ListChecks } from '@phosphor-icons/react';
import { PageTabs } from '@/components/PageTabs';
import { ExtLink } from '@/components/TextLinks';
import { BESU_DOCS } from '@/lib/besuDocs';

/**
 * Page header. "RPC access identities" and "transaction account allowlist" used to be two tabs of one page; they now
 * live in different groups: RPC access identities concern only this org's own nodes and sit under platform settings
 * ("Node access settings"); transaction address authorization (the Besu account allowlist) must be
 * aligned with other orgs and sits under consortium governance ("Authorized sender addresses"). With a single tab, PageTabs draws no tab bar.
 */
export function AccessTabs({ page, action }: { page: 'rpc' | 'allowlist'; action?: React.ReactNode }) {
  const { t } = useTranslation();
  return page === 'rpc'
    ? <PageTabs title={t('access.title')} subtitle={t('access.subtitleRpc')} help={(
        <>
          {t('access.rpcHelp')}
          {/* Official docs: general methods (eth, net, txpool, admin) and private-network-only ones (qbft, perm) are on two separate pages */}
          <span className="-ml-1.5 mt-2 flex flex-col gap-0.5">
            <ExtLink inTip href={BESU_DOCS.api}>{t('access.docApi')}</ExtLink>
            <ExtLink inTip href={BESU_DOCS.privateApi}>{t('access.docPrivateApi')}</ExtLink>
          </span>
        </>
      )} action={action} tabs={[{ to: '/access', label: t('access.tabRpcUsers'), icon: Key }]} />
    : <PageTabs title={t('access.permTitle')} subtitle={t('access.subtitleAllowlist')} action={action} tabs={[{ to: '/access/permissioning', label: t('access.permTitle'), icon: ListChecks }]} />;
}
