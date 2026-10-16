import { Activity, Download, Server, Settings, Upload } from "lucide-react";
import symbolUrl from "./assets/decisyon-symbol.svg";
import type { EngineInfo } from "./engine";
import { type MessageKey, useI18n } from "./i18n";

export type Page = "instances" | "settings";

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

  return (
    <aside className="sidebar">
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
        {SOON.map(({ icon: Icon, key }) => (
          <span key={key} className="nav-link nav-disabled" aria-disabled="true">
            <Icon size={16} aria-hidden="true" />
            {t(key)}
            <span className="soon">{t("nav.soon")}</span>
          </span>
        ))}
      </nav>

      <div className="sidebar-footer">
        <a href="#settings" className="nav-link" aria-current={current("settings")}>
          <Settings size={16} className="nav-icon" aria-hidden="true" />
          {t("nav.settings")}
        </a>
        <div className="engine" title={engineError || undefined}>
          <span className={dot} />
          {status}
        </div>
      </div>
    </aside>
  );
}
