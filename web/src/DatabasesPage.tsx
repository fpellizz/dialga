import {
  ArrowDown,
  ArrowUp,
  ArrowUpDown,
  CloudDownload,
  Database,
  RefreshCw,
  SearchX,
  X,
} from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { type Database as DatabaseRow, listDatabases, listTargets, type TargetList } from "./api";
import {
  compatRelease,
  DEFAULT_SORT,
  databaseBytes,
  databasesHref,
  dbStateKey,
  filterDatabases,
  formatBytes,
  loadLastTarget,
  nextSort,
  pickTarget,
  type Sort,
  type SortKey,
  saveLastTarget,
  sortDatabases,
  stateBadge,
  targetFromHash,
} from "./databases";
import type { EngineInfo } from "./engine";
import { ErrorBanner, HintText } from "./Feedback";
import { type MessageKey, useI18n } from "./i18n";
import { PlanDialog, type PlanResult } from "./PlanDialog";
import type { Operation } from "./plans";
import { ReportDialog } from "./ReportDialog";

const COLUMNS: { key: SortKey; label: MessageKey; numeric?: boolean }[] = [
  { key: "name", label: "databases.col.name" },
  { key: "state", label: "databases.col.state" },
  { key: "size", label: "databases.col.size", numeric: true },
  { key: "compatibility", label: "databases.col.compatibility", numeric: true },
  { key: "recovery", label: "databases.col.recovery" },
  { key: "owner", label: "databases.col.owner" },
];

const MIB = 1024 * 1024;

/** What a database row, or the page, opens: a plan dialog or the report. */
type Dialog =
  | { kind: "plan"; operation: Operation; database?: string }
  | { kind: "report"; database: string };

/** The databases read for one target; another target shows the skeleton until its own arrive. */
interface Loaded {
  target: string;
  list: DatabaseRow[];
}

