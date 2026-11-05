import { cn, Mono, Numeral, tokens } from '@lerryn/ui-native';
import type { ReactNode } from 'react';
import { Pressable, View } from 'react-native';

import { Txt } from './Txt';

// Trailing numbers in rows (elapsed time) are DM Sans at the title-3 size.
const rowNumber = tokens.typeStyles['title-3'];

/**
 * A list row (prototype m-item): glyph, name with an optional detail line,
 * and a trailing value. Rows are separated by a hairline, not boxed.
 */
export function ListRow({
  glyph,
  title,
  titleMuted,
  detail,
  trailing,
  trailingTone,
  onPress,
  first,
  accessibilityLabel,
}: {
  glyph: ReactNode;
  title: string;
  titleMuted?: boolean;
  detail?: ReactNode;
  trailing?: string;
  trailingTone?: 'muted' | 'bad' | 'number';
  onPress?: () => void;
  first?: boolean;
  accessibilityLabel?: string;
}) {
  return (
    <Pressable
      onPress={onPress}
      disabled={!onPress}
      accessibilityRole={onPress ? 'button' : undefined}
      accessibilityLabel={accessibilityLabel}
      className={cn('min-h-14 flex-row items-center gap-3 px-1 py-2 active:opacity-70', !first && 'border-t border-line')}
    >
      {glyph}
      <View className="min-w-0 flex-1">
        <Txt variant="body" weight="medium" numberOfLines={1} className={titleMuted ? 'text-ink-muted' : undefined}>
          {title}
        </Txt>
        {detail}
      </View>
      {trailing ? (
        trailingTone === 'number' ? (
          <Numeral size="title-1" style={{ fontSize: rowNumber.fontSize, lineHeight: rowNumber.lineHeight }}>
            {trailing}
          </Numeral>
        ) : (
          <Mono size="mono-sm" className={trailingTone === 'bad' ? 'text-danger' : undefined}>
            {trailing}
          </Mono>
        )
      ) : null}
    </Pressable>
  );
}

/** A big number with its label underneath (prototype .stat). */
export function Stat({
  value,
  label,
  tone,
  size = 'md',
  suffix,
}: {
  value: string;
  label: string;
  tone?: 'bad' | 'run';
  size?: 'lg' | 'md';
  suffix?: string;
}) {
  const color = tone === 'bad' ? 'text-danger' : tone === 'run' ? 'text-run' : undefined;
  return (
    <View className="gap-1" accessible accessibilityLabel={`${label} ${value}${suffix ? ` ${suffix}` : ''}`}>
      <View className="flex-row items-baseline gap-1">
        <Numeral size={size === 'lg' ? 'display-xl' : 'display'} className={color}>
          {value}
        </Numeral>
        {suffix ? (
          <Txt variant="body" weight="semibold" className="text-ink-muted">
            {suffix}
          </Txt>
        ) : null}
      </View>
      <Txt variant="body-sm" className="text-ink-muted">
        {label}
      </Txt>
    </View>
  );
}
