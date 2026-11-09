/**
 * Card styling (one definition site-wide; the version tuned on the "Authorized sender addresses" page is canonical):
 * rounded-lg frame + hair border + surface background; fixed-height header (doesn't grow with filters or buttons), px-4; body px-4 py-3.
 * When composing a card by hand (e.g. the apply bar must be sticky and can't be clipped), use these classes rather than another copy.
 */
export const CARD = 'min-w-0 rounded-lg border border-hair bg-surface';
export const CARD_HEAD = 'flex min-h-[54px] flex-wrap items-center gap-2 border-b border-hair px-4 py-2';
export const CARD_TITLE = 'flex items-center gap-2 text-sm font-semibold text-ink';
export const CARD_BODY = 'flex flex-col gap-3 px-4 py-3';

/** A settings-page section: header (right side can hold a short note or controls) + content. danger is for irreversible sections such as delete. */
export function Card({ title, aside, danger, alert, unclipped, children, bodyClass = CARD_BODY }: {
  title: React.ReactNode;
  aside?: React.ReactNode;
  danger?: boolean;
  /** Only outline the frame in red (this section needs attention now, but is not itself a destructive action) */
  alert?: boolean;
  /** Don't clip content: use when something inside is sticky (e.g. the apply bar) */
  unclipped?: boolean;
  children: React.ReactNode;
  bodyClass?: string;
}) {
  return (
    <section className={`${CARD} ${unclipped ? '' : 'overflow-hidden'}`} style={danger || alert ? { borderColor: 'var(--bsos-status-error-fg)' } : undefined}>
      {/* aside sits outside the heading: it may contain controls, which inside h2 would become part of the heading's name */}
      <div className={CARD_HEAD}>
        <h2 className={CARD_TITLE} style={danger ? { color: 'var(--bsos-status-error-fg)' } : undefined}>{title}</h2>
        {aside && <div className="ml-auto flex items-center gap-2 text-xs text-ink-3">{aside}</div>}
      </div>
      <div className={bodyClass}>{children}</div>
    </section>
  );
}
