import { Button, useLerrynTheme, WaveLoader } from '@lerryn/ui-native';
import { CircleAlert, SearchX, WifiOff } from 'lucide-react-native';
import { View } from 'react-native';

import { useStrings } from '../i18n/useStrings';
import { errorKind } from '../lib/errors';
import { Txt } from './Txt';

export function LoadingState() {
  const { t } = useStrings();
  return (
    <View className="items-center justify-center gap-3 py-16">
      <WaveLoader size="md" accessibilityLabel={t('common.loading')} />
    </View>
  );
}

/**
 * What a screen shows when its data could not be read. Says what happened
 * in a few words and offers to retry; there is no fallback data.
 */
export function ErrorState({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const { t, m } = useStrings();
  const { colors } = useLerrynTheme();
  const kind = errorKind(error);
  let Icon = CircleAlert;
  let title = m('errors.failed');
  let hint: string | null = m('errors.failedHint');
  if (kind === 'offline') {
    Icon = WifiOff;
    title = m('errors.offline');
    hint = m('errors.offlineHint');
  } else if (kind === 'notFound') {
    Icon = SearchX;
    title = t('states.notFoundTitle');
    hint = null;
  } else if (kind === 'signedOut') {
    title = m('errors.signedOut');
    hint = null;
  } else if (kind === 'readOnly') {
    title = m('errors.readOnly');
    hint = null;
  }
  const retryable = kind === 'offline' || kind === 'failed' || kind === 'rateLimited';
  return (
    <View className="items-center gap-3 py-16" accessibilityRole="alert">
      <Icon size={28} color={colors['ink-muted']} strokeWidth={2} />
      <Txt variant="title-3" className="text-center">
        {title}
      </Txt>
      {hint ? (
        <Txt variant="body-sm" className="text-center text-ink-muted">
          {hint}
        </Txt>
      ) : null}
      {retryable && onRetry ? (
        <Button variant="secondary" size="sm" onPress={onRetry} containerClassName="mt-2">
          {t('common.retry')}
        </Button>
      ) : null}
    </View>
  );
}

/** A quiet line for an empty list. */
export function EmptyState({ text }: { text: string }) {
  return (
    <View className="items-center py-16">
      <Txt variant="body-sm" className="text-ink-muted">
        {text}
      </Txt>
    </View>
  );
}
