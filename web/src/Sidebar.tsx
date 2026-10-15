import { Activity, Download, Server, Upload } from "lucide-react";
import symbolUrl from "./assets/decisyon-symbol.svg";
import type { EngineInfo } from "./engine";
import { type Lang, type MessageKey, useI18n } from "./i18n";

const SOON: { icon: typeof Upload; key: MessageKey }[] = [
  { icon: Upload, key: "nav.import" },
  { icon: Download, key: "nav.export" },
  { icon: Activity, key: "nav.activity" },
];

const LANGS: Lang[] = ["en", "it"];

export function Sidebar({
  engine,
  engineError,
}: {
  engine: EngineInfo | null;
  engineError: string;
}) {
  const { t, lang, setLang } = useI18n();
  let dot = "dot dot-wait";
  let status = t("engine.starting");
  if (engine) {
    dot = "dot";
    status = t("engine.version", { version: engine.version });
  } else if (engineError) {
    dot = "dot dot-off";
    status = t("engine.unavailable");
  }

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
        <a href="#instances" className="nav-link" aria-current="page">
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
        <fieldset className="lang">
          <legend>{t("lang.label")}</legend>
          {LANGS.map((l) => (
            <button
              key={l}
              type="button"
              className="lang-btn"
              aria-pressed={lang === l}
              onClick={() => setLang(l)}
            >
              {l.toUpperCase()}
            </button>
          ))}
        </fieldset>
        <div className="engine" title={engineError || undefined}>
          <span className={dot} />
          {status}
        </div>
      </div>
    </aside>
  );
}
