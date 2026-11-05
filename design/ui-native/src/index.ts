// @lerryn/ui-native：Lerryn React Native 组件库。
// HeroUI Native 的全部组件从这里原样导出，并通过 theme.css 换上 Lerryn 主题；
// 同名的 Button 被替换为保留布局参数的扁平版本，其余是 Lerryn 自己的组件。

export * from 'heroui-native';

export { Button, type ButtonProps, type ButtonVariant } from './components/Button';
export { Raised, type RaisedProps } from './primitives/Raised';

export { StatusChip, type RunStatus, type StatusChipProps } from './components/StatusChip';
export { FlowStep, type FlowStepKind, type FlowStepProps } from './components/FlowStep';
export {
  PromptComposer,
  type PromptComposerProps,
  type PromptContext,
} from './components/PromptComposer';
export { AgentMessage, type AgentMessageProps } from './components/AgentMessage';
export {
  WaveGlyph,
  WaveLoader,
  WaveRule,
  wavePath,
  type WaveGlyphProps,
  type WaveLoaderProps,
  type WaveRuleProps,
} from './components/Wave';
export { Mono, Numeral, type MonoProps, type NumeralProps } from './components/Typography';
export { Logo, type LogoProps } from './components/Logo';

export { LerrynProvider, type LerrynProviderProps } from './provider';
export { useLerrynFonts, lerrynFonts } from './fonts';
export { useLerrynTheme } from './theme/useLerrynTheme';
export * as tokens from './theme/tokens';
export type { Flute, LerrynColor, LerrynTheme, LerrynTypeStyle } from './theme/tokens';
