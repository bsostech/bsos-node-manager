import { useSyncExternalStore } from 'react';

/**
 * Site-wide polling interval: the user picks a step, it is stored in the browser, and it is the same value whichever page changes it.
 * The smallest step matches the backend collection interval (COLLECT_SEC): polling faster just rereads the same cache.
 * Pages with a header refresh area (RefreshControls) pass refreshMs to their polling queries, so a change takes effect on the next round.
 */
const KEY = 'bsos-refresh-sec';
/** How often the backend collects, in seconds (collectInterval in backend/cmd/server/main.go); change both together */
export const COLLECT_SEC = 10;
export const REFRESH_STEPS = [10, 30, 60] as const;
export type RefreshSec = (typeof REFRESH_STEPS)[number];
const DEFAULT: RefreshSec = 10;

function read(): RefreshSec {
  try {
    const v = Number(localStorage.getItem(KEY));
    return (REFRESH_STEPS as readonly number[]).includes(v) ? (v as RefreshSec) : DEFAULT;
  } catch {
    return DEFAULT;
  }
}

let current: RefreshSec = read();
const listeners = new Set<() => void>();

/** Pass this function directly as react-query's refetchInterval so the value is reread on every scheduled round. */
export const refreshMs = () => current * 1000;

export function setRefreshSec(v: RefreshSec) {
  current = v;
  try { localStorage.setItem(KEY, String(v)); } catch { /* if it cannot be written (private mode etc.) it lives only for this session */ }
  listeners.forEach((l) => l());
}

export function useRefreshSec(): [RefreshSec, (v: RefreshSec) => void] {
  const v = useSyncExternalStore(
    (cb) => { listeners.add(cb); return () => { listeners.delete(cb); }; },
    () => current,
    () => current,
  );
  return [v, setRefreshSec];
}
