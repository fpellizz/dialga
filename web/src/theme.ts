/** Light/dark theme: the Design System's dark tokens hang off data-theme="dark". */

export type ThemePref = "system" | "light" | "dark";

export const THEME_PREFS: ThemePref[] = ["system", "light", "dark"];

const STORAGE_KEY = "dialga.theme";

export function loadThemePref(): ThemePref {
  try {
    const value = localStorage.getItem(STORAGE_KEY);
    return value === "light" || value === "dark" ? value : "system";
  } catch {
    return "system";
  }
}

export function saveThemePref(pref: ThemePref): void {
  try {
    localStorage.setItem(STORAGE_KEY, pref);
  } catch {
    // Private mode or blocked storage: the choice lasts for this session.
  }
}

/** The theme to draw for a preference, given whether the system is dark. */
export function resolveTheme(pref: ThemePref, systemDark: boolean): "light" | "dark" {
  if (pref === "system") {
    return systemDark ? "dark" : "light";
  }
  return pref;
}

/**
 * Applies a preference to the document and, for "system", follows the
 * system theme until the returned function is called.
 */
export function applyTheme(pref: ThemePref): () => void {
  const query = window.matchMedia("(prefers-color-scheme: dark)");
  const apply = () => {
    document.documentElement.dataset.theme = resolveTheme(pref, query.matches);
  };
  apply();
  if (pref !== "system") {
    return () => {};
  }
  query.addEventListener("change", apply);
  return () => query.removeEventListener("change", apply);
}
