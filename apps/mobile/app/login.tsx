import { isApiError } from '@lerryn/core';
import { Button, Input, Label, Logo, TextField, useLerrynTheme } from '@lerryn/ui-native';
import { router } from 'expo-router';
import { Eye, LockOpen, ScanLine } from 'lucide-react-native';
import { useEffect, useRef, useState } from 'react';
import { KeyboardAvoidingView, Platform, ScrollView, View, type TextInput } from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

import { useStrings } from '../src/i18n/useStrings';
import { errorKind } from '../src/lib/errors';
import { probeServer, type ServerProbe } from '../src/lib/login';
import { isInsecure, normalizeServerUrl } from '../src/lib/server';
import { useSession } from '../src/session/SessionProvider';
import { loadLastServer } from '../src/storage';
import { Txt } from '../src/ui/Txt';

/** Sign-in (prototype M-Login): scan the console's pairing code, or a built-in account. */
export default function LoginScreen() {
  const { t, m } = useStrings();
  const { colors } = useLerrynTheme();
  const insets = useSafeAreaInsets();
  const { state, signInWithPassword } = useSession();
  const [server, setServer] = useState('');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const expired = state.status === 'signedOut' && state.expired;
  const usernameRef = useRef<TextInput>(null);
  const passwordRef = useRef<TextInput>(null);

  useEffect(() => {
    void loadLastServer().then((s) => {
      if (s) setServer((current) => current || s);
    });
  }, []);

  const baseUrl = normalizeServerUrl(server);

  // Read the server's sign-in settings once the address settles, so a
  // deployment without password login hides the form before anyone types
  // into it (tech design 12.1); admins keep a quiet entry.
  const [probe, setProbe] = useState<{ url: string; result: ServerProbe } | null>(null);
  const [adminEntry, setAdminEntry] = useState(false);
  useEffect(() => {
    if (!baseUrl) return;
    let live = true;
    const id = setTimeout(() => {
      void probeServer(baseUrl).then((result) => {
        if (live) setProbe({ url: baseUrl, result });
      });
    }, PROBE_DELAY_MS);
    return () => {
      live = false;
      clearTimeout(id);
    };
  }, [baseUrl]);
  const known = probe && probe.url === baseUrl && probe.result.kind === 'ok' ? probe.result.layout : null;
  const passwordMode = known?.password ?? 'primary';
  const showForm = passwordMode === 'primary' || (passwordMode === 'admin' && adminEntry);

  const submit = async () => {
    setError(null);
    if (!baseUrl) return setError(m('login.errors.serverInvalid'));
    if (!username.trim() || !password) return setError(t('auth.errors.required'));
    setBusy(true);
    try {
      // Check the address first, so a typo reads as "not a Lerryn server"
      // rather than as a wrong password.
      const result = await probeServer(baseUrl);
      setProbe({ url: baseUrl, result });
      if (result.kind === 'unreachable') throw new LoginError(m('login.errors.serverUnreachable'));
      if (result.kind === 'notLerryn') throw new LoginError(m('login.errors.notLerryn'));
      if (result.layout.password === 'none') throw new LoginError(m('login.errors.passwordDisabled'));
      await signInWithPassword(baseUrl, username.trim(), password);
    } catch (err) {
      setError(loginMessage(err, t, m));
    } finally {
      setBusy(false);
    }
  };

  return (
    <KeyboardAvoidingView className="flex-1 bg-paper" behavior={Platform.OS === 'ios' ? 'padding' : undefined}>
      <ScrollView
        keyboardShouldPersistTaps="handled"
        contentContainerClassName="flex-grow gap-7 px-6"
        contentContainerStyle={{ paddingTop: insets.top + 72, paddingBottom: insets.bottom + 32 }}
      >
        <View className="items-center gap-3.5">
          <Logo variant="square" size={72} className="self-center" />
          <Txt variant="title-2" weight="bold" accessibilityRole="header">
            {t('app.name')}
          </Txt>
        </View>

        {expired ? (
          <Txt variant="body-sm" className="text-center text-ink-muted" accessibilityRole="alert">
            {m('errors.signedOut')}
          </Txt>
        ) : null}

        <Button size="lg" onPress={() => router.push('/scan')}>
          <ScanLine size={22} color={colors['on-kraft']} strokeWidth={2.25} />
          <Button.Label>{m('login.scan')}</Button.Label>
        </Button>

        <View className="flex-row items-center gap-3" accessibilityElementsHidden importantForAccessibility="no-hide-descendants">
          <View className="h-px flex-1 bg-line" />
          <Txt variant="caption" className="text-ink-muted">
            {m('login.or')}
          </Txt>
          <View className="h-px flex-1 bg-line" />
        </View>

        <View className="gap-3">
          <TextField>
            <Label>{m('login.server')}</Label>
            <Input
              value={server}
              onChangeText={setServer}
              placeholder={m('login.serverPlaceholder')}
              autoCapitalize="none"
              autoCorrect={false}
              keyboardType="url"
              textContentType="URL"
              returnKeyType="next"
              submitBehavior="submit"
              onSubmitEditing={() => usernameRef.current?.focus()}
            />
          </TextField>
          {baseUrl && isInsecure(baseUrl) ? (
            <View className="flex-row items-center gap-1.5">
              <LockOpen size={14} color={colors['warning-ink']} strokeWidth={2.25} />
              <Txt variant="caption" className="text-warning-ink">
                {m('login.insecure')}
              </Txt>
            </View>
          ) : null}
          {showForm ? (
            <>
          <TextField>
            <Label>{t('auth.username')}</Label>
            <Input
              ref={usernameRef}
              value={username}
              onChangeText={setUsername}
              autoCapitalize="none"
              autoCorrect={false}
              autoComplete="username"
              textContentType="username"
              returnKeyType="next"
              submitBehavior="submit"
              onSubmitEditing={() => passwordRef.current?.focus()}
            />
          </TextField>
          <TextField>
            <Label>{t('auth.password')}</Label>
            <Input
              ref={passwordRef}
              value={password}
              onChangeText={setPassword}
              secureTextEntry
              autoComplete="current-password"
              textContentType="password"
              returnKeyType="go"
              onSubmitEditing={submit}
            />
          </TextField>
            </>
          ) : null}
          {error ? (
            <Txt variant="body-sm" className="text-danger" accessibilityRole="alert" accessibilityLiveRegion="polite">
              {error}
            </Txt>
          ) : null}
          {showForm ? (
            <Button variant="secondary" size="lg" onPress={submit} isDisabled={busy}>
              {t('auth.login')}
            </Button>
          ) : passwordMode === 'admin' ? (
            <Button variant="ghost" size="sm" onPress={() => setAdminEntry(true)}>
              {t('auth.adminLogin')}
            </Button>
          ) : null}
        </View>

        <View className="mt-auto flex-row items-center justify-center gap-1.5">
          <Eye size={14} color={colors['ink-muted']} strokeWidth={2.25} />
          <Txt variant="caption" className="text-ink-muted">
            {m('login.readOnly')}
          </Txt>
        </View>
      </ScrollView>
    </KeyboardAvoidingView>
  );
}

class LoginError extends Error {}

/** How long the address must stay unchanged before the screen reads its sign-in settings. */
const PROBE_DELAY_MS = 500;

function loginMessage(
  err: unknown,
  t: ReturnType<typeof useStrings>['t'],
  m: ReturnType<typeof useStrings>['m'],
): string {
  if (err instanceof LoginError) return err.message;
  if (isApiError(err)) {
    if (err.code === 'password_login_disabled') return m('login.errors.passwordDisabled');
    if (err.code === 'rate_limited') return t('auth.errors.rateLimited');
  }
  switch (errorKind(err)) {
    case 'signedOut':
      return t('auth.errors.invalidCredentials');
    case 'offline':
      return m('login.errors.serverUnreachable');
    default:
      return t('auth.errors.generic');
  }
}
