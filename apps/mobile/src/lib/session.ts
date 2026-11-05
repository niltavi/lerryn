// The session the app holds: which deployment, and the `lerryn_session`
// cookie value. The cookie is read from the sign-in response, kept in the
// platform keychain (expo-secure-store) and sent explicitly on each request,
// so it never sits in a shared cookie jar.

export const SESSION_COOKIE = 'lerryn_session';

export interface StoredSession {
  server: string;
  /** Value of the session cookie. */
  cookie: string;
}

/**
 * Finds the session cookie in a Set-Cookie header. Native networking joins
 * several Set-Cookie headers with ", ", and Expires dates contain commas too,
 * so this matches the cookie name at a cookie boundary rather than splitting.
 * Returns the value, "" when the server cleared it, or null when absent.
 */
export function readSessionCookie(
  header: string | null | undefined,
  now: number = Date.now(),
): string | null {
  if (!header) return null;
  let found: string | null = null;
  // A comma starts a new cookie only when a `name=` follows it; the comma
  // in "Expires=Wed, 21 Oct 2026" is followed by a date.
  for (const cookie of header.split(/,(?=\s*[!#$%&'*+.^_`|~0-9A-Za-z-]+=)/)) {
    const [pair = '', ...attrs] = cookie.split(';');
    const eq = pair.indexOf('=');
    if (eq === -1 || pair.slice(0, eq).trim() !== SESSION_COOKIE) continue;
    const value = pair.slice(eq + 1).trim().replace(/^"(.*)"$/, '$1');
    found = value && !isExpired(attrs, now) ? value : '';
  }
  return found;
}

function isExpired(attrs: string[], now: number): boolean {
  for (const attr of attrs) {
    const eq = attr.indexOf('=');
    const name = (eq === -1 ? attr : attr.slice(0, eq)).trim().toLowerCase();
    const value = eq === -1 ? '' : attr.slice(eq + 1).trim();
    if (name === 'max-age' && /^-?\d+$/.test(value) && Number(value) <= 0) return true;
    if (name === 'expires') {
      const at = Date.parse(value);
      if (Number.isFinite(at) && at <= now) return true;
    }
  }
  return false;
}

const VALUE_RE = /^[\x21\x23-\x2B\x2D-\x3A\x3C-\x5B\x5D-\x7E]{1,4096}$/;

/** Whether a value can be sent back in a Cookie header as is. */
export function isValidCookieValue(value: string): boolean {
  return VALUE_RE.test(value);
}

/** Serializes the session for secure storage. */
export function encodeSession(session: StoredSession): string {
  return JSON.stringify({ v: 1, server: session.server, cookie: session.cookie });
}

/** Reads a stored session; anything unexpected counts as signed out. */
export function decodeSession(raw: string | null | undefined): StoredSession | null {
  if (!raw) return null;
  try {
    const value = JSON.parse(raw) as { v?: unknown; server?: unknown; cookie?: unknown };
    if (value.v !== 1 || typeof value.server !== 'string' || !/^https?:\/\//.test(value.server)) return null;
    if (typeof value.cookie !== 'string' || !isValidCookieValue(value.cookie)) return null;
    return { server: value.server, cookie: value.cookie };
  } catch {
    return null;
  }
}
