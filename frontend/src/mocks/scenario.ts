/**
 * Demo scenarios (only effective in dev / MSW):
 * - existing: an established environment: 11 nodes in our org, with alerts and allowlists, as in day-to-day operations.
 * - fresh: a brand-new deployment: the platform was just installed, with one admin and no registered nodes; the chain is running, but the platform only sees it once the first node connects.
 * The scenario lives in localStorage and the fake data reads it once at module load, so switching requires a full page reload.
 * Switch it from the dev hint area below the login form, or with ?demo=fresh / ?demo=existing in the URL (bookmarkable).
 */
export type DemoScenario = 'existing' | 'fresh';
const KEY = 'demo:scenario';
/** sessionStorage key of the mock session (shared by fixtures and scenario switching: switching logs the user out and starts again from the login page). */
export const MOCK_SESSION_KEY = 'bsos-mock-session';
/** Password shared by the demo accounts: easy to log in with, but it must still pass the platform's password rules (12+ chars, not containing the username, not a common password); no exceptions for demos.
 *  Kept here rather than in fixtures so the login page's dev hint does not drag all the fake data into the production build. */
export const DEMO_PASSWORD = 'BesuDemo-2026';

/** Progress of the fresh scenario (in memory, reset on reload, same lifetime as the fake data): whether admin has done the first login and set a new password.
 *  The login page's "where does the initial password come from" note only shows until that is done. */
export const freshProgress = { adminReady: false };

export function demoScenario(): DemoScenario {
  try { return localStorage.getItem(KEY) === 'fresh' ? 'fresh' : 'existing'; } catch { return 'existing'; }
}
export const isFreshDemo = () => demoScenario() === 'fresh';

function store(s: DemoScenario) {
  try { s === 'fresh' ? localStorage.setItem(KEY, 'fresh') : localStorage.removeItem(KEY); } catch { /* private browsing: skip */ }
  try { sessionStorage.removeItem(MOCK_SESSION_KEY); } catch { /* same as above */ }
}

/** Switch scenario and reload the page (used by the login page toggle). */
export function switchDemoScenario(s: DemoScenario): void {
  store(s);
  location.reload();
}

/** On page open, check the URL for ?demo=...: if present, apply it and strip the parameter from the URL (so a reload does not apply it again); the fake data loaded next is that scenario. */
export function applyDemoFromUrl(): void {
  const params = new URLSearchParams(location.search);
  const v = params.get('demo');
  if (v !== 'fresh' && v !== 'existing') return;
  store(v);
  params.delete('demo');
  const q = params.toString();
  history.replaceState(null, '', location.pathname + (q ? `?${q}` : '') + location.hash);
}
