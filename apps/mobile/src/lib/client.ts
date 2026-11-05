import { createLerrynClient, type LerrynClient } from '@lerryn/api';

import { SESSION_COOKIE } from './session';
import { serverOrigin } from './server';

export interface MobileClientOptions {
  server: string;
  /** Session cookie value; null for the sign-in requests. */
  cookie: string | null;
  fetch?: typeof globalThis.fetch;
}

/**
 * Headers that authenticate a request made outside openapi-fetch, such as
 * the event streams.
 */
export function authHeaders(cookie: string | null): Record<string, string> {
  return cookie ? { Cookie: `${SESSION_COOKIE}=${cookie}` } : {};
}

/**
 * The API client for one deployment. The cookie is sent explicitly and the
 * platform cookie store is bypassed, so signing out locally really signs the
 * app out. Writes carry the deployment's own Origin: the server refuses
 * cookie-authenticated writes from other origins to stop cross-site request
 * forgery in browsers, and a native app has no page origin of its own. The
 * only write the app makes is logout.
 */
export function createMobileClient({ server, cookie, fetch }: MobileClientOptions): LerrynClient {
  const client = createLerrynClient({
    baseUrl: server,
    credentials: 'omit',
    ...(fetch ? { fetch } : {}),
  });
  const origin = serverOrigin(server);
  client.use({
    onRequest({ request }) {
      if (cookie) request.headers.set('Cookie', `${SESSION_COOKIE}=${cookie}`);
      if (request.method !== 'GET' && request.method !== 'HEAD') request.headers.set('Origin', origin);
      return request;
    },
  });
  return client;
}
