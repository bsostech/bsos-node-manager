import { Link, useLocation } from 'react-router-dom';
import { useState } from 'react';
import type { Icon } from '@phosphor-icons/react';
import { EditionTag, EnterpriseDialog } from '@/components/EnterpriseTeaser';
import { HelpTip } from '@/components/HelpTip';
import type { EnterpriseFeature } from '@/lib/enterpriseFeatures';

export interface PageTab {
  to: string;
  label: string;
  icon: Icon;
  /** Commercial-edition tab shown in the open-source edition: does not navigate; clicking opens an explanatory popup */
  locked?: EnterpriseFeature;
}

/**
 * Shared page header under one sidebar entry: title (+ one-line description, actions on the right) + sibling tabs. Each tab is its own route,
 * sharing one tab bar (each tab is a <Link>, active = current route). No tab bar is drawn when there is only one tab.
 * Tab bar styling follows NodeSettings/AccessNode (border-b + border-b-2 active accent).
 * Unsaved drafts are caught by the site-wide UnsavedBlocker (useUnsavedGuard); nothing extra here.
 */
export function PageTabs({ title, subtitle, help, action, tabs }: { title: string; subtitle?: string; /** ⓘ next to the subtitle: for what one sentence cannot cover but is easy to misread */ help?: React.ReactNode; action?: React.ReactNode; tabs: PageTab[] }) {
  const { pathname } = useLocation();

  return (
    <div className="flex flex-col gap-4">
      <header className="flex flex-wrap items-center gap-3">
        <div className="flex flex-col gap-0.5">
          <h1 className="text-lg font-semibold text-ink">{title}</h1>
          {subtitle && <span className="flex items-center gap-1 text-xs text-ink-3">{subtitle}{help && <HelpTip title={title}>{help}</HelpTip>}</span>}
        </div>
        {action && <div className="ml-auto flex items-center gap-2">{action}</div>}
      </header>

      {/* tab bar (same as monitoring pages) */}
      {tabs.length > 1 && (
      <div className="flex gap-1 border-b border-hair">
        {tabs.map((tb) => {
          if (tb.locked) return <LockedTab key={tb.to} tab={tb} slug={tb.locked} />;
          const active = pathname === tb.to;
          return (
            <Link
              key={tb.to}
              to={tb.to}
              className={`-mb-px flex items-center gap-1.5 border-b-2 px-3 py-2 text-sm transition-colors ${
                active ? 'border-accent text-accent font-medium' : 'border-transparent text-ink-2 hover:text-ink'
              }`}
            >
              <tb.icon size={16} weight={active ? 'fill' : 'regular'} aria-hidden />
              {tb.label}
            </Link>
          );
        })}
      </div>
      )}

    </div>
  );
}

/** Locked commercial-edition tab: in the same row as normal tabs, text one shade lighter, with the commercial-edition badge; clicking opens an explanatory popup instead of navigating. */
export function LockedTab({ tab, slug }: { tab: Pick<PageTab, 'label' | 'icon'>; slug: EnterpriseFeature }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="-mb-px flex items-center gap-1.5 border-b-2 border-transparent px-3 py-2 text-sm text-ink-3 transition-colors hover:text-ink-2"
      >
        <tab.icon size={16} aria-hidden />
        {tab.label}
        <EditionTag />
      </button>
      {open && <EnterpriseDialog slug={slug} onClose={() => setOpen(false)} />}
    </>
  );
}
