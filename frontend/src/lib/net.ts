// Address format checks (the frontend rejects obviously wrong input; the backend validates again). Some of these values are assembled into commands pasted into a shell, so only explicit formats pass, not "anything without whitespace".

const OCTET = '(25[0-5]|2[0-4]\\d|1\\d\\d|[1-9]?\\d)';
const IPV4 = new RegExp(`^${OCTET}(\\.${OCTET}){3}$`);
const HOSTNAME = /^(?=.{1,253}$)[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$/;

export const isIPv4 = (s: string) => IPV4.test(s);

/** IPv6 is judged by the browser's URL parser (a hand-written regex is hard to get right), after first rejecting anything other than hex digits, colons and dots. */
export function isIPv6(s: string): boolean {
  if (!s.includes(':') || !/^[0-9a-fA-F:.]+$/.test(s)) return false;
  try {
    new URL(`http://[${s}]/`);
    return true;
  } catch {
    return false;
  }
}

/** IP or CIDR; prefix length 0–32 for IPv4, 0–128 for IPv6. */
export function isIpOrCidr(s: string): boolean {
  const parts = s.split('/');
  if (parts.length > 2) return false;
  const [ip, len] = parts;
  const v4 = isIPv4(ip);
  if (!v4 && !isIPv6(ip)) return false;
  return len === undefined || (/^\d{1,3}$/.test(len) && +len <= (v4 ? 32 : 128));
}

/** host:port, where host must be IPv4, [IPv6] or a DNS hostname and port 1–65535. */
export function isHostPort(v: string): boolean {
  const m = /^(?:\[([^\]]+)\]|([^:[\]]+)):(\d{1,5})$/.exec(v);
  if (!m || +m[3] < 1 || +m[3] > 65535) return false;
  return m[1] !== undefined ? isIPv6(m[1]) : isIPv4(m[2]) || HOSTNAME.test(m[2]);
}
