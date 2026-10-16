import { CircleCheck, RefreshCw, TriangleAlert } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { getPrerequisites, type InstallHint, type Prerequisite } from "./api";
import type { EngineInfo } from "./engine";
import { ErrorBanner } from "./Feedback";
import { useI18n } from "./i18n";

/** Distro names: the same in every UI language. */
const DISTROS: Record<InstallHint["distro"], string> = {
  fedora: "Fedora",
  debian: "Debian/Ubuntu",
};

/** The programs Dialga runs: found with their version, or how to install them. */
export function PrerequisitesCard({ engine }: { engine: EngineInfo | null }) {
  const { t } = useI18n();
  const [items, setItems] = useState<Prerequisite[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    if (!engine) {
      return;
    }
    setLoading(true);
    try {
      setItems(await getPrerequisites(engine));
      setError(null);
    } catch (e) {
      setError(e);
    } finally {
      setLoading(false);
    }
  }, [engine]);

  useEffect(() => {
    load();
  }, [load]);

  return (
    <section
      className="card settings"
      aria-labelledby="settings-prereqs"
      aria-busy={loading || (items === null && error === null)}
    >
      <div className="card-head">
        <div>
          <h2 id="settings-prereqs">{t("settings.prereqs")}</h2>
          <p className="hint">{t("settings.prereqsLead")}</p>
        </div>
        <button
          type="button"
          className="btn btn-outline btn-sm"
          onClick={() => !loading && load()}
          disabled={!engine}
          aria-disabled={loading || undefined}
        >
          <RefreshCw size={14} className={loading ? "spin" : undefined} aria-hidden="true" />
          {t("settings.recheck")}
        </button>
      </div>
      {error !== null && <ErrorBanner error={error} />}
      {items === null && error === null && (
        <div aria-hidden="true">
          <div className="skeleton skeleton-row" />
          <div className="skeleton skeleton-row" />
        </div>
      )}
      {items && (
        <ul className="item-list">
          {items.map((item) => {
            const ok = item.found && !item.outdated;
            let badge = t("prereq.missing");
            if (ok) {
              badge = t("prereq.found");
            } else if (item.found) {
              badge = t("prereq.outdated");
            }
            return (
              <li key={item.id} className="item">
                <div className="item-row">
                  {ok ? (
                    <CircleCheck size={18} className="tone-success" aria-hidden="true" />
                  ) : (
                    <TriangleAlert size={18} className="tone-warning" aria-hidden="true" />
                  )}
                  <span className="item-title">{t(`prereq.${item.id}`)}</span>
                  <span className={`badge ${ok ? "badge-ok" : "badge-warning"}`}>{badge}</span>
                </div>
                {item.found && (
                  <div className="mono muted item-detail">
                    {[item.program, item.version, item.path && `(${item.path})`]
                      .filter(Boolean)
                      .join(" ")}
                  </div>
                )}
                {!ok && (
                  <>
                    <p className="hint">
                      {item.found
                        ? t("prereq.outdatedHint", { version: item.version ?? "" })
                        : t(`prereq.purpose.${item.id}`)}
                    </p>
                    {item.install.map((hint) => (
                      <div key={hint.distro} className="install">
                        <span className="hint">
                          {t("prereq.install", { distro: DISTROS[hint.distro] ?? hint.distro })}
                        </span>
                        <code className="code-line">{hint.command}</code>
                      </div>
                    ))}
                  </>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
