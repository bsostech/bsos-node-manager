import { useRef, useState } from 'react';

/**
 * The moment a status "gets worse": returns an increasing number to pass to <FlashBehind> so the background flashes once. Improving, unchanged or first-seen states do not flash
 * (a freshly opened page should not flash). Higher rank = more severe (normal 0 / warning 1 / critical 2); null = not known yet, not recorded.
 * For the first SETTLE_MS after opening, record without flashing: a page's data often arrives from several APIs in sequence (e.g. on-chain nodes first, our own nodes' status a bit later),
 * and the late one makes the indicator "get worse" even though nothing just happened.
 * For a table with many rows use useWorsenedMap, which records per row by id.
 */
const SETTLE_MS = 4_000;

export function useWorsenedMap() {
  const prev = useRef(new Map<string, number>());
  const tokens = useRef(new Map<string, number>());
  const [since] = useState(() => Date.now());
  return (id: string, rank: number | null) => {
    if (rank == null) return tokens.current.get(id) ?? 0;
    const p = prev.current.get(id);
    if (p != null && rank > p && Date.now() - since > SETTLE_MS) tokens.current.set(id, (tokens.current.get(id) ?? 0) + 1);
    prev.current.set(id, rank);
    return tokens.current.get(id) ?? 0;
  };
}

export function useWorsened(rank: number | null) {
  return useWorsenedMap()('', rank);
}
