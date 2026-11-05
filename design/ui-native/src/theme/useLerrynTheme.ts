import { Uniwind, useUniwind } from 'uniwind';
import { colors, type LerrynTheme } from './tokens';

/**
 * 当前主题与对应的颜色值。
 * 只在拿不到 className 的地方用（SVG 描边、图标 color、动画插值），其余一律写 className。
 */
export function useLerrynTheme() {
  const { theme } = useUniwind();
  const name: LerrynTheme = theme === 'dark' ? 'dark' : 'light';
  return {
    theme: name,
    colors: colors[name],
    setTheme: (next: LerrynTheme | 'system') => Uniwind.setTheme(next),
  };
}
