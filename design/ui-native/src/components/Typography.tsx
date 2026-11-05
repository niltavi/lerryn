import { cn } from 'heroui-native';
import { Text, type TextProps } from 'react-native';
import { fontFamily, typeStyles } from '../theme/tokens';

export type NumeralProps = TextProps & {
  size?: 'display-xl' | 'display' | 'title-1';
  className?: string;
};

/**
 * DM Sans 数字与拉丁字样：运行次数、耗时、节省的小时数、品牌英文。
 * DM Sans 没有中文字形，中文标题请用 Typography.Heading（系统黑体），否则中文会回落成常规字重。
 */
export function Numeral({ size = 'display-xl', className, style, ...rest }: NumeralProps) {
  const t = typeStyles[size];
  return (
    <Text
      {...rest}
      className={cn('text-ink', className)}
      style={[
        {
          fontFamily: size === 'display-xl' ? fontFamily['display-heavy'] : fontFamily.display,
          fontSize: t.fontSize,
          lineHeight: t.lineHeight,
          letterSpacing: size === 'display-xl' ? -0.8 : -0.4,
          fontVariant: ['tabular-nums'],
        },
        style,
      ]}
    />
  );
}

export type MonoProps = TextProps & {
  size?: 'mono' | 'mono-sm';
  className?: string;
};

/** IBM Plex Mono：Cron 表达式、运行 ID、变量名、耗时。默认 ink-muted。 */
export function Mono({ size = 'mono', className, style, ...rest }: MonoProps) {
  const t = typeStyles[size];
  return (
    <Text
      {...rest}
      className={cn('text-ink-muted', className)}
      style={[
        {
          fontFamily: size === 'mono' ? fontFamily['mono-medium'] : fontFamily.mono,
          fontSize: t.fontSize,
          lineHeight: t.lineHeight,
        },
        style,
      ]}
    />
  );
}
