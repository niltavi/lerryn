import { cn } from 'heroui-native';
import { ArrowUp, AtSign, Plus, Square, X } from 'lucide-react-native';
import { Pressable, ScrollView, Text, TextInput, View } from 'react-native';
import { Raised } from '../primitives/Raised';
import { useLerrynTheme } from '../theme/useLerrynTheme';
import { Button } from './Button';
import { WaveLoader } from './Wave';

export type PromptContext = { id: string; label: string };

export type PromptComposerProps = {
  value: string;
  onChangeText: (text: string) => void;
  /** 收到去掉首尾空白的文本 */
  onSubmit: (text: string) => void;
  /** Agent 生成中时显示停止按钮 */
  onStop?: () => void;
  isBusy?: boolean;
  busyLabel?: string;
  placeholder?: string;
  /** 已接入的应用或数据源，显示为 @ 标签 */
  contexts?: PromptContext[];
  onAddContext?: () => void;
  onRemoveContext?: (id: string) => void;
  /** 输入框为空时显示的快捷描述，点按后填入输入框 */
  suggestions?: string[];
  className?: string;
};

/** 用一句话描述要自动化的事。Lerryn 的主入口：B 楞厚度的面纸输入区 + 上下文标签 + 发送按钮。 */
export function PromptComposer({
  value,
  onChangeText,
  onSubmit,
  onStop,
  isBusy = false,
  busyLabel = '正在拆解步骤…',
  placeholder = '描述你想自动化的事，比如「每天 9 点汇总新线索发到群里」',
  contexts,
  onAddContext,
  onRemoveContext,
  suggestions,
  className,
}: PromptComposerProps) {
  const { colors } = useLerrynTheme();
  const text = value.trim();
  const showSuggestions = !isBusy && !text && suggestions && suggestions.length > 0;

  return (
    <View className={cn('gap-3', className)}>
      {showSuggestions ? (
        <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerClassName="gap-2">
          {suggestions.map((suggestion) => (
            <Pressable
              key={suggestion}
              accessibilityRole="button"
              onPress={() => onChangeText(suggestion)}
              className="h-9 justify-center rounded-full border border-line bg-liner px-3.5 active:bg-liner-2"
            >
              <Text className="text-sm text-ink">{suggestion}</Text>
            </Pressable>
          ))}
        </ScrollView>
      ) : null}

      <Raised flute="B" radiusClassName="rounded-3xl">
        <View className="gap-2 rounded-3xl border border-line bg-liner p-3">
          {isBusy ? (
            <View className="min-h-16 flex-row items-center gap-2.5 px-1">
              <WaveLoader size="md" />
              <Text className="text-base leading-6 text-ink-muted">{busyLabel}</Text>
            </View>
          ) : (
            <TextInput
              value={value}
              onChangeText={onChangeText}
              placeholder={placeholder}
              placeholderTextColor={colors['ink-muted']}
              selectionColor={colors.kraft}
              multiline
              textAlignVertical="top"
              accessibilityLabel="描述自动化"
              className="max-h-40 min-h-16 px-1 pt-1 text-base leading-6 text-ink"
            />
          )}

          <View className="flex-row items-end gap-2">
            <View className="flex-1 flex-row flex-wrap items-center gap-1.5">
              {contexts?.map((context) => (
                <View
                  key={context.id}
                  className={cn(
                    'h-8 flex-row items-center gap-1 rounded-full bg-kraft-soft pl-2.5',
                    onRemoveContext ? 'pr-1' : 'pr-3',
                  )}
                >
                  <AtSign size={13} color={colors['kraft-strong']} strokeWidth={2.5} />
                  <Text className="text-[13px] font-semibold text-kraft-strong">{context.label}</Text>
                  {onRemoveContext ? (
                    <Pressable
                      accessibilityRole="button"
                      accessibilityLabel={`移除 ${context.label}`}
                      hitSlop={8}
                      onPress={() => onRemoveContext(context.id)}
                      className="size-6 items-center justify-center rounded-full active:bg-liner-3"
                    >
                      <X size={13} color={colors['kraft-strong']} strokeWidth={2.5} />
                    </Pressable>
                  ) : null}
                </View>
              ))}
              {onAddContext ? (
                <Pressable
                  accessibilityRole="button"
                  onPress={onAddContext}
                  className="h-8 flex-row items-center gap-1 rounded-full border border-dashed border-line-strong px-2.5 active:bg-liner-2"
                >
                  <Plus size={14} color={colors['ink-muted']} strokeWidth={2.25} />
                  <Text className="text-[13px] text-ink-muted">接入应用</Text>
                </Pressable>
              ) : null}
            </View>

            {isBusy ? (
              <Button variant="secondary" size="sm" isIconOnly onPress={onStop} accessibilityLabel="停止生成">
                <Square size={14} color={colors.ink} fill={colors.ink} />
              </Button>
            ) : (
              <Button size="sm" isIconOnly isDisabled={!text} onPress={() => onSubmit(text)} accessibilityLabel="发送">
                <ArrowUp size={18} color={colors['on-kraft']} strokeWidth={2.5} />
              </Button>
            )}
          </View>
        </View>
      </Raised>
    </View>
  );
}
