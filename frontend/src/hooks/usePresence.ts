import { useLayoutEffect, useRef, useState } from 'react';

/**
 * Micro-interaction when list rows are added / removed (the kind caused by pressing a button: propose, register, add peer, add identity...).
 * Add: the row expands first (150ms), then the content fades in (150ms). Remove: the content fades out first (120ms), then the row collapses (150ms).
 * No animation on first load (before ready, and rows already present when ready flips); none when the system asks for reduced motion, where removed rows just disappear.
 *
 * Usage: rows = usePresence(items, keyOf, ready), render rows one by one, and attach ref={presenceRef(state)} to each row.
 * For table rows (tr), wrap each cell's content in <div className="row-cell"> (a td cannot collapse its height itself; what animates is this wrapper plus the td's vertical padding);
 * other elements (li, div) collapse as a whole. A removed row stays in its original position a little longer and is dropped once collapsed.
 */
const EXPAND_MS = 150;
const FILL_MS = 150;
const FADE_MS = 120;
const COLLAPSE_MS = 150;
const ENTER_MS = EXPAND_MS + FILL_MS;
const LEAVE_MS = FADE_MS + COLLAPSE_MS;

export type PresenceState = 'enter' | 'leave' | null;
export type PresenceRow<T> = { key: string; item: T; state: PresenceState };

const reducedMotion = () => !!window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;

export function usePresence<T>(items: readonly T[], keyOf: (x: T) => string, ready = true): PresenceRow<T>[] {
  const prev = useRef<{ keys: string[]; byKey: Map<string, T> } | null>(null);
  const [entering, setEntering] = useState<ReadonlySet<string>>(new Set());
  const [leaving, setLeaving] = useState<ReadonlyMap<string, { item: T; at: number }>>(new Map());
  const keys = items.map(keyOf);
  const sig = keys.join('\u0000');

  // Record the current contents after every render (even when only content changed and no rows were added or removed: a collapsing removed row shows how it last looked before removal)
  useLayoutEffect(() => {
    if (!ready) return;
    const byKey = new Map(items.map((x) => [keyOf(x), x] as const));
    const p = prev.current;
    prev.current = { keys, byKey };
    if (!p || p.keys.join('\u0000') === sig || reducedMotion()) return;
    const added = keys.filter((k) => !p.byKey.has(k));
    const removed = p.keys.filter((k) => !byKey.has(k));
    if (added.length) {
      setEntering((s) => new Set([...s, ...added]));
      setTimeout(() => setEntering((s) => new Set([...s].filter((k) => !added.includes(k)))), ENTER_MS);
    }
    if (removed.length) {
      setLeaving((m) => new Map([...m, ...removed.map((k) => [k, { item: p.byKey.get(k)!, at: p.keys.indexOf(k) }] as const)]));
      setTimeout(() => setLeaving((m) => new Map([...m].filter(([k]) => !removed.includes(k)))), LEAVE_MS);
    }
  });

  const rows: PresenceRow<T>[] = items.map((x) => {
    const key = keyOf(x);
    return { key, item: x, state: entering.has(key) ? 'enter' : null };
  });
  // Put removed rows back in their original positions (reinserted in the original order) and drop them once collapsed
  for (const [key, { item, at }] of [...leaving].sort((a, b) => a[1].at - b[1].at)) {
    if (!rows.some((r) => r.key === key)) rows.splice(Math.min(at, rows.length), 0, { key, item, state: 'leave' });
  }
  return rows;
}

/** The parts of a row that animate: for table rows, each cell's .row-cell plus the td's vertical padding; for other elements, the element itself (including its vertical padding) */
function parts(el: HTMLElement) {
  if (el instanceof HTMLTableRowElement) {
    return {
      boxes: [...el.querySelectorAll<HTMLElement>(':scope > td > .row-cell')],
      pads: [...el.children] as HTMLElement[],
    };
  }
  return { boxes: [el], pads: [el] };
}

function pad(el: HTMLElement) {
  const cs = getComputedStyle(el);
  return { paddingTop: cs.paddingTop, paddingBottom: cs.paddingBottom };
}

const ZERO_PAD = { paddingTop: '0px', paddingBottom: '0px' };

/** Add: first expand to the natural height (content still invisible), then fade in. Measures the actual height at this moment rather than guessing.
 *  Overflow (pill negative margins, focus rings) is clipped only while expanding, not while fading in; otherwise the edges would pop in at the very end */
function animateEnter(el: HTMLElement | null) {
  if (!el) return;
  const { boxes, pads } = parts(el);
  for (const b of boxes) {
    b.style.overflow = 'hidden';
    b.animate([{ maxHeight: '0px', easing: 'ease-out' }, { maxHeight: `${b.offsetHeight}px` }], { duration: EXPAND_MS })
      .onfinish = () => { b.style.overflow = ''; };
    b.animate([{ opacity: 0, easing: 'ease-out' }, { opacity: 1 }], { duration: FILL_MS, delay: EXPAND_MS, fill: 'backwards' });
  }
  for (const p of pads) p.animate([{ ...ZERO_PAD, easing: 'ease-out' }, pad(p)], { duration: EXPAND_MS });
}

/** Remove: fade the content out first, then collapse; once collapsed the row is dropped, so stay collapsed and do not spring back */
function animateLeave(el: HTMLElement | null) {
  if (!el) return;
  el.style.pointerEvents = 'none';
  const { boxes, pads } = parts(el);
  for (const b of boxes) {
    b.animate([{ opacity: 1, easing: 'ease-in' }, { opacity: 0 }], { duration: FADE_MS, fill: 'forwards' });
    b.animate([{ maxHeight: `${b.offsetHeight}px`, easing: 'ease-in' }, { maxHeight: '0px' }], { duration: COLLAPSE_MS, delay: FADE_MS, fill: 'both' });
    setTimeout(() => { b.style.overflow = 'hidden'; }, FADE_MS);
  }
  for (const p of pads) {
    p.animate([{ ...pad(p), easing: 'ease-in' }, ZERO_PAD], { duration: COLLAPSE_MS, delay: FADE_MS, fill: 'both' });
  }
}

/** The ref attached to a row: when the state changes React calls the new ref again, which starts the animation right before the frame is painted */
export const presenceRef = (state: PresenceState) =>
  state === 'enter' ? animateEnter : state === 'leave' ? animateLeave : undefined;
