import { Button, Logo, WaveLoader } from '@lerryn/ui-native';
import { router, useLocalSearchParams } from 'expo-router';
import { useMemo, useState } from 'react';
import { View } from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

import { useStrings } from '../src/i18n/useStrings';
import { pairingFromParams, serverHost } from '../src/lib/server';
import { pairingErrorMessage } from '../src/session/messages';
import { useSession } from '../src/session/SessionProvider';
import { Txt } from '../src/ui/Txt';

/**
 * Opened by `lerryn://pair?server=…&code=…` when the pairing QR code is
 * scanned with the system camera. Unlike a scan inside the app, a link can
 * come from anywhere, so it shows which server it signs in to and waits for
 * a tap before using the code.
 */
export default function PairScreen() {
  const { t, m } = useStrings();
  const insets = useSafeAreaInsets();
  const params = useLocalSearchParams<{ server?: string; code?: string }>();
  const { state, pair } = useSession();
  const link = useMemo(() => pairingFromParams(params), [params]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const leave = () => (router.canGoBack() ? router.back() : router.replace(state.status === 'signedIn' ? '/' : '/login'));

  const confirm = async () => {
    if (!link) return;
    setBusy(true);
    setError(null);
    try {
      await pair(link);
      router.replace('/');
    } catch (err) {
      setError(pairingErrorMessage(err, t, m));
    } finally {
      setBusy(false);
    }
  };

  return (
    <View
      className="flex-1 gap-7 bg-paper px-6"
      style={{ paddingTop: insets.top + 96, paddingBottom: insets.bottom + 40 }}
    >
      <View className="items-center gap-3.5">
        <Logo variant="square" size={72} className="self-center" />
        <Txt variant="title-2" weight="bold" className="text-center" accessibilityRole="header">
          {link ? m('pair.title', { host: serverHost(link.baseUrl) }) : t('app.name')}
        </Txt>
      </View>
      {!link ? (
        <Txt variant="body-sm" className="text-center text-ink-muted" accessibilityRole="alert">
          {m('pair.invalidLink')}
        </Txt>
      ) : (
        <Button size="lg" onPress={confirm} isDisabled={busy}>
          {busy ? <WaveLoader size="sm" accessibilityLabel={m('scan.pairing')} /> : null}
          <Button.Label>{m('pair.confirm')}</Button.Label>
        </Button>
      )}
      {error ? (
        <Txt variant="body-sm" className="text-center text-danger" accessibilityRole="alert">
          {error}
        </Txt>
      ) : null}
      <Button variant="ghost" size="lg" onPress={leave} isDisabled={busy}>
        {t('common.cancel')}
      </Button>
    </View>
  );
}
