import { cn, tokens, type LerrynTypeStyle } from '@lerryn/ui-native';
import { Text, type TextProps } from 'react-native';

export type TxtProps = TextProps & {
  /** A type style from design/tokens.json. Sans styles use the system font. */
  variant?: Exclude<LerrynTypeStyle, 'display-xl' | 'display' | 'title-1' | 'mono' | 'mono-sm'>;
  weight?: 'regular' | 'medium' | 'semibold' | 'bold';
  className?: string;
};

const WEIGHT = { regular: '400', medium: '500', semibold: '600', bold: '700' } as const;

/**
 * Text in a token type style. Numbers and Latin display text use Numeral,
 * identifiers use Mono (both from @lerryn/ui-native); this is everything else.
 */
export function Txt({ variant = 'body', weight, className, style, ...rest }: TxtProps) {
  const t = tokens.typeStyles[variant];
  return (
    <Text
      {...rest}
      className={cn('text-ink', className)}
      style={[
        {
          fontSize: t.fontSize,
          lineHeight: t.lineHeight,
          fontWeight: weight ? WEIGHT[weight] : (t.fontWeight as '400'),
        },
        style,
      ]}
    />
  );
}
