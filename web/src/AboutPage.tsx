import dialgaUrl from "./assets/dialga.svg";
import type { EngineInfo } from "./engine";
import { useI18n } from "./i18n";

// TODO: the real GitHub repository
const REPO_URL = "https://github.com/TODO/dialga";

/** The product, its versions, its author and where to find it. */
export function AboutPage({ engine }: { engine: EngineInfo | null }) {
  const { t } = useI18n();
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{t("about.title")}</h1>
          <p className="lead">{t("about.lead")}</p>
        </div>
      </header>

      <section className="card settings" aria-labelledby="about-product">
        <div className="brand">
          {/* An easter egg will go here later (e.g. on the logo). */}
          <img src={dialgaUrl} alt="" className="logo" />
          <div>
            <h2 id="about-product" className="product">
              Dialga
            </h2>
            <div className="tagline">{t("app.tagline")}</div>
          </div>
        </div>
        <dl className="facts">
          <dt>{t("about.engine")}</dt>
          <dd className="mono">{engine?.version ?? "—"}</dd>
          <dt>{t("about.api")}</dt>
          <dd className="mono">{engine ? `v1 · ${engine.url}` : "—"}</dd>
          <dt>{t("about.author")}</dt>
          <dd>Fabio Pellizzaro</dd>
          <dt>{t("about.repository")}</dt>
          <dd>
            <a href={REPO_URL} target="_blank" rel="noreferrer">
              {REPO_URL}
            </a>
          </dd>
        </dl>
      </section>
    </div>
  );
}
