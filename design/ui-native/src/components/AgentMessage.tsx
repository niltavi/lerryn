import { cn } from 'heroui-native';
import type { ReactNode } from 'react';
import { Text, View } from 'react-native';
import { Logo } from './Logo';
import { WaveLoader } from './Wave';

export type AgentMessageProps = {
  role?: 'agent' | 'user';
  /** 字符串按正文排版；也可以放流程预览、按钮等富内容 */
  children?: ReactNode;
  /** Agent 正在思考：显示流动波纹和 thinkingLabel */
  isThinking?: boolean;
  thinkingLabel?: string;
  name?: string;
  time?: string;
  className?: string;
};

const body = (children: ReactNode) =>
  typeof children === 'string' ? <Text className="text-base leading-6 text-ink">{children}</Text> : children;

/** 对话中的一条消息。Agent 用牛皮纸浅底气泡和 logo 头像，用户消息靠右、用三级面纸。 */
export function AgentMessage({
  role = 'agent',
  children,
  isThinking = false,
  thinkingLabel = '正在拆解步骤…',
  name = 'Lerryn',
  time,
  className,
}: AgentMessageProps) {
  if (role === 'user') {
    return (
      <View
        className={cn(
          'max-w-[85%] self-end rounded-bl-3xl rounded-br-lg rounded-tl-3xl rounded-tr-3xl bg-liner-3 px-4 py-3',
          className,
        )}
      >
        {body(children)}
      </View>
    );
  }

  return (
    <View className={cn('flex-row gap-2.5 self-stretch', className)}>
      <Logo variant="circle" size={32} />
      <View className="flex-1 gap-1.5">
        <View className="flex-row items-baseline gap-2">
          <Text className="text-[13px] font-semibold leading-[18px] text-ink">{name}</Text>
          {time ? <Text className="text-xs leading-4 text-ink-muted">{time}</Text> : null}
        </View>
        <View className="gap-3 self-start rounded-bl-3xl rounded-br-3xl rounded-tl-lg rounded-tr-3xl bg-kraft-soft px-4 py-3">
          {isThinking ? (
            <View className="flex-row items-center gap-2.5" accessibilityLiveRegion="polite">
              <WaveLoader size="md" accessibilityLabel={thinkingLabel} />
              <Text className="text-base leading-6 text-ink-muted">{thinkingLabel}</Text>
            </View>
          ) : (
            body(children)
          )}
        </View>
      </View>
    </View>
  );
}
