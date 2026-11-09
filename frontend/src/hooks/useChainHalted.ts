import { useQuery } from '@tanstack/react-query';
import { chainQuery } from '@/api/client';
import { useThresholds } from '@/lib/thresholds';
import { chainSpeed } from '@/lib/blockSpeed';

/** Whether the chain has halted: block production is judged "severely slow or stopped" (chainSpeed 'err'). If the head or block time is unknown, treat it as not halted. */
export function useChainHalted(): boolean {
  const chain = useQuery(chainQuery).data;
  return chainSpeed(chain, useThresholds()) === 'err';
}
