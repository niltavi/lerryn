import { Button, Input, Label, TextField, useLerrynTheme, WaveLoader } from '@lerryn/ui-native';
import { CameraView, useCameraPermissions, type BarcodeScanningResult } from 'expo-camera';
import { Camera } from 'lucide-react-native';
import { useRef, useState } from 'react';
import { KeyboardAvoidingView, Linking, Platform, ScrollView, View } from 'react-native';

import { useStrings } from '../src/i18n/useStrings';
import { normalizePairingCode, normalizeServerUrl, parsePairingUri, type PairingLink } from '../src/lib/server';
import { useSession } from '../src/session/SessionProvider';
import { BackLink, Screen } from '../src/ui/Screen';
import { Txt } from '../src/ui/Txt';
import { pairingErrorMessage } from '../src/session/messages';

/** Scans the pairing QR code shown in the web console's settings, or takes the code by hand. */
export default function ScanScreen() {
  const { t, m } = useStrings();
  const { colors } = useLerrynTheme();
  const { pair } = useSession();
  const [permission, requestPermission] = useCameraPermissions();
  const [manual, setManual] = useState(false);
  const [server, setServer] = useState('');
  const [code, setCode] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // The camera reports the same code many times a second; handle it once.
  const handling = useRef(false);

  const start = async (link: PairingLink) => {
    setBusy(true);
    setError(null);
    try {
      await pair(link);
      // Signed in: the guarded stack replaces this screen with the overview.
    } catch (err) {
      setError(pairingErrorMessage(err, t, m));
      handling.current = false;
    } finally {
      setBusy(false);
    }
  };

  const onScanned = ({ data }: BarcodeScanningResult) => {
    if (handling.current || busy) return;
    const link = parsePairingUri(data);
    if (!link) {
      setError(m('scan.notPairing'));
      return;
    }
    handling.current = true;
    void start(link);
  };

  const submitManual = () => {
    const baseUrl = normalizeServerUrl(server);
    if (!baseUrl) return setError(m('login.errors.serverInvalid'));
    const normalized = normalizePairingCode(code);
    if (!normalized) return setError(m('scan.errors.codeFormat'));
    // A typed address is normalized; the server compares it with its public URL.
    void start({ server: baseUrl, baseUrl, code: normalized });
  };

  return (
    <Screen header={<BackLink label={t('auth.login')} fallback="/login" />} scroll={false}>
      <KeyboardAvoidingView className="flex-1" behavior={Platform.OS === 'ios' ? 'padding' : undefined}>
        <ScrollView keyboardShouldPersistTaps="handled" contentContainerClassName="gap-6">
          <Txt variant="title-2" weight="bold" accessibilityRole="header">
            {m('scan.title')}
          </Txt>

          {!manual ? (
            <View className="aspect-square overflow-hidden rounded-3xl border border-line bg-liner-2">
              {permission?.granted ? (
                <CameraView
                  style={{ flex: 1 }}
                  facing="back"
                  barcodeScannerSettings={{ barcodeTypes: ['qr'] }}
                  onBarcodeScanned={busy ? undefined : onScanned}
                />
              ) : (
                <View className="flex-1 items-center justify-center gap-4 p-6">
                  <Camera size={32} color={colors['ink-muted']} strokeWidth={2} />
                  <Txt variant="body-sm" className="text-center text-ink-muted">
                    {m('scan.needCamera')}
                  </Txt>
                  {permission && !permission.canAskAgain ? (
                    <Button variant="secondary" size="sm" onPress={() => Linking.openSettings()}>
                      {m('scan.openSettings')}
                    </Button>
                  ) : (
                    <Button variant="secondary" size="sm" onPress={requestPermission}>
                      {m('scan.allowCamera')}
                    </Button>
                  )}
                </View>
              )}
              {busy ? (
                <View className="absolute inset-0 items-center justify-center gap-3 bg-paper/80">
                  <WaveLoader size="md" accessibilityLabel={m('scan.pairing')} />
                  <Txt variant="body-sm">{m('scan.pairing')}</Txt>
                </View>
              ) : null}
            </View>
          ) : (
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
                />
              </TextField>
              <TextField>
                <Label>{m('scan.code')}</Label>
                <Input
                  value={code}
                  onChangeText={setCode}
                  placeholder={m('scan.codePlaceholder')}
                  autoCapitalize="characters"
                  autoCorrect={false}
                  maxLength={16}
                  returnKeyType="go"
                  onSubmitEditing={submitManual}
                />
              </TextField>
              <Button size="lg" onPress={submitManual} isDisabled={busy}>
                {t('auth.login')}
              </Button>
            </View>
          )}

          {error ? (
            <Txt variant="body-sm" className="text-danger" accessibilityRole="alert" accessibilityLiveRegion="polite">
              {error}
            </Txt>
          ) : null}

          <Button
            variant="ghost"
            size="sm"
            onPress={() => {
              setError(null);
              setManual((v) => !v);
            }}
          >
            {manual ? m('scan.title') : m('scan.manual')}
          </Button>
        </ScrollView>
      </KeyboardAvoidingView>
    </Screen>
  );
}
