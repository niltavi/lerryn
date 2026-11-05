import { useLerrynTheme } from '@lerryn/ui-native';
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';

import { defaultPrefs, loadPrefs, savePrefs, type Prefs } from '../storage';

interface PrefsValue {
  prefs: Prefs;
  loaded: boolean;
  update: (patch: Partial<Prefs>) => void;
}

const PrefsContext = createContext<PrefsValue | null>(null);

/** Theme, language and project filter chosen on this phone. */
export function PrefsProvider({ children }: { children: ReactNode }) {
  const [prefs, setPrefs] = useState<Prefs>(defaultPrefs);
  const [loaded, setLoaded] = useState(false);
  const { setTheme } = useLerrynTheme();

  useEffect(() => {
    loadPrefs().then((p) => {
      setPrefs(p);
      setLoaded(true);
    });
  }, []);

  useEffect(() => {
    setTheme(prefs.theme);
    // setTheme is a new function each render; the theme is all that matters.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [prefs.theme]);

  const update = useCallback((patch: Partial<Prefs>) => {
    setPrefs((current) => {
      const next = { ...current, ...patch };
      void savePrefs(next);
      return next;
    });
  }, []);

  const value = useMemo(() => ({ prefs, loaded, update }), [prefs, loaded, update]);
  return <PrefsContext.Provider value={value}>{children}</PrefsContext.Provider>;
}

export function usePrefs(): PrefsValue {
  const value = useContext(PrefsContext);
  if (!value) throw new Error('usePrefs must be used inside PrefsProvider');
  return value;
}
