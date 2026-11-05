import { cn } from 'heroui-native';
import { View } from 'react-native';
import Svg, { Circle, G, Path, Rect } from 'react-native-svg';
import { useLerrynTheme } from '../theme/useLerrynTheme';
import { LOGO_BADGE, LOGO_MARK, LOGO_PLATE } from './logoMark';

export type LogoProps = {
  /** horizontal：透明底横向图形；square：纸色方形底板；circle：纸色圆形底板 */
  variant?: 'horizontal' | 'square' | 'circle';
  /** horizontal 指图形高度，square / circle 指边长或直径 */
  size?: number;
  /** 仅 horizontal：垫一块纸色底板。暗色主题下默认开启，因为深棕侧面在深色底上看不清 */
  plate?: boolean;
  className?: string;
  accessibilityLabel?: string;
};

function Mark() {
  return (
    <>
      <Path fill={LOGO_MARK.bark.fill} d={LOGO_MARK.bark.d} />
      <Path fill={LOGO_MARK.kraft.fill} d={LOGO_MARK.kraft.d} />
    </>
  );
}

/** Lerryn logo，纯图形矢量绘制，颜色固定为 logo 原色。 */
export function Logo({
  variant = 'horizontal',
  size = 32,
  plate,
  className,
  accessibilityLabel = 'Lerryn',
}: LogoProps) {
  const { theme } = useLerrynTheme();
  const { width: mw, height: mh } = LOGO_MARK;

  if (variant === 'horizontal') {
    const mark = (
      <Svg
        width={Math.round(size * (mw / mh))}
        height={size}
        viewBox={`0 0 ${mw} ${mh}`}
        accessibilityRole="image"
        accessibilityLabel={accessibilityLabel}
      >
        <Mark />
      </Svg>
    );
    if (!(plate ?? theme === 'dark')) return <View className={cn('self-start', className)}>{mark}</View>;
    return (
      <View className={cn('self-start rounded-2xl bg-plate', className)} style={{ padding: size * 0.25 }}>
        {mark}
      </View>
    );
  }

  // 与 design/brand/logo/svg 下的正方形、圆形版本一致
  const w = LOGO_BADGE[variant];
  const s = w / mw;
  const tx = 512 - w / 2;
  const ty = 512 - (mh * s) / 2;

  return (
    <View className={cn('self-start', className)}>
      <Svg width={size} height={size} viewBox="0 0 1024 1024" accessibilityRole="image" accessibilityLabel={accessibilityLabel}>
        {variant === 'circle' ? (
          <Circle cx={512} cy={512} r={512} fill={LOGO_PLATE} />
        ) : (
          <Rect width={1024} height={1024} rx={225} fill={LOGO_PLATE} />
        )}
        <G transform={`translate(${tx} ${ty}) scale(${s})`}>
          <Mark />
        </G>
      </Svg>
    </View>
  );
}
