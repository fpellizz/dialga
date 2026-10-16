import {
  CalendarClock,
  CircleCheck,
  CircleX,
  LoaderCircle,
  ShieldAlert,
  TriangleAlert,
} from "lucide-react";
import { type ReactNode, useId, useMemo, useState } from "react";
import type { Database, Overview, StepEvent, TargetList } from "./api";
import {
  DEFAULT_SORT,
  databaseBytes,
  filterDatabases,
  formatBytes,
  sortDatabases,
} from "./databases";
import type { ExportPreview } from "./exportBackend";
import {
  BACKUP_MB_PER_MINUTE,
  type DownloadOptions,
  estimateMinutes,
  exportable,
  nextOccurrence,
  type Occurrence,
  type OptionErrors,
  overlap,
  parseWindow,
  TASK_START_MINUTES,
} from "./exportPlan";
import { type ExportItemRun, type ItemState, tally } from "./exportRun";
import { ErrorBanner } from "./Feedback";
import { type Lang, messages, type Translate, useI18n } from "./i18n";
import { planStepKey } from "./plans";
import { StepList } from "./StepList";
import { TaskProgress } from "./TaskProgress";

const STATE_BADGE: Record<ItemState, string> = {
  queued: "badge-skipped",
  running: "badge-running",
  succeeded: "badge-ok",
  failed: "badge-failed",
  cancelled: "badge-skipped",
};

/** The label of an export step, a pre-flight check or a download step. */
function stepLabel(t: Translate, s: StepEvent, database: string, key: string): string {
  if (s.step === "export") {
    return t("planaction.export", { database, key });
  }
  if (s.step.startsWith("download:")) {
    return s.label;
  }
  const known = planStepKey(s.step);
  return known ? t(known) : s.label;
}

/** Step 1: the instance and its online databases, several at once. */
export function DatabasesStep({
  targets,
  target,
  onTarget,
  list,
  error,
  selected,
  onSelect,
}: {
  targets: TargetList | null;
  target: string;
  onTarget: (name: string) => void;
  list: Database[] | null;
  error: unknown;
  selected: string[];
  onSelect: (names: string[]) => void;
}) {
  const { t, lang } = useI18n();
  const [query, setQuery] = useState("");
  const ids = useId();
  // By name, as the Databases page shows them by default.
  const online = useMemo(
    () => sortDatabases((list ?? []).filter(exportable), DEFAULT_SORT),
    [list],
  );
  const shown = useMemo(() => filterDatabases(online, query), [online, query]);
  const others = (list ?? []).filter((d) => !exportable(d));
  const total = online
    .filter((d) => selected.includes(d.name))
    .reduce((sum, d) => sum + databaseBytes(d), 0);

  const toggle = (name: string, on: boolean) =>
    onSelect(
      // The order of the list, whatever the order of the clicks.
      online.map((d) => d.name).filter((n) => (n === name ? on : selected.includes(n))),
    );

  return (
    <>
      {targets && targets.targets.length > 0 && (
        <label className="field-inline export-target">
          <span>{t("export.target")}</span>
          <select
            name="target"
            className="select"
            value={target}
            onChange={(e) => onTarget(e.currentTarget.value)}
          >
            {targets.targets.map((x) => (
              <option key={x.name} value={x.name}>
                {x.name}
              </option>
            ))}
          </select>
        </label>
      )}
      {targets && targets.targets.length === 0 && !targets.error && (
        <section className="empty">
          <h3>{t("databases.noTargets.title")}</h3>
          <p>{t("databases.noTargets.hint")}</p>
          <a className="btn btn-outline" href="#instances">
            {t("databases.noTargets.action")}
          </a>
        </section>
      )}
      {error !== null && <ErrorBanner error={error} fallback={{ key: "databases.errorHint" }} />}
      {target && error === null && !list && (
        <div aria-hidden="true">
          <div className="skeleton skeleton-row" />
          <div className="skeleton skeleton-row" />
        </div>
      )}
      {list && online.length === 0 && <p className="muted">{t("export.noDatabases")}</p>}
      {list && online.length > 0 && (
        <fieldset className="form-section" aria-describedby={`${ids}-hint`}>
          <legend>{t("export.dbLegend")}</legend>
          <p id={`${ids}-hint`} className="hint">
            {t("export.dbHint")}
          </p>
          <div className="export-tools">
            {online.length > 6 && (
              <label className="field-inline db-filter">
                <span>{t("databases.filter")}</span>
                <input
                  type="search"
                  name="filter"
                  className="input"
                  value={query}
                  placeholder={t("databases.filterPlaceholder")}
                  onChange={(e) => setQuery(e.currentTarget.value)}
                />
              </label>
            )}
            <button
              type="button"
              className="btn btn-ghost btn-sm"
              onClick={() => onSelect(online.map((d) => d.name))}
            >
              {t("export.selectAll")}
            </button>
            <button type="button" className="btn btn-ghost btn-sm" onClick={() => onSelect([])}>
              {t("export.selectNone")}
            </button>
          </div>
          <ul className="export-dbs">
            {shown.map((db) => (
              <li key={db.name}>
                <label className="check export-db">
                  <input
                    type="checkbox"
                    name="database"
                    value={db.name}
                    checked={selected.includes(db.name)}
                    onChange={(e) => toggle(db.name, e.currentTarget.checked)}
                  />
                  <span className="mono">{db.name}</span>
                  <span className="muted">{formatBytes(databaseBytes(db), lang)}</span>
                </label>
              </li>
            ))}
          </ul>
          <p className="muted" role="status">
            {t("export.selected", { count: selected.length, size: formatBytes(total, lang) })}
          </p>
          {others.length > 0 && (
            <p className="hint">
              {t("export.notOnline", { names: others.map((d) => d.name).join(", ") })}
            </p>
          )}
        </fieldset>
      )}
    </>
  );
}

