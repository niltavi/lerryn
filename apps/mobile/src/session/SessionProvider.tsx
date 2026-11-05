import type { Schemas, LerrynClient } from '@lerryn/api';
import { ApiError, queryKeys, unwrap } from '@lerryn/core';
import { useQueryClient } from '@tanstack/react-query';
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Platform } from 'react-native';

import { createMobileClient } from '../lib/client';
import { deviceInfo } from '../lib/device';
import type { PairingLink } from '../lib/server';
import { isValidCookieValue, readSessionCookie, type StoredSession } from '../lib/session';
import { clearSession, loadSession, saveSession } from '../storage';

type Me = Schemas['Me'];

export type SessionState =
  | { status: 'loading' }
  | { status: 'signedOut'; expired: boolean }
  | { status: 'signedIn'; session: StoredSession; client: LerrynClient };

interface SessionValue {
  state: SessionState;
  /** Signs in with a built-in account. The server makes it a read-only device session. */
  signInWithPassword: (server: string, username: string, password: string) => Promise<void>;
  /** Trades a pairing code from the console's QR code for a read-only device session. */
  pair: (link: PairingLink) => Promise<void>;
  /** Ends the session on the server when it can, and always forgets it here. */
  signOut: () => Promise<void>;
  /** The server no longer accepts the session; forget it and show sign-in. */
  expire: () => void;
  /**
   * The most recent signed-in session. Screens behind the guard render once
   * more after sign-out before the navigator removes them; they read this.
   */
  last: { session: StoredSession; client: LerrynClient } | null;
}

const SessionContext = createContext<SessionValue | null>(null);

interface CallResult {
  data?: Me;
  error?: unknown;
  response: Response;
}

export function SessionProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const [state, setState] = useState<SessionState>({ status: 'loading' });
  const stateRef = useRef(state);
  stateRef.current = state;

  useEffect(() => {
    let cancelled = false;
    loadSession().then((session) => {
      if (cancelled) return;
      setState(
        session
          ? { status: 'signedIn', session, client: clientFor(session) }
          : { status: 'signedOut', expired: false },
      );
    });
    return () => {
      cancelled = true;
    };
  }, []);

  const establish = useCallback(
    async (server: string, call: (client: LerrynClient) => Promise<CallResult>) => {
      const anonymous = createMobileClient({ server, cookie: null });
      let result: CallResult;
      try {
        result = await call(anonymous);
      } catch (err) {
        throw new ApiError(0, 'network_error', null, err instanceof Error ? err.message : String(err));
      }
      // Throws ApiError for problem responses.
      const me = await unwrap(Promise.resolve(result));
      const cookie = readSessionCookie(result.response.headers.get('set-cookie'));
      if (!cookie || !isValidCookieValue(cookie)) {
        // Signed in, but the session cannot be kept: treat it as a failed sign-in.
        throw new ApiError(result.response.status, 'internal_error', null, 'session cookie missing from the sign-in response');
      }
      const session: StoredSession = { server, cookie };
      await saveSession(session);
      // Nothing cached so far belongs to this account.
      queryClient.clear();
      queryClient.setQueryData(queryKeys.me, me);
      setState({ status: 'signedIn', session, client: clientFor(session) });
    },
    [queryClient],
  );

  const signInWithPassword = useCallback(
    (server: string, username: string, password: string) =>
      establish(server, (client) =>
        client.POST('/api/v1/auth/login', {
          body: { username, password, device: deviceInfo(Platform) },
        }),
      ),
    [establish],
  );

  const pair = useCallback(
    (link: PairingLink) =>
      establish(link.baseUrl, (client) =>
        client.POST('/api/v1/auth/pairing/exchange', {
          body: { server: link.server, code: link.code, device: deviceInfo(Platform) },
        }),
      ),
    [establish],
  );

  const forget = useCallback(
    async (expired: boolean) => {
      await clearSession().catch(() => undefined);
      queryClient.clear();
      setState({ status: 'signedOut', expired });
    },
    [queryClient],
  );

  const signOut = useCallback(async () => {
    const current = stateRef.current;
    if (current.status === 'signedIn') {
      // Best effort: the device stays signed out locally even when the
      // server is unreachable; the user can also revoke the device on the web.
      await current.client.POST('/api/v1/auth/logout').catch(() => undefined);
    }
    await forget(false);
  }, [forget]);

  const expire = useCallback(() => {
    if (stateRef.current.status === 'signedIn') void forget(true);
  }, [forget]);

  const lastRef = useRef<SessionValue['last']>(null);
  if (state.status === 'signedIn') lastRef.current = { session: state.session, client: state.client };
  const last = lastRef.current;

  const value = useMemo<SessionValue>(
    () => ({ state, signInWithPassword, pair, signOut, expire, last }),
    [state, signInWithPassword, pair, signOut, expire, last],
  );
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

function clientFor(session: StoredSession): LerrynClient {
  return createMobileClient({ server: session.server, cookie: session.cookie });
}

export function useSession(): SessionValue {
  const value = useContext(SessionContext);
  if (!value) throw new Error('useSession must be used inside SessionProvider');
  return value;
}

/** The signed-in session; only for screens behind the sign-in guard. */
export function useSignedIn(): { session: StoredSession; client: LerrynClient } {
  const { state, last } = useSession();
  if (state.status === 'signedIn') return state;
  if (last) return last;
  throw new Error('useSignedIn used before signing in');
}