/** The user databases of one instance: filter, sort, refresh. */
export function DatabasesPage({
  engine,
  engineError,
}: {
  engine: EngineInfo | null;
  engineError: string;
}) {
  const { t } = useI18n();
  const [targets, setTargets] = useState<TargetList | null>(null);
  const [targetsError, setTargetsError] = useState<unknown>(null);
  const [target, setTarget] = useState("");
  const [loaded, setLoaded] = useState<Loaded | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);
  const [reload, setReload] = useState(0);
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<Sort>(DEFAULT_SORT);
  const [dialog, setDialog] = useState<Dialog | null>(null);
  // A task the plan dialog was following when it closed: it keeps running.
  const [running, setRunning] = useState<{ id: number; target: string } | null>(null);

  useEffect(() => {
    if (!engine) {
      return;
    }
    let live = true;
    listTargets(engine).then(
      (list) => {
        if (live) {
          const names = list.targets.map((x) => x.name);
          // A #databases/<target> link wins over the remembered target.
          const wanted = targetFromHash(window.location.hash);
          setTargets(list);
          setTarget(pickTarget(names, loadLastTarget(), wanted));
          if (wanted && names.includes(wanted)) {
            saveLastTarget(wanted);
          }
        }
      },
      (e: unknown) => live && setTargetsError(e),
    );
    return () => {
      live = false;
    };
  }, [engine]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: reload is the refresh trigger.
  useEffect(() => {
    if (!engine || !target) {
      return;
    }
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    listDatabases(engine, target, controller.signal).then(
      (list) => {
        if (!controller.signal.aborted) {
          setLoaded({ target, list });
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
  }, [engine, target, reload]);

  const list = loaded?.target === target ? loaded.list : null;
  // Busy from the first render of a target, like its card: the request starts in an effect.
  const busy = loading || (!!target && !list && !error);
  const rows = useMemo(
    () => (list ? sortDatabases(filterDatabases(list, query), sort) : []),
    [list, query, sort],
  );

  const closePlan = (result: PlanResult) => {
    setDialog(null);
    if (result.running) {
      setRunning({ id: result.running.id, target });
    }
    if (result.changed) {
      setReload((n) => n + 1);
    }
  };

  const choose = (name: string) => {
    saveLastTarget(name);
    setTarget(name);
    // Keep the address in step without a hashchange (the page stays mounted).
    window.history.replaceState(null, "", databasesHref(name));
  };

  const noTargets = targets !== null && !targets.error && targets.targets.length === 0;
  const pageError = engineError || targetsError;
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{t("databases.title")}</h1>
          <p className="lead">{t("databases.lead")}</p>
        </div>
        {targets && targets.targets.length > 0 && (
          <div className="toolbar">
            <label className="field-inline">
              <span>{t("databases.target")}</span>
              <select
                name="target"
                className="select"
                value={target}
                onChange={(e) => choose(e.currentTarget.value)}
              >
                {targets.targets.map((x) => (
                  <option key={x.name} value={x.name}>
                    {x.name}
                  </option>
                ))}
              </select>
            </label>
            <button
              type="button"
              className="btn btn-outline"
              onClick={() => !busy && setReload((n) => n + 1)}
              aria-disabled={busy || undefined}
              disabled={!target}
              aria-label={t("databases.refresh", { target })}
            >
              <RefreshCw size={16} className={busy ? "spin" : undefined} aria-hidden="true" />
              {t("instances.refresh")}
            </button>
            <button
              type="button"
              className="btn btn-primary"
              onClick={() => setDialog({ kind: "plan", operation: "restore" })}
              disabled={!engine || !target}
            >
              <CloudDownload size={16} aria-hidden="true" />
              {t("databases.restore")}
            </button>
          </div>
        )}
      </header>

      {running && (
        <div className="banner banner-warn" role="status">
          <span className="banner-text">
            {t("databases.taskRunning", { id: running.id, target: running.target })}{" "}
            <a href="#instances">{t("databases.taskRunningLink")}</a>
          </span>
          <button
            type="button"
            className="btn btn-ghost btn-sm"
            onClick={() => setRunning(null)}
            aria-label={t("databases.dismiss")}
            title={t("databases.dismiss")}
          >
            <X size={14} aria-hidden="true" />
          </button>
        </div>
      )}

      {pageError !== null && pageError !== "" && <ErrorBanner error={pageError} />}
      {targets?.error && (
        <div className="banner banner-fail" role="alert">
          <strong>{t("instances.error")}</strong>
          <pre className="error-pre">{targets.error}</pre>
          <HintText hint={{ key: "hint.config" }} />
        </div>
      )}
      {noTargets && (
        <section className="empty" role="status">
          <Database size={20} aria-hidden="true" />
          <h2>{t("databases.noTargets.title")}</h2>
          <p>{t("databases.noTargets.hint")}</p>
          <a className="btn btn-outline" href="#instances">
            {t("databases.noTargets.action")}
          </a>
        </section>
      )}
      {error !== null && <ErrorBanner error={error} fallback={{ key: "databases.errorHint" }} />}

      {target && error === null && (
        <section
          className="card db-card"
          aria-label={t("databases.caption", { target })}
          aria-busy={loading || !list}
        >
          <div className="db-toolbar">
            <label className="field-inline db-filter">
              <span>{t("databases.filter")}</span>
              <input
                type="search"
                name="filter"
                className="input"
                value={query}
                placeholder={t("databases.filterPlaceholder")}
                onChange={(e) => setQuery(e.currentTarget.value)}
                disabled={!list}
              />
            </label>
            {list && (
              <span className="muted" role="status">
                {rows.length !== list.length
                  ? t("databases.countFiltered", { shown: rows.length, count: list.length })
                  : list.length === 1
                    ? t("databases.countOne")
                    : t("databases.count", { count: list.length })}
              </span>
            )}
          </div>

          {!list && (
            <div aria-hidden="true">
              <div className="skeleton skeleton-row" />
              <div className="skeleton skeleton-row" />
              <div className="skeleton skeleton-row" />
            </div>
          )}
          {list?.length === 0 && (
            <section className="empty" role="status">
              <Database size={20} aria-hidden="true" />
              <h2>{t("databases.empty.title")}</h2>
              <p>{t("databases.empty.hint")}</p>
            </section>
          )}
          {list && list.length > 0 && rows.length === 0 && (
            <section className="empty">
              <SearchX size={20} aria-hidden="true" />
              <h2>{t("databases.noMatch.title")}</h2>
              <button type="button" className="btn btn-outline" onClick={() => setQuery("")}>
                {t("databases.noMatch.clear")}
              </button>
            </section>
          )}
          {list && rows.length > 0 && (
            <div className="table-wrap">
              <table className="db-table">
                <caption className="sr-only">{t("databases.caption", { target })}</caption>
                <thead>
                  <tr>
                    {COLUMNS.map((col) => (
                      <SortHeader
                        key={col.key}
                        label={t(col.label)}
                        numeric={col.numeric}
                        dir={sort.key === col.key ? sort.dir : undefined}
                        onSort={() => setSort((s) => nextSort(s, col.key))}
                      />
                    ))}
                    <th scope="col" className="row-actions-head">
                      {t("databases.col.actions")}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((db) => (
                    <DatabaseLine key={db.name} db={db} onOpen={setDialog} />
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>
      )}

      {engine && target && dialog?.kind === "plan" && (
        <PlanDialog
          engine={engine}
          target={target}
          operation={dialog.operation}
          request={dialog.database ? { database: dialog.database } : {}}
          fields={
            dialog.operation === "restore"
              ? "restore"
              : dialog.operation === "export"
                ? "export"
                : undefined
          }
          onClose={closePlan}
          onShowReport={(database) => {
            setReload((n) => n + 1);
            setDialog({ kind: "report", database });
          }}
        />
      )}
      {engine && target && dialog?.kind === "report" && (
        <ReportDialog
          engine={engine}
          target={target}
          database={dialog.database}
          onClose={() => setDialog(null)}
        />
      )}
    </div>
  );
}

function SortHeader({
  label,
  numeric,
  dir,
  onSort,
}: {
  label: string;
  numeric?: boolean;
  dir?: Sort["dir"];
  onSort: () => void;
}) {
  let Icon = ArrowUpDown;
  if (dir === "ascending") {
    Icon = ArrowUp;
  } else if (dir === "descending") {
    Icon = ArrowDown;
  }
  return (
    <th scope="col" aria-sort={dir} className={numeric ? "num" : undefined}>
      <button type="button" className="sort" onClick={onSort}>
        {label}
        <Icon size={14} className={dir ? undefined : "sort-idle"} aria-hidden="true" />
      </button>
    </th>
  );
}

function DatabaseLine({ db, onOpen }: { db: DatabaseRow; onOpen: (dialog: Dialog) => void }) {
  const { t, lang } = useI18n();
  const plan = (operation: Operation) => onOpen({ kind: "plan", operation, database: db.name });
  const stateKey = dbStateKey(db.state);
  const release = compatRelease(db.compatibility);
  return (
    <tr>
      <th scope="row" className="db-name">
        <span className="mono">{db.name}</span>
        {db.readOnly && <span className="badge badge-skipped">{t("databases.readOnly")}</span>}
        {db.userAccess === "SINGLE_USER" && (
          <span className="badge badge-warning">{t("databases.singleUser")}</span>
        )}
        {db.userAccess === "RESTRICTED_USER" && (
          <span className="badge badge-warning">{t("databases.restrictedUser")}</span>
        )}
      </th>
      <td>
        <span className={`badge ${stateBadge(db.state)}`}>{stateKey ? t(stateKey) : db.state}</span>
      </td>
      <td
        className="num"
        title={t("databases.sizeDetail", {
          data: formatBytes(db.dataMB * MIB, lang),
          log: formatBytes(db.logMB * MIB, lang),
        })}
      >
        {formatBytes(databaseBytes(db), lang)}
      </td>
      <td className="num">
        <span className="mono">{db.compatibility}</span>
        {release && <span className="muted"> · {release}</span>}
      </td>
      <td className="mono">{db.recovery}</td>
      <td>
        {db.owner ?? (
          // A screen reader reads the reason, not "em dash".
          <span className="muted" title={t("databases.noOwner")}>
            <span aria-hidden="true">—</span>
            <span className="sr-only">{t("databases.noOwner")}</span>
          </span>
        )}
      </td>
      <td className="row-actions">
        <button
          type="button"
          className="btn btn-ghost btn-sm"
          aria-label={t("dbaction.reportOf", { database: db.name })}
          onClick={() => onOpen({ kind: "report", database: db.name })}
        >
          {t("dbaction.report")}
        </button>
        {db.state === "ONLINE" && (
          <button
            type="button"
            className="btn btn-ghost btn-sm"
            aria-label={t("dbaction.exportOf", { database: db.name })}
            onClick={() => plan("export")}
          >
            {t("dbaction.export")}
          </button>
        )}
        {db.state === "RESTORING" && (
          <button
            type="button"
            className="btn btn-ghost btn-sm"
            aria-label={t("dbaction.finishOf", { database: db.name })}
            onClick={() => plan("finish")}
          >
            {t("dbaction.finish")}
          </button>
        )}
        <button
          type="button"
          className="btn btn-ghost btn-sm btn-danger-text"
          aria-label={t("dbaction.dropOf", { database: db.name })}
          onClick={() => plan("drop")}
        >
          {t("dbaction.drop")}
        </button>
      </td>
    </tr>
  );
}