/** Step 2: the S3 key of each database and the optional download. */
export function OptionsStep({
  databases,
  keys,
  onKeys,
  download,
  onDownload,
  errors,
}: {
  databases: Database[];
  keys: Record<string, string>;
  onKeys: (keys: Record<string, string>) => void;
  download: DownloadOptions;
  onDownload: (download: DownloadOptions) => void;
  errors: OptionErrors;
}) {
  const { t } = useI18n();
  const ids = useId();
  return (
    <>
      <fieldset className="form-section" aria-describedby={`${ids}-keys`}>
        <legend>{t("export.keysLegend")}</legend>
        <p id={`${ids}-keys`} className="hint">
          {t("export.keyHint")}
        </p>
        {databases.map((db) => {
          const error = errors.keys[db.name];
          const id = `${ids}-key-${db.name}`;
          return (
            <div key={db.name} className="form-field">
              <label htmlFor={id}>{t("export.key", { database: db.name })}</label>
              <input
                id={id}
                name={`key-${db.name}`}
                className="input mono"
                value={keys[db.name] ?? ""}
                placeholder={t("export.keyDefault")}
                autoComplete="off"
                spellCheck={false}
                aria-invalid={error ? true : undefined}
                aria-describedby={error ? `${id}-error` : undefined}
                onChange={(e) => onKeys({ ...keys, [db.name]: e.currentTarget.value })}
              />
              {error && (
                <p id={`${id}-error`} className="field-error">
                  {t(error)}
                </p>
              )}
            </div>
          );
        })}
      </fieldset>

      <fieldset className="form-section">
        <legend>{t("export.downloadLegend")}</legend>
        <label className="check">
          <input
            type="checkbox"
            name="download"
            checked={download.enabled}
            onChange={(e) => onDownload({ ...download, enabled: e.currentTarget.checked })}
          />
          <span>{t("export.download")}</span>
        </label>
        {download.enabled && (
          <div className="indent form-section">
            <div className="form-field">
              <label htmlFor={`${ids}-folder`}>{t("export.folder")}</label>
              <input
                id={`${ids}-folder`}
                name="folder"
                className="input mono"
                value={download.folder}
                autoComplete="off"
                spellCheck={false}
                aria-required="true"
                aria-invalid={errors.folder ? true : undefined}
                aria-describedby={`${ids}-folder-hint${errors.folder ? ` ${ids}-folder-error` : ""}`}
                onChange={(e) => onDownload({ ...download, folder: e.currentTarget.value })}
              />
              <p id={`${ids}-folder-hint`} className="hint">
                {t("export.folderHint")}
              </p>
              {errors.folder && (
                <p id={`${ids}-folder-error`} className="field-error">
                  {t(errors.folder)}
                </p>
              )}
            </div>
            <label className="check">
              <input
                type="checkbox"
                name="overwrite"
                checked={download.overwrite}
                onChange={(e) => onDownload({ ...download, overwrite: e.currentTarget.checked })}
              />
              <span>{t("export.overwrite")}</span>
            </label>
          </div>
        )}
      </fieldset>
    </>
  );
}

