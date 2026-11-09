import type { SVGProps } from 'react';

/**
 * Ballot stamp: a vertical stroke with a short diagonal tick inside a circle -- the stamp pressed onto a ballot when voting.
 * The icon library (phosphor) has no such symbol, so it is drawn here with a stroke width close to phosphor bold, used inside a pill as the validator mark.
 */
export function BallotStamp({ size = 12, ...rest }: { size?: number } & SVGProps<SVGSVGElement>) {
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth={1.8} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" {...rest}>
      <circle cx="8" cy="8" r="6.3" />
      <path d="M7 4.2v7.6M7 7.2l2.8 2.1" />
    </svg>
  );
}
