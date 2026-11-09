import type { ChainInfo } from '@/api/schemas';
import type { Thresholds } from '@/lib/thresholds';

/**
 * How old the latest block was at response time: asOf minus the block timestamp, both server clock (the head is updated by RunHead every 2 seconds, independent of the 10-second collection).
 * Do not add "time elapsed since the response arrived": that is the frontend not having asked again, not the chain slowing. With a 10-second poll and a 2-second-block chain the threshold is 10 seconds,
 * so adding it would cross the line right before every next poll and falsely report "slow block production". If the chain really stops, the next response shows it, at most one poll late.
 */
export function blockAge(chain: ChainInfo | undefined): number | null {
  if (!chain?.latestBlockAt) return null;
  return Math.max(0, (Date.parse(chain.asOf) - Date.parse(chain.latestBlockAt)) / 1000);
}

/**
 * Two lines for block speed: time since the last block as a multiple of the "normal block time" (expectedBlockPeriodSeconds, the same number as the ChainSlow alert).
 * The multiples come from the ChainSlow / ChainSlowSevere alert settings (default 5 and 20). 'err' means "block production severely slow or stopped",
 * same as the backend chainHalted and bsos_chain_halted in the QuorumLost alert; the overview, the needs-attention list, on-chain nodes and turn history all read this one.
 * It is 'none' when the chain cannot be read (no source node or height) or the head or normal block time is unknown.
 */
export type Speed = 'ok' | 'warn' | 'err' | 'none';
export function chainSpeed(chain: ChainInfo | undefined, th: Thresholds): Speed {
  const age = blockAge(chain);
  const base = chain?.expectedBlockPeriodSeconds;
  if (!chain?.sourceNodeId || chain.height == null || age == null || !base) return 'none';
  const r = age / base;
  return r >= th.severeX ? 'err' : r >= th.slowX ? 'warn' : 'ok';
}
