import { useEffect, useState } from "react";
import { errorText } from "./api";
import { type EngineInfo, getEngineInfo } from "./engine";
import { InstancesPage } from "./InstancesPage";
import { I18nProvider } from "./i18n";
import { Sidebar } from "./Sidebar";

export function App() {
  const [engine, setEngine] = useState<EngineInfo | null>(null);
  const [engineError, setEngineError] = useState("");

  useEffect(() => {
    getEngineInfo().then(setEngine, (e: unknown) => setEngineError(errorText(e)));
  }, []);

  return (
    <I18nProvider>
      <div className="layout">
        <Sidebar engine={engine} engineError={engineError} />
        <main className="main">
          <InstancesPage engine={engine} engineError={engineError} />
        </main>
      </div>
    </I18nProvider>
  );
}
