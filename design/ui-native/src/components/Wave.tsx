import { useEffect, useState } from 'react';
import { View, type LayoutChangeEvent, type ViewProps } from 'react-native';
import Animated, {
  Easing,
  cancelAnimation,
  useAnimatedStyle,
  useReducedMotion,
  useSharedValue,
  withRepeat,
  withTiming,
} from 'react-native-reanimated';
import Svg, { Path } from 'react-native-svg';
import { useLerrynTheme } from '../theme/useLerrynTheme';

/**
 * Lerryn 波形的 path：沿主轴每半个周期交替起伏。
 * 第一段用 Q，之后用 T 自动镜像控制点，所以波峰波谷之间是平滑连续的。
 */
export function wavePath(length: number, period: number, amplitude: number, center: number, vertical = false) {
  const half = period / 2;
  const pt = (along: number, across: number) => (vertical ? `${across} ${along}` : `${along} ${across}`);
  let d = `M ${pt(0, center)} Q ${pt(half / 2, center - amplitude * 2)} ${pt(half, center)}`;
  for (let along = period; along <= length + half; along += half) d += ` T ${pt(along, center)}`;
  return d;
}

export type WaveRuleProps = ViewProps & {
  orientation?: 'horizontal' | 'vertical';
  /** kraft：牛皮纸波纹带深棕侧面，用于品牌分隔；quiet：单线，用于流程连接线 */
  tone?: 'kraft' | 'quiet';
  period?: number;
  amplitude?: number;
  strokeWidth?: number;
  className?: string;
};

/** 波纹分隔线。横向撑满父容器宽度，纵向撑满父容器高度。 */
export function WaveRule({
  orientation = 'horizontal',
  tone = 'kraft',
  period = 24,
  amplitude = 3,
  strokeWidth = 3,
  style,
  onLayout,
  ...rest
}: WaveRuleProps) {
  const { colors } = useLerrynTheme();
  const [length, setLength] = useState(0);
  const vertical = orientation === 'vertical';
  const side = tone === 'kraft' ? 2 : 0;
  const thickness = amplitude * 2 + strokeWidth + side;
  const center = amplitude + strokeWidth / 2;
  const face = tone === 'kraft' ? colors.kraft : colors['line-strong'];

  const handleLayout = (event: LayoutChangeEvent) => {
    const { width, height } = event.nativeEvent.layout;
    setLength(Math.round(vertical ? height : width));
    onLayout?.(event);
  };

  return (
    <View
      {...rest}
      onLayout={handleLayout}
      style={[vertical ? { width: thickness } : { height: thickness }, style]}
      accessibilityElementsHidden
      importantForAccessibility="no-hide-descendants"
    >
      {length > 0 ? (
        <Svg width={vertical ? thickness : length} height={vertical ? length : thickness}>
          {side > 0 ? (
            <Path
              d={wavePath(length, period, amplitude, center + side, vertical)}
              stroke={colors['kraft-edge']}
              strokeWidth={strokeWidth}
              strokeLinecap="round"
              fill="none"
            />
          ) : null}
          <Path
            d={wavePath(length, period, amplitude, center, vertical)}
            stroke={face}
            strokeWidth={strokeWidth}
            strokeLinecap="round"
            fill="none"
          />
        </Svg>
      ) : null}
    </View>
  );
}

const LOADER = {
  sm: { width: 16, period: 8, amplitude: 2, stroke: 2 },
  md: { width: 28, period: 12, amplitude: 3, stroke: 2.5 },
  lg: { width: 44, period: 18, amplitude: 4, stroke: 3.5 },
} as const;

export type WaveLoaderProps = {
  size?: keyof typeof LOADER;
  /** 默认 kraft */
  color?: string;
  accessibilityLabel?: string;
};

/** 波纹流动：Agent 思考、流程运行中的等待指示。系统开启「减弱动态效果」时静止。 */
export function WaveLoader({ size = 'md', color, accessibilityLabel = '正在处理' }: WaveLoaderProps) {
  const { colors } = useLerrynTheme();
  const spec = LOADER[size];
  const reduceMotion = useReducedMotion();
  const shift = useSharedValue(0);

  useEffect(() => {
    if (reduceMotion) return;
    shift.value = 0;
    shift.value = withRepeat(withTiming(-spec.period, { duration: 900, easing: Easing.linear }), -1, false);
    return () => cancelAnimation(shift);
  }, [reduceMotion, shift, spec.period]);

  const animated = useAnimatedStyle(() => ({ transform: [{ translateX: shift.value }] }));
  const height = spec.amplitude * 2 + spec.stroke;
  const length = spec.width + spec.period;

  return (
    <View
      accessibilityRole="progressbar"
      accessibilityLabel={accessibilityLabel}
      style={{ width: spec.width, height, overflow: 'hidden' }}
    >
      <Animated.View style={animated}>
        <Svg width={length} height={height}>
          <Path
            d={wavePath(length, spec.period, spec.amplitude, spec.amplitude + spec.stroke / 2)}
            stroke={color ?? colors.kraft}
            strokeWidth={spec.stroke}
            strokeLinecap="round"
            fill="none"
          />
        </Svg>
      </Animated.View>
    </View>
  );
}

export type WaveGlyphProps = { size?: number; color?: string; strokeWidth?: number };

/** 静态的两段波纹，24 栅格，可与 lucide 图标混用（同样的 size / color / strokeWidth）。用作 Agent 步骤的图标。 */
export function WaveGlyph({ size = 24, color, strokeWidth = 2 }: WaveGlyphProps) {
  const { colors } = useLerrynTheme();
  return (
    <Svg width={size} height={size} viewBox="0 0 24 24">
      <Path
        d="M 3 12 Q 6 5 9 12 T 15 12 T 21 12"
        stroke={color ?? colors.ink}
        strokeWidth={strokeWidth}
        strokeLinecap="round"
        strokeLinejoin="round"
        fill="none"
      />
    </Svg>
  );
}
