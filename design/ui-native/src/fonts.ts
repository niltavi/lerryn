// 按字重子路径导入，包根的 index 会把所有字重都打进 bundle
import { IBMPlexMono_400Regular } from '@expo-google-fonts/ibm-plex-mono/400Regular';
import { IBMPlexMono_500Medium } from '@expo-google-fonts/ibm-plex-mono/500Medium';
import { DMSans_600SemiBold } from '@expo-google-fonts/dm-sans/600SemiBold';
import { DMSans_700Bold } from '@expo-google-fonts/dm-sans/700Bold';
import { DMSans_800ExtraBold } from '@expo-google-fonts/dm-sans/800ExtraBold';
import { useFonts } from 'expo-font';

/** Lerryn 用到的全部字重。键名就是 RN 里的 fontFamily，与 theme.css 的 --font-* 对应。 */
export const lerrynFonts = {
  DMSans_600SemiBold,
  DMSans_700Bold,
  DMSans_800ExtraBold,
  IBMPlexMono_400Regular,
  IBMPlexMono_500Medium,
};

/** 在根组件里调用，返回 [loaded, error]。正文用系统字体，不受加载影响。 */
export function useLerrynFonts() {
  return useFonts(lerrynFonts);
}
