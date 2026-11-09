import './theme/cssVars'; // inject brand CSS variables first (avoids a flash)
import './index.css';
import React from 'react';
import ReactDOM from 'react-dom/client';
import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { HttpError } from './api/client';
import { isPlatformDown, platformDownKey } from './lib/platformDown';
import { ThemeProvider } from './theme/ThemeProvider';
import './i18n';
import { App } from './App';

// A 401 does not log out directly; refetch /me and let it decide: a wrong login password or a wrong current password on password change also returns 401 while the session is still valid.
// When the session really is gone, /me returns 401 -> me = null -> Gate switches to the login page. A 401 from /me itself becomes null in AuthContext, so there is no loop.
// cancelRefetch: false: a burst of 401s should not keep cancelling the in-flight /me.
const recheckSessionOn401 = (e: unknown) => {
  if (e instanceof HttpError && e.status === 401) void queryClient.invalidateQueries({ queryKey: ['me'] }, { cancelRefetch: false });
};

const queryClient: QueryClient = new QueryClient({
  queryCache: new QueryCache({
    onError: (e) => {
      recheckSessionOn401(e);
      if (isPlatformDown(e)) queryClient.setQueryData(platformDownKey, true);
    },
    onSuccess: () => {
      if (queryClient.getQueryData(platformDownKey)) queryClient.setQueryData(platformDownKey, false);
    },
  }),
  mutationCache: new MutationCache({ onError: recheckSessionOn401 }),
  defaultOptions: {
    queries: {
      staleTime: 5_000,
      refetchOnWindowFocus: false,
      // 4xx (forbidden, not found) will not change on retry; three retries would spin for about 7 seconds before showing the error
      retry: (n, e) => !(e instanceof HttpError && e.status < 500) && n < 3,
    },
  },
});

// When coming back from another site via Back, the browser may restore the whole page from bfcache as is (including the pre-logout cache) and /me is not re-requested; always refetch
window.addEventListener('pageshow', (e) => {
  if (e.persisted) void queryClient.resetQueries();
});

async function bootstrap() {
  // Dev server only: serve the API from MSW mocks so the UI runs without a backend. Production builds call the real API.
  if (import.meta.env.DEV) {
    // The demo scenario (?demo=fresh / existing) must be applied before mock data loads, see mocks/scenario.ts
    const { applyDemoFromUrl } = await import('./mocks/scenario');
    applyDemoFromUrl();
    const { enableMocking } = await import('./mocks/browser');
    await enableMocking();
  }

  ReactDOM.createRoot(document.getElementById('root')!).render(
    <React.StrictMode>
      <ThemeProvider>
        <QueryClientProvider client={queryClient}>
          <App />
        </QueryClientProvider>
      </ThemeProvider>
    </React.StrictMode>,
  );
}

void bootstrap();