/** "Sun 07:00–07:30 (05:00–05:30 UTC)": local time first, UTC after. */
function occurrenceText(lang: Lang, o: Occurrence): string {
  const local = new Intl.DateTimeFormat(lang, {
    weekday: "short",
    hour: "2-digit",
    minute: "2-digit",
  });
  const time = (zone?: string) =>
    new Intl.DateTimeFormat(lang, { hour: "2-digit", minute: "2-digit", timeZone: zone });
  return `${local.format(o.start)}–${time().format(o.end)} (${time("UTC").format(o.start)}–${time("UTC").format(o.end)} UTC)`;
}

/** Step 3: the automated backup and maintenance windows against a rough duration. */
export function WindowsStep({
  overview,
  error,
  databases,
  onRetry,
  now = new Date(),
}: {
  overview: Overview | null;
  error: unknown;
  databases: Database[];
  onRetry: () => void;
  now?: Date;
}) {
  const { t, lang } = useI18n();
  const minutes = estimateMinutes(databases);
  const end = new Date(now.getTime() + minutes * 60_000);
  if (error !== null) {
    return (
      <>
        <ErrorBanner error={error} />
        <p className="hint">{t("export.windows.unknown")}</p>
        <div>
          <button type="button" className="btn btn-outline btn-sm" onClick={onRetry}>
            {t("overview.retry")}
          </button>
        </div>
      </>
    );
  }
  if (!overview) {
    return <p className="muted">{t("export.windows.loading")}</p>;
  }
  const rows = (
    [
      { key: "backup", text: overview.backupWindow },
      { key: "maintenance", text: overview.maintenanceWindow },
    ] as const
  ).map(({ key, text }) => {
    const parsed = parseWindow(text);
    const hit = parsed ? overlap(parsed, now, end) : null;
    return {
      key,
      text,
      name: t(`export.windows.${key}`),
      hit,
      next: parsed ? (hit ?? nextOccurrence(parsed, now)) : null,
    };
  });
  const crossing = rows.filter((r) => r.hit);
  return (
    <>
      <p className="lead">{t("export.windows.lead")}</p>
      <dl className="facts export-windows">
        {rows.map((r) => (
          <Fact key={r.key} label={r.name}>
            {r.next ? (
              <>
                <span className="mono">{r.text}</span>
                <span className="muted">
                  {" "}
                  · {t("export.windows.next", { when: occurrenceText(lang, r.next) })}
                </span>
              </>
            ) : (
              <span className="muted">{t("export.windows.none")}</span>
            )}
          </Fact>
        ))}
        <Fact label={t("export.windows.duration")}>
          {t("export.windows.estimate", {
            minutes,
            rate: Math.round(BACKUP_MB_PER_MINUTE / 60),
            start: TASK_START_MINUTES,
          })}
        </Fact>
      </dl>
      {crossing.map((c) => (
        <div key={c.key} className="banner banner-warn">
          <CalendarClock size={16} aria-hidden="true" />
          <span className="banner-text">
            {t("export.windows.overlap", {
              window: c.name.toLocaleLowerCase(lang),
              when: c.hit ? occurrenceText(lang, c.hit) : "",
            })}
          </span>
        </div>
      ))}
      {crossing.length === 0 && (
        <div className="banner banner-ok">
          <CircleCheck size={16} aria-hidden="true" />
          <span className="banner-text">{t("export.windows.clear")}</span>
        </div>
      )}
      {overview.activeTasks !== undefined && overview.activeTasks > 0 && (
        <div className="banner banner-warn">
          <TriangleAlert size={16} aria-hidden="true" />
          <span className="banner-text">
            {t("export.windows.tasks", { count: overview.activeTasks })}
          </span>
        </div>
      )}
    </>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt>{label}</dt>
      <dd>{children}</dd>
    </>
  );
}

