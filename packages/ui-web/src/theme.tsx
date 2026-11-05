import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

export type ThemePreference = "system" | "light" | "dark";
export type ResolvedTheme = "light" | "dark";

const STORAGE_KEY = "lerryn.theme";
const QUERY = "(prefers-color-scheme: dark)";

function readPreference(): ThemePreference {
  try {
    const saved = globalThis.localStorage?.getItem(STORAGE_KEY);
    if (saved === "light" || saved === "dark" || saved === "system") return saved;
  } catch {
    // Blocked storage: follow the system.
  }
  return "system";
}

function systemTheme(): ResolvedTheme {
  return typeof window !== "undefined" && window.matchMedia?.(QUERY).matches ? "dark" : "light";
}

/**
 * Sets the theme on <html>. tokens.css switches on data-theme, HeroUI on the
 * .dark / .light class and data-theme; color-scheme follows from both.
 */
export function applyTheme(theme: ResolvedTheme) {
  const root = document.documentElement;
  root.dataset.theme = theme;
  root.classList.toggle("dark", theme === "dark");
  root.classList.toggle("light", theme === "light");
}

interface ThemeValue {
  preference: ThemePreference;
  theme: ResolvedTheme;
  setPreference: (p: ThemePreference) => void;
}

const ThemeContext = createContext<ThemeValue | null>(null);

/** Light, dark or follow the system; the choice is remembered in this browser. */
export function LerrynThemeProvider({ children }: { children: ReactNode }) {
  const [preference, setPref] = useState<ThemePreference>(readPreference);
  const [system, setSystem] = useState<ResolvedTheme>(systemTheme);

  useEffect(() => {
    const mql = window.matchMedia?.(QUERY);
    if (!mql) return;
    const onChange = () => setSystem(mql.matches ? "dark" : "light");
    mql.addEventListener("change", onChange);
    return () => mql.removeEventListener("change", onChange);
  }, []);

  const theme = preference === "system" ? system : preference;
  useEffect(() => applyTheme(theme), [theme]);

  const setPreference = useCallback((p: ThemePreference) => {
    setPref(p);
    try {
      globalThis.localStorage?.setItem(STORAGE_KEY, p);
    } catch {
      // Applies now, not remembered.
    }
  }, []);

  const value = useMemo(() => ({ preference, theme, setPreference }), [preference, theme, setPreference]);
  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useLerrynTheme(): ThemeValue {
  const value = useContext(ThemeContext);
  if (!value) throw new Error("useLerrynTheme must be used inside LerrynThemeProvider");
  return value;
}
