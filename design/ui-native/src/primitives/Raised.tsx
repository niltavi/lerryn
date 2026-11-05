import { cn } from 'heroui-native';
import { View, type ViewProps } from 'react-native';
import type { SharedValue } from 'react-native-reanimated';
import type { Flute } from '../theme/tokens';

export type RaisedProps = ViewProps & {
  /** @deprecated Niltavi surfaces are flat; kept for existing callers. */
  flute?: Flute | 'none';
  /** @deprecated No edge is drawn in the flat theme. */
  edgeClassName?: string;
  /** @deprecated Apply the radius to the surface itself. */
  radiusClassName?: string;
  /** @deprecated Press feedback is handled by the control. */
  press?: SharedValue<number>;
  className?: string;
};

/** Keeps the layout API of legacy surfaces without depth or vertical motion. */
export function Raised({
  flute: _flute,
  edgeClassName: _edgeClassName,
  radiusClassName: _radiusClassName,
  press: _press,
  className,
  ...rest
}: RaisedProps) {
  return <View className={cn('relative', className)} {...rest} />;
}
