/**
 * Tx pool tab: turn the accounts returned by the backend (nextNonce + transactions) into the shape the nonce track needs: which can be mined, which nonces are missing, which are blocked.
 * Pool-wide pending / queued counts come from totals in the response (computed by the backend); this only handles each account.
 */
import type { TxPoolSender, TxPoolTx } from '@/api/schemas';
import { fmtSpan } from '@/lib/format';

/** gap = a nonce is missing and every transaction after it is blocked; ok = queued normally */
export type LaneState = 'gap' | 'ok';

export type Lane = {
  sender: TxPoolSender;
  state: LaneState;
  /** nonce already below the on-chain one: a block was just produced and the pool has not been cleaned yet; will disappear soon */
  landing: TxPoolTx[];
  /** Transactions contiguous from nextNonce that can be mined right away (pending) */
  ready: TxPoolTx[];
  /** Missing nonces (between nextNonce and the highest nonce in the pool) */
  missing: number[];
  /** Transactions stuck behind a gap (queued) */
  blocked: TxPoolTx[];
  /** gap: when the blockage started (pool entry time of the earliest blocked tx); ok: pool entry time of the earliest minable tx. epoch ms */
  since: number;
};

export function analyzeLane(sender: TxPoolSender, now: number): Lane {
  const landing = sender.txs.filter((tx) => tx.nonce < sender.nextNonce);
  const byNonce = new Map(sender.txs.map((tx) => [tx.nonce, tx]));
  const max = Math.max(sender.nextNonce - 1, ...sender.txs.map((tx) => tx.nonce));
  const ready: TxPoolTx[] = [];
  const missing: number[] = [];
  const blocked: TxPoolTx[] = [];
  for (let n = sender.nextNonce; n <= max; n++) {
    const tx = byNonce.get(n);
    if (!tx) missing.push(n);
    else if (missing.length) blocked.push(tx);
    else ready.push(tx);
  }
  const oldest = (txs: TxPoolTx[]) => (txs.length ? Math.min(...txs.map((tx) => new Date(tx.addedAt).getTime())) : now);
  return missing.length
    ? { sender, state: 'gap', landing, ready, missing, blocked, since: oldest(blocked) }
    : { sender, state: 'ok', landing, ready, missing, blocked, since: oldest(ready) };
}

/** Blocked first, longest-blocked earliest; normal ones by transaction count, most to fewest */
export function analyzePool(senders: TxPoolSender[], now: number): Lane[] {
  return senders
    .map((s) => analyzeLane(s, now))
    .sort((a, b) => (a.state === b.state ? (a.state === 'gap' ? a.since - b.since : b.sender.txs.length - a.sender.txs.length) : a.state === 'gap' ? -1 : 1));
}

/** Collapse consecutive numbers into ranges: [42] -> "42", [109,110] -> "109–110", [1,2,5] -> "1–2、5" */
export function fmtRanges(ns: number[]): string {
  const out: string[] = [];
  for (let i = 0; i < ns.length; ) {
    let j = i;
    while (j + 1 < ns.length && ns[j + 1] === ns[j] + 1) j++;
    out.push(i === j ? `${ns[i]}` : `${ns[i]}–${ns[j]}`);
    i = j + 1;
  }
  return out.join('、');
}

/** Show seconds under a minute too (fmtSpan's smallest unit is minutes) */
export const fmtAge = (ms: number) => (ms < 60_000 ? `${Math.max(1, Math.round(ms / 1000))}s` : fmtSpan(ms));

/** Transaction source of an account: all local, all forwarded, or mixed (returns an i18n key) */
export function sourceKey(l: Lane) {
  const local = l.sender.txs.filter((tx) => tx.local).length;
  return local === l.sender.txs.length ? 'txpool.srcLocal' : local === 0 ? 'txpool.srcRelay' : 'txpool.srcMixed';
}

/** A full address (0x + 40 hex); when the list is truncated, searching for a full address asks the backend instead */
export const isAddress = (s: string) => /^0x[0-9a-fA-F]{40}$/.test(s.trim());
