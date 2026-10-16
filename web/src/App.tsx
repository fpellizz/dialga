import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { errorText } from "./api";
import { DatabasesPage } from "./DatabasesPage";
import { type EngineInfo, getEngineInfo } from "./engine";
import { InstancesPage } from "./InstancesPage";
import { I18nProvider } from "./i18n";
import { SettingsPage } from "./SettingsPage";
import { type Page, Sidebar } from "./Sidebar";
import { applyTheme, loadThemePref, saveThemePref, type ThemePref } from "./theme";

function pageFromHash(): Page {
  const hash = window.location.hash;
  if (hash === "#settings") {
    return "settings";
  }
  // #databases/<target> opens the screen on that target.
  if (hash === "#databases" || hash.startsWith("#databases/")) {
    return "databases";
  }
  return "instances";
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

  // A new page moves the focus to its title, as a page load would; not on start.
  const shown = useRef(page);
  useEffect(() => {
    if (shown.current === page) {
      return;
    }
    shown.current = page;
    const title = document.querySelector<HTMLElement>("#main h1");
    if (title) {
      title.tabIndex = -1;
      title.focus();
    }
  }, [page]);

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
        <main id="main" className="main" tabIndex={-1}>
          {page === "settings" && (
            <SettingsPage engine={engine} theme={theme} onThemeChange={changeTheme} />
          )}
          {page === "databases" && <DatabasesPage engine={engine} engineError={engineError} />}
          {page === "instances" && <InstancesPage engine={engine} engineError={engineError} />}
        </main>
      </div>
    </I18nProvider>
  );
}
