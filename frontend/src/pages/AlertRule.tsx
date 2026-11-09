import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import type { AlertRule } from '@/api/schemas';
import { useAuth } from '@/auth/AuthContext';
import { Skeleton, SkeletonRows, EmptyState, ErrorState } from '@/components/States';
import { AlertRuleSettings } from '@/components/AlertRuleEditor';
import { forPhrase, ruleTitle, ruleCondition } from '@/lib/alertRules';
import { BackLink } from '@/components/TextLinks';
import { Button } from '@/components/Button';
import { ConfirmModal } from '@/components/ConfirmModal';
import { enterprise } from '@/edition';
import { errMsg } from '@/lib/errors';
import { LEAVE_WITHOUT_ASKING } from '@/hooks/useUnsavedGuard';

// Only Enterprise can delete user-added rules; default rules can only be disabled in both editions
const deleteRule = enterprise.deleteAlertRule;

/**
 * Single rule page: settings only. It sits under "Alerts & notifications" and does no monitoring:
 * whether the rule is firing, per-node status and firing history all live in "Alert notifications".
 */
export function AlertRulePage() {
  const { t } = useTranslation();
  const { id = '' } = useParams();
  const { can } = useAuth();
  const canWrite = can('nodes:onboard');
  // Save time: the editor remounts after saving, so "Saved" is kept here and passed back down
  const [savedAt, setSavedAt] = useState(0);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const nav = useNavigate();
  const qc = useQueryClient();

  const rules = useQuery<AlertRule[]>({ queryKey: ['alertRules'], queryFn: () => api.listAlertRules() });
  const rule = rules.data?.find((r) => r.id === id);
  const nodes = useQuery({ queryKey: ['nodes'], queryFn: () => api.listNodes(), select: (r) => r.nodes.map((n) => n.id) });

  const remove = useMutation({
    mutationFn: () => (deleteRule ? deleteRule(id) : Promise.reject(new Error('deleteAlertRule: Enterprise only'))),
    onSuccess: () => {
      qc.setQueryData<AlertRule[]>(['alertRules'], (l) => l?.filter((x) => x.id !== id));
      nav('/alerts', LEAVE_WITHOUT_ASKING);
    },
    // Already deleted elsewhere (404): refetch the list and this page turns into "Rule not found"
    onError: () => qc.invalidateQueries({ queryKey: ['alertRules'] }),
  });

  const back = <BackLink to="/alerts">{t('nav.alertSettings')}</BackLink>;

  if (rules.isLoading) return <div className="mx-auto flex max-w-5xl flex-col gap-4">{back}<Skeleton className="h-10 w-64" /><SkeletonRows rows={4} /></div>;
  if (rules.isError || !rule) {
    return (
      <div className="mx-auto flex max-w-5xl flex-col gap-4">
        {back}
        {rules.isError ? (
          <ErrorState onRetry={() => void rules.refetch()} />
        ) : (
          <EmptyState message={t('alerts.ruleNotFound')} />
        )}
      </div>
    );
  }

  return (
    <div className="mx-auto flex max-w-5xl flex-col gap-4">
      {back}
      <header className="flex flex-wrap items-center gap-3">
        <div className="flex min-w-0 flex-col gap-0.5">
          <h1 className="text-lg font-semibold text-ink">{ruleTitle(t, rule)}</h1>
          <span className="text-xs text-ink-3">
            {t('alerts.ruleSubtitle', { sev: t(`alerts.rules.severity.${rule.severity}`), cond: ruleCondition(t, rule), d: forPhrase(t, rule.forDuration) })}
          </span>
        </div>
        {/* Delete acts on the whole rule, so it goes in the page header, not crowded next to the form's save (same as the RPC identity page) */}
        {canWrite && deleteRule && (
          <div className="ml-auto flex items-center gap-2">
            {rule.isDefault ? (
              // In Enterprise some rules can be deleted and some can't: default rules state why in the delete button's place (Core has no delete, nothing shown)
              <span className="rounded border border-hair px-2 py-0.5 text-xs text-ink-3">{t('alerts.rules.defaultNoDelete')}</span>
            ) : (
              <Button variant="dangerOutline" onClick={() => { remove.reset(); setConfirmDelete(true); }}>{t('alerts.rules.delete')}</Button>
            )}
          </div>
        )}
      </header>
      {confirmDelete && (
        <ConfirmModal
          title={t('alerts.rules.deleteTitle')}
          body={remove.isError ? errMsg(remove.error) || t('alerts.rules.deleteError') : t('alerts.form.deleteConfirm', { name: ruleTitle(t, rule) })}
          confirmLabel={remove.isPending ? t('alerts.rules.deleting') : t('alerts.rules.delete')}
          danger
          busy={remove.isPending}
          onConfirm={() => remove.mutate()}
          onCancel={() => setConfirmDelete(false)}
        />
      )}
      {/* key: switching rules or saving replaces the rule, and the draft resets with it */}
      <AlertRuleSettings key={`${rule.id}:${rule.updatedAt}`} rule={rule} nodes={nodes.data ?? []} canWrite={canWrite} savedAt={savedAt} onSaved={setSavedAt} />
    </div>
  );
}
