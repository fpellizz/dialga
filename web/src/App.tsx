import { useEffect, useLayoutEffect, useState } from "react";
import { errorText } from "./api";
import { type EngineInfo, getEngineInfo } from "./engine";
import { InstancesPage } from "./InstancesPage";
import { I18nProvider } from "./i18n";
import { SettingsPage } from "./SettingsPage";
import { type Page, Sidebar } from "./Sidebar";
import { applyTheme, loadThemePref, saveThemePref, type ThemePref } from "./theme";

function pageFromHash(): Page {
  return window.location.hash === "#settings" ? "settings" : "instances";
}

export function App() {
  const [engine, setEngine] = useState<EngineInfo | null>(null);
  const [engineError, setEngineError] = useState("");
  const [page, setPage] = useState<Page>(pageFromHash);
  const [theme, setTheme] = useState<ThemePref>(loadThemePref);

  useEffect(() => {
    getEngineInfo().then(setEngine, (e: unknown) => setEngineError(errorText(e)));
  }, []);

  useEffect(() => {
    const onHash = () => setPage(pageFromHash());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  // Before the first paint, so a dark system never flashes the light theme.
  useLayoutEffect(() => applyTheme(theme), [theme]);

  const changeTheme = (pref: ThemePref) => {
    saveThemePref(pref);
    setTheme(pref);
  };

  return (
    <I18nProvider>
      <div className="layout">
        <Sidebar page={page} engine={engine} engineError={engineError} />
        <main className="main">
          {page === "settings" ? (
            <SettingsPage engine={engine} theme={theme} onThemeChange={changeTheme} />
          ) : (
            <InstancesPage engine={engine} engineError={engineError} />
          )}
        </main>
      </div>
    </I18nProvider>
  );
}
