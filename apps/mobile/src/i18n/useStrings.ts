import { useI18n, type Locale, type MessageVars, type Translate } from '@lerryn/core';
import { useCallback } from 'react';

import { translateMobile, type MobileKey } from './translate';

export interface Strings {
  locale: Locale;
  /** Shared copy from @lerryn/core. */
  t: Translate;
  /** Mobile-only copy. */
  m: (key: MobileKey, vars?: MessageVars) => string;
}

export function useStrings(): Strings {
  const { locale, t } = useI18n();
  const m = useCallback((key: MobileKey, vars?: MessageVars) => translateMobile(locale, key, vars), [locale]);
  return { locale, t, m };
}
