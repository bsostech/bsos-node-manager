import { useAuth } from '@/auth/AuthContext';

/**
 * The platform address to use in commands and URLs pasted onto a node.
 * mtls: the inbound address given by the caller (editable when registering a node); otherwise the platform's PLATFORM_ADDR, and only then the browser host.
 * http: PUBLIC_API_BASE if set at deploy time; otherwise the host of the inbound address, with protocol and port taken from the browser URL.
 * When the admin comes in via 127.0.0.1, a VPN or a jump host, nodes cannot reach the browser host; the inbound address is the one nodes can reach.
 */
export function usePlatformAddress(inbound?: string) {
  const { me } = useAuth();
  const { protocol, hostname, port } = window.location;
  const mtls = inbound || me?.platformAddr || `${hostname}:8443`;
  const host = mtls.replace(/:\d+$/, '');
  return { mtls, http: me?.publicApiBase ?? `${protocol}//${host}${port ? `:${port}` : ''}` };
}
