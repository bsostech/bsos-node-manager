import {
  ArrowsClockwise,
  Buildings,
  ClockCounterClockwise,
  ClipboardText,
  Fingerprint,
  HardDrives,
  Siren,
  SlidersHorizontal,
  Stamp,
  Swap,
  Vault,
  type Icon,
} from '@phosphor-icons/react';

/**
 * Commercial features surfaced in the open-source edition (locked sidebar items, commercial entry points inside pages).
 * Each has a one-line value proposition + three short points, deliberately light on detail. Copy lives in i18n under edition.<slug>.*.
 * Intro page at /enterprise/<slug>; in-page entry points open a small dialog with the same content.
 */
export const ENTERPRISE_FEATURES = {
  'cross-org': { icon: Buildings },
  audit: { icon: ClipboardText },
  'node-keys': { icon: Vault },
  failover: { icon: Swap },
  incident: { icon: Siren },
  identity: { icon: Fingerprint },
  'maker-checker': { icon: Stamp },
  'consortium-sync': { icon: ArrowsClockwise },
  'config-history': { icon: ClockCounterClockwise },
  'custom-rules': { icon: SlidersHorizontal },
  'rpc-node-scope': { icon: HardDrives },
} satisfies Record<string, { icon: Icon }>;

export type EnterpriseFeature = keyof typeof ENTERPRISE_FEATURES;
export const featurePath = (slug: EnterpriseFeature) => `/enterprise/${slug}`;
export const isEnterpriseFeature = (s: string | undefined): s is EnterpriseFeature => !!s && s in ENTERPRISE_FEATURES;
