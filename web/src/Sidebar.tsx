import {
  Activity,
  Database,
  Download,
  Info,
  PanelLeftClose,
  PanelLeftOpen,
  Server,
  Settings,
  Upload,
} from "lucide-react";
import { useEffect, useState } from "react";
import dialgaUrl from "./assets/dialga.svg";
import type { EngineInfo } from "./engine";
import { type MessageKey, useI18n } from "./i18n";
import { TunnelSummary } from "./TunnelIndicator";

export type Page =
  | "instances"
  | "databases"
  | "import"
  | "export"
  | "activity"
  | "settings"
  | "about";

/** The window title of each page: "Databases · Dialga". */
const PAGE_TITLE: Record<Page, MessageKey> = {
  instances: "nav.instances",
  databases: "nav.databases",
  import: "nav.import",
  export: "nav.export",
  activity: "nav.activity",
  settings: "nav.settings",
  about: "nav.about",
};

/** The viewer's choice, remembered in this browser. Storage can be missing or throw. */
export const SIDEBAR_KEY = "dialga.sidebar.collapsed";

function readCollapsed(): boolean {
  try {
    return localStorage.getItem(SIDEBAR_KEY) === "1";
  } catch {
    return false;
  }
}

function writeCollapsed(on: boolean) {
  try {
    localStorage.setItem(SIDEBAR_KEY, on ? "1" : "0");
  } catch {
    // The choice only lasts until the window closes.
  }
}

export function Sidebar({
  page,
  engine,
  engineError,
  runningJobs = 0,
}: {
  page: Page;
  engine: EngineInfo | null;
  engineError: string;
  /** Jobs queued or running: a badge on Activity. */
  runningJobs?: number;
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
  const [collapsed, setCollapsed] = useState(readCollapsed);
  const toggle = () => {
    setCollapsed(!collapsed);
    writeCollapsed(!collapsed);
  };
  // In the rail the icon is all there is to see: the name goes in the tooltip.
  const tip = (key: MessageKey) => (collapsed ? t(key) : undefined);
  const current = (p: Page) => (page === p ? "page" : undefined);

  useEffect(() => {
    document.title = `${t(PAGE_TITLE[page])} · Dialga`;
  }, [page, t]);

  return (
    <aside className={collapsed ? "sidebar sidebar-collapsed" : "sidebar"}>
      {/* Keyboard users skip the navigation; a button, as a #fragment link would change the page. */}
      <button
        type="button"
        className="skip-link"
        onClick={() => document.getElementById("main")?.focus()}
      >
        {t("app.skip")}
      </button>
      <div className="brand">
        <img src={dialgaUrl} alt="" className="logo" />
        <div className="brand-text">
          <div className="product">Dialga</div>
          <div className="tagline">{t("app.tagline")}</div>
        </div>
      </div>

      <button
        type="button"
        className="sidebar-toggle"
        aria-expanded={!collapsed}
        aria-controls="sidebar-nav"
        aria-label={t(collapsed ? "nav.expand" : "nav.collapse")}
        title={t(collapsed ? "nav.expand" : "nav.collapse")}
        onClick={toggle}
      >
        {collapsed ? (
          <PanelLeftOpen size={16} aria-hidden="true" />
        ) : (
          <PanelLeftClose size={16} aria-hidden="true" />
        )}
      </button>

      <nav id="sidebar-nav" aria-label={t("nav.label")} className="nav">
        <span className="nav-group">{t("nav.operations")}</span>
        <a
          href="#instances"
          className="nav-link"
          aria-current={current("instances")}
          title={tip("nav.instances")}
        >
          <Server size={16} className="nav-icon" aria-hidden="true" />
          <span className="nav-label">{t("nav.instances")}</span>
        </a>
        <a
          href="#databases"
          className="nav-link"
          aria-current={current("databases")}
          title={tip("nav.databases")}
        >
          <Database size={16} className="nav-icon" aria-hidden="true" />
          <span className="nav-label">{t("nav.databases")}</span>
        </a>
        <a
          href="#import"
          className="nav-link"
          aria-current={current("import")}
          title={tip("nav.import")}
        >
          <Upload size={16} className="nav-icon" aria-hidden="true" />
          <span className="nav-label">{t("nav.import")}</span>
        </a>
        <a
          href="#export"
          className="nav-link"
          aria-current={current("export")}
          title={tip("nav.export")}
        >
          <Download size={16} className="nav-icon" aria-hidden="true" />
          <span className="nav-label">{t("nav.export")}</span>
        </a>
        <a
          href="#activity"
          className="nav-link"
          aria-current={current("activity")}
          title={tip("nav.activity")}
        >
          <Activity size={16} className="nav-icon" aria-hidden="true" />
          <span className="nav-label">{t("nav.activity")}</span>
          {runningJobs > 0 && (
            <>
              <span className="nav-count" aria-hidden="true">
                {runningJobs}
              </span>
              <span className="sr-only">{t("nav.activityRunning", { count: runningJobs })}</span>
            </>
          )}
        </a>
        <a
          href="#settings"
          className="nav-link"
          aria-current={current("settings")}
          title={tip("nav.settings")}
        >
          <Settings size={16} className="nav-icon" aria-hidden="true" />
          <span className="nav-label">{t("nav.settings")}</span>
        </a>
        <a
          href="#about"
          className="nav-link"
          aria-current={current("about")}
          title={tip("nav.about")}
        >
          <Info size={16} className="nav-icon" aria-hidden="true" />
          <span className="nav-label">{t("nav.about")}</span>
        </a>
      </nav>

      <div className="engine" title={engineError || (collapsed ? status : undefined)}>
        <span className={dot} />
        {status}
      </div>
      <TunnelSummary engine={engine} />
    </aside>
  );
}
