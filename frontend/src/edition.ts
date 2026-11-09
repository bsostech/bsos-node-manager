import type { Permission } from '@/auth/permissions';
import type { ReactElement } from 'react';
import type { Icon } from '@phosphor-icons/react';
import type { NavGroup, NavItem } from '@/nav';
import type { OrgGroup } from '@/components/OrgFilter';
import type { AlertRule, AlertRuleCreateRequest } from '@/api/schemas';

/**
 * Enterprise frontend extension point. If src/enterprise/ exists, its index.ts is loaded;
 * the open-source edition has no such directory, the glob yields an empty object and everything here is empty. Core code does not need to know what Enterprise has.
 * The role table and strings do not go through here; permissions.ts and i18n/index.ts glob the plain data files directly,
 * because Enterprise pages themselves import permissions.ts and routing through here would create a circular import.
 */
export interface EnterpriseModule {
  /** perm: the permission required to enter (same as the sidebar perm); omitted = any logged-in user. */
  routes: { path: string; element: ReactElement; perm?: Permission | Permission[] }[];
  /** Which sidebar section to attach to (caption in nav.ts); placed after that section's existing items. */
  navItems: { group: string; item: NavItem }[];
  /** Enterprise's own sidebar sections, placed after all Core sections. */
  navGroups: NavGroup[];
  /** Tabs under the "Accounts & permissions" entry (each its own route, placed after local users). */
  identityTabs: { to: string; labelKey: string; icon: Icon }[];
  /** Other orgs (external orgs) for the overview node table: when provided, merged into the same table with an extra org filter.
   *  useGroups is a hook (called on every overview render); refreshKey is its query key so "Refresh" refetches it too. */
  overviewOrgs?: { useGroups: () => OrgGroup[] | undefined; refreshKey: string };
  /** Create a custom alert rule (Enterprise contract); absent = the create button on the new-rule form is disabled and shows the commercial badge. */
  createAlertRule?: (req: AlertRuleCreateRequest) => Promise<AlertRule>;
  /** Delete a user-created alert rule (Enterprise contract); absent = the rule settings page shows no delete button. Default rules cannot be deleted in either edition. */
  deleteAlertRule?: (id: string) => Promise<void>;
}

const empty: EnterpriseModule = { routes: [], navItems: [], navGroups: [], identityTabs: [] };

const found = import.meta.glob<EnterpriseModule>('./enterprise/index.ts', { eager: true, import: 'default' });

export const enterprise: EnterpriseModule = Object.values(found)[0] ?? empty;

/** Whether this is the Enterprise build. Core uses it to show the entry to the Enterprise info page. */
export const isEnterprise = Object.keys(found).length > 0;