/** Step 4: the plan of each database with its checks, then the confirmation. */
export function PreviewStep({
  preview,
  typed,
  onTyped,
  onRun,
}: {
  preview: ExportPreview;
  typed: string;
  onTyped: (value: string) => void;
  onRun: () => void;
}) {
  const { t, lang } = useI18n();
  const ids = useId();
  const [before, after] = messages[lang]["plan.confirm.label"].split("{text}");
  const confirmation = preview.confirmation;
  return (
    <>
      {preview.protected && (
        <div className="banner banner-warn">
          <ShieldAlert size={16} aria-hidden="true" />
          <span className="banner-text">
            <strong>{t("plan.protected")}</strong> ·{" "}
            {t("plan.confirm.protected", { target: preview.target })}
          </span>
        </div>
      )}
      <p className="hint">{t(preview.via === "job" ? "export.via.job" : "export.via.plans")}</p>
      <ul className="export-previews">
        {preview.items.map((item) => (
          <li key={item.database} className="export-preview">
            <div className="export-preview-head">
              <h3 className="mono">{item.database}</h3>
              {item.ready ? (
                <span className="badge badge-ok">{t("export.preview.ready")}</span>
              ) : (
                <span className="badge badge-failed">{t("export.preview.notReadyBadge")}</span>
              )}
            </div>
            {item.error !== undefined && <ErrorBanner error={item.error} />}
            {item.s3Uri && (
              <p className="export-uri">
                {t("export.preview.s3")} <code>{item.s3Uri}</code>
              </p>
            )}
            {item.localFile && (
              <p className="export-uri">
                {t("export.preview.local")} <code>{item.localFile}</code>
              </p>
            )}
            {item.checks.length > 0 && (
              <StepList
                steps={item.checks}
                label={(s) => stepLabel(t, s, item.database, item.key)}
              />
            )}
            {item.statements.length > 0 && (
              <details className="task-details">
                <summary>{t("export.preview.statement")}</summary>
                {item.statements.map((statement) => (
                  <pre key={statement} className="statement">
                    {statement}
                  </pre>
                ))}
              </details>
            )}
          </li>
        ))}
      </ul>
      {!preview.ready && (
        <div className="banner banner-fail">
          <CircleX size={16} aria-hidden="true" />
          <span className="banner-text">{t("export.preview.notReady")}</span>
        </div>
      )}
      {confirmation && preview.ready && (
        <div className="plan-confirm">
          <label className="field-inline">
            <span id={`${ids}-confirm`}>
              {before}
              <code className="confirm-text">{confirmation}</code>
              {after}
            </span>
            <input
              className="input mono"
              name="confirm"
              value={typed}
              autoComplete="off"
              spellCheck={false}
              onChange={(e) => onTyped(e.currentTarget.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && typed === confirmation) {
                  e.preventDefault();
                  onRun();
                }
              }}
            />
          </label>
        </div>
      )}
    </>
  );
}

function transferText(lang: Lang, t: Translate, item: ExportItemRun): string {
  const p = item.transfer;
  if (!p) {
    return "";
  }
  return t("export.progress.bytes", {
    done: formatBytes(p.done, lang),
    total: formatBytes(p.total, lang),
  });
}

