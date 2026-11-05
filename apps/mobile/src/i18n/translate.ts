import { matchLocale, type Locale, type MessageVars } from '@lerryn/core';

import { en, zhCN, type MobileMessages } from './messages';

const catalogs: Record<Locale, MobileMessages> = { 'zh-CN': zhCN, en };

type Paths<T, Prefix extends string = ''> = {
  [K in keyof T & string]: T[K] extends string ? `${Prefix}${K}` : Paths<T[K], `${Prefix}${K}.`>;
}[keyof T & string];

export type MobileKey = Paths<MobileMessages>;

/** Looks up a mobile-only message and fills `{name}` placeholders. */
export function translateMobile(locale: Locale, key: MobileKey, vars?: MessageVars): string {
  let node: unknown = catalogs[locale];
  for (const part of key.split('.')) node = (node as Record<string, unknown> | undefined)?.[part];
  const template = typeof node === 'string' ? node : key;
  if (!vars) return template;
  return template.replace(/\{(\w+)\}/g, (match, name: string) => (name in vars ? String(vars[name]) : match));
}

export type LocalePreference = Locale | 'system';

/**
 * Chooses the display language: the choice made in the app, then the
 * account's setting, then the phone's preferred languages in order.
 */
export function resolveLocale(
  preference: LocalePreference,
  account: string | null | undefined,
  system: readonly (string | null | undefined)[],
): Locale {
  if (preference !== 'system') return preference;
  if (account === 'zh-CN' || account === 'en') return account;
  for (const tag of system) {
    if (!tag) continue;
    const lower = tag.toLowerCase();
    if (lower.startsWith('zh')) return 'zh-CN';
    if (lower.startsWith('en')) return 'en';
  }
  return matchLocale(system[0] ?? null);
}
