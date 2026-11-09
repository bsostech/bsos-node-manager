import { HttpError, NetworkError } from '@/api/client';

/** Flag for the platform being unreachable (kept in the query cache; set by the QueryCache in main.tsx, read by AppShell) */
export const platformDownKey = ['platform-down'] as const;

/** Platform down: fetch itself failed, or a reverse proxy returned 502/504 (body is not a platform JSON error).
 *  The platform itself also returns 502 (forwarding to a node / external org failed) and 503 (monitoring source unreachable); those are downstream outages, not the platform being down. */
export const isPlatformDown = (e: unknown) =>
  e instanceof NetworkError || (e instanceof HttpError && !e.fromPlatform && (e.status === 502 || e.status === 504));
