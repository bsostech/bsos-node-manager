import {
  SquaresFour,
  Pulse,
  BellSimple,
  Tray,
  ShieldCheck,
  ListChecks,
  ShareNetwork,
  Key,
  SlidersHorizontal,
  
  IdentificationBadge,
  SignIn,
  type IconProps,
} from '@phosphor-icons/react';
import type { ComponentType } from 'react';
import type { Permission } from '@/auth/permissions';
import { enterprise, isEnterprise } from '@/edition';
import { ENTERPRISE_FEATURES, featurePath, type EnterpriseFeature } from '@/lib/enterpriseFeatures';

export interface NavItem {
  to: string;
  key: string;
  icon: ComponentType<IconProps>;
  end?: boolean;
  status: 'live' | 'enterprise'; // enterprise = commercial feature visible but not usable in the open-source edition (points to the next step)
  perm?: Permission | Permission[]; // absent = visible to every logged-in user; present = requires that permission, array = any one suffices (RBAC gating, UX layer)
  /** Other URLs that also count as "under this item" (e.g. node detail belongs to my nodes, node settings belong to node registration and settings) */
  match?: RegExp;
}
export interface NavGroup {
  caption: string;
  items: NavItem[];
}

/**
 * Locked commercial items in the open-source sidebar: same icon as the intro page, clicking goes to /enterprise/<slug>.
 * By default they follow "view monitoring": operators and viewers see them; admins only manage accounts, so these features are not for them and are not listed.
 * Account-related ones (enterprise identity integration) pass members:manage instead, admins only.
 */
const locked = (slug: EnterpriseFeature, key: string, perm: Permission = 'monitor:view'): NavItem => (
  { to: featurePath(slug), key, icon: ENTERPRISE_FEATURES[slug].icon, status: 'enterprise', perm }
);

/** Sidebar. live = implemented; enterprise = commercial feature shown with an intro page. Shared by AppShell and the router. */
const coreNavGroups: NavGroup[] = [
  {
    caption: 'nav.secMonitoring',
    items: [
      { to: '/chain', key: 'nav.chain', icon: Pulse, status: 'live', perm: 'monitor:view' },
      // Node detail and its tx pool account page both count as "My nodes"
      { to: '/nodes', key: 'nav.overview', icon: SquaresFour, end: true, status: 'live', perm: 'monitor:view', match: /^\/nodes\/[^/]+(\/txpool(\/.*)?)?\/?$/ },
      { to: '/notifications', key: 'nav.notifications', icon: Tray, status: 'live', perm: 'monitor:view' },
      // Commercial features: the open-source edition lists them locked (commercial badge), clicking opens the intro page; the Enterprise build has its own items.
      // Ordered by how incidents are handled: check status -> get an alert -> respond -> fail over if needed; cross-org monitoring goes last
      ...(isEnterprise ? [] : [
        locked('incident', 'nav.incident'),
        locked('failover', 'nav.failover'),
        locked('cross-org', 'nav.crossOrgMonitor'),
      ]),
    ],
  },
  {
    // Consortium governance: things tied to consortium rules (who can be a validator, who can send transactions through us)
    caption: 'nav.secOperations',
    items: [
      { to: '/validators', key: 'nav.validators', icon: ShieldCheck, status: 'live', perm: 'validators:view' },
      { to: '/access/permissioning', key: 'nav.allowlist', icon: ListChecks, status: 'live', perm: 'monitor:view' },
    ],
  },
  // Open-source edition: the commercial-only section, placed between consortium governance and platform settings (not hidden at the bottom); every item is a locked intro page
  ...(isEnterprise ? [] : [
    { caption: 'nav.secSecurity', items: [locked('audit', 'nav.auditCenter'), locked('node-keys', 'nav.nodeKeys')] },
  ]),
  {
    caption: 'nav.secSettings',
    items: [
      // Platform settings: only things about our own org (how nodes join, how they are configured, who can call them, how alerts notify); who can log in is under user management below.
      // Viewers can also see these pages (monitor:view) but only read: each page checks nodes:onboard separately for writes
      { to: '/node-join', key: 'nav.nodeSettings', icon: SlidersHorizontal, end: true, status: 'live', perm: 'monitor:view', match: /^\/(nodes\/[^/]+\/settings|node-add)/ }, // node registration is entered from the node management page header, not listed separately
      { to: '/access', key: 'nav.access', icon: Key, end: true, status: 'live', perm: 'monitor:view', match: /^\/access\/rpc-users\// },
      // Peering: who our nodes connect out to is our own network setting (no other org's consent needed); the outbound counterpart to node access settings
      { to: '/node-join/peers', key: 'nav.peering', icon: ShareNetwork, status: 'live', perm: 'monitor:view' },
      // Alerts are events, not a feature: rules and notification channels are settings and live here; incidents surface through the top bell and the notification channels
      { to: '/alerts', key: 'nav.alertSettings', icon: BellSimple, status: 'live', perm: 'monitor:view' },
    ],
  },
  {
    // User management: who can log in and with which role. This is all admins manage, so it is the section they see in the sidebar
    caption: 'nav.secUsers',
    items: [
      { to: '/identity', key: 'nav.identity', icon: IdentificationBadge, status: 'live', perm: 'members:manage' },
      // Login events: who logged in when, success or failure (not an audit log; admin change history is, and that is commercial)
      { to: '/login-history', key: 'nav.loginEvents', icon: SignIn, status: 'live', perm: 'members:manage' },
      // Open-source edition: enterprise identity integration (SSO, LDAP / AD) is commercial, shown as a locked intro; in the Enterprise build it is a tab of the account and permission management page
      ...(isEnterprise ? [] : [locked('identity', 'edition.identity.title', 'members:manage')]),
    ],
  },
];

/** Core sidebar + Enterprise items (each appended to its target section) and Enterprise's own sections (placed last), see src/edition.ts. */
export const navGroups: NavGroup[] = [
  ...coreNavGroups.map((g) => ({
    ...g,
    items: [...g.items, ...enterprise.navItems.filter((e) => e.group === g.caption).map((e) => e.item)],
  })),
  ...enterprise.navGroups,
];
