import { useState, type ReactNode } from 'react';
import { NavLink, useNavigate, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import * as Popover from '@radix-ui/react-popover';
import { ArrowClockwise, ArrowLineLeft, ArrowLineRight, CaretDown, Moon, SignOut, Sun, Warning } from '@phosphor-icons/react';
import { Modal, ModalTitle } from './Modal';
import { Button } from './Button';
import { ABOUT } from '@/about';
import { AlertBell } from './AlertBell';
import { Logo } from './Logo';
import { navGroups, type NavItem } from '@/nav';
import { isEnterprise } from '@/edition';
import { useTheme } from '@/theme/ThemeProvider';
import { setLang } from '@/i18n';
import { useAuth } from '@/auth/AuthContext';
import { platformDownKey } from '@/lib/platformDown';
import { TextButton } from '@/components/TextLinks';
import { EditionTag } from '@/components/EnterpriseTeaser';
import { roleLabel } from '@/auth/permissions';

function Item({ item, collapsed }: { item: NavItem; collapsed: boolean }) {
  const { t } = useTranslation();
  const { pathname } = useLocation();
  const pad = collapsed ? 'justify-center px-0' : 'px-3';
  // Commercial-edition features: text one step lighter + commercial-edition tag; still clickable, leading to that feature's intro page
  if (item.status === 'enterprise') {
    return (
      <NavLink
        to={item.to}
        title={`${t(item.key)} — ${t('nav.enterpriseOnly')}`}
        className={({ isActive }) => `flex items-center gap-2.5 rounded-md py-1.5 text-xs transition-colors ${pad} ${isActive ? 'bg-elevated text-ink-2' : 'text-ink-3 hover:bg-elevated hover:text-ink-2'}`}
      >
        <item.icon size={16} aria-hidden="true" />
        {!collapsed && <span className="min-w-0 flex-1 truncate">{t(item.key)}</span>}
        {!collapsed && <EditionTag />}
      </NavLink>
    );
  }
  return (
    <NavLink
      to={item.to}
      end={item.end}
      title={collapsed ? t(item.key) : undefined}
      className={({ isActive: exact }) => { const isActive = exact || !!item.match?.test(pathname); return (
        `flex items-center gap-2.5 rounded-md py-1.5 text-xs transition-colors ${pad} ${
          isActive
            ? 'bg-accent-soft text-accent-hover font-medium' // use the hover shade for text on soft: passes WCAG AA (accent itself is only 4.27:1)
            : 'text-ink-2 hover:bg-elevated hover:text-ink'
        }`
      ); }}
    >
      <item.icon size={16} aria-hidden="true" />
      {!collapsed && <span className="flex-1">{t(item.key)}</span>}
    </NavLink>
  );
}

/** Two-way choices in the menu (language, theme): small segmented buttons. */
// Selected language: light green background + dark green text (same in light and dark themes; like the theme switch, a fixed light chip)
const LANG_ON: React.CSSProperties = { background: '#D3EEDB', color: '#1E6B3A' };
// Background when the light theme is selected: pale blue sky + two layers of green hills (SVG along the bottom); the sun uses a darker gold so it stays visible on the light background
const DAY_HILLS = `<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 72 24' preserveAspectRatio='none'><path d='M0 20 L14 16 L26 19 L40 14.5 L56 19 L72 15.5 L72 24 L0 24 Z' fill='%23A8DDB0'/><path d='M0 22.5 L12 19.5 L24 22 L38 18.5 L52 22 L64 19.5 L72 21 L72 24 L0 24 Z' fill='%2357B46F'/></svg>`;
const DAY_STYLE: React.CSSProperties = {
  background: `url("data:image/svg+xml,${DAY_HILLS.replace(/</g, '%3C').replace(/>/g, '%3E')}") bottom / 100% 100% no-repeat, linear-gradient(#BFE3FF, #E8F5FF)`,
  color: '#E9A400',
};

/**
 * Small two-way switch (language, theme): both groups share the same frame width and height, each split into two equal cells.
 * The selected cell defaults to solid primary color; activeStyle overrides it when given (theme: sun on sky blue, moon on night sky).
 */
function Seg2<T extends string>({ value, options, onChange, activeStyle }: {
  value: T;
  options: [T, React.ReactNode, string?][];
  onChange: (v: T) => void;
  activeStyle?: Partial<Record<T, React.CSSProperties>>;
}) {
  return (
    <div className="grid w-36 shrink-0 grid-cols-2 rounded-md border border-hair p-0.5">
      {options.map(([v, label, title]) => {
        const on = v === value;
        const custom = on ? activeStyle?.[v] : undefined;
        return (
          <button
            key={v}
            type="button"
            aria-pressed={on}
            aria-label={title}
            title={title}
            onClick={() => onChange(v)}
            style={custom}
            className={`flex h-6 items-center justify-center rounded text-xs transition-colors focus:outline-none focus-visible:ring-1 focus-visible:ring-accent ${on ? (custom ? 'font-medium' : 'bg-accent font-medium text-white') : 'text-ink-3 hover:text-ink'}`}
          >
            {label}
          </button>
        );
      })}
    </div>
  );
}

/** Top-right user menu: normally shows only "account · role"; language, theme and logout appear when expanded - language and theme are rarely changed after the first time, so they don't take prime space. */
function UserMenu() {
  const { t, i18n } = useTranslation();
  const { me, logout } = useAuth();
  const { theme, toggle } = useTheme();
  const nav = useNavigate();
  if (!me) return null;
  const lang = i18n.language === 'zh' ? 'zh' : 'en';
  return (
    <Popover.Root>
      <Popover.Trigger asChild>
        <button
          type="button"
          className="flex items-center gap-2 rounded-md px-2 py-1.5 text-xs transition-colors hover:bg-elevated focus:outline-none focus-visible:ring-1 focus-visible:ring-accent data-[state=open]:bg-elevated"
        >
          <span className="text-ink">{me.user.name}</span>
          <span className="text-ink-3">
            {/* SSO accounts: display names may collide, only the account identifies who it is (the backend falls back to sub when the IdP gives no account); local accounts don't have this part */}
            {me.user.account && <><span className="font-mono">{me.user.account}</span>{' · '}</>}
            {roleLabel(t, me.role)}{isEnterprise && <> · {me.org}</>}
          </span>
          <CaretDown size={12} aria-hidden="true" className="text-ink-3" />
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          align="end"
          side="bottom"
          sideOffset={6}
          collisionPadding={16}
          onOpenAutoFocus={(e) => e.preventDefault()} // when opened by mouse, don't move focus to the first button (it would draw a keyboard focus ring); Tab still works for keyboard users
          className="pop-in z-50 flex w-64 flex-col gap-1 rounded-lg border border-hair bg-surface p-2 text-xs shadow-lg focus:outline-none"
        >
          <div className="flex items-center justify-between gap-3 px-2 py-1.5">
            <span className="text-xs text-ink-2">{t('menu.language')}</span>
            {/* Selected: light green background + dark green text (not solid primary with white text; a light background like the theme switch) */}
            <Seg2 value={lang} options={[['zh', t('lang.zh')], ['en', t('lang.en')]]} onChange={setLang}
              activeStyle={{ zh: LANG_ON, en: LANG_ON }} />
          </div>
          <div className="flex items-center justify-between gap-3 px-2 py-1.5">
            <span className="text-xs text-ink-2">{t('menu.theme')}</span>
            {/* The selected icon turns solid; the sun is bright yellow on daytime sky blue, the moon is moon yellow on night sky - both bright on dark so the icon pops (same colors in light and dark themes) */}
            <Seg2
              value={theme}
              options={[
                ['light', <Sun size={14} weight={theme === 'light' ? 'fill' : 'regular'} aria-hidden="true" />, t('theme.light')],
                ['dark', <Moon size={14} weight={theme === 'dark' ? 'fill' : 'regular'} aria-hidden="true" />, t('theme.dark')],
              ]}
              activeStyle={{ light: DAY_STYLE, dark: { background: '#1D2742', color: '#F5D061' } }}
              onChange={(v) => { if (v !== theme) toggle(); }}
            />
          </div>
          <div className="my-1 h-px bg-hair" />
          <button
            type="button"
            onClick={() => { void logout(); nav('/', { replace: true }); }}
            className="flex items-center gap-2 rounded-md px-2 py-1.5 text-left text-xs text-ink transition-colors hover:bg-elevated"
          >
            <SignOut size={15} aria-hidden="true" className="text-ink-3" />
            {t('common.logout')}
          </button>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

/** Top banner when the platform is unreachable (the flag is set by the QueryCache in main.tsx and cleared by any successful query). Retry = refetch the queries on screen. */
function PlatformDownBanner() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { data: down } = useQuery({ queryKey: platformDownKey, queryFn: () => false, enabled: false, initialData: false });
  if (!down) return null;
  return (
    <div
      role="alert"
      className="flex shrink-0 items-center gap-2 border-b border-hair px-6 py-2 text-sm text-ink-2"
      style={{ background: 'var(--bsos-status-error-bg)' }}
    >
      <Warning size={16} style={{ color: 'var(--bsos-status-error-fg)' }} aria-hidden="true" />
      {t('error.platformDown')}
      <TextButton onClick={() => void qc.refetchQueries({ type: 'active' })}>
        <ArrowClockwise size={14} aria-hidden="true" /> {t('common.retry')}
      </TextButton>
    </div>
  );
}

/** Sidebar collapse toggle: placed in the logo row (the sidebar's own top), not mixed with the main area's controls. */
function CollapseButton({ collapsed, onClick, className = '' }: { collapsed: boolean; onClick: () => void; className?: string }) {
  const { t } = useTranslation();
  const label = t(collapsed ? 'nav.expand' : 'nav.collapse');
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      title={label}
      className={`inline-flex size-8 items-center justify-center rounded-md text-ink-3 transition-colors hover:bg-elevated hover:text-ink ${className}`}
    >
      {collapsed ? <ArrowLineRight size={16} aria-hidden="true" /> : <ArrowLineLeft size={16} aria-hidden="true" />}
    </button>
  );
}

const COLLAPSED_KEY = 'bsos-sidebar-collapsed';
const readCollapsed = () => { try { return localStorage.getItem(COLLAPSED_KEY) === '1'; } catch { return false; } };

/** About: the open-source software's version card - version, license, release date, source code. Data lives in src/about.ts. */
function AboutModal({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const link = 'text-accent hover:underline underline-offset-2';
  const rows: [string, ReactNode][] = [
    [t('about.version'), `v${ABOUT.version}`],
    [t('about.released'), ABOUT.releasedAt],
    [t('about.license'), <a href={ABOUT.licenseUrl} target="_blank" rel="noreferrer" className={link}>{ABOUT.license}</a>],
    [t('about.source'), <a href={ABOUT.sourceUrl} target="_blank" rel="noreferrer" className={`${link} break-all`}>{ABOUT.sourceUrl.replace(/^https?:\/\//, '')}</a>],
  ];
  return (
    <Modal width="max-w-xs" contentClass="gap-3 rounded-lg border border-hair bg-surface p-4 shadow-xl" onClose={onClose}>
      <ModalTitle>{t('app.name')}</ModalTitle>
      <dl className="grid grid-cols-[auto_1fr] gap-x-5 gap-y-1.5 text-xs">
        {rows.map(([k, v]) => (
          <div key={k} className="contents">
            <dt className="text-xs text-ink-3">{k}</dt>
            <dd className="text-ink">{v}</dd>
          </div>
        ))}
      </dl>
      <p className="text-xs text-ink-3">{ABOUT.copyright}</p>
      <div className="flex justify-end"><Button onClick={onClose}>{t('common.close')}</Button></div>
    </Modal>
  );
}

export function AppShell({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const { can } = useAuth();
  // Sidebar collapse: icons only, remembered in localStorage (per browser)
  const [collapsed, setCollapsed] = useState(readCollapsed);
  const toggleCollapsed = () => setCollapsed((c) => { try { localStorage.setItem(COLLAPSED_KEY, c ? '0' : '1'); } catch { /* private mode: ignore */ } return !c; });
  const [about, setAbout] = useState(false);

  return (
    <div className="flex h-full">
      {/* Sidebar */}
      <aside className={`flex shrink-0 flex-col border-r border-hair bg-surface transition-[width] ${collapsed ? 'w-14' : 'w-56'}`}>
        <div className={`flex h-14 items-center border-b border-hair ${collapsed ? 'justify-center' : 'pl-5 pr-2'}`}>
          <Logo variant={collapsed ? 'mark' : 'horizontal'} className="h-[1.625rem] w-auto" />
          {!collapsed && <CollapseButton collapsed={false} onClick={toggleCollapsed} className="ml-auto" />}
        </div>
        {collapsed && (
          <div className="flex justify-center border-b border-hair py-1.5">
            <CollapseButton collapsed onClick={toggleCollapsed} />
          </div>
        )}

        <nav className={`flex flex-1 flex-col gap-4 overflow-y-auto ${collapsed ? 'p-2' : 'p-3'}`}>
          {navGroups.map((g) => {
            const items = g.items.filter((i) => !i.perm || can(i.perm)); // RBAC: hide features the user has no permission for
            if (items.length === 0) return null;
            return (
              <div key={g.caption} className="flex flex-col gap-0.5">
                {collapsed ? (
                  <span className="mx-auto mb-1 h-px w-6 bg-hair" aria-hidden="true" />
                ) : (
                  <span className="px-3 pb-1 text-2xs font-medium uppercase tracking-wider text-ink-3">{t(g.caption)}</span>
                )}
                {items.map((item) => (
                  <Item key={item.to} item={item} collapsed={collapsed} />
                ))}
              </div>
            );
          })}
        </nav>

        {/* Bottom: version (opens About); hidden when collapsed. One size smaller than regular small text - just a label, not competing with the sidebar */}
        {!collapsed && (
          // flex: keeps the small-text button from being pushed down by the normal line-height baseline; extra bottom space so text doesn't touch the window edge
          <div className="flex items-center border-t border-hair px-3 pb-2 pt-1.5">
            <button type="button" onClick={() => setAbout(true)} className="rounded px-1.5 py-px text-[0.5625rem] text-ink-3 transition-colors hover:bg-elevated hover:text-ink">
              {t('app.name')} v{ABOUT.version}
            </button>
          </div>
        )}
        {about && <AboutModal onClose={() => setAbout(false)} />}
      </aside>

      {/* Main area */}
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 shrink-0 items-center gap-3 border-b border-hair bg-surface px-6">
          <div className="ml-auto flex items-center gap-1">
            {can('monitor:view') && <AlertBell />}
            <UserMenu />
          </div>
        </header>
        <PlatformDownBanner />
        {/* relative: sr-only is position:absolute; without this positioned ancestor it falls back to the initial containing block,
            and its static position (deep in the page) counts toward <html>'s scrollHeight - adding a stretch of scrollable blank space to the window. */}
        {/* Always reserve scrollbar space: as content grows or shrinks (expanding notifications, filtering) the scrollbar appears / disappears, and with width-taking scrollbars like Windows' the whole screen shifts sideways */}
        <main className="relative flex-1 overflow-auto p-6 [scrollbar-gutter:stable]">{children}</main>
      </div>
    </div>
  );
}
