/**
 * Segmented progress for short step wizards (StepWizard, AddPeerModal): one segment per step, filled once the step is reached.
 * Discrete segments instead of a continuous bar, since the steps are countable and a percentage would only add noise.
 */
type Segment = 'done' | 'pending';

const SEGMENT_BG: Record<Segment, string> = {
  done: 'var(--bsos-accent)',
  pending: 'var(--bsos-border-hair)',
};

export function SegmentedProgress({ segments, className = 'w-20' }: { segments: Segment[]; className?: string }) {
  if (segments.length === 0) return null;
  return (
    <span aria-hidden className={`inline-flex h-[3px] shrink-0 gap-0.5 ${className}`}>
      {segments.map((s, i) => (
        <span key={i} className="h-full flex-1 rounded-full" style={{ background: SEGMENT_BG[s] }} />
      ))}
    </span>
  );
}
