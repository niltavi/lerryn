import { Card, PressableFeedback, cn } from 'heroui-native';
import { Box, Split, Zap } from 'lucide-react-native';
import type { ComponentType } from 'react';
import { Text, View } from 'react-native';
import { Raised } from '../primitives/Raised';
import type { LerrynColor } from '../theme/tokens';
import { useLerrynTheme } from '../theme/useLerrynTheme';
import { StatusChip, type RunStatus } from './StatusChip';
import { Mono } from './Typography';
import { WaveGlyph, WaveRule } from './Wave';

type IconComponent = ComponentType<{ size?: number; color?: string; strokeWidth?: number }>;

export type FlowStepKind = 'trigger' | 'agent' | 'action' | 'condition';

const KIND: Record<
  FlowStepKind,
  { label: string; icon: IconComponent; badge: string; edge: string; iconColor: LerrynColor }
> = {
  trigger: { label: '触发', icon: Zap, badge: 'bg-kraft', edge: 'bg-kraft-edge', iconColor: 'on-kraft' },
  agent: { label: 'Agent', icon: WaveGlyph, badge: 'bg-kraft-soft', edge: 'bg-edge', iconColor: 'kraft-strong' },
  action: { label: '动作', icon: Box, badge: 'bg-liner-2', edge: 'bg-edge', iconColor: 'ink' },
  condition: { label: '条件', icon: Split, badge: 'bg-liner-2', edge: 'bg-edge', iconColor: 'ink' },
};

export type FlowStepProps = {
  kind: FlowStepKind;
  /** 从 1 开始的步骤序号 */
  index: number;
  title: string;
  description?: string;
  /** 等宽一行：Cron、字段映射、耗时 */
  meta?: string;
  status?: RunStatus;
  /** 覆盖默认图标，接受 lucide 图标或同签名组件 */
  icon?: IconComponent;
  /** 最后一步不画连接线 */
  isLast?: boolean;
  onPress?: () => void;
  className?: string;
};

/** 自动化流程中的一步。左侧是类型徽标和波纹连接线，右侧是步骤卡片。 */
export function FlowStep({
  kind,
  index,
  title,
  description,
  meta,
  status,
  icon,
  isLast,
  onPress,
  className,
}: FlowStepProps) {
  const { colors } = useLerrynTheme();
  const spec = KIND[kind];
  const Icon = icon ?? spec.icon;

  const card = (
    <Card>
      <View className="flex-row items-start justify-between gap-3">
        <View className="flex-1 gap-1">
          <Text className="text-xs font-medium leading-4 text-ink-muted">
            {String(index).padStart(2, '0')} · {spec.label}
          </Text>
          <Text className="text-[17px] font-semibold leading-6 text-ink">{title}</Text>
          {description ? <Text className="text-sm leading-5 text-ink-muted">{description}</Text> : null}
        </View>
        {status ? <StatusChip status={status} size="sm" /> : null}
      </View>
      {meta ? (
        <View className="mt-3 self-start rounded-md bg-liner-2 px-2 py-0.5">
          <Mono size="mono-sm">{meta}</Mono>
        </View>
      ) : null}
    </Card>
  );

  return (
    <View className={cn('flex-row gap-3', className)}>
      <View className="items-center">
        <Raised flute="E" edgeClassName={spec.edge} radiusClassName="rounded-xl">
          <View className={cn('size-11 items-center justify-center rounded-xl', spec.badge)}>
            <Icon size={20} color={colors[spec.iconColor]} strokeWidth={2.25} />
          </View>
        </Raised>
        {isLast ? null : (
          <WaveRule orientation="vertical" tone="quiet" period={20} amplitude={2} strokeWidth={2} className="my-1.5 flex-1" />
        )}
      </View>
      <View className={cn('flex-1', isLast ? null : 'pb-4')}>
        {onPress ? (
          <PressableFeedback onPress={onPress} accessibilityRole="button">
            {card}
          </PressableFeedback>
        ) : (
          card
        )}
      </View>
    </View>
  );
}
