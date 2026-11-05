import { Button, cn, useLerrynTheme } from '@lerryn/ui-native';
import { router } from 'expo-router';
import { ChevronLeft } from 'lucide-react-native';
import { useState, type ReactNode } from 'react';
import { Pressable, RefreshControl, ScrollView, View } from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

import { Txt } from './Txt';

/**
 * A page: the 56pt top bar from the prototype (m-top) and a scrolling body
 * with pull to refresh. Safe-area padding is applied with style because
 * SafeAreaView does not take className.
 */
export function Screen({
  header,
  children,
  onRefresh,
  bodyClassName,
  scroll = true,
}: {
  header?: ReactNode;
  children: ReactNode;
  onRefresh?: () => Promise<unknown>;
  bodyClassName?: string;
  scroll?: boolean;
}) {
  const insets = useSafeAreaInsets();
  const { colors } = useLerrynTheme();
  const [refreshing, setRefreshing] = useState(false);

  const refresh = onRefresh
    ? async () => {
        setRefreshing(true);
        try {
          await onRefresh();
        } finally {
          setRefreshing(false);
        }
      }
    : undefined;

  const body = cn('gap-6 px-4 pb-8 pt-1', bodyClassName);
  return (
    <View className="flex-1 bg-paper" style={{ paddingTop: insets.top }}>
      {header ? (
        <View className="h-14 flex-row items-center justify-between gap-3 px-4">{header}</View>
      ) : null}
      {scroll ? (
        <ScrollView
          contentContainerClassName={body}
          contentContainerStyle={{ paddingBottom: insets.bottom + 32 }}
          refreshControl={
            refresh ? <RefreshControl refreshing={refreshing} onRefresh={refresh} tintColor={colors['ink-muted']} /> : undefined
          }
        >
          {children}
        </ScrollView>
      ) : (
        <View className={cn('flex-1', body)} style={{ paddingBottom: insets.bottom + 32 }}>
          {children}
        </View>
      )}
    </View>
  );
}

/** The back link in the top bar: chevron plus the name of the page it returns to. */
export function BackLink({ label, fallback = '/' }: { label: string; fallback?: string }) {
  const { colors } = useLerrynTheme();
  const back = () => (router.canGoBack() ? router.back() : router.replace(fallback as '/'));
  return (
    <Pressable
      onPress={back}
      accessibilityRole="link"
      accessibilityLabel={label}
      hitSlop={8}
      className="h-11 min-w-0 flex-1 flex-row items-center gap-0.5"
    >
      <ChevronLeft size={22} color={colors.ink} strokeWidth={2.25} />
      <Txt variant="body" weight="medium" numberOfLines={1} className="min-w-0 flex-shrink">
        {label}
      </Txt>
    </Pressable>
  );
}

/** A small icon button for the top bar (copy, open in browser). */
export function IconAction({
  label,
  onPress,
  children,
}: {
  label: string;
  onPress: () => void;
  children: ReactNode;
}) {
  return (
    <Button variant="ghost" size="sm" isIconOnly accessibilityLabel={label} onPress={onPress}>
      {children}
    </Button>
  );
}

/** The muted section label above a list ("运行中", "任务"). */
export function SectionLabel({ children }: { children: ReactNode }) {
  return (
    <Txt variant="label" weight="semibold" className="text-ink-muted">
      {children}
    </Txt>
  );
}
