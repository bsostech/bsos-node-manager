import type { ChainInfo, NodeSummary, ValidatorSetEntry, ValidatorTurn, ValidatorVote, ValidatorVotes } from '@/api/schemas';

/** Display name: the user-given name first, then the platform node id; null if neither (the UI shows "—"). */
export const validatorName = (v?: Pick<ValidatorSetEntry, 'name' | 'nodeId'> | null) => v?.name ?? v?.nodeId ?? null;

/** Our org's validators: the backend fills nodeId only for our own nodes (matched by the address derived from the enode public key). */
export const isOwnValidator = (v: Pick<ValidatorSetEntry, 'nodeId'>) => v.nodeId != null;

/**
 * Validators that are actually unreachable right now (when the chain halts only these are named; turn history stops at the halt and is not used).
 * Ours are judged by the node itself (offline or RPC unreachable); other orgs' by all our nodes having lost P2P to it
 * (connectedNodes is 0; null when none of our nodes can see peers, which does not count). Shared by the overview needs-attention list and the on-chain node indicators
 */
export const isUnreachable = (v: Pick<ValidatorSetEntry, 'nodeId' | 'connectedNodes'>, nodes?: Pick<NodeSummary, 'id' | 'status'>[]) => {
  if (!isOwnValidator(v)) return v.connectedNodes === 0;
  const node = nodes?.find((x) => x.id === v.nodeId);
  return !!node && (node.status === 'offline' || node.status === 'rpc_unreachable');
};

/** URL fragment for the proposal page: address + direction (one address may have both add and remove votes at once). */
export const proposalKey = (v: Pick<ValidatorVote, 'address' | 'add'>) => `${v.address.toLowerCase()}-${v.add ? 'add' : 'remove'}`;

/** Votes counted this epoch (network-wide, including other orgs); takes effect at needed. */
export const countedOf = (v: ValidatorVote) => v.voters.filter((x) => x.countedAt != null).length;

export const withdrawingOf = (x: ValidatorVote['voters'][number]) => !!x.withdrawing;

/** Our nodes are still casting this vote (can be withdrawn); treated as still casting when the node did not report (pending absent). Withdrawing ones do not count */
export const ownActive = (v: ValidatorVote) => v.voters.some((x) => x.nodeId != null && x.pending !== false && !withdrawingOf(x));

/** Our vote is in a block and counted: only then can it be withdrawn (not yet on chain cannot be withdrawn; wait for it to land) */
export const ownCounted = (v: ValidatorVote) => v.voters.some((x) => x.nodeId != null && x.countedAt != null && !withdrawingOf(x));

/** Our vote is sent but not yet in a block (landing); if any vote is already on chain it counts as withdrawable, not this */
export const ownSending = (v: ValidatorVote) => !ownCounted(v) && v.voters.some((x) => x.nodeId != null && x.pending !== false && x.countedAt == null && !withdrawingOf(x));

/**
 * Still voting: at least one vote still counts, is landing, or is being withdrawn. Zero votes (even the proposing org withdrew) means it ended without passing;
 * drop it from "Voting" and record it as not passed (the backend should not list it either; this is an extra safeguard).
 */
export const stillOpen = (v: ValidatorVote) => v.voters.some((x) => x.countedAt != null || withdrawingOf(x) || (x.nodeId != null && x.pending !== false));

/** Our org is withdrawing: the vote is in a block and the opposite vote is sent but not yet in a block to cancel it */
export const ownWithdrawing = (v: ValidatorVote) => v.voters.some((x) => x.nodeId != null && withdrawingOf(x));

/** Validator count after the change takes effect, block quorum (⌈2N/3⌉, same as the backend blockQuorum) and how many can be offline. */
export const setAfter = (n: number, add: boolean) => {
  const next = n + (add ? 1 : -1);
  const quorum = Math.floor((2 * next + 2) / 3);
  return { from: n, next, quorum, spare: next - quorum };
};

/** Platform validators that have not sent this vote yet (to vote again). */
export const missingVoters = (v: ValidatorVote, votes: ValidatorVotes) => votes.ownValidators.filter((id) => !v.voters.some((x) => x.nodeId === id));

/** Epoch progress: current block, blocks left, estimated time to the next epoch (from the average block interval); null if the epoch is unknown. */
export const epochProgress = (c?: ChainInfo) => {
  if (!c?.epochLength || c.epochStart == null || c.height == null) return null;
  const pos = c.height - c.epochStart;
  const left = c.epochLength - pos;
  const avg = c.blockIntervals.length ? c.blockIntervals.reduce((a, b) => a + b, 0) / c.blockIntervals.length : null;
  return { pos, len: c.epochLength, left, etaMs: avg == null ? null : left * avg * 1000 };
};

/**
 * A BFT chain whose epoch length is unknown: Besu reports it only in admin_nodeInfo, so it cannot be read if none of our nodes enable ADMIN.
 * The platform does not guess a default, so epoch progress, next-epoch time and on-chain vote counts are not shown.
 */
export const epochUnknown = (c?: ChainInfo) => !!c?.consensus && c.epochLength == null;

/** Vote tally still backfilling (after a platform restart, or the source node lacks earlier headers): returns the tally start, null when complete. */
export const votesBackfilling = (c?: ChainInfo) =>
  c?.votesFrom != null && c.epochStart != null && c.votesFrom > Math.max(c.epochStart, 1) ? c.votesFrom : null;

export const sameAddr = (a?: string | null, b?: string | null) => !!a && !!b && a.toLowerCase() === b.toLowerCase();

export const isAddress = (s: string) => /^0x[0-9a-fA-F]{40}$/.test(s.trim());

// Same number as the backend producingMissedInARow (server/validators.go): the same turn history must yield the same verdict on both sides
const DOWN_AFTER = 2;
/** Whether it is producing: missed all of the last DOWN_AFTER turns = false; no history = null. */
export const isProducing = (turns: Pick<ValidatorTurn, 'proposed'>[]) => {
  if (turns.length === 0) return null;
  const tail = turns.slice(-DOWN_AFTER);
  return tail.length < DOWN_AFTER || tail.some((x) => x.proposed);
};
