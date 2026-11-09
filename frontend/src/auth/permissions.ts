/** RBAC permission table. Frontend gating is UX only; real enforcement is on the backend. */
import type { TFunction } from 'i18next';
import type { Role } from '@/api/schemas';

export type Permission =
  | 'monitor:view' // view monitoring (nodes / overview)
  | 'validators:view' // view validator governance (on-chain business, not for admins)
  | 'nodes:onboard' // node onboarding (join token) + operations
  | 'members:manage'; // manage local users

// Admins run the system (MIS): they handle only platform settings and accounts, and neither do nor see node monitoring or on-chain business
// (monitoring, node registration, allowlists and validator governance are operator work; viewers can look but not change).
// The backend has the same table; unlisted routes require monitor:view by default.
const CORE_ROLE_PERMS: Record<Role, Permission[]> = {
  viewer: ['monitor:view', 'validators:view'],
  operator: ['monitor:view', 'validators:view', 'nodes:onboard'],
  admin: ['members:manage'],
};

// Merge point: Enterprise has its own roles and permissions. The Role / Permission types list only Core values;
// Enterprise values only show up at runtime, so this is widened to string here; everywhere else uses the Core types.
// When src/enterprise/roles.ts does not exist (open-source edition) the glob is an empty object.
const enterpriseRolePerms = Object.values(
  import.meta.glob<Record<string, string[]>>('../enterprise/roles.ts', { eager: true, import: 'rolePerms' }),
)[0];
const ROLE_PERMS: Record<string, string[]> = { ...CORE_ROLE_PERMS, ...enterpriseRolePerms };

export function roleCan(role: Role, p: Permission): boolean {
  return ROLE_PERMS[role]?.includes(p) ?? false; // unknown roles (e.g. Core seeing an account left by Enterprise) get no permissions
}

export const ALL_ROLES = Object.keys(ROLE_PERMS) as Role[];

/** Whether this edition recognizes the role; accounts with unrecognized roles cannot log in (the backend blocks them). */
export function roleKnown(role: string): boolean {
  return Object.hasOwn(ROLE_PERMS, role);
}

/** Display name of a role; unrecognized roles (e.g. the open-source edition seeing an auditor left by the commercial edition) show "Unknown role (auditor)" instead of the raw i18n key. */
export function roleLabel(t: TFunction, role: string): string {
  return t([`role.${role}`, 'role.unknown'], { role });
}

// Summary of what a role can do: lists the role's permissions in order. nodelogs:view and audit:view exist only in the Enterprise role table,
// so Core never lists them.
const SUMMARY_PERMS = ['monitor:view', 'validators:view', 'nodes:onboard', 'nodelogs:view', 'members:manage', 'audit:view'] as Permission[];

export function roleCaps(t: TFunction, role: Role): string {
  return SUMMARY_PERMS.filter((p) => roleCan(role, p)).map((p) => t(`users.perm.${p.replace(':', '_')}`)).join(' · ') || '—';
}

/** Role description: one sentence on what the role does (i18n roleDesc.<role>); roles without one (e.g. those added by Enterprise) fall back to the permission summary */
export function roleDesc(t: TFunction, role: Role): string {
  return t(`roleDesc.${role}`, { defaultValue: roleCaps(t, role) });
}
