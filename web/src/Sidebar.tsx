import { Activity, Database, Download, Server, Settings, Upload } from "lucide-react";
import { useEffect } from "react";
import symbolUrl from "./assets/decisyon-symbol.svg";
import type { EngineInfo } from "./engine";
import { type MessageKey, useI18n } from "./i18n";

export type Page = "instances" | "databases" | "settings";

/** The window title of each page: "Databases · Dialga". */
const PAGE_TITLE: Record<Page, MessageKey> = {
  instances: "nav.instances",
  databases: "nav.databases",
  settings: "nav.settings",
};

const SOON: { icon: typeof Upload; key: MessageKey }[] = [
  { icon: Upload, key: "nav.import" },
  { icon: Download, key: "nav.export" },
  { icon: Activity, key: "nav.activity" },
];

export function Sidebar({
  page,
  engine,
  engineError,
}: {
  page: Page;
  engine: EngineInfo | null;
  engineError: string;
}) {
  const { t } = useI18n();
  let dot = "dot dot-wait";
  let status = t("engine.starting");
  if (engine) {
    dot = "dot";
    status = t("engine.version", { version: engine.version });
  } else if (engineError) {
    dot = "dot dot-off";
    status = t("engine.unavailable");
  }
  const current = (p: Page) => (page === p ? "page" : undefined);

  useEffect(() => {
    document.title = `${t(PAGE_TITLE[page])} · Dialga`;
  }, [page, t]);

  return (
    <aside className="sidebar">
      {/* Keyboard users skip the navigation; a button, as a #fragment link would change the page. */}
      <button
        type="button"
        className="skip-link"
        onClick={() => document.getElementById("main")?.focus()}
      >
        {t("app.skip")}
      </button>
      <div className="brand">
        <img src={symbolUrl} alt="Decisyon" className="logo" />
        <div>
          <div className="product">Dialga</div>
          <div className="tagline">{t("app.tagline")}</div>
        </div>
      </div>

      <nav aria-label={t("nav.label")} className="nav">
        <span className="nav-group">{t("nav.operations")}</span>
        <a href="#instances" className="nav-link" aria-current={current("instances")}>
          <Server size={16} className="nav-icon" aria-hidden="true" />
          {t("nav.instances")}
        </a>
        <a href="#databases" className="nav-link" aria-current={current("databases")}>
          <Database size={16} className="nav-icon" aria-hidden="true" />
          {t("nav.databases")}
        </a>
        {SOON.map(({ icon: Icon, key }) => (
          <span key={key} className="nav-link nav-disabled" aria-disabled="true">
            <Icon size={16} aria-hidden="true" />
            {t(key)}
            <span className="soon">{t("nav.soon")}</span>
          </span>
        ))}
        <a href="#settings" className="nav-link nav-bottom" aria-current={current("settings")}>
          <Settings size={16} className="nav-icon" aria-hidden="true" />
          {t("nav.settings")}
        </a>
      </nav>

      <div className="engine" title={engineError || undefined}>
        <span className={dot} />
        {status}
      </div>
    </aside>
  );
}