/** Step 5: each database with its steps, its native task and its download. */
export function ProgressStep({
  items,
  running,
  jobId,
}: {
  items: ExportItemRun[];
  running: boolean;
  jobId: string | null;
}) {
  const { t, lang } = useI18n();
  return (
    <>
      <p className="lead">{t("export.progress.lead")}</p>
      {items.length === 0 && running && (
        <p className="plan-loading">
          <LoaderCircle size={16} className="spin tone-info" aria-hidden="true" />
          {t("export.progress.starting")}
        </p>
      )}
      <ol className="export-items">
        {items.map((item) => (
          <li key={item.database} className="export-item">
            <div className="export-preview-head">
              <h3 className="mono">{item.database}</h3>
              <span className={`badge ${STATE_BADGE[item.state]}`}>
                {t(`export.state.${item.state}`)}
              </span>
            </div>
            {item.steps.length > 0 && (
              <StepList
                steps={item.steps}
                label={(s) => stepLabel(t, s, item.database, item.key)}
              />
            )}
            {item.task && <TaskProgress task={item.task} />}
            {item.step === "download" && item.transfer && item.state === "running" && (
              <>
                <div
                  className="progress"
                  role="progressbar"
                  aria-label={t("export.progress.download", { database: item.database })}
                  aria-valuemin={0}
                  aria-valuemax={item.transfer.total || 1}
                  aria-valuenow={item.transfer.done}
                  aria-valuetext={transferText(lang, t, item)}
                >
                  <div
                    className="progress-bar"
                    style={{
                      width: `${item.transfer.total ? (100 * item.transfer.done) / item.transfer.total : 0}%`,
                    }}
                  />
                </div>
                <p className="hint">{transferText(lang, t, item)}</p>
              </>
            )}
            {item.state === "failed" && item.error !== undefined && (
              <ErrorBanner error={item.error} />
            )}
          </li>
        ))}
      </ol>
      {running && (
        <p className="hint">
          {t(jobId ? "export.progress.stopHintJob" : "export.progress.stopHint")}
        </p>
      )}
    </>
  );
}

/** Step 6: where each backup went and how it was checked. */
export function ResultStep({
  items,
  target,
  jobId,
  stopped,
  error,
}: {
  items: ExportItemRun[];
  target: string;
  jobId: string | null;
  /** The page stopped following before the end (a job goes on in the engine). */
  stopped: boolean;
  error: unknown;
}) {
  const { t, lang } = useI18n();
  const count = tally(items);
  const ok = count.failed === 0 && count.cancelled === 0 && !stopped && error === null;
  return (
    <>
      {error !== null && <ErrorBanner error={error} />}
      {stopped && jobId ? (
        <div className="banner banner-warn">
          <span className="banner-text">
            {t("export.result.following")} <a href="#activity">{t("export.result.activity")}</a>
          </span>
        </div>
      ) : (
        <div className={ok ? "banner banner-ok" : "banner banner-warn"}>
          <span className="banner-text">{t("export.result.summary", count)}</span>
        </div>
      )}
      <ul className="export-items">
        {items.map((item) => {
          const local = item.download?.path ?? item.localFile;
          return (
            <li key={item.database} className="export-item">
              <div className="export-preview-head">
                <h3 className="mono">{item.database}</h3>
                <span className={`badge ${STATE_BADGE[item.state]}`}>
                  {t(`export.state.${item.state}`)}
                </span>
              </div>
              <dl className="facts">
                {item.s3Uri && (
                  <Fact label={t("export.result.s3")}>
                    <code className="export-code">{item.s3Uri}</code>
                  </Fact>
                )}
                {local && (
                  <Fact label={t("export.result.local")}>
                    <code className="export-code">{local}</code>
                  </Fact>
                )}
                {item.download && (
                  <>
                    <Fact label={t("export.result.size")}>
                      {formatBytes(item.download.size, lang)}
                    </Fact>
                    <Fact label={t("export.result.checksum")}>
                      {item.download.checksum ? (
                        <>
                          {/* In full: it is what someone compares the file with. */}
                          <span className="mono export-code">
                            {item.download.checksumAlgorithm} {item.download.checksum}
                          </span>{" "}
                          {item.download.verified ? (
                            <span className="badge badge-ok">{t("export.result.verified")}</span>
                          ) : (
                            <span className="badge badge-warning">
                              {t("export.result.notVerified")}
                            </span>
                          )}
                        </>
                      ) : (
                        <span className="muted">
                          {item.download.note || t("export.result.noChecksum")}
                        </span>
                      )}
                      {item.download.checksum && !item.download.verified && item.download.note && (
                        <span className="hint"> {item.download.note}</span>
                      )}
                    </Fact>
                  </>
                )}
                {!item.download && item.note && (
                  <Fact label={t("export.result.download")}>{item.note}</Fact>
                )}
              </dl>
              {item.state === "failed" && item.error !== undefined && (
                <ErrorBanner error={item.error} />
              )}
              {(item.state === "cancelled" || item.taskLeftRunning) && (
                <p className="hint">
                  {item.task
                    ? t("export.result.leftRunning", { id: item.task.id, target })
                    : t("export.result.notRun")}
                </p>
              )}
            </li>
          );
        })}
      </ul>
    </>
  );
}
