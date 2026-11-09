import * as RadixTooltip from '@radix-ui/react-tooltip';

/**
 * Tooltip overlay (replaces the native title attribute). Native title only appears on mouse hover -- keyboard focus and touch never get it,
 * and some tooltips here carry diagnostic facts found nowhere else (e.g. "which nodes are missing this metric" in overlaid charts).
 *
 * Visuals follow the brand vocabulary: hairline + surface background, no arrow, no entrance animation.
 * When label is empty, children pass straight through, so callers need no conditional.
 */
export function Tooltip({
  label,
  side = 'top',
  children,
}: {
  label?: React.ReactNode;
  side?: 'top' | 'right' | 'bottom' | 'left';
  children: React.ReactElement;
}) {
  if (!label) return children;
  return (
    <RadixTooltip.Root>
      <RadixTooltip.Trigger asChild>{children}</RadixTooltip.Trigger>
      <RadixTooltip.Portal>
        <RadixTooltip.Content
          side={side}
          sideOffset={6}
          collisionPadding={8}
          className="z-50 max-w-xs rounded-md border border-hair bg-surface px-2 py-1 text-xs leading-relaxed text-ink-2 shadow-lg"
        >
          {label}
        </RadixTooltip.Content>
      </RadixTooltip.Portal>
    </RadixTooltip.Root>
  );
}

/** Single site-wide Provider (delay a bit shorter than Radix's default 700ms, closer to how native title feels). */
export const TooltipProvider = ({ children }: { children: React.ReactNode }) => (
  <RadixTooltip.Provider delayDuration={300} skipDelayDuration={300}>
    {children}
  </RadixTooltip.Provider>
);
