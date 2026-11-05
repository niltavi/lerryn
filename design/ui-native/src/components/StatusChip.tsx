import { cn } from 'heroui-native';
import { Check, Clock, Hand, Pause, X } from 'lucide-react-native';
import type { ComponentType } from 'react';
import { Text, View } from 'react-native';
import type { LerrynColor } from '../theme/tokens';
import { useLerrynTheme } from '../theme/useLerrynTheme';
import { WaveLoader } from './Wave';

export type RunStatus = 'queued' | 'running' | 'waiting' | 'succeeded' | 'failed' | 'paused';

type IconComponent = ComponentType<{ size?: number; color?: string; strokeWidth?: number }>;

const STATUS: Record<
  RunStatus,
  { label: string; icon: IconComponent | null; container: string; text: string; color: LerrynColor }
> = {
  queued: { label: '排队中', icon: Clock, container: 'bg-liner-2', text: 'text-ink-muted', color: 'ink-muted' },
  running: { label: '运行中', icon: null, container: 'bg-run-soft', text: 'text-run', color: 'run' },
  waiting: { label: '待确认', icon: Hand, container: 'bg-warning-soft', text: 'text-warning-ink', color: 'warning-ink' },
  succeeded: { label: '已完成', icon: Check, container: 'bg-success-soft', text: 'text-success', color: 'success' },
  failed: { label: '失败', icon: X, container: 'bg-danger-soft', text: 'text-danger', color: 'danger' },
  paused: { label: '已暂停', icon: Pause, container: 'bg-liner-2', text: 'text-ink-muted', color: 'ink-muted' },
};

export type StatusChipProps = {
  status: RunStatus;
  /** 覆盖默认文案，例如「运行中 · 第 3 步」 */
  label?: string;
  size?: 'sm' | 'md';
  className?: string;
};

/** 流程与运行的状态标签。每个状态都带图标和文字，不只靠颜色区分；运行中用流动的波纹代替图标。 */
export function StatusChip({ status, label, size = 'md', className }: StatusChipProps) {
  const { colors } = useLerrynTheme();
  const spec = STATUS[status];
  const color = colors[spec.color];
  const sm = size === 'sm';
  const text = label ?? spec.label;

  return (
    <View
      accessible
      accessibilityLabel={text}
      className={cn(
        'flex-row items-center self-start rounded-full',
        sm ? 'h-6 gap-1 px-2' : 'h-7 gap-1.5 px-2.5',
        spec.container,
        className,
      )}
    >
      {spec.icon ? (
        <spec.icon size={sm ? 12 : 14} color={color} strokeWidth={2.5} />
      ) : (
        <WaveLoader size="sm" color={color} accessibilityLabel={text} />
      )}
      <Text className={cn('font-semibold', sm ? 'text-xs leading-4' : 'text-[13px] leading-[18px]', spec.text)}>
        {text}
      </Text>
    </View>
  );
}
