import { HttpError, type ErrorReason } from '@/api/client';
import i18n from '@/i18n';

// Errors with a reason are translated into the UI language (apiError.<reason>); errors without a reason yet show the backend message (English) as is.
// Errors not from the platform (reverse proxy, dev server): the message is just HTTP status text (Not Found...), so replace it with wording in the UI language
export const errMsg = (e: unknown) =>
  e instanceof HttpError
    ? e.fromPlatform && e.reason && i18n.exists(`apiError.${e.reason}`) ? i18n.t(`apiError.${e.reason}`, e.params)
      : !e.fromPlatform ? i18n.t(e.status === 403 ? 'error.forbidden' : 'error.title')
      : e.message
    : e ? String(e) : '';

/** Which message to show when a time-series query hits a monitoring source problem: PROMETHEUS_URL not set and unreachable are different, and retrying does not help the former. Other errors return null. */
export const monitoringIssue = (e: unknown): 'error.monitoringOff' | 'error.monitoringDown' | null =>
  e instanceof HttpError && e.code === 'monitoring_unavailable' ? (e.reason === 'monitoring_not_configured' ? 'error.monitoringOff' : 'error.monitoringDown') : null;

/** Per-item failure reasons inside a 200 response (reason / params side by side): translated into the UI language when there is a reason, otherwise the text field is used. */
export const itemMsg = (x: { reason?: ErrorReason | null; params?: Record<string, string> | null; error?: string | null } | undefined) =>
  x?.reason && i18n.exists(`apiError.${x.reason}`) ? i18n.t(`apiError.${x.reason}`, x.params ?? {}) : x?.error ?? undefined;
