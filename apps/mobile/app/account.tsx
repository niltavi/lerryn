import { useMe, type Locale } from '@lerryn/core';
import { Button, cn, Mono, useLerrynTheme } from '@lerryn/ui-native';
import { Check, Eye } from 'lucide-react-native';
import { Pressable, View } from 'react-native';

import { useStrings } from '../src/i18n/useStrings';
import type { LocalePreference } from '../src/i18n/translate';
import { serverHost } from '../src/lib/server';
import { usePrefs } from '../src/prefs/PrefsProvider';
import { useSession, useSignedIn } from '../src/session/SessionProvider';
import type { ThemePreference } from '../src/storage';
import { BackLink, Screen, SectionLabel } from '../src/ui/Screen';
import { Txt } from '../src/ui/Txt';

/** Who is signed in where, this phone's appearance and language, and sign-out. */
export default function AccountScreen() {
  const { t, m } = useStrings();
  const { colors } = useLerrynTheme();
  const { session } = useSignedIn();
  const { signOut } = useSession();
  const { prefs, update } = usePrefs();
  const me = useMe();

  const themes: { value: ThemePreference; label: string }[] = [
    { value: 'system', label: t('common.themeSystem') },
    { value: 'light', label: t('common.themeLight') },
    { value: 'dark', label: t('common.themeDark') },
  ];
  const languages: { value: LocalePreference; label: string }[] = [
    { value: 'system', label: t('common.themeSystem') },
    { value: 'zh-CN' satisfies Locale, label: '简体中文' },
    { value: 'en' satisfies Locale, label: 'English' },
  ];

  return (
    <Screen header={<BackLink label={t('overview.title')} />}>
      <View className="gap-1">
        <Txt variant="title-2" weight="bold" accessibilityRole="header">
          {me.data?.display_name || me.data?.username || ' '}
        </Txt>
        {me.data?.display_name ? <Mono size="mono-sm">{me.data.username}</Mono> : null}
      </View>

      <View className="rounded-3xl border border-line bg-liner px-4 shadow-flute-b">
        <InfoRow label={m('account.server')} value={serverHost(session.server)} mono />
        <InfoRow
          label={m('account.session')}
          value={m('account.readOnlySession')}
          icon={<Eye size={14} color={colors['ink-muted']} strokeWidth={2.25} />}
          last
        />
      </View>

      <Choice title={t('common.theme')} options={themes} value={prefs.theme} onChange={(theme) => update({ theme })} />
      <Choice
        title={t('common.language')}
        options={languages}
        value={prefs.locale}
        onChange={(locale) => update({ locale })}
      />

      <Button
        variant="danger-soft"
        size="lg"
        onPress={() => void signOut()}
      >
        {t('auth.logout')}
      </Button>
    </Screen>
  );
}

function InfoRow({
  label,
  value,
  mono,
  icon,
  last,
}: {
  label: string;
  value: string;
  mono?: boolean;
  icon?: React.ReactNode;
  last?: boolean;
}) {
  return (
    <View className={cn('min-h-12 flex-row items-center justify-between gap-3 py-3', !last && 'border-b border-line')}>
      <Txt variant="body-sm" className="text-ink-muted">
        {label}
      </Txt>
      <View className="min-w-0 flex-shrink flex-row items-center gap-1.5">
        {icon}
        {mono ? (
          <Mono size="mono-sm" numberOfLines={1} className="text-ink">
            {value}
          </Mono>
        ) : (
          <Txt variant="body-sm">{value}</Txt>
        )}
      </View>
    </View>
  );
}

function Choice<T extends string>({
  title,
  options,
  value,
  onChange,
}: {
  title: string;
  options: { value: T; label: string }[];
  value: T;
  onChange: (value: T) => void;
}) {
  const { colors } = useLerrynTheme();
  return (
    <View className="gap-2">
      <SectionLabel>{title}</SectionLabel>
      <View accessibilityRole="radiogroup">
        {options.map((o, i) => {
          const selected = o.value === value;
          return (
            <Pressable
              key={o.value}
              onPress={() => onChange(o.value)}
              accessibilityRole="radio"
              accessibilityState={{ checked: selected }}
              className={cn('min-h-12 flex-row items-center justify-between px-1', i > 0 && 'border-t border-line')}
            >
              <Txt variant="body">{o.label}</Txt>
              {selected ? <Check size={18} color={colors['kraft-strong']} strokeWidth={2.5} /> : null}
            </Pressable>
          );
        })}
      </View>
    </View>
  );
}
