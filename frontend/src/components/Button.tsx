import { useRef, type ButtonHTMLAttributes, type MouseEvent } from 'react';
import { Link, type LinkProps } from 'react-router-dom';

/**
 * Unified buttons for modal footers (styles are defined only here):
 * - secondary: cancel / secondary action (plain background + strong border)
 * - primary: main action (solid accent)
 * - danger: destructive confirmation (status-error background, white text; same size as cancel, distinguished only by color). Only for the confirm button inside a confirm dialog
 * - dangerOutline: destructive trigger that opens a confirm dialog (delete, revoke, withdraw, remove, disconnect...): secondary's frame with red text
 * Disabled: primary gray background and text, secondary / dangerOutline gray text, danger semi-transparent; all cursor-not-allowed.
 * Pressed: primary switches to the spec's accent-active, others go one shade darker; color only, no scaling (disabled buttons don't trigger :active).
 *
 * There is only one size site-wide (text-xs, same as Listbox, inputs and body text): page headers, cards and modals alike.
 * Don't override padding / font size via className (for the same property Tailwind depends on CSS order, so it often doesn't win).
 */
const VARIANT_CLS = {
  secondary:
    'border border-strong text-ink transition-colors hover:bg-elevated active:bg-hair disabled:cursor-not-allowed disabled:text-ink-3',
  primary:
    'border border-transparent bg-accent font-medium text-white transition-colors hover:bg-accent-hover active:bg-[var(--bsos-accent-active)] disabled:cursor-not-allowed disabled:bg-elevated disabled:text-ink-3',
  danger:
    'border border-transparent bg-[var(--bsos-status-error-fg)] font-medium text-white transition-opacity hover:opacity-80 active:opacity-70 disabled:cursor-not-allowed disabled:opacity-60',
  dangerOutline:
    'border border-strong text-[var(--bsos-status-error-fg)] transition-colors hover:bg-[var(--bsos-status-error-bg)] active:bg-[color-mix(in_srgb,var(--bsos-status-error-fg)_14%,transparent)] disabled:cursor-not-allowed disabled:text-ink-3',
} as const;

/**
 * Synchronous double-click lock: callers disable the button with isPending, but TanStack Query's state dispatch only
 * re-renders on the next macrotask, so a second click before that sends again. Usage `onClick={lock(fn)}`; one lock is shared per lock instance.
 * preventDefault also blocks type=submit form submission (pressing Enter in an input makes the browser dispatch click on the submit button too).
 * ponytail: the lock is timed (300ms), not tied to request completion. It only needs to cover the few ms before isPending re-renders,
 * after which the caller's disabled={x.isPending} takes over until the request ends; only if some button doesn't wire isPending does it need to be tied to the request.
 */
export function useClickLock() {
  const busy = useRef(false);
  return (fn?: (e: MouseEvent<HTMLButtonElement>) => void) => (e: MouseEvent<HTMLButtonElement>) => {
    if (busy.current) {
      e.preventDefault();
      return;
    }
    busy.current = true;
    setTimeout(() => { busy.current = false; }, 300);
    fn?.(e);
  };
}

export function Button({
  variant = 'secondary',
  className = '',
  type = 'button',
  onClick,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: keyof typeof VARIANT_CLS }) {
  const lock = useClickLock();
  return (
    <button
      type={type}
      className={`rounded-md px-3 py-1.5 text-xs ${VARIANT_CLS[variant]} ${className}`}
      onClick={lock(onClick)}
      {...rest}
    />
  );
}

/**
 * Text actions in table / list rows (edit, remove, restore, revoke...): small gray text, background on hover.
 * danger: destructive ones (remove, revoke) are gray as usual and turn red only on hover, so a whole column of red text doesn't shout.
 */
export function RowAction({ danger, className = '', ...rest }: ButtonHTMLAttributes<HTMLButtonElement> & { danger?: boolean }) {
  return (
    <button
      type="button"
      className={`inline-flex items-center gap-1 rounded-md px-2 py-1 text-xs text-ink-2 transition-colors focus:outline-none focus-visible:ring-1 focus-visible:ring-accent disabled:cursor-not-allowed disabled:text-ink-3 ${
        danger ? 'hover:bg-[var(--bsos-status-error-bg)] hover:text-[var(--bsos-status-error-fg)]' : 'hover:bg-elevated hover:text-ink'
      } ${className}`}
      {...rest}
    />
  );
}

/** A navigation link that looks like a Button (e.g. "Add ..." in page headers): shares Button's styles; don't hand-write another accent button. */
export function ButtonLink({ variant = 'primary', className = '', ...rest }: LinkProps & { variant?: keyof typeof VARIANT_CLS }) {
  return <Link className={`inline-flex items-center rounded-md px-3 py-1.5 focus:outline-none focus-visible:ring-1 focus-visible:ring-accent text-xs ${VARIANT_CLS[variant]} ${className}`} {...rest} />;
}
