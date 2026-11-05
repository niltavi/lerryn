import '../global.css';

import type { Schemas } from '@lerryn/api';
import { ApiProvider, I18nProvider, queryKeys, useMe } from '@lerryn/core';
import { useLerrynFonts, useLerrynTheme, LerrynProvider } from '@lerryn/ui-native';
import { QueryCache, QueryClient, QueryClientProvider, useQueryClient } from '@tanstack/react-query';
import { useLocales } from 'expo-localization';
import { Stack } from 'expo-router';
import { StatusBar } from 'expo-status-bar';
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';
import { SafeAreaProvider } from 'react-native-safe-area-context';

import { resolveLocale } from '../src/i18n/translate';
import { createMobileClient } from '../src/lib/client';
import { errorKind } from '../src/lib/errors';
import { LiveEventsProvider } from '../src/live/LiveEvents';
import { PrefsProvider, usePrefs } from '../src/prefs/PrefsProvider';
import { SessionProvider, useSession } from '../src/session/SessionProvider';

type Me = Schemas['Me'];

export default function RootLayout() {
  const [fontsLoaded] = useLerrynFonts();
  // Set once the session layer mounts; the cache reports expired sessions to it.
  const expireRef = useRef<() => void>(() => {});
  const [queryClient] = useState(
    () =>
      new QueryClient({
        queryCache: new QueryCache({
          onError: (err) => {
            if (errorKind(err) === 'signedOut') expireRef.current();
          },
        }),
        defaultOptions: { queries: { staleTime: 5_000 } },
      }),
  );
  if (!fontsLoaded) return null;

  return (
    <SafeAreaProvider>
      <LerrynProvider>
        <QueryClientProvider client={queryClient}>
          <PrefsProvider>
            <SessionProvider>
              <ExpireBridge expireRef={expireRef} />
              <Localized>
                <Navigator />
              </Localized>
            </SessionProvider>
          </PrefsProvider>
        </QueryClientProvider>
      </LerrynProvider>
    </SafeAreaProvider>
  );
}

function ExpireBridge({ expireRef }: { expireRef: { current: () => void } }) {
  const { expire } = useSession();
  useEffect(() => {
    expireRef.current = expire;
  }, [expire, expireRef]);
  return null;
}

/** Picks the language: this phone's choice, then the account's, then the system's. */
function Localized({ children }: { children: ReactNode }) {
  const { prefs } = usePrefs();
  const locales = useLocales();
  const me = useCachedMe();
  const locale = resolveLocale(
    prefs.locale,
    me?.locale,
    locales.map((l) => l.languageTag),
  );
  return (
    <I18nProvider key={locale} locale={locale}>
      {children}
    </I18nProvider>
  );
}

/**
 * The account as cached by the signed-in screens, without adding a query
 * observer of its own (MeGuard owns the fetch).
 */
function useCachedMe(): Me | null | undefined {
  const queryClient = useQueryClient();
  const read = useCallback(() => queryClient.getQueryData<Me | null>(queryKeys.me), [queryClient]);
  const [me, setMe] = useState(read);
  useEffect(
    () =>
      queryClient.getQueryCache().subscribe((event) => {
        if (event.query.queryKey[0] !== queryKeys.me[0]) return;
        // Cache events can fire while another component renders; apply after it.
        queueMicrotask(() => setMe(read()));
      }),
    [queryClient, read],
  );
  return me;
}

function Navigator() {
  const { state, last } = useSession();
  const { prefs, loaded } = usePrefs();
  const { theme } = useLerrynTheme();
  if (state.status === 'loading' || !loaded) return null;
  const signedIn = state.status === 'signedIn';

  const stack = (
    <Stack screenOptions={{ headerShown: false, contentStyle: { backgroundColor: 'transparent' } }}>
      <Stack.Protected guard={signedIn}>
        <Stack.Screen name="index" />
        <Stack.Screen name="tasks/[taskId]" />
        <Stack.Screen name="runs/[runId]" />
        <Stack.Screen name="account" options={{ presentation: 'modal' }} />
        <Stack.Screen name="projects" options={{ presentation: 'modal' }} />
      </Stack.Protected>
      <Stack.Protected guard={!signedIn}>
        <Stack.Screen name="login" />
        <Stack.Screen name="scan" />
      </Stack.Protected>
      <Stack.Screen name="pair" />
    </Stack>
  );

  return (
    <>
      <StatusBar style={theme === 'dark' ? 'light' : 'dark'} />
      {/* The tree keeps one shape signed in and out, so the navigator is
          never remounted and the sign-in guards just switch screens. */}
      <ApiProvider client={signedIn ? state.client : (last?.client ?? signedOutClient)}>
        {signedIn ? <MeGuard /> : null}
        <LiveEventsProvider session={signedIn ? state.session : null} projectId={prefs.projectId}>
          {stack}
        </LiveEventsProvider>
      </ApiProvider>
    </>
  );
}

// Screens shown while signed out make their own requests to the server the
// user enters; nothing reads this client before the first sign-in.
const signedOutClient = createMobileClient({ server: 'http://localhost', cookie: null });

/**
 * Loads the account once signed in. getMe answers null when the session is
 * no longer valid (expired, revoked from the web, account disabled), which
 * signs the phone out.
 */
function MeGuard() {
  const { expire } = useSession();
  const me = useMe();
  useEffect(() => {
    if (me.isSuccess && me.data === null) expire();
  }, [expire, me.data, me.isSuccess]);
  return null;
}
