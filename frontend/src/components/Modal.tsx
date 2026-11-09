import { useState } from 'react';
import * as Dialog from '@radix-ui/react-dialog';
import { useTranslation } from 'react-i18next';
import { X } from '@phosphor-icons/react';

/**
 * Site-wide modal shell (replaces the hand-rolled fixed inset-0 ModalShell each page used to have).
 * Visuals match the previous local convention exactly (hairline border + surface + 8px radius, no extra shadow language);
 * behavior is delegated to Radix Dialog: focus trapped inside, Esc closes,
 * background scroll locked, rendered into body (not clipped by ancestor overflow), correct role/aria.
 *
 * Open on mount -- keeps the existing `{open && <Modal …/>}` conditional-render convention, so pages keep their state model.
 * So there is no Dialog.Trigger, and Radix on close only returns focus to the Trigger (i.e. nowhere); instead we record the element focused at open time and return focus to it on close.
 *
 * Name it one of two ways: use <ModalTitle> in the content (automatic aria-labelledby), or pass label (visually hidden title).
 */
export function Modal({
  width = 'max-w-lg',
  contentClass = 'gap-3 rounded-lg border border-hair bg-surface p-4',
  onClose,
  dismissOnOutside = true,
  dismissOnEscape = true,
  autoFocusChild = false,
  label,
  children,
}: {
  width?: string;
  /** Skin + padding (replaces the defaults, not added on top): each page's existing radius/background/spacing differences stay as they were. */
  contentClass?: string;
  onClose: () => void;
  /** Pass false for form modals: a stray click outside should not throw away what was typed (Esc still closes). */
  dismissOnOutside?: boolean;
  /** Pass false for irreversible screens such as one-time reveals: Esc should not close either, only the explicit button on screen. */
  dismissOnEscape?: boolean;
  /** Pass true when the content already has an autoFocus field: lets it win over Radix's "focus the first tabbable element". */
  autoFocusChild?: boolean;
  label?: string;
  children: React.ReactNode;
}) {
  // The element focused at open time (usually the button that was pressed); captured only on the first render
  const [opener] = useState(() => (document.activeElement instanceof HTMLElement ? document.activeElement : null));
  return (
    <Dialog.Root open onOpenChange={(o) => !o && onClose()}>
      <Dialog.Portal>
        <Dialog.Overlay className="fade-in fixed inset-0 z-50 grid place-items-center overflow-y-auto bg-black/40 p-4">
          <Dialog.Content
            // Explicitly disable when there is no Description, so Radix does not point at a nonexistent node
            aria-describedby={undefined}
            onInteractOutside={(e) => dismissOnOutside || e.preventDefault()}
            onEscapeKeyDown={(e) => dismissOnEscape || e.preventDefault()}
            onOpenAutoFocus={(e) => autoFocusChild && e.preventDefault()}
            // On close, focus returns to the element that opened it; if that element is gone (e.g. the row just deleted), leave it to the browser
            onCloseAutoFocus={(e) => { if (opener?.isConnected) { e.preventDefault(); opener.focus(); } }}
            // overscroll-contain: scrolling past the end inside the modal does not chain to the page behind (otherwise in a long modal the list behind moves too)
            className={`flex max-h-[90vh] w-full ${width} flex-col overflow-y-auto overscroll-contain focus:outline-none ${contentClass}`}
          >
            {label && (
              <Dialog.Title className="sr-only">{label}</Dialog.Title>
            )}
            {children}
          </Dialog.Content>
        </Dialog.Overlay>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

/**
 * Modal type scale (uniform across all modals; body text must never be larger than the title):
 * - Title ModalTitle: text-base font-semibold text-ink
 * - Option names, inputs, buttons: text-sm text-ink
 * - Body text, list rows, field labels, helper text: text-xs (the site-wide html font size is 19px, so text-sm as body text looks too large)
 * - Button row: right-aligned, primary action rightmost; labels name only the action ("Add", "Disable"), never repeat the title
 * Semantically an <h2>, and automatically becomes the modal's accessible name. To change color (destructive actions), pass className to override.
 */
export function ModalTitle({ className = 'text-base font-semibold text-ink', ...rest }: React.ComponentProps<typeof Dialog.Title>) {
  return <Dialog.Title className={className} {...rest} />;
}

/**
 * Layout for form modals (add RPC access identity, issue token, add user...):
 * title bar on top (title + ✕), content in the middle (px-5 py-4), button row at the bottom, three sections separated by hairlines.
 * Usage: <Modal contentClass={MODAL_SHELL}> <ModalHead/> <div className="px-5 py-4">…</div> <ModalFoot/> </Modal>
 */
export const MODAL_SHELL = 'rounded-lg border border-hair bg-surface';

export function ModalHead({ children, icon, onClose }: { children: React.ReactNode; icon?: React.ReactNode; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <div className="flex items-center gap-2 border-b border-hair px-5 py-3">
      {icon}
      <ModalTitle>{children}</ModalTitle>
      <button type="button" onClick={onClose} aria-label={t('common.close')} className="ml-auto rounded p-1 text-ink-3 transition-colors hover:bg-elevated hover:text-ink">
        <X size={16} aria-hidden="true" />
      </button>
    </div>
  );
}

export function ModalFoot({ children }: { children: React.ReactNode }) {
  return <div className="flex items-center justify-end gap-2 border-t border-hair px-5 py-3">{children}</div>;
}
