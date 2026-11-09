import { getResponse } from 'msw';
import { setupWorker } from 'msw/browser';
import { handlers } from './handlers';

// Enterprise mocks come first (they win on the same path); the open-source build has no src/enterprise/, so the glob is empty.
const enterpriseHandlers = Object.values(
  import.meta.glob<typeof handlers>('../enterprise/mocks.ts', { eager: true, import: 'handlers' }),
).flat();

const allHandlers = [...enterpriseHandlers, ...handlers];
export const worker = setupWorker(...allHandlers);

/** Starts the MSW mocks; main.tsx calls this only on the dev server. */
export async function enableMocking() {
  installFetchMock();
  // The service worker still starts and catches non-fetch requests (e.g. download links); if it cannot start (served over http) everything still works
  try {
    await worker.start({ onUnhandledFrame: 'bypass' });
  } catch {
    /* no service worker: fetch is still caught above */
  }
}

/**
 * fetch is answered in-page by the same handlers, without going through the service worker.
 * Why: when a tab sits in the background for a while the browser stops the idle service worker; after restarting it no longer knows this tab, and requests leak to the real server
 * (logout takes several clicks, data fails to load). Browsers also refuse service workers when the page is served over plain http.
 * Requests no handler matches go out as usual.
 */
function installFetchMock() {
  const realFetch = window.fetch.bind(window);
  window.fetch = async (input, init) => {
    // Match against a clone: a handler reading the body must not consume the request that actually goes out
    const request = input instanceof Request ? new Request(input.clone(), init) : new Request(input, init);
    const mocked = await getResponse(allHandlers, request);
    return mocked ?? realFetch(input, init);
  };
}

