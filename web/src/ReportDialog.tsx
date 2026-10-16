import { LoaderCircle } from "lucide-react";
import { useEffect, useState } from "react";
import { errorText } from "./api";
import { formatBytes } from "./databases";
import type { EngineInfo } from "./engine";
import { useI18n } from "./i18n";
import { Modal } from "./Modal";
import { type DatabaseReport, errorHint, getDatabaseReport, planStepKey } from "./plans";
import { StepList } from "./StepList";

const MIB = 1024 * 1024;

/** The fix of an orphaned SQL user, as the runbook gives it. */
const ORPHAN_FIX = `CREATE LOGIN [u] WITH PASSWORD = '…';
-- then, in the database:
ALTER USER [u] WITH LOGIN = [u];`;

/** The post-restore report of a database (mssql-import.sh post), read-only. */
export function ReportDialog({
  engine,
  target,
  database,
  onClose,
}: {
  engine: EngineInfo;
  target: string;
  database: string;
  onClose: () => void;
}) {
  const { t, lang } = useI18n();
  const [report, setReport] = useState<DatabaseReport | null>(null);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    const controller = new AbortController();
    getDatabaseReport(engine, target, database, controller.signal).then(
      (r) => !controller.signal.aborted && setReport(r),
      (e: unknown) => !controller.signal.aborted && setError(e),
    );
    return () => controller.abort();
  }, [engine, target, database]);

  const hint = error === null ? undefined : errorHint(error);
  const d = report?.database;
  return (
    <Modal
      title={t("report.title", { database })}
      onClose={onClose}
      busy={!report && error === null}
      className="report-dialog"
      footer={
        <button type="button" className="btn btn-primary" onClick={onClose}>
          {t("plan.close")}
        </button>
      }
    >
      {!report && error === null && (
        <div className="plan-loading">
          <LoaderCircle size={16} className="spin tone-info" aria-hidden="true" />
          <span>{t("report.loading")}</span>
        </div>
      )}
      {error !== null && (
        <div className="banner banner-fail" role="alert">
          <span className="banner-text">{errorText(error)}</span>
          {hint && <span className="banner-hint">{t(hint)}</span>}
        </div>
      )}
      {report && d && (
        <>
          <section aria-label={t("report.findings")}>
            <h3 className="plan-heading">{t("report.findings")}</h3>
            <StepList
              steps={report.findings}
              label={(s) => {
                const key = planStepKey(s.step);
                return key ? t(key) : s.label;
              }}
            />
          </section>

          <section aria-label={t("report.properties")}>
            <h3 className="plan-heading">{t("report.properties")}</h3>
            <dl className="facts">
              <dt>{t("databases.col.state")}</dt>
              <dd className="mono">{d.state}</dd>
              <dt>{t("databases.col.recovery")}</dt>
              <dd className="mono">{d.recoveryModel}</dd>
              <dt>{t("databases.col.compatibility")}</dt>
              <dd className="mono">{d.compatibilityLevel}</dd>
              <dt>{t("report.collation")}</dt>
              <dd className="mono">{d.collation ?? "—"}</dd>
              <dt>{t("databases.col.owner")}</dt>
              <dd>{d.owner ?? "—"}</dd>
              <dt>{t("report.userAccess")}</dt>
              <dd className="mono">{d.userAccess}</dd>
              <dt>{t("report.readOnly")}</dt>
              <dd>{d.readOnly ? t("report.yes") : t("report.no")}</dd>
            </dl>
          </section>

          <section aria-label={t("report.files")}>
            <h3 className="plan-heading">{t("report.files")}</h3>
            <div className="table-wrap">
              <table className="db-table">
                <thead>
                  <tr>
                    <th scope="col">{t("report.file.type")}</th>
                    <th scope="col">{t("report.file.name")}</th>
                    <th scope="col" className="num">
                      {t("report.file.size")}
                    </th>
                    <th scope="col" className="num">
                      {t("report.file.max")}
                    </th>
                    <th scope="col">{t("report.file.path")}</th>
                  </tr>
                </thead>
                <tbody>
                  {report.files.map((f) => (
                    <tr key={`${f.type}-${f.name}`}>
                      <td className="mono">{f.type}</td>
                      <td className="mono">{f.name}</td>
                      <td className="num">{formatBytes(f.sizeMB * MIB, lang)}</td>
                      <td className="num">
                        {f.maxSizeMB === undefined
                          ? t("report.unlimited")
                          : formatBytes(f.maxSizeMB * MIB, lang)}
                      </td>
                      <td className="mono report-path">{f.physicalName}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>

          {report.orphanUsers.length > 0 && (
            <section aria-label={t("report.orphans")}>
              <h3 className="plan-heading">{t("report.orphans")}</h3>
              <ul className="report-users">
                {report.orphanUsers.map((u) => (
                  <li key={u} className="mono">
                    {u}
                  </li>
                ))}
              </ul>
              <p className="hint">{t("report.orphansFix")}</p>
              <pre className="statement">{ORPHAN_FIX}</pre>
            </section>
          )}

          {report.access ? (
            <section aria-label={t("report.objects")}>
              <h3 className="plan-heading">{t("report.objects")}</h3>
              {report.objects.length === 0 ? (
                <p className="muted">{t("report.noObjects")}</p>
              ) : (
                <table className="db-table report-objects">
                  <thead>
                    <tr>
                      <th scope="col">{t("report.objects.type")}</th>
                      <th scope="col" className="num">
                        {t("report.objects.count")}
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {report.objects.map((o) => (
                      <tr key={o.type}>
                        <td className="mono">{o.type}</td>
                        <td className="num">{o.count}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </section>
          ) : (
            d.state === "ONLINE" && <p className="muted">{t("report.noAccess")}</p>
          )}
        </>
      )}
    </Modal>
  );
}
