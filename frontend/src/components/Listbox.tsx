import { useRef, type ReactNode } from 'react';
import * as Select from '@radix-ui/react-select';
import { CaretDown, Check } from '@phosphor-icons/react';

/** Radix reserves the empty string for "nothing selected", but this project uses '' as a real option in places (Alerts' "all nodes"),
 *  so the public API stays the same and a sentinel carries it internally. */
const EMPTY = ' empty';

/** Listbox dropdown: mono trigger + vertically scrollable list (for high-cardinality enums where a segmented control won't fit).
 *  Extracted from NodeJoin for sharing; placeholder covers the empty "not yet chosen" state.
 *
 *  Built on Radix Select: arrow keys/Home/End/typeahead, menu portaled to body (not clipped by a modal's
 *  overflow, auto-flips at edges), closes on Tab out, aria roles and activedescendant maintained by Radix. */
export function Listbox<T extends string>({
  value,
  options,
  onChange,
  placeholder,
  'aria-label': ariaLabel,
  width = 'w-32',
  mono = false,
  variant = 'field',
  disabled,
}: {
  value: T;
  /** dot: small color dot before the option (e.g. severity red / orange), also shown on the trigger.
   *  disabled: visible but not selectable (e.g. commercial-edition-only options); tag: small tag on the option's right (e.g. "Enterprise") */
  options: readonly { value: T; label: string; dot?: string; disabled?: boolean; tag?: ReactNode }[];
  onChange: (v: T) => void;
  placeholder?: string;
  'aria-label'?: string;
  width?: string; // trigger width (Tailwind class); can be narrower for short options (comparison operators, durations)
  /** Regular font by default, matching adjacent buttons and help text; pass true only when monospace is needed (e.g. aligning long codes) */
  mono?: boolean;
  /** field = framed field (default); quiet = small text embedded in a cell, just text and a small chevron, taking no extra space */
  variant?: 'field' | 'quiet';
  /** Disabled: grayed out and can't open (e.g. fields editable only in the commercial edition) */
  disabled?: boolean;
}) {
  // Whether '' is a real option or "not yet chosen" is decided by the option list - the latter must let Radix show the placeholder.
  const triggerRef = useRef<HTMLButtonElement>(null);
  const hasEmptyOption = options.some((o) => o.value === ('' as T));
  const enc = (v: string) => (v === '' ? EMPTY : v);
  const selected = value === '' && !hasEmptyOption ? undefined : enc(value);
  // When options haven't loaded (or the value is no longer in the list), Radix's Select.Value renders blank - show the raw value as a fallback,
  // keeping the earlier `current?.label ?? placeholder ?? value` behavior so the trigger never becomes an empty box.
  const current = options.find((o) => o.value === value);
  const fallback = placeholder ?? value;

  const font = mono ? 'font-mono' : 'font-sans';
  // For content-width (w-auto) dropdowns: fix the width to the longest option, so changing selection or counts (All (10) -> All (4))
  // doesn't make it grow or shrink and the adjacent search box and count chip don't jump. How: stack all options in one grid cell
  // as an invisible ruler, with every digit replaced by 0, at least two digits, using tabular numerals
  const steady = variant === 'field' && /\bw-auto\b/.test(width);
  const ruler = (s: string) => s.replace(/\d+/g, (m) => '0'.repeat(Math.max(2, m.length)));
  const label = (o?: { label: string; dot?: string }) => (o ? (
    <span className="inline-flex items-center gap-1.5 leading-none">{o.dot && <span aria-hidden className="size-1.5 shrink-0 rounded-full" style={{ background: o.dot }} />}{o.label}</span>
  ) : fallback);
  const trigger = variant === 'quiet'
    ? `group inline-flex max-w-full items-center gap-1 whitespace-nowrap rounded text-2xs text-ink-3 transition-colors hover:text-ink focus:outline-none focus-visible:ring-1 focus-visible:ring-accent data-[state=open]:text-ink`
    : `group inline-flex ${width} items-center justify-between gap-2 whitespace-nowrap rounded-md border border-hair bg-canvas px-2.5 py-1.5 text-xs transition-colors hover:border-strong focus:border-accent focus:outline-none ${steady ? 'tabular-nums' : ''} ${current ? 'text-ink' : 'text-ink-3'}`;
  return (
    <Select.Root
      value={selected}
      disabled={disabled}
      onValueChange={(v) => onChange((v === EMPTY ? '' : v) as T)}
      // On open Radix moves focus into the menu, and the browser incidentally scrolls the outer scrollable container (the page appears to jump).
      // Remember the scroll position before opening and restore it after focus has moved.
      onOpenChange={(open) => {
        if (!open) return;
        const scroller = triggerRef.current?.closest<HTMLElement>('main, [data-scroller]');
        if (!scroller) return;
        const y = scroller.scrollTop;
        requestAnimationFrame(() => { scroller.scrollTop = y; requestAnimationFrame(() => { scroller.scrollTop = y; }); });
      }}
    >
      <Select.Trigger ref={triggerRef} aria-label={ariaLabel} className={`${trigger} ${font} disabled:cursor-not-allowed disabled:opacity-60 disabled:hover:border-hair`}>
        {steady ? (
          <span className="grid text-left">
            <span className="[grid-area:1/1]"><Select.Value placeholder={fallback}>{label(current)}</Select.Value></span>
            {[...options.map((o) => ({ ...o, label: ruler(o.label) })), ...(placeholder ? [{ value: '', label: ruler(placeholder) }] : [])].map((o, i) => (
              <span key={i} aria-hidden className="invisible [grid-area:1/1]">{label(o)}</span>
            ))}
          </span>
        ) : (
          <Select.Value placeholder={fallback}>{label(current)}</Select.Value>
        )}
        <Select.Icon asChild>
          <CaretDown size={variant === 'quiet' ? 10 : 12} className="shrink-0 text-ink-3 transition-transform group-data-[state=open]:rotate-180" />
        </Select.Icon>
      </Select.Trigger>
      <Select.Portal>
        <Select.Content
          position="popper"
          sideOffset={4}
          align={variant === 'quiet' ? 'end' : 'start'}
          className={`pop-in z-50 max-h-52 overflow-hidden rounded-md border border-hair bg-surface shadow-lg ${variant === 'quiet' ? 'w-max min-w-[8rem]' : 'w-max min-w-[var(--radix-select-trigger-width)]'}`}
        >
          <Select.Viewport className="overflow-y-auto py-1">
            {options.map((o) => (
              <Select.Item
                key={o.value}
                value={enc(o.value)}
                disabled={o.disabled}
                className={`flex w-full cursor-default select-none items-center justify-between gap-3 px-2.5 py-1.5 text-left ${font} text-xs text-ink-2 transition-colors focus:outline-none data-[highlighted]:bg-elevated data-[state=checked]:font-medium data-[state=checked]:text-ink data-[disabled]:cursor-not-allowed data-[disabled]:text-ink-3`}
              >
                <span className="inline-flex items-center gap-1.5">{o.dot && <span aria-hidden className="size-1.5 shrink-0 rounded-full" style={{ background: o.dot }} />}<Select.ItemText>{o.label}</Select.ItemText></span>
                {o.tag}
                <Select.ItemIndicator>
                  <Check size={12} className="shrink-0" />
                </Select.ItemIndicator>
              </Select.Item>
            ))}
          </Select.Viewport>
        </Select.Content>
      </Select.Portal>
    </Select.Root>
  );
}
