import { Lock } from "lucide-react";
import { useEffect, useId, useState } from "react";
import { type CollationOverview, getCollationOverview } from "./collation";
import { databasesHref } from "./databases";
import type { EngineInfo } from "./engine";
import { ErrorBanner } from "./Feedback";
import { useI18n } from "./i18n";
import { usePassword } from "./PasswordProvider";

/**
 * The collations of an instance in its card: the server's (fixed on RDS),
 * tempdb's and how many databases differ from the server. It is read on
 * demand ("Show collation"), not with the card: a read is one more SQL
 * session, and a card per target would open them all.
 */
export function CollationSummary({ engine, target }: { engine: EngineInfo; target: string }) {
  const { t } = useI18n();
  const { epoch } = usePassword();
  const id = useId();
  const [shown, setShown] = useState(false);
  const [overview, setOverview] = useState<CollationOverview | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    // epoch: a SQL password was saved; read again if it is open.
    void epoch;
    if (!shown) {
      return;
    }
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    getCollationOverview(engine, target, controller.signal).then(
      (o) => {
        if (!controller.signal.aborted) {
          setOverview(o);
          setLoading(false);
        }
      },
      (e: unknown) => {
        if (!controller.signal.aborted) {
          setError(e);
          setLoading(false);
        }
      },
    );
    return () => controller.abort();
  }, [engine, target, shown, epoch]);

  const differ = overview?.databases.filter((d) => d.mismatchServer).length ?? 0;
  const risk = overview?.databases.filter((d) => d.tempTableRisk).length ?? 0;
  return (
    <div className="collation-summary">
      <button
        type="button"
        className="btn btn-ghost btn-sm"
        aria-expanded={shown}
        aria-controls={id}
        aria-label={t(shown ? "collation.summary.hideOf" : "collation.summary.showOf", { target })}
        onClick={() => setShown((s) => !s)}
      >
        {t(shown ? "collation.summary.hide" : "collation.summary.show")}
      </button>
      <div id={id} aria-busy={loading} hidden={!shown}>
        {shown && loading && !overview && (
          <div aria-hidden="true">
            <div className="skeleton skeleton-line" />
          </div>
        )}
        {shown && error !== null && <ErrorBanner error={error} />}
        {shown && overview && error === null && (
          <dl className="overview-stats collation-stats">
            <div>
              <dt>{t("collation.server")}</dt>
              <dd>
                <span className="mono">{overview.server}</span>{" "}
                {overview.serverFixed && (
                  <span className="badge badge-skipped" title={t("collation.serverFixedHint")}>
                    <Lock size={12} aria-hidden="true" />
                    {t("collation.serverFixed")}
                    <span className="sr-only">: {t("collation.serverFixedHint")}</span>
                  </span>
                )}
              </dd>
            </div>
            <div>
              <dt>{t("collation.tempdb")}</dt>
              <dd className="mono">{overview.tempdb}</dd>
            </div>
            <div>
              <dt>{t("collation.databases")}</dt>
              <dd>
                {differ === 0 ? (
                  t("collation.summary.allMatch")
                ) : (
                  <>
                    <a href={databasesHref(target)}>
                      {t("collation.summary.differ", { count: differ })}
                    </a>
                    {risk > 0 && (
                      <span className="muted">
                        {" "}
                        · {t("collation.summary.risk", { count: risk })}
                      </span>
                    )}
                  </>
                )}
              </dd>
            </div>
          </dl>
        )}
      </div>
    </div>
  );
}
