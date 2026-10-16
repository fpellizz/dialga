import { Activity, Database, Download, Server, Settings, Upload } from "lucide-react";
import { useEffect } from "react";
import symbolUrl from "./assets/decisyon-symbol.svg";
import type { EngineInfo } from "./engine";
import { type MessageKey, useI18n } from "./i18n";
import { TunnelSummary } from "./TunnelIndicator";

export type Page = "instances" | "databases" | "export" | "settings";

/** The window title of each page: "Databases · Dialga". */
const PAGE_TITLE: Record<Page, MessageKey> = {
  instances: "nav.instances",
  databases: "nav.databases",
  export: "nav.export",
  settings: "nav.settings",
};

/** The entries not built yet, around Export (the order of the plan). */
const SOON_BEFORE: { icon: typeof Upload; key: MessageKey }[] = [
  { icon: Upload, key: "nav.import" },
];
const SOON_AFTER: { icon: typeof Upload; key: MessageKey }[] = [
  { icon: Activity, key: "nav.activity" },
];

function Soon({ icon: Icon, label, soon }: { icon: typeof Upload; label: string; soon: string }) {
  return (
    <span className="nav-link nav-disabled" aria-disabled="true">
      <Icon size={16} aria-hidden="true" />
      {label}
      <span className="soon">{soon}</span>
    </span>
  );
}

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
        {SOON_BEFORE.map(({ icon, key }) => (
          <Soon key={key} icon={icon} label={t(key)} soon={t("nav.soon")} />
        ))}
        <a href="#export" className="nav-link" aria-current={current("export")}>
          <Download size={16} className="nav-icon" aria-hidden="true" />
          {t("nav.export")}
        </a>
        {SOON_AFTER.map(({ icon, key }) => (
          <Soon key={key} icon={icon} label={t(key)} soon={t("nav.soon")} />
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
      <TunnelSummary engine={engine} />
    </aside>
  );
}
