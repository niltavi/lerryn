// Server addresses and pairing links. Parsed by hand rather than with URL:
// React Native's URL polyfill does not implement most accessors, and these
// functions also run in the unit tests under Node.

const SCHEME_RE = /^([a-z][a-z0-9+.-]*):\/\//i;
// host: a DNS name, an IPv4 address or a bracketed IPv6 address; optional port.
const ORIGIN_RE = /^(https?):\/\/(\[[0-9a-f:.]+\]|[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*)(:\d{1,5})?$/i;

/**
 * Turns what the user typed into the deployment address the app talks to:
 * `lerryn.example.com` becomes `https://lerryn.example.com`. A path is kept
 * (a deployment may live under one), the query, fragment and trailing slash
 * are dropped. Returns null when it is not an http or https address.
 */
export function normalizeServerUrl(input: string): string | null {
  let value = input.trim();
  if (!value) return null;
  const scheme = SCHEME_RE.exec(value)?.[1];
  if (!scheme) value = `https://${value}`;
  else if (!/^https?$/i.test(scheme)) return null;

  value = value.replace(/[?#].*$/, '');
  const slash = value.indexOf('/', value.indexOf('://') + 3);
  const origin = slash === -1 ? value : value.slice(0, slash);
  let path = slash === -1 ? '' : value.slice(slash);
  if (origin.includes('@')) return null; // user info would be sent to the server
  if (!ORIGIN_RE.test(origin)) return null;
  const port = /:(\d+)$/.exec(origin)?.[1];
  if (port && (Number(port) < 1 || Number(port) > 65535)) return null;
  path = path.replace(/\/+$/, '');
  if (/\s/.test(path)) return null;
  const [, proto, rest] = /^(https?):\/\/(.*)$/i.exec(origin)!;
  return `${proto!.toLowerCase()}://${rest!.toLowerCase()}${path}`;
}

/** scheme://host[:port] of a normalized server address, for the Origin header. */
export function serverOrigin(server: string): string {
  const m = /^(https?:\/\/[^/]+)/i.exec(server);
  return m ? m[1]! : server;
}

/** The host shown to the user: `lerryn.example.com`, with the port when there is one. */
export function serverHost(server: string): string {
  return serverOrigin(server).replace(/^https?:\/\//i, '');
}

/** Whether the address is plain http, so the app can say the connection is not encrypted. */
export function isInsecure(server: string): boolean {
  return /^http:\/\//i.test(server);
}

export interface PairingLink {
  /** The deployment address exactly as the console put it in the link. */
  server: string;
  /** Normalized form of server, used as the API base address. */
  baseUrl: string;
  code: string;
}

const CODE_RE = /^[A-Za-z0-9]{4,16}$/;

/** Normalizes a pairing code typed by hand: drops spaces and dashes, upper-cases. */
export function normalizePairingCode(input: string): string | null {
  const code = input.replace(/[\s-]+/g, '').toUpperCase();
  return CODE_RE.test(code) ? code : null;
}

function parseQuery(query: string): Map<string, string> {
  const out = new Map<string, string>();
  for (const part of query.split('&')) {
    if (!part) continue;
    const eq = part.indexOf('=');
    const rawKey = eq === -1 ? part : part.slice(0, eq);
    const rawValue = eq === -1 ? '' : part.slice(eq + 1);
    try {
      const key = decodeURIComponent(rawKey.replace(/\+/g, ' '));
      if (!out.has(key)) out.set(key, decodeURIComponent(rawValue.replace(/\+/g, ' ')));
    } catch {
      // A malformed escape makes the whole link invalid below.
      return new Map();
    }
  }
  return out;
}

/**
 * Reads the console's pairing QR code: `lerryn://pair?server=<address>&code=<code>`
 * (contract: PairingCode.uri). Anything else returns null, so a random QR
 * code never starts a sign-in.
 */
export function parsePairingUri(raw: string): PairingLink | null {
  const m = /^lerryn:\/\/pair\/?\?(.*)$/i.exec(raw.trim());
  if (!m) return null;
  return pairingFromParams(parseQuery(m[1]!.replace(/#.*$/, '')));
}

/** Validates pairing parameters, e.g. from a deep link the router already parsed. */
export function pairingFromParams(params: { get(key: string): string | undefined | null } | Record<string, unknown>): PairingLink | null {
  const get = (key: string): unknown =>
    typeof (params as Map<string, string>).get === 'function'
      ? (params as Map<string, string>).get(key)
      : (params as Record<string, unknown>)[key];
  const server = get('server');
  const rawCode = get('code');
  if (typeof server !== 'string' || typeof rawCode !== 'string') return null;
  // The link must already name a full http(s) address; do not guess a scheme.
  if (!/^https?:\/\//i.test(server.trim())) return null;
  const baseUrl = normalizeServerUrl(server);
  const code = normalizePairingCode(rawCode);
  if (!baseUrl || !code) return null;
  return { server: server.trim(), baseUrl, code };
}
