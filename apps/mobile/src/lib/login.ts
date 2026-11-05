import { errorKind, loginLayout, unwrap, type LoginLayout } from '@lerryn/core';

import { createMobileClient } from './client';

/** What a sign-in screen learns from a server's discover before anyone types a password. */
export type ServerProbe =
  | { kind: 'ok'; layout: LoginLayout }
  | { kind: 'unreachable' }
  | { kind: 'notLerryn' };

/**
 * Reads discover anonymously. The phone has no single sign-on, so the
 * layout's password mode decides the form: "primary" shows it, "admin"
 * keeps it behind the quiet admin entry, "none" leaves pairing only.
 */
export async function probeServer(baseUrl: string, fetch?: typeof globalThis.fetch): Promise<ServerProbe> {
  try {
    const discovery = await unwrap(createMobileClient({ server: baseUrl, cookie: null, fetch }).GET('/api/v1/discover'));
    if (!Array.isArray(discovery?.capabilities?.login_methods)) return { kind: 'notLerryn' };
    return { kind: 'ok', layout: loginLayout(discovery.capabilities) };
  } catch (err) {
    return errorKind(err) === 'offline' ? { kind: 'unreachable' } : { kind: 'notLerryn' };
  }
}
