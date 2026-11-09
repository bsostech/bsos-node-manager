import * as Popover from '@radix-ui/react-popover';
import { Info } from '@phosphor-icons/react';
import { useTranslation } from 'react-i18next';

/**
 * Help card (toggletip): click ⓘ to open, click outside or press Esc to close. One or two sentences with a title. ⓘ is the only icon used site-wide.
 * A paragraph meant to be read goes here rather than in a hover Tooltip, since it doesn't vanish when the mouse leaves.
 */
export function HelpTip({ title, children }: { title: string; children: React.ReactNode }) {
  const { t } = useTranslation();
  return (
    <Popover.Root>
      <Popover.Trigger asChild>
        <button
          type="button"
          aria-label={t('common.helpFor', { title })}
          className="inline-flex shrink-0 rounded-full align-middle text-ink-3 transition-colors hover:text-ink focus:outline-none focus-visible:ring-1 focus-visible:ring-accent data-[state=open]:text-accent"
        >
          <Info size={15} aria-hidden="true" />
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          align="start"
          side="bottom"
          sideOffset={6}
          collisionPadding={16}
          aria-label={title}
          className="pop-in z-50 flex w-80 max-w-[calc(100vw-2rem)] flex-col gap-2 rounded-lg border border-hair bg-surface p-3 text-left shadow-lg focus:outline-none"
        >
          {/* Help text one size smaller: title xs, body 2xs, so content-heavy rule tables fit */}
          <span className="text-xs font-semibold text-ink">{title}</span>
          {/* Keep line breaks in the copy: long help text is split into paragraphs by blank lines */}
          <div className="whitespace-pre-line text-2xs font-normal leading-relaxed text-ink-2">{children}</div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
