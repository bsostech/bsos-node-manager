import { useId, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/api/client';
import type { AlertRule, AlertRuleCreateRequest, AlertRuleDuration, AlertRuleOperator, AlertRuleUpdateRequest, MetricId } from '@/api/schemas';
import { Listbox } from '@/components/Listbox';
import { inputCls } from '@/components/Field';
import { Button } from '@/components/Button';
import { RuleChart, useRuleSeries } from '@/components/RuleChart';
import { METRIC_OPTIONS, OPERATOR_SYMBOL, durLabel, metricLabel, metricUnit, rangeText, ruleCondition, thresholdRange } from '@/lib/alertRules';
import { errMsg } from '@/lib/errors';
import { appLocale } from '@/lib/format';
import { LEAVE_WITHOUT_ASKING, useUnsavedGuard } from '@/hooks/useUnsavedGuard';
import { Card } from '@/components/Card';
import { Switch } from '@/components/Switches';
import { EnterpriseTeaser } from '@/components/EnterpriseTeaser';
import { enterprise } from '@/edition';
import { SaveButton, SavedNote } from '@/components/SaveButton';

// Creating rules is an Enterprise feature: the open-source edition lacks this API, so the create button is disabled and shows the commercial-edition tag
const createRule = enterprise.createAlertRule;

const OPERATORS: AlertRuleOperator[] = ['gt', 'lt', 'gte', 'lte'];
// 0s = fire as soon as it happens (Prometheus for: 0s, sent on the next evaluation)
const DURATIONS: AlertRuleDuration[] = ['0s', '1m', '2m', '5m', '10m', '30m', '1h'];
const PREVIEW_RANGE = '24h';

/** Form draft. Thresholds are stored as strings so partial input (empty string, "-") isn't swallowed. */
export interface RuleDraft {
  name: string;
  metric: MetricId;
  nodeId: string; // '' = all nodes
  operator: AlertRuleOperator;
  threshold: string;
  forDuration: AlertRuleDuration;
  severity: AlertRule['severity'];
  enabled: boolean;
  keepFiringFor: AlertRuleDuration;
  clear: string; // recovery threshold (threshold rules only); empty string = same as the firing threshold (no hysteresis)
}

export const emptyDraft = (): RuleDraft => ({
  name: '', metric: 'cpu', nodeId: '', operator: 'gt', threshold: '80', forDuration: '5m', severity: 'warning', enabled: true, keepFiringFor: '5m', clear: '',
});

export const draftFromRule = (r: AlertRule): RuleDraft => ({
  name: r.name, metric: r.metric ?? 'cpu', nodeId: r.nodeId ?? '', operator: r.operator ?? 'gt',
  threshold: String(r.threshold ?? 0), forDuration: r.forDuration, severity: r.severity, enabled: r.enabled,
  keepFiringFor: r.keepFiringFor, clear: r.clearThreshold != null && r.clearThreshold !== r.threshold ? String(r.clearThreshold) : '',
});

const thresholdOk = (d: RuleDraft) => {
  const v = Number(d.threshold), { min, max } = thresholdRange(d.metric);
  return d.threshold.trim() !== '' && Number.isFinite(v) && v >= min && (max == null || v <= max);
};

/** Recovery threshold: empty = same as the firing threshold; otherwise it must be in range and on the other side of the firing threshold (same as backend ValidClear). */
const clearValue = (d: RuleDraft) => (d.clear.trim() === '' ? Number(d.threshold) : Number(d.clear));
const clearError = (d: RuleDraft): 'range' | 'side' | null => {
  if (d.clear.trim() === '') return null;
  const c = Number(d.clear), th = Number(d.threshold), { min, max } = thresholdRange(d.metric);
  if (!Number.isFinite(c) || c < min || (max != null && c > max)) return 'range';
  if ((d.operator === 'gt' || d.operator === 'gte') ? c > th : c < th) return 'side';
  return null;
};

/** The draft's condition (the condition text is used as the default name). */
const draftCond = (d: RuleDraft) => ({ kind: 'threshold' as const, metric: d.metric, operator: d.operator, threshold: Number(d.threshold), nodeId: d.nodeId || null });

/** Request body: rules with a fixed condition omit condition fields; default threshold rules send only the threshold (the backend forbids changing metric/comparison/node, so original values are sent). */
function toUpdate(d: RuleDraft, r: AlertRule): AlertRuleUpdateRequest {
  const base = { name: d.name.trim() || r.name, forDuration: d.forDuration, severity: d.severity, enabled: d.enabled, keepFiringFor: d.keepFiringFor };
  if (r.kind === 'chain_slow') return { ...base, threshold: Number(d.threshold) };
  if (r.kind !== 'threshold') return base;
  if (r.isDefault) return { ...base, metric: r.metric, operator: r.operator, threshold: Number(d.threshold), clearThreshold: clearValue(d) };
  return { ...base, metric: d.metric, operator: d.operator, threshold: Number(d.threshold), clearThreshold: clearValue(d), nodeId: d.nodeId || undefined };
}

const selectCls = 'inline-block shrink-0';
const Fixed = ({ children }: { children: React.ReactNode }) => <span className="font-medium text-ink">{children}</span>;

/**
 * The condition reads as one sentence: "when [node]'s [metric] [comparison] [threshold] for [duration]".
 * Default threshold rules have fixed node, metric and comparison (their name and summary describe that metric);
 * fixed-condition rules leave only the duration.
 */
function ConditionSentence({ d, set, kind, isDefault, nodes }: {
  d: RuleDraft;
  set: (p: Partial<RuleDraft>) => void;
  kind: AlertRule['kind'];
  isDefault: boolean;
  nodes: string[];
}) {
  const { t } = useTranslation();
  const rangeHintId = useId();
  const dur = (
    <span className={selectCls}>
      <Listbox value={d.forDuration} options={DURATIONS.map((v) => ({ value: v, label: v === '0s' ? t('alerts.form.immediate') : durLabel(t, v) }))} onChange={(v) => set({ forDuration: v as AlertRuleDuration })} aria-label={t('alerts.rules.formDuration')} width="w-auto" />
    </span>
  );
  if (kind === 'chain_slow') {
    // The multiplier is editable: the page's "Blocks slowing down" / "Blocks severely slow or stopped" reads this number
    return (
      <p className="flex flex-wrap items-center gap-2 text-xs text-ink-2">
        {t('alerts.form.slowPre')}
        <input type="number" inputMode="decimal" min={1} value={d.threshold} onChange={(e) => set({ threshold: e.target.value })}
          aria-label={t('alerts.rules.formThreshold')} className={`w-20 font-mono ${inputCls}`} />
        <span>{t('alerts.form.slowPost')}</span>{d.forDuration !== ('0s' as string) && <span>{t('alerts.form.for')}</span>}{dur}
      </p>
    );
  }
  if (kind !== 'threshold') {
    return <p className="flex flex-wrap items-center gap-2 text-xs text-ink-2"><Fixed>{t(`alerts.kind.${kind}`)}</Fixed>{d.forDuration !== ('0s' as string) && t('alerts.form.for')}{dur}</p>;
  }
  const unit = metricUnit(t, d.metric);
  const range = thresholdRange(d.metric);
  const outOfRange = d.threshold.trim() !== '' && !thresholdOk(d);
  return (
    <p className="flex flex-wrap items-center gap-2 text-xs text-ink-2">
      {t('alerts.form.when')}
      {isDefault ? (
        <Fixed>{t('alerts.rules.allNodes')}</Fixed>
      ) : (
        <span className={selectCls}>
          <Listbox
            value={d.nodeId}
            options={[{ value: '', label: t('alerts.rules.allNodes') }, ...nodes.map((n) => ({ value: n, label: n }))]}
            onChange={(v) => set({ nodeId: v })}
            aria-label={t('alerts.rules.formNode')}
          />
        </span>
      )}
      {t('alerts.form.of')}
      {isDefault ? (
        <Fixed>{metricLabel(t, d.metric)} {OPERATOR_SYMBOL[d.operator]}</Fixed>
      ) : (
        <>
          <span className={selectCls}>
            <Listbox value={d.metric} options={METRIC_OPTIONS.map(([m, key]) => ({ value: m, label: t(key) }))} onChange={(v) => set({ metric: v })} aria-label={t('alerts.rules.formMetric')} />
          </span>
          <span className={selectCls}>
            <Listbox value={d.operator} options={OPERATORS.map((op) => ({ value: op, label: OPERATOR_SYMBOL[op] }))} onChange={(v) => set({ operator: v })} aria-label={t('alerts.rules.formOperator')} width="w-16" />
          </span>
        </>
      )}
      <span className="inline-flex items-center gap-1">
        <input
          type="number"
          inputMode="decimal"
          min={range.min}
          max={range.max}
          value={d.threshold}
          onChange={(e) => set({ threshold: e.target.value })}
          aria-label={t('alerts.rules.formThreshold')}
          aria-invalid={!thresholdOk(d)}
          aria-describedby={outOfRange ? rangeHintId : undefined}
          className={`w-24 font-mono ${inputCls}`}
        />
        {unit && <span className="text-ink-3">{unit}</span>}
      </span>
      {d.forDuration !== ('0s' as string) && t('alerts.form.for')}
      {dur}
      {/* Error goes at the end of the sentence so the preceding text doesn't shift */}
      {outOfRange && (
        <span id={rangeHintId} className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>
          {t('alerts.form.thresholdRange', { range: rangeText(d.metric) })}
        </span>
      )}
    </p>
  );
}

// Recovery comparison: the firing condition reversed (fires on > means it recovers only when back to <= the recovery threshold)
const RECOVER_SYMBOL: Record<AlertRuleOperator, string> = { gt: '≤', gte: '<', lt: '≥', lte: '>' };

/**
 * The recovery condition reads as one sentence under the firing condition: for threshold rules "recovers only once back to
 * <= [recovery threshold] for [recovery wait]"; other rules have no recovery threshold, only "recovers once the condition
 * clears for [recovery wait]". Recovery wait 0 = recover as soon as the condition clears.
 */
function RecoverySentence({ d, set, kind }: { d: RuleDraft; set: (p: Partial<RuleDraft>) => void; kind: AlertRule['kind'] }) {
  const { t } = useTranslation();
  const errId = useId();
  const wait = d.keepFiringFor !== ('0s' as string);
  const keep = (
    <span className={selectCls}>
      <Listbox value={d.keepFiringFor} options={DURATIONS.map((v) => ({ value: v, label: v === '0s' ? t('alerts.form.recoverImmediate') : durLabel(t, v) }))} onChange={(v) => set({ keepFiringFor: v as AlertRuleDuration })} aria-label={t('alerts.form.recoverWait')} width="w-auto" />
    </span>
  );
  const tail = <>{wait && <span>{t('alerts.form.for')}</span>}{keep}{wait && t('alerts.form.recoverPost') && <span>{t('alerts.form.recoverPost')}</span>}</>;
  if (kind !== 'threshold') {
    return <p className="flex flex-wrap items-center gap-2 text-xs text-ink-2">{t('alerts.form.recoverCleared')}{tail}</p>;
  }
  const unit = metricUnit(t, d.metric);
  const err = clearError(d);
  return (
    <p className="flex flex-wrap items-center gap-2 text-xs text-ink-2">
      {t('alerts.form.recoverPre')}
      <Fixed>{RECOVER_SYMBOL[d.operator]}</Fixed>
      <span className="inline-flex items-center gap-1">
        <input
          type="number"
          inputMode="decimal"
          min={thresholdRange(d.metric).min}
          max={thresholdRange(d.metric).max}
          value={d.clear}
          placeholder={d.threshold}
          onChange={(e) => set({ clear: e.target.value })}
          aria-label={t('alerts.form.recoverThreshold')}
          aria-invalid={!!err}
          aria-describedby={err ? errId : undefined}
          className={`w-24 font-mono ${inputCls}`}
        />
        {unit && <span className="text-ink-3">{unit}</span>}
      </span>
      {tail}
      {err && (
        <span id={errId} className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>
          {err === 'range' ? t('alerts.form.thresholdRange', { range: rangeText(d.metric) }) : t(d.operator === 'gt' || d.operator === 'gte' ? 'alerts.form.recoverAbove' : 'alerts.form.recoverBelow')}
        </span>
      )}
    </p>
  );
}

/** Preview: plot the last 24 hours with the draft's condition and mark nodes that would fire now. */
function Preview({ d, nodes }: { d: RuleDraft; nodes: string[] }) {
  const { t } = useTranslation();
  const ids = d.nodeId ? [d.nodeId] : nodes;
  const th = Number(d.threshold) || 0;
  const series = useRuleSeries(ids, d.metric, d.operator, th, PREVIEW_RANGE);
  return (
    <section className="flex flex-col gap-2 rounded-lg border border-hair bg-surface px-4 py-3">
      <div className="flex flex-wrap items-center gap-3 text-xs">
        <span className="font-medium text-ink-2">{t('alerts.form.preview', { metric: metricLabel(t, d.metric), range: PREVIEW_RANGE })}</span>
        <span className="ml-auto inline-flex items-center gap-1.5 text-ink-2">
          <span className="size-1.5 rounded-full" style={{ background: series.hotIds.length ? 'var(--bsos-status-error-fg)' : 'var(--bsos-status-healthy-fg)' }} />
          {series.hotIds.length ? (
            <span>
              <span className="font-mono">{new Intl.ListFormat(appLocale(), { type: 'conjunction' }).format(series.hotIds)}</span>
              {t('alerts.form.wouldFire')}
            </span>
          ) : t('alerts.form.wouldNotFire')}
        </span>
      </div>
      <RuleChart ids={ids} series={series} th={th} range={PREVIEW_RANGE} metric={d.metric} label={t('alerts.form.previewLabel')} height={200} />
    </section>
  );
}

function SeverityField({ d, set }: { d: RuleDraft; set: (p: Partial<RuleDraft>) => void }) {
  const { t } = useTranslation();
  return (
    <div className="flex items-center gap-3 text-xs text-ink-2">
      <span className="w-16 shrink-0 text-xs text-ink-3">{t('alerts.rules.formSeverity')}</span>
      {/* Dropdown (same as "for" in the condition sentence); red / orange dots indicate severity */}
      <Listbox
        value={d.severity}
        width="w-28"
        options={[
          { value: 'critical', label: t('alerts.rules.severity.critical'), dot: 'var(--bsos-status-error-fg)' },
          { value: 'warning', label: t('alerts.rules.severity.warning'), dot: 'var(--bsos-status-syncing-fg)' },
        ]}
        onChange={(v) => set({ severity: v })}
        aria-label={t('alerts.rules.formSeverity')}
      />
    </div>
  );
}

function NameField({ d, set, placeholder }: { d: RuleDraft; set: (p: Partial<RuleDraft>) => void; placeholder: string }) {
  const { t } = useTranslation();
  return (
    <label className="flex items-center gap-3 text-xs">
      <span className="w-16 shrink-0 text-xs text-ink-3">{t('alerts.rules.formName')}</span>
      <input value={d.name} onChange={(e) => set({ name: e.target.value })} placeholder={placeholder} className={`w-full max-w-md ${inputCls}`} />
    </label>
  );
}

function SaveError({ error }: { error: unknown }) {
  const { t } = useTranslation();
  return (
    <p role="alert" className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>
      {errMsg(error) || t('alerts.rules.saveError')}
    </p>
  );
}

/** The "Rule settings" tab of the rule page. */
/** savedAt / onSaved: the save time is kept by the parent. After saving, this component remounts because of its key (updatedAt) and loses its own state, so "Saved" must come back from the parent */
export function AlertRuleSettings({ rule, nodes, canWrite, savedAt = 0, onSaved }: { rule: AlertRule; nodes: string[]; canWrite: boolean; savedAt?: number; onSaved?: (at: number) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const saved = draftFromRule(rule);
  const [d, setD] = useState<RuleDraft>(saved);
  const set = (p: Partial<RuleDraft>) => setD((x) => ({ ...x, ...p }));
  const dirty = JSON.stringify(d) !== JSON.stringify(saved);
  useUnsavedGuard(dirty);

  const save = useMutation({
    mutationFn: () => api.updateAlertRule(rule.id, toUpdate(d, rule)),
    onSuccess: (r) => {
      onSaved?.(Date.now());
      qc.setQueryData<AlertRule[]>(['alertRules'], (l) => l?.map((x) => (x.id === r.id ? r : x)));
      setD(draftFromRule(r));
    },
  });
  const canSave = canWrite && dirty && (rule.kind !== 'threshold' ? rule.kind !== 'chain_slow' || Number(d.threshold) >= 1 : thresholdOk(d) && !clearError(d)) && !save.isPending;

  return (
    <div className="flex flex-col gap-4">
      {/* One card: the header's right side has the "Enabled" switch (disabled = turned off, the rule still exists); changes still need Save */}
      <Card
        title={t('alerts.rules.settingsTitle')}
        aside={
          <span className="flex items-center gap-2 text-ink-2">
            {t('alerts.rules.formEnabled')}
            <Switch on={d.enabled} onChange={(on) => canWrite && set({ enabled: on })} aria-label={t('alerts.rules.formEnabled')} />
          </span>
        }
      >
        <fieldset disabled={!canWrite} className="flex flex-col gap-4">
          {!rule.isDefault && <NameField d={d} set={set} placeholder={ruleCondition(t, draftCond(d))} />}
          <ConditionSentence d={d} set={set} kind={rule.kind} isDefault={rule.isDefault} nodes={nodes} />
          <RecoverySentence d={d} set={set} kind={rule.kind} />
          <SeverityField d={d} set={set} />
        </fieldset>
      </Card>

      {rule.kind === 'threshold' && thresholdOk(d) && <Preview d={d} nodes={nodes} />}

      {canWrite && (
        // Disabling = unchecking "Enabled" above; delete (for rules added by Enterprise users) is in the page header, not crowded next to Save
        <div className="flex flex-wrap items-center gap-3">
          <span className="ml-auto flex items-center gap-3">
            {/* Saved: "Discard" is already hidden, and "Saved" sits right left of the Save button */}
            <SavedNote at={savedAt} />
            {save.isError && <SaveError error={save.error} />}
            {/* Hide "Discard" as soon as Save is pressed since saving is underway; afterwards only the Save button's label changes and nothing beside it moves */}
            {dirty && !save.isPending && <Button onClick={() => setD(saved)}>{t('alerts.form.discard')}</Button>}
            <SaveButton onClick={() => save.mutate()} pending={save.isPending} disabled={!canSave} label={t('alerts.rules.save')} pendingLabel={t('alerts.rules.saving')} />
          </span>
        </div>
      )}
    </div>
  );
}

/** Form body of the new-rule page. An empty name falls back to the condition text. */
export function AlertRuleCreateForm({ nodes }: { nodes: string[] }) {
  const { t } = useTranslation();
  const nav = useNavigate();
  const qc = useQueryClient();
  const [d, setD] = useState<RuleDraft>(emptyDraft());
  const set = (p: Partial<RuleDraft>) => setD((x) => ({ ...x, ...p }));
  const fallbackName = ruleCondition(t, draftCond(d));
  useUnsavedGuard(JSON.stringify(d) !== JSON.stringify(emptyDraft()));

  const create = useMutation({
    mutationFn: () => {
      const req: AlertRuleCreateRequest = {
        name: d.name.trim() || fallbackName, metric: d.metric, nodeId: d.nodeId || undefined, operator: d.operator,
        threshold: Number(d.threshold), forDuration: d.forDuration, severity: d.severity, enabled: d.enabled,
        keepFiringFor: d.keepFiringFor, clearThreshold: clearValue(d),
      };
      return createRule ? createRule(req) : Promise.reject(new Error('createAlertRule: Enterprise only'));
    },
    onSuccess: (r) => {
      // Put it in the cache before navigating; otherwise the rule page gets the old list and briefly flashes "rule not found"
      qc.setQueryData<AlertRule[]>(['alertRules'], (l) => [...(l ?? []), r]);
      nav(`/alerts/rules/${encodeURIComponent(r.id)}`, { replace: true, ...LEAVE_WITHOUT_ASKING });
    },
  });

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-4 rounded-lg border border-hair bg-surface px-4 py-3">
        <NameField d={d} set={set} placeholder={fallbackName} />
        <ConditionSentence d={d} set={set} kind="threshold" isDefault={false} nodes={nodes} />
        <RecoverySentence d={d} set={set} kind="threshold" />
        <SeverityField d={d} set={set} />
      </div>
      {thresholdOk(d) && <Preview d={d} nodes={nodes} />}
      <div className="flex items-center justify-end gap-3">
        {create.isError && <SaveError error={create.error} />}
        <Button onClick={() => nav('/alerts', LEAVE_WITHOUT_ASKING)}>{t('alerts.rules.cancel')}</Button>
        {/* Open-source edition: button disabled, with the commercial-edition tag beside it (click to open the explanation) */}
        <Button variant="primary" onClick={() => create.mutate()} disabled={!createRule || !thresholdOk(d) || !!clearError(d) || create.isPending}>
          {create.isPending ? t('alerts.rules.saving') : t('alerts.form.create')}
        </Button>
        {!createRule && <EnterpriseTeaser slug="custom-rules" tagOnly />}
      </div>
    </div>
  );
}
