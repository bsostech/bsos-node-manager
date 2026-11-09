import * as Select from '@radix-ui/react-select';
import { useTranslation } from 'react-i18next';
import { CaretDown, Check } from '@phosphor-icons/react';
import type { NodeSummary } from '@/api/schemas';
import type { Tone } from '@/lib/status';

/**
 * Other organizations in the overview node table (Enterprise's external organizations). Core only declares the shape and renders it, without knowing what each status means:
 * status text, description lines and the empty state are all supplied by whoever provides the data.
 */
export interface OrgGroup {
  org: string;
  nodes: NodeSummary[];
  hrefFor: (n: NodeSummary) => string;
  tone: Tone;
  statusLabel: string;
  /** One-line note above the table when this organization is selected (e.g. how long it has been unreachable). */
  note?: string;
  /** Reason shown in place of the table when there are no nodes to list. */
  emptyText?: string;
}

/** What the organization filter has selected. */
export type OrgSel = { kind: 'own' } | { kind: 'all' } | { kind: 'org'; org: string };

/** URL param org <-> OrgSel: no param = our org, '*' = all organizations, anything else = external organization id; an unknown id (organization deleted) falls back to our org. */
export const orgSelFromParam = (raw: string, groups: OrgGroup[]): OrgSel =>
  raw === '*' ? { kind: 'all' } : groups.some((g) => g.org === raw) ? { kind: 'org', org: raw } : { kind: 'own' };
export const orgSelToParam = (s: OrgSel) => (s.kind === 'all' ? '*' : s.kind === 'org' ? s.org : '');

// Dropdown option values (Radix does not accept empty strings): external organizations get a prefix so they cannot clash with own / all
const toItem = (s: OrgSel) => (s.kind === 'org' ? `org:${s.org}` : s.kind);
const fromItem = (v: string): OrgSel => (v.startsWith('org:') ? { kind: 'org', org: v.slice(4) } : { kind: v === 'all' ? 'all' : 'own' });

export const StatusDot = ({ tone }: { tone: Tone }) => <span aria-hidden className="size-1.5 shrink-0 rounded-full" style={{ background: `var(--bsos-status-${tone}-fg)` }} />;

/** Organization dropdown: same family as Seg (gray framed box, text-xs); external organizations carry a status dot and status text. */
export function OrgFilter({ value, onChange, ownOrg, ownCount, groups, countOf }: {
  value: OrgSel;
  onChange: (v: OrgSel) => void;
  ownOrg?: string;
  ownCount: number;
  groups: OrgGroup[];
  /** Number next to each option; defaults to the node count (overview). The peer list passes peer counts. */
  countOf?: (s: OrgSel) => number;
}) {
  const { t } = useTranslation();
  const cur = value.kind === 'org' ? groups.find((g) => g.org === value.org) : undefined;
  const curLabel = value.kind === 'all' ? t('overview.orgAll') : cur ? cur.org : t('overview.orgOwn');
  const total = ownCount + groups.reduce((n, g) => n + g.nodes.length, 0);
  const item = (s: OrgSel, label: React.ReactNode, count: number, sub?: React.ReactNode) => (
    <Select.Item
      key={toItem(s)}
      value={toItem(s)}
      className="grid w-full cursor-default select-none grid-cols-[1fr_auto_0.75rem] items-center gap-3 px-2.5 py-1.5 text-left text-xs text-ink-2 focus:outline-none data-[highlighted]:bg-elevated data-[state=checked]:text-ink"
    >
      <span className="flex min-w-0 flex-col gap-0.5">
        <Select.ItemText>{label}</Select.ItemText>
        {sub}
      </span>
      <span className="font-mono text-ink-3">{countOf ? countOf(s) : count}</span>
      <Select.ItemIndicator><Check size={12} /></Select.ItemIndicator>
    </Select.Item>
  );
  return (
    <Select.Root value={toItem(value)} onValueChange={(v) => onChange(fromItem(v))}>
      <Select.Trigger
        aria-label={t('overview.orgLabel')}
        className="group inline-flex items-center rounded-md border border-hair bg-elevated p-0.5 text-xs focus:outline-none focus-visible:ring-1 focus-visible:ring-accent"
      >
        <span className="inline-flex items-center gap-1.5 rounded px-2.5 py-1 transition-colors group-hover:bg-surface group-data-[state=open]:bg-surface">
          <span className="text-ink-3">{t('overview.orgLabel')}</span>
          <span className="font-medium text-ink"><Select.Value>{curLabel}</Select.Value></span>
          {cur && cur.tone !== 'healthy' && <StatusDot tone={cur.tone} />}
          <CaretDown size={11} className="text-ink-3 transition-transform group-data-[state=open]:rotate-180" />
        </span>
      </Select.Trigger>
      <Select.Portal>
        <Select.Content position="popper" sideOffset={4} className="pop-in z-50 max-h-80 min-w-64 overflow-hidden rounded-md border border-hair bg-surface shadow-lg">
          <Select.Viewport className="overflow-y-auto py-1">
            {item({ kind: 'all' }, t('overview.orgAll'), total)}
            {item({ kind: 'own' }, <>{t('overview.orgOwn')}{ownOrg && <span className="ml-1.5 font-mono text-ink-3">{ownOrg}</span>}</>, ownCount)}
            <Select.Separator className="my-1 h-px bg-hair" />
            {groups.map((g) => item({ kind: 'org', org: g.org }, <span className="font-mono">{g.org}</span>, g.nodes.length,
              <span className="inline-flex items-center gap-1.5 text-2xs text-ink-3"><StatusDot tone={g.tone} />{g.statusLabel}</span>))}
          </Select.Viewport>
        </Select.Content>
      </Select.Portal>
    </Select.Root>
  );
}
