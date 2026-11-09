import type { ReactNode } from 'react';

/**
 * Expanded content: on open it grows from zero height and fades in (150ms, in sync with the adjacent chevron's rotation); collapsing is instant.
 * It only handles the opening moment; whether to show it is decided outside (`{open && <Expand>…</Expand>}`). Put padding in children, not on Expand.
 */
export function Expand({ children }: { children: ReactNode }) {
  return (
    <div className="expand-in">
      <div>{children}</div>
    </div>
  );
}
