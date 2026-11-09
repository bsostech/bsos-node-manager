import type { NotifyLanguage } from '@/api/schemas';

/** Notification languages, each named in itself so it reads the same whatever the UI language is. */
export const NOTIFY_LANGUAGES: readonly { value: NotifyLanguage; label: string }[] = [
  { value: 'zh', label: '中文' },
  { value: 'en', label: 'English' },
];

export const notifyLanguageLabel = (l: NotifyLanguage) => NOTIFY_LANGUAGES.find((x) => x.value === l)?.label ?? l;

/** IANA zones to offer: every zone the browser knows, plus UTC (some browsers leave it out) and the stored one.
 *  Older browsers lack Intl.supportedValuesOf; they get just the stored zone and UTC. */
export function timeZones(current: string): string[] {
  const known = typeof Intl.supportedValuesOf === 'function' ? Intl.supportedValuesOf('timeZone') : [];
  return [...new Set([...known, 'UTC', current].filter(Boolean))].sort();
}
