import { HeroUINativeProvider } from 'heroui-native';
import type { ComponentProps, ReactNode } from 'react';
import { View } from 'react-native';
import { GestureHandlerRootView } from 'react-native-gesture-handler';

type HeroConfig = ComponentProps<typeof HeroUINativeProvider>['config'];

export type LerrynProviderProps = {
  children: ReactNode;
  /** 透传给 HeroUINativeProvider 的配置 */
  config?: HeroConfig;
};

/**
 * 应用根部包一层即可：手势根节点 + HeroUI Native 的主题与 Toast，并铺上 paper 底色。
 * 第三方容器（如 react-native-safe-area-context 的 SafeAreaView）不认 className，
 * 底色在这里铺好，页面就不必依赖它们。
 */
export function LerrynProvider({ children, config }: LerrynProviderProps) {
  return (
    <GestureHandlerRootView style={{ flex: 1 }}>
      <HeroUINativeProvider config={{ devInfo: { stylingPrinciples: false }, ...config }}>
        <View className="flex-1 bg-paper">{children}</View>
      </HeroUINativeProvider>
    </GestureHandlerRootView>
  );
}
