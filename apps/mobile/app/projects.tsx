import { cn, useLerrynTheme } from '@lerryn/ui-native';
import { router } from 'expo-router';
import { Check } from 'lucide-react-native';
import { Pressable, View } from 'react-native';

import { useStrings } from '../src/i18n/useStrings';
import { usePrefs } from '../src/prefs/PrefsProvider';
import { useProjects } from '../src/queries/queries';
import { BackLink, Screen } from '../src/ui/Screen';
import { ErrorState, LoadingState } from '../src/ui/States';
import { Txt } from '../src/ui/Txt';

/** Picks which project the overview shows, or all of them. */
export default function ProjectsScreen() {
  const { t, m } = useStrings();
  const { colors } = useLerrynTheme();
  const { prefs, update } = usePrefs();
  const projects = useProjects();

  const choose = (projectId: string | null) => {
    update({ projectId });
    if (router.canGoBack()) router.back();
  };

  const options = [
    { id: null as string | null, name: m('overview.allProjects') },
    ...(projects.data?.items ?? []).map((p) => ({ id: p.id as string | null, name: p.name })),
  ];

  return (
    <Screen header={<BackLink label={t('overview.title')} />}>
      <Txt variant="title-2" weight="bold" accessibilityRole="header">
        {m('projects.title')}
      </Txt>
      {projects.isPending ? (
        <LoadingState />
      ) : projects.isError ? (
        <ErrorState error={projects.error} onRetry={() => void projects.refetch()} />
      ) : (
        <View accessibilityRole="radiogroup">
          {options.map((o, i) => {
            const selected = o.id === prefs.projectId;
            return (
              <Pressable
                key={o.id ?? 'all'}
                onPress={() => choose(o.id)}
                accessibilityRole="radio"
                accessibilityState={{ checked: selected }}
                className={cn('min-h-14 flex-row items-center justify-between gap-3 px-1', i > 0 && 'border-t border-line')}
              >
                <Txt variant="body" weight={selected ? 'semibold' : undefined} numberOfLines={1} className="flex-1">
                  {o.name}
                </Txt>
                {selected ? <Check size={18} color={colors['kraft-strong']} strokeWidth={2.5} /> : null}
              </Pressable>
            );
          })}
        </View>
      )}
    </Screen>
  );
}
