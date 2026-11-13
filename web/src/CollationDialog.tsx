import { LoaderCircle, Lock, RefreshCw, TriangleAlert } from "lucide-react";
import { useEffect, useId, useMemo, useState } from "react";
import {
  type CollationColumn,
  type DatabaseCollationView,
  getDatabaseCollation,
  plainTableRef,
} from "./collation";
import type { EngineInfo } from "./engine";
import { ErrorBanner } from "./Feedback";
import { useI18n } from "./i18n";
import { Modal } from "./Modal";
import { usePassword } from "./PasswordProvider";
import { PlanDialog, type PlanResult } from "./PlanDialog";
import { planHint } from "./plans";

/** The plan dialog open on top of the panel. */
type Operation = { kind: "set" } | { kind: "convert" };

/** The columns drawn at once: the engine sends at most 2,000. */
const MAX_ROWS = 200;

/**
 * The collations of a database: its default beside the server's and
 * tempdb's, the groups of its columns and the columns that differ from the
 * default. From here, "Change default collation" and "Convert columns" open
 * a plan (PlanDialog: preview, confirm, execute); the panel reads again when
 * one closes after running.
 */
export function CollationDialog({
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
  const { t } = useI18n();
  const { epoch } = usePassword();
  const ids = useId();
  const [view, setView] = useState<DatabaseCollationView | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(true);
  const [reload, setReload] = useState(0);
  const [operation, setOperation] = useState<Operation | null>(null);
  const [filter, setFilter] = useState("");

  // biome-ignore lint/correctness/useExhaustiveDependencies: reload and epoch are the refresh triggers.
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    getDatabaseCollation(engine, target, database, controller.signal).then(
      (v) => {
        if (!controller.signal.aborted) {
          setView(v);
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
  }, [engine, target, database, reload, epoch]);

  const needle = filter.trim().toLowerCase();
  const columns = useMemo(
    () =>
      (view?.columns ?? []).filter(
        (c) =>
          !needle ||
          [plainTableRef({ schema: c.schema, name: c.table }), c.column, c.collation].some((x) =>
            x.toLowerCase().includes(needle),
          ),
      ),
    [view, needle],
  );

  function operationClosed(result: PlanResult) {
    setOperation(null);
    if (result.changed) {
      setReload((n) => n + 1);
    }
    setTimeout(() => {
      document
        .querySelector<HTMLElement>(`[data-collation="${ids}"] [data-action="change"]`)
        ?.focus();
    }, 0);
  }

  return (
    <>
      <Modal
        title={t("collation.title", { database })}
        onClose={onClose}
        busy={loading}
        className="collation-dialog"
        footer={
          <>
            <button type="button" className="btn btn-outline" onClick={onClose}>
              {t("plan.close")}
            </button>
            <button
              type="button"
              className="btn btn-ghost"
              disabled={loading}
              onClick={() => setReload((n) => n + 1)}
            >
              <RefreshCw size={16} className={loading ? "spin" : undefined} aria-hidden="true" />
              {t("collation.refresh")}
            </button>
            <button
              type="button"
              className="btn btn-outline"
              data-action="convert"
              disabled={!view}
              onClick={() => setOperation({ kind: "convert" })}
            >
              {t("collation.convert")}
            </button>
            <button
              type="button"
              className="btn btn-primary"
              data-action="change"
              disabled={!view}
              onClick={() => setOperation({ kind: "set" })}
            >
              {t("collation.change")}
            </button>
          </>
        }
      >
        <div data-collation={ids}>
          <p className="lead">{t("collation.lead", { target })}</p>
          {!view && loading && (
            <div className="plan-loading" role="status">
              <LoaderCircle size={16} className="spin tone-info" aria-hidden="true" />
              <span>{t("collation.loading")}</span>
            </div>
          )}
          {error !== null && <ErrorBanner error={error} fallback={planHint(error)} />}
          {view && (
            <>
              <dl className="collation-facts">
                <div>
                  <dt>{t("collation.database")}</dt>
                  <dd className="mono">{view.collation}</dd>
                </div>
                <div>
                  <dt>{t("collation.server")}</dt>
                  <dd>
                    <span className="mono">{view.server}</span>{" "}
                    <span className="badge badge-skipped" title={t("collation.serverFixedHint")}>
                      <Lock size={12} aria-hidden="true" />
                      {t("collation.serverFixed")}
                      <span className="sr-only">: {t("collation.serverFixedHint")}</span>
                    </span>
                  </dd>
                </div>
                <div>
                  <dt>{t("collation.tempdb")}</dt>
                  <dd className="mono">{view.tempdb}</dd>
                </div>
                {view.containment !== "NONE" && (
                  <div>
                    <dt>{t("collation.containment")}</dt>
                    <dd>{t("collation.contained")}</dd>
                  </div>
                )}
              </dl>

              {view.mismatchServer && (
                <div className="banner banner-warn">
                  <TriangleAlert size={16} aria-hidden="true" />
                  <span className="banner-text">
                    <strong>{t("collation.mismatch")}</strong> ·{" "}
                    {t("collation.mismatchHint", { server: view.server })}
                  </span>
                </div>
              )}
              {view.tempTableRisk && (
                <div className="banner banner-warn">
                  <TriangleAlert size={16} aria-hidden="true" />
                  <span className="banner-text">
                    <strong>{t("collation.tempRisk")}</strong> · {t("collation.tempRiskHint")}{" "}
                    <code className="mono">COLLATE DATABASE_DEFAULT</code>
                  </span>
                </div>
              )}

              {view.groups.length > 0 && (
                <div className="table-wrap">
                  <table className="db-table collation-groups">
                    <caption className="sr-only">
                      {t("collation.groups.caption", { database })}
                    </caption>
                    <thead>
                      <tr>
                        <th scope="col">{t("collation.groups.col.collation")}</th>
                        <th scope="col" className="num">
                          {t("collation.groups.col.columns")}
                        </th>
                        <th scope="col" className="num">
                          {t("collation.groups.col.tables")}
                        </th>
                        <th scope="col" className="num">
                          {t("collation.groups.col.codePage")}
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {view.groups.map((g) => (
                        <tr key={g.collation}>
                          <th scope="row" className="db-name">
                            <span className="mono">{g.collation}</span>
                            {g.default && (
                              <span className="badge badge-ok">
                                {t("collation.groups.default")}
                              </span>
                            )}
                          </th>
                          <td className="num">{g.columns}</td>
                          <td className="num">{g.tables}</td>
                          <td className="num">{g.codePage === 0 ? "—" : g.codePage}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}

              <h3 className="plan-heading">{t("collation.columns.caption", { database })}</h3>
              {view.columns.length === 0 ? (
                <p className="hint">{t("collation.columns.none")}</p>
              ) : (
                <>
                  <label className="field-inline collation-filter">
                    <span>{t("collation.columns.filter")}</span>
                    <input
                      type="search"
                      name="columnFilter"
                      className="input"
                      value={filter}
                      onChange={(e) => setFilter(e.currentTarget.value)}
                    />
                  </label>
                  <div className="table-wrap">
                    <table className="db-table collation-columns">
                      <caption className="sr-only">
                        {t("collation.columns.caption", { database })}
                      </caption>
                      <thead>
                        <tr>
                          <th scope="col">{t("collation.columns.col.table")}</th>
                          <th scope="col">{t("collation.columns.col.column")}</th>
                          <th scope="col">{t("collation.columns.col.type")}</th>
                          <th scope="col">{t("collation.columns.col.collation")}</th>
                        </tr>
                      </thead>
                      <tbody>
                        {columns.slice(0, MAX_ROWS).map((c) => (
                          <ColumnLine key={`${c.schema}.${c.table}.${c.column}`} column={c} />
                        ))}
                      </tbody>
                    </table>
                  </div>
                  {columns.length > MAX_ROWS && (
                    <p className="hint">
                      {t("collation.columns.more", { shown: MAX_ROWS, count: columns.length })}
                    </p>
                  )}
                  {view.truncated && <p className="hint">{t("collation.columns.truncated")}</p>}
                </>
              )}
            </>
          )}
        </div>
      </Modal>

      {operation?.kind === "set" && (
        <PlanDialog
          key="set"
          engine={engine}
          target={target}
          operation="set-collation"
          request={{ database }}
          fields="set-collation"
          onClose={operationClosed}
          onConvertColumns={() => {
            // The new collation is the default of the database now: that is the target.
            setReload((n) => n + 1);
            setOperation({ kind: "convert" });
          }}
        />
      )}
      {operation?.kind === "convert" && (
        <PlanDialog
          key="convert"
          engine={engine}
          target={target}
          operation="convert-columns"
          request={{ database }}
          fields="convert-columns"
          onClose={operationClosed}
        />
      )}
    </>
  );
}

function ColumnLine({ column: c }: { column: CollationColumn }) {
  const { t } = useI18n();
  return (
    <tr>
      <th scope="row" className="db-name">
        <span className="mono">{plainTableRef({ schema: c.schema, name: c.table })}</span>
      </th>
      <td className="mono">{c.column}</td>
      <td className="mono">
        {c.type}
        {c.baseType !== c.type ? ` (${c.baseType})` : ""}
      </td>
      <td>
        <span className="mono">{c.collation}</span>
        {c.unsupported && (
          <span className="badge badge-warning" title={c.unsupported}>
            {t("collation.unsupported")}
            <span className="sr-only">: {c.unsupported}</span>
          </span>
        )}
      </td>
    </tr>
  );
}
