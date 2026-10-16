import { useEffect, useLayoutEffect, useState } from "react";
import { ActivityPage } from "./ActivityPage";
import { errorText } from "./api";
import { DatabasesPage } from "./DatabasesPage";
import { type EngineInfo, getEngineInfo } from "./engine";
import { InstancesPage } from "./InstancesPage";
import { I18nProvider } from "./i18n";
import { isJobActive } from "./jobs";
import { SettingsPage } from "./SettingsPage";
import { type Page, Sidebar } from "./Sidebar";
import { applyTheme, loadThemePref, saveThemePref, type ThemePref } from "./theme";
import { useJobList } from "./useJobs";

function pageFromHash(): Page {
  const hash = window.location.hash;
  if (hash === "#settings") {
    return "settings";
  }
  // #activity/<job id> opens the detail of a job.
  if (hash === "#activity" || hash.startsWith("#activity/")) {
    return "activity";
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
  // One poll of the jobs for the sidebar badge and the Activity page.
  const jobs = useJobList(engine, page === "activity");
  const runningJobs = jobs.jobs?.filter((j) => isJobActive(j.state)).length ?? 0;

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
        <Sidebar page={page} engine={engine} engineError={engineError} runningJobs={runningJobs} />
        <main className="main">
          {page === "settings" && (
            <SettingsPage engine={engine} theme={theme} onThemeChange={changeTheme} />
          )}
          {page === "databases" && <DatabasesPage engine={engine} engineError={engineError} />}
          {page === "activity" && (
            <ActivityPage engine={engine} engineError={engineError} jobs={jobs} />
          )}
          {page === "instances" && <InstancesPage engine={engine} engineError={engineError} />}
        </main>
      </div>
    </I18nProvider>
  );
}
