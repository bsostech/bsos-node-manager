import { useEffect, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { CircleNotch } from '@phosphor-icons/react';
import type { ValidatorVote } from '@/api/schemas';
import { Swatch } from '@/components/Badges';
import { countedOf, withdrawingOf } from '@/lib/validators';

/**
 * How far a proposal is from a majority: one cell = one vote needed for the majority. Votes written into a block are solid, votes our org has sent but not yet written into a block are hollow, the rest are votes still needed.
 * Our org's votes being withdrawn are also hollow (the opposite vote has been sent but not yet written into a block): they still count on chain, so the tally includes them, but the cell is "in progress" just like a half-cast vote.
 * Hollow cells being sent light up in sequence: cell 1 lit -> cells 1 and 2 lit -> ... -> all dark, repeating (switching directly between lit / unlit, no fade), to show it is in progress and will change on its own.
 * Cells being withdrawn switch between solid and hollow (starting from the rightmost cell turning hollow -> all hollow -> back to solid): these votes are about to be taken away.
 * The backend returns only our org's pending votes (other organizations' exist only on their nodes, see groupVotes), so hollow cells are always ours.
 * The bar is role="img" with the same summary sentence; the adjacent text is hidden from screen readers to avoid reading it twice.
 * known = false (ValidatorVotes.tallyKnown, epoch unknown): the tally is unreliable, so only the reason is shown and no progress is drawn.
 */
export function VoteProgress({ vote, needed, known = true }: { vote: ValidatorVote; needed: number; known?: boolean }) {
  const { t } = useTranslation();
  const counted = countedOf(vote);
  const sent = vote.voters.filter((x) => x.pending !== false && x.countedAt == null).length;
  const leaving = vote.voters.filter((x) => withdrawingOf(x) && x.countedAt != null).length;
  const solid = counted - leaving;
  // Withdrawing: solid <-> hollow, starting from the right (votes recede outward); sending: lit <-> dark, starting from the left
  const hollow = useSequentialBlink(Math.max(0, Math.min(counted + sent, needed) - solid), leaving > 0);
  if (!known) return <span className="whitespace-nowrap text-xs text-ink-3">{t('validators.countsNeedAdmin')}</span>;
  const text = `${t('validators.counted', { n: counted, total: needed })} · ${t('validators.votesLeft', { n: Math.max(needed - counted, 0) })}`;
  return (
    <span className="inline-flex items-center gap-2">
      <span role="img" aria-label={text} className="flex gap-[3px]">
        {Array.from({ length: needed }, (_, i) => (
          <span
            key={i}
            ref={i >= solid && i < counted + sent ? (el) => { hollow.current[i - solid] = el; } : undefined}
            className={`h-2 w-3 rounded-[2px] ${i < solid ? 'bg-accent' : i < counted + sent ? 'shadow-[inset_0_0_0_1.5px_var(--color-accent)]' : 'bg-hair'}`}
          />
        ))}
      </span>
      <span aria-hidden className="whitespace-nowrap text-xs tabular-nums text-ink-2">{text}</span>
    </span>
  );
}

const STEP_MS = 500;

/**
 * Switch in sequence and back: n cells take n + 1 steps, cell i switches at step i, all return together at step n, repeating.
 * Sending (withdraw = false): hollow frame lit <-> dark, starting from the left. Withdrawing (withdraw = true): solid -> hollow, starting from the rightmost.
 * Each cell has a different timing, set per cell with Web Animations (created together so they stay aligned); with reduced motion enabled in system settings nothing moves and cells stay lit.
 */
function useSequentialBlink(n: number, withdraw: boolean) {
  const refs = useRef<(HTMLSpanElement | null)[]>([]);
  useEffect(() => {
    if (n === 0 || window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return;
    const steps = n + 1;
    const hold = 'steps(1, end)'; // each segment holds its starting value: switch directly, no interpolation
    // Sending: normally dark, lit on its turn; withdrawing: normally solid, hollow on its turn (fill removed, outline kept)
    const [rest, on] = withdraw
      ? [{ backgroundColor: 'var(--color-accent)' }, { backgroundColor: 'transparent' }]
      : [{ opacity: 0.15 }, { opacity: 1 }];
    const anims = refs.current.slice(0, n).map((el, i) => el?.animate(
      [
        { ...rest, offset: 0, easing: hold },
        { ...on, offset: (withdraw ? n - 1 - i : i) / steps, easing: hold },
        { ...rest, offset: n / steps, easing: hold },
        { ...rest, offset: 1 },
      ],
      { duration: steps * STEP_MS, iterations: Infinity },
    ));
    return () => anims.forEach((a) => a?.cancel());
  }, [n, withdraw]);
  return refs;
}

/** Transitional state in the button's place (spinner + text): the vote was sent but not yet written into a block, and there is no button to press meanwhile. */
function TransitNote({ text, hint }: { text: string; hint: string }) {
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap text-xs text-ink-3" title={hint}>
      <CircleNotch size={14} aria-hidden className="animate-spin" />{text}
    </span>
  );
}

/** Being written on chain: after approve / propose is sent and before our org's vote is written into a block. No withdraw (wait until it is on chain); once written it becomes "Withdraw vote". */
export function SendingNote() {
  const { t } = useTranslation();
  return <TransitNote text={t('validators.sending')} hint={t('validators.sendingHint')} />;
}

/** Withdrawing: our org's "Withdraw" button is temporarily replaced by this text, returning to "Vote" once the opposite vote is written into a block. Nothing remains after withdrawal completes: withdrawn = never voted. */
export function WithdrawingNote() {
  const { t } = useTranslation();
  return <TransitNote text={t('validators.withdrawing')} hint={t('validators.withdrawingHint')} />;
}

/** Legend under the voting table. The majority threshold is already stated in the page header and each row's "n to go", so it is not repeated here. */
export function VoteProgressLegend() {
  const { t } = useTranslation();
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1 border-t border-hair px-4 py-2.5 text-xs text-ink-3">
      <span className="inline-flex items-center gap-1.5"><Swatch className="bg-accent" />{t('validators.legendCounted')}</span>
      <span className="inline-flex items-center gap-1.5"><Swatch outline />{t('validators.legendSent')}</span>
      <span className="inline-flex items-center gap-1.5"><Swatch className="bg-hair" />{t('validators.legendNeeded')}</span>
    </div>
  );
}
