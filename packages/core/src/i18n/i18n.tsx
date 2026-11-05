import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from "react";

import { en } from "./en";
import { zhCN, type Messages } from "./zh-CN";

export const LOCALES = ["zh-CN", "en"] as const;
export type Locale = (typeof LOCALES)[number];

const catalogs: Record<Locale, Messages> = { "zh-CN": zhCN, en };

/** Dotted paths to every string leaf, e.g. "run.status.queued". */
type Paths<T, Prefix extends string = ""> = {
  [K in keyof T & string]: T[K] extends string ? `${Prefix}${K}` : Paths<T[K], `${Prefix}${K}.`>;
}[keyof T & string];

export type MessageKey = Paths<Messages>;
export type MessageVars = Record<string, string | number>;
export type Translate = (key: MessageKey, vars?: MessageVars) => string;

/** Looks up key in the locale's catalog and fills `{name}` placeholders. */
export function translate(locale: Locale, key: MessageKey, vars?: MessageVars): string {
  let node: unknown = catalogs[locale];
  for (const part of key.split(".")) {
    node = (node as Record<string, unknown> | undefined)?.[part];
  }
  // Missing keys cannot happen with typed keys; fall back to the key so a
  // bad cast shows up in the UI instead of rendering nothing.
  const template = typeof node === "string" ? node : key;
  if (!vars) return template;
  return template.replace(/\{(\w+)\}/g, (match, name: string) =>
    name in vars ? String(vars[name]) : match,
  );
}

/** Picks a supported locale from a BCP 47 tag such as navigator.language. */
export function matchLocale(tag: string | undefined | null): Locale {
  if (!tag) return "zh-CN";
  return tag.toLowerCase().startsWith("zh") ? "zh-CN" : "en";
}

const STORAGE_KEY = "lerryn.locale";

/** Reads the saved locale, then the browser language. Defaults to zh-CN. */
export function initialLocale(): Locale {
  try {
    const saved = globalThis.localStorage?.getItem(STORAGE_KEY);
    if (saved && (LOCALES as readonly string[]).includes(saved)) return saved as Locale;
  } catch {
    // Storage can be blocked; the browser language still applies.
  }
  return matchLocale(globalThis.navigator?.language);
}

interface I18nValue {
  locale: Locale;
  setLocale: (locale: Locale) => void;
  t: Translate;
}

const I18nContext = createContext<I18nValue | null>(null);

export interface I18nProviderProps {
  children: ReactNode;
  /** Fixed locale, for tests. Omit to detect and remember the user's choice. */
  locale?: Locale;
}

export function I18nProvider({ children, locale: fixed }: I18nProviderProps) {
  const [locale, setLocaleState] = useState<Locale>(() => fixed ?? initialLocale());
  const setLocale = useCallback((next: Locale) => {
    setLocaleState(next);
    try {
      globalThis.localStorage?.setItem(STORAGE_KEY, next);
    } catch {
      // Not remembered across visits; the choice still applies now.
    }
    if (typeof document !== "undefined") document.documentElement.lang = next;
  }, []);
  const value = useMemo<I18nValue>(
    () => ({ locale, setLocale, t: (key, vars) => translate(locale, key, vars) }),
    [locale, setLocale],
  );
  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

/** The current locale and translate function. Outside a provider, zh-CN. */
export function useI18n(): I18nValue {
  const value = useContext(I18nContext);
  if (value) return value;
  return {
    locale: "zh-CN",
    setLocale: () => {},
    t: (key, vars) => translate("zh-CN", key, vars),
  };
}
