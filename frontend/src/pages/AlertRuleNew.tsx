import { useTranslation } from 'react-i18next';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import { AlertRuleCreateForm } from '@/components/AlertRuleEditor';
import { BackLink } from '@/components/TextLinks';

/** New threshold rule: write the condition as one sentence, with a preview below of which nodes would have fired in the last 24 hours. Once created, go straight to that rule's page. */
export function AlertRuleNew() {
  const { t } = useTranslation();
  const nodes = useQuery({ queryKey: ['nodes'], queryFn: () => api.listNodes(), select: (r) => r.nodes.map((n) => n.id) });
  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-4">
      <BackLink to="/alerts">{t('alerts.title')}</BackLink>
      <header className="flex flex-col gap-0.5">
        <h1 className="text-lg font-semibold text-ink">{t('alerts.rules.addTitle')}</h1>
        <span className="text-xs text-ink-3">{t('alerts.form.newSubtitle')}</span>
      </header>
      <AlertRuleCreateForm nodes={nodes.data ?? []} />
    </div>
  );
}
