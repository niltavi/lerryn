import { Button as HeroButton } from 'heroui-native';
import type { ComponentProps } from 'react';
import type { PressableProps } from 'react-native';
import { Raised } from '../primitives/Raised';
import type { Flute } from '../theme/tokens';

type HeroButtonProps = ComponentProps<typeof HeroButton>;
export type ButtonVariant = NonNullable<HeroButtonProps['variant']>;

export type ButtonProps = Omit<HeroButtonProps, 'feedbackVariant' | 'animation' | 'onPressIn' | 'onPressOut'> & {
  onPressIn?: PressableProps['onPressIn'];
  onPressOut?: PressableProps['onPressOut'];
  /** @deprecated Niltavi controls are flat; kept for existing callers. */
  flute?: Flute | 'none';
  containerClassName?: string;
};

function LerrynButton({ flute: _flute, containerClassName, ...rest }: ButtonProps) {
  return (
    <Raised className={containerClassName}>
      <HeroButton {...rest} feedbackVariant="scale-highlight" animation={{ scale: false }} />
    </Raised>
  );
}

/** HeroUI button with shared Niltavi styling and the existing layout API. */
export const Button = Object.assign(LerrynButton, { Label: HeroButton.Label });
