import { createContext, createElement, useContext, useEffect, useState, type Dispatch, type ReactNode, type SetStateAction } from 'react';
import { useTranslation } from 'react-i18next';
import { useBlocker } from 'react-router-dom';
import { ConfirmModal } from '@/components/ConfirmModal';

const DirtyCount = createContext<Dispatch<SetStateAction<number>> | null>(null);

/** Navigation after saving, or after the user explicitly chose to discard (cancel, delete), carries this state so we do not ask again. */
export const LEAVE_WITHOUT_ASKING = { state: { leaveWithoutAsking: true } };

/**
 * Block leaving while dirty: reload / closing the tab (beforeunload) and in-app navigation (UnsavedBlocker).
 * Each component holding a draft calls it; any one being dirty blocks.
 */
export function useUnsavedGuard(dirty: boolean) {
  const setCount = useContext(DirtyCount);
  useEffect(() => {
    if (!dirty) return;
    const h = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener('beforeunload', h);
    setCount?.((n) => n + 1);
    return () => {
      window.removeEventListener('beforeunload', h);
      setCount?.((n) => n - 1);
    };
  }, [dirty, setCount]);
}

/**
 * The single in-app navigation interception point for the whole site: the router allows only one blocker at a time and a hook cannot render a confirm dialog, so it is centralized here.
 * Blocks only when the path or query string changes (tabs are also stored in the URL); hash-only changes are not blocked. Must live under the data router (App.tsx).
 */
export function UnsavedBlocker({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const [count, setCount] = useState(0);
  const blocker = useBlocker(
    ({ currentLocation: a, nextLocation: b }) =>
      count > 0 && !(b.state as { leaveWithoutAsking?: boolean } | null)?.leaveWithoutAsking && (a.pathname !== b.pathname || a.search !== b.search),
  );
  return createElement(
    DirtyCount.Provider,
    { value: setCount },
    children,
    blocker.state === 'blocked' &&
      createElement(ConfirmModal, {
        title: t('common.leaveDirtyTitle'),
        body: t('common.leaveDirtyBody'),
        confirmLabel: t('common.leaveDirtyConfirm'),
        danger: true,
        onConfirm: () => blocker.proceed(),
        onCancel: () => blocker.reset(),
      }),
  );
}
