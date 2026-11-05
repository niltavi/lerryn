import * as SecureStore from 'expo-secure-store';

import type { LocalePreference } from './i18n/translate';
import { decodeSession, encodeSession, type StoredSession } from './lib/session';

// Everything the app keeps lives in the platform keychain/keystore. The
// session cookie is the only secret, but keeping preferences there too
// avoids a second storage module. Items are bound to this device and are
// not readable before the first unlock, so they are left out of backups
// and iCloud Keychain sync.
const OPTIONS: SecureStore.SecureStoreOptions = {
  keychainAccessible: SecureStore.AFTER_FIRST_UNLOCK_THIS_DEVICE_ONLY,
};

const SESSION_KEY = 'lerryn.session';
const LAST_SERVER_KEY = 'lerryn.lastServer';
const PREFS_KEY = 'lerryn.prefs';

export async function loadSession(): Promise<StoredSession | null> {
  try {
    return decodeSession(await SecureStore.getItemAsync(SESSION_KEY, OPTIONS));
  } catch {
    // A keychain that cannot be read (e.g. restored onto another device)
    // means signing in again.
    return null;
  }
}

export async function saveSession(session: StoredSession): Promise<void> {
  await SecureStore.setItemAsync(SESSION_KEY, encodeSession(session), OPTIONS);
  await SecureStore.setItemAsync(LAST_SERVER_KEY, session.server, OPTIONS);
}

export async function clearSession(): Promise<void> {
  await SecureStore.deleteItemAsync(SESSION_KEY, OPTIONS);
}

/** The server signed in to last time, to prefill the sign-in form. */
export async function loadLastServer(): Promise<string | null> {
  try {
    return await SecureStore.getItemAsync(LAST_SERVER_KEY, OPTIONS);
  } catch {
    return null;
  }
}

export type ThemePreference = 'system' | 'light' | 'dark';

export interface Prefs {
  theme: ThemePreference;
  locale: LocalePreference;
  /** Project filter for the overview; null shows all projects. */
  projectId: string | null;
}

export const defaultPrefs: Prefs = { theme: 'system', locale: 'system', projectId: null };

export async function loadPrefs(): Promise<Prefs> {
  try {
    const raw = await SecureStore.getItemAsync(PREFS_KEY, OPTIONS);
    if (!raw) return defaultPrefs;
    const v = JSON.parse(raw) as Partial<Prefs>;
    return {
      theme: v.theme === 'light' || v.theme === 'dark' ? v.theme : 'system',
      locale: v.locale === 'zh-CN' || v.locale === 'en' ? v.locale : 'system',
      projectId: typeof v.projectId === 'string' ? v.projectId : null,
    };
  } catch {
    return defaultPrefs;
  }
}

export async function savePrefs(prefs: Prefs): Promise<void> {
  try {
    await SecureStore.setItemAsync(PREFS_KEY, JSON.stringify(prefs), OPTIONS);
  } catch {
    // The preference still applies for this launch.
  }
}
