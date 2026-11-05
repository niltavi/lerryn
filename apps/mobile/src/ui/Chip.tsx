import { cn, useLerrynTheme } from '@lerryn/ui-native';
import type { LucideIcon } from 'lucide-react-native';
import { View } from 'react-native';

import { Txt } from './Txt';

/** A read-only fact in a pill (prototype .chip): schedule, credentials, version. */
export function Chip({
  icon: Icon,
  label,
  dot,
  accessibilityLabel,
}: {
  icon?: LucideIcon;
  label: string;
  /** A warning dot, e.g. a credential needs attention. */
  dot?: 'warn' | 'bad';
  accessibilityLabel?: string;
}) {
  const { colors } = useLerrynTheme();
  return (
    <View
      accessible
      accessibilityLabel={accessibilityLabel ?? label}
      className="h-9 flex-row items-center gap-2 rounded-full border border-line bg-liner px-3.5"
    >
      {Icon ? <Icon size={16} color={colors['ink-muted']} strokeWidth={2.25} /> : null}
      <Txt variant="body-sm">{label}</Txt>
      {dot ? <View className={cn('h-2 w-2 rounded-full', dot === 'bad' ? 'bg-danger' : 'bg-warning')} /> : null}
    </View>
  );
}
