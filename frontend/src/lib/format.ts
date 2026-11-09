import i18n from '@/i18n';

/** Dates / numbers follow the UI language, not the browser locale (otherwise the Chinese UI would show "8/15/2026, 2:27:55 AM"). */
export const appLocale = () => (i18n.language === 'en' ? 'en-US' : 'zh-TW');

export const fmtInt = (n?: number | null) => (n == null ? '—' : n.toLocaleString(appLocale()));
export const fmtPct = (n?: number | null) => (n == null ? '—' : `${n.toFixed(0)}%`);
export const fmtTime = (iso?: string) => (iso ? new Date(iso).toLocaleTimeString(appLocale(), { hourCycle: 'h23' }) : '');
export const fmtDateTime = (iso?: string | null) => (iso ? new Date(iso).toLocaleString(appLocale(), { hourCycle: 'h23' }) : '—');
/** To the second, 24-hour: 2026/09/25 14:23:07. For places like "since when" that need second precision */
export const fmtDateTimeSec = (iso?: string | null) =>
  iso ? new Date(iso).toLocaleString(appLocale(), { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }) : '—';
/** Date + hour:minute (24-hour), for records and expiry times that must stay readable across years. */
export const fmtDateTimeMin = (iso?: string | null) =>
  iso ? new Date(iso).toLocaleString(appLocale(), { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' }) : '—';
/** Month/day + hour:minute, without year: for notifications / activity rows that are recent by nature, shown alongside a relative time. */
export const fmtHM = (ms: number) => new Date(ms).toLocaleTimeString(appLocale(), { hour: '2-digit', minute: '2-digit', hour12: false });
export const fmtDateTimeShort = (iso: string) =>
  new Date(iso).toLocaleString(appLocale(), { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' });
/** Compact time-since-now format (status bar / legend tooltip): 45m / 2h / 3d. */
export const fmtDur = (sinceIso: string) => fmtSpan(Date.now() - new Date(sinceIso).getTime());
/** Compact format of a duration (ms): 45m / 2h / 3d. */
export const fmtSpan = (ms: number) => {
  const m = Math.max(0, Math.round(ms / 60_000));
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h`;
  return `${Math.floor(h / 24)}d`;
};
/** Truncate long strings (address / hash) in the middle: 0x9255…898e1, so they do not stretch the layout. */
export const fmtAddr = (s: string, head = 6, tail = 5) => (s.length > head + tail + 1 ? `${s.slice(0, head)}…${s.slice(-tail)}` : s);

/** host:port of an enode (after @, before ?); empty string if absent. */
export const enodeHostPort = (enode?: string | null) => enode?.split('@')[1]?.split('?')[0] ?? '';

/** Take only the version from the second segment of web3_clientVersion (besu/v26.7.1/linux-aarch_64/...); show it as is if the format is unrecognized. */
export const shortVersion = (v?: string | null) => (v ? (/^[^/]+\/v?(\d[\w.-]*)/.exec(v)?.[1] ?? v) : '—');

/** Display of the source IP allowlist: /32 and /128 for single hosts are omitted. */
export const fmtSources = (cidrs: string[]) => cidrs.map((c) => c.replace(/\/(32|128)$/, '')).join(', ');
