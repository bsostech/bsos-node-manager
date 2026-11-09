import { ArrowLeft, ArrowSquareOut, CaretRight } from '@phosphor-icons/react';
import { Link } from 'react-router-dom';

const FOCUS = 'focus:outline-none focus-visible:ring-1 focus-visible:ring-accent';
const ACCENT = `inline-flex w-fit items-center gap-1 rounded-md px-1.5 py-0.5 text-xs font-medium text-accent transition-colors hover:bg-accent-soft ${FOCUS}`;
const BACK = `flex w-fit items-center gap-1 rounded-sm text-sm text-ink-2 transition-colors hover:text-ink ${FOCUS}`;

type Props = { to?: string; onClick?: () => void; children: React.ReactNode };

function Base({ to, onClick, className, children }: Props & { className: string }) {
  if (to) return <Link to={to} className={className}>{children}</Link>;
  return <button type="button" onClick={onClick} className={className}>{children}</button>;
}

/**
 * Text link that "goes somewhere else" (manage peers, view proposal, rename in node settings...): blue text + ›, light blue background on hover.
 * With to it navigates; with onClick it switches to another tab on the same page. The arrow is drawn here, so copy must not type its own "→".
 */
export function GoLink({ children, ...p }: Props) {
  return <Base {...p} className={ACCENT}>{children}<CaretRight size={12} weight="bold" aria-hidden="true" /></Base>;
}

/**
 * Text link that leaves the site (official docs...): same family as GoLink, with the arrow replaced by an "open externally" icon, opens in a new tab.
 * inTip: when placed in an ⓘ help card, uses the same type size as the card's body text.
 */
export function ExtLink({ href, inTip, children }: { href: string; inTip?: boolean; children: React.ReactNode }) {
  return (
    <a href={href} target="_blank" rel="noreferrer" className={inTip ? ACCENT.replace('text-xs', 'text-2xs') : ACCENT}>
      {children}<ArrowSquareOut size={12} weight="bold" aria-hidden="true" />
    </a>
  );
}

/** Blue text button for same-page actions (retry, clear filters, use defaults...): same family as GoLink, but without the arrow. The caller puts the icon in children. */
export function TextButton({ onClick, children }: { onClick: () => void; children: React.ReactNode }) {
  return <Base onClick={onClick} className={ACCENT}>{children}</Base>;
}

/** "← back up one level" at the top left of the page header. */
export function BackLink({ children, ...p }: Props) {
  return <Base {...p} className={BACK}><ArrowLeft size={15} aria-hidden="true" />{children}</Base>;
}
