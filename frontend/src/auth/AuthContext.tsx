import { createContext, useContext, useEffect, useRef, type ReactNode } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api, HttpError } from '@/api/client';
import type { Me, LoginRequest } from '@/api/schemas';
import { roleCan, type Permission } from './permissions';

interface AuthValue {
  me: Me | null;
  isLoading: boolean;
  /** /me failed but not with 401 (platform or database temporarily unreachable): not logged out, so do not fall back to the login page */
  unavailable: boolean;
  retry: () => void;
  can: (p: Permission | Permission[]) => boolean; // array = any one suffices
  login: (req: LoginRequest) => Promise<void>;
  logout: () => Promise<void>;
}

const AuthCtx = createContext<AuthValue | null>(null);

/** /me 401 = not logged in (me is null); other errors = temporarily unreachable (unavailable). login/logout invalidate queries to refetch. */
export function AuthProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();
  const { data: me, isLoading, isError, refetch } = useQuery({
    queryKey: ['me'],
    queryFn: async () => {
      try {
        return await api.getMe();
      } catch (e) {
        if (e instanceof HttpError && e.status === 401) return null;
        throw e;
      }
    },
    retry: false,
    staleTime: Infinity,
  });

  // When the identity changes (logout, session expiry to null, a different user), clear the previous user's cache so the next user never sees it.
  // Use resetQueries, not removeQueries: queries just mounted by the login page still have observers, and removing would detach them from the cache;
  // reset clears the data and refetches what is on screen. ['me'] itself is left alone.
  const id = me === undefined ? undefined : (me?.user.id ?? null);
  const prevId = useRef(id);
  useEffect(() => {
    if (id === undefined) return; // still loading (including refetching after a reset on return from bfcache)
    if (prevId.current !== undefined && prevId.current !== id) {
      void qc.resetQueries({ predicate: (q) => q.queryKey[0] !== 'me' });
      qc.getMutationCache().clear();
    }
    prevId.current = id;
  }, [id, qc]);

  const login = async (req: LoginRequest) => {
    await api.login(req);
    await qc.invalidateQueries({ queryKey: ['me'] });
  };
  const logout = async () => {
    await api.logout();
    qc.setQueryData(['me'], null);
    await qc.invalidateQueries({ queryKey: ['me'] });
  };
  const can = (p: Permission | Permission[]) => (me ? [p].flat().some((x) => roleCan(me.role, x)) : false);

  return <AuthCtx.Provider value={{ me: me ?? null, isLoading, unavailable: isError, retry: () => void refetch(), can, login, logout }}>{children}</AuthCtx.Provider>;
}

// eslint-disable-next-line react-refresh/only-export-components
export function useAuth(): AuthValue {
  const v = useContext(AuthCtx);
  if (!v) throw new Error('useAuth must be used within AuthProvider');
  return v;
}
