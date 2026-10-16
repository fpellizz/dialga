import { ArrowLeft, Database, Play, RefreshCw, Square } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, errorText, type TargetSummary } from "./api";
import { databasesHref, formatBytes } from "./databases";
import type { EngineInfo } from "./engine";
import {
  type Job,
  type JobItemPlan,
  type JobPlan,
  NoJobsError,
  previewJob,
  startJob,
} from "./exportJob";
import { type ExportItemRun, type ItemState, tally } from "./exportRun";
import { ErrorBanner } from "./Feedback";
import { type MessageKey, useI18n } from "./i18n";
import {
  baseName,
  type Destination,
  formatDuration,
  importItems,
  importReady,
  orphanFixSql,
  percentOf,
  startItems,
} from "./importWizard";
import type { InspectResult } from "./inspect";
import { PlanDialog } from "./PlanDialog";
import { PlanPreviewView } from "./PlanPreview";
import { type DatabaseReport, getDatabaseReport, type PlanPreview as Preview } from "./plans";
import { ReplaceOutcome } from "./ReplaceOutcome";
import { StepList } from "./StepList";
import { TaskProgress } from "./TaskProgress";

/** The execution of an import: one engine job, followed to its end. */
export interface RunState {
  phase: "idle" | "running" | "done" | "failed" | "stopped";
  jobId?: string;
  /** The databases as the last snapshot of the job shows them. */
  items: ExportItemRun[];
  /** Following the job broke (not the job itself): the error, for its hint. */
  error?: unknown;
  /** Stops following the job: it goes on in the engine. */
  abort?: () => void;
}

const STATE_BADGE: Record<ItemState, string> = {
  queued: "badge-skipped",
  running: "badge-running",
  succeeded: "badge-ok",
  failed: "badge-failed",
  cancelled: "badge-skipped",
};

/** The job route of the import: the kind, and the S3 folder of the uploads. */
function jobOptions(folder: string) {
  return { kind: "import" as const, prefix: folder };
}

interface PreviewState {
  loading: boolean;
  plan: JobPlan | null;
  error: unknown;
}

/** A job item as the shared plan view takes it: the checks and the exact statements. */
function itemPreview(plan: JobPlan, item: JobItemPlan): Preview {
  return {
    plan: {
      operation: "restore",
      target: plan.target,
      instance: plan.instance,
      protected: plan.protected,
      subject: item.database,
      actions: item.actions.map((a) => ({ kind: "sql", risk: "change", ...a })),
    },
    checks: item.checks,
    ready: item.ready,
    digest: plan.digest,
    preview: "",
    replace: item.replace,
    request: { database: item.database, key: item.key },
  };
}

/** The hint under the confirmation field: the instance, a database to replace, or the upload. */
function confirmHint(
  confirmation: string,
  target: string,
  destinations: readonly Destination[],
): MessageKey {
  if (confirmation === target) {
    return "import.confirmHint";
  }
  return destinations.some((d) => d.replace && d.database === confirmation)
    ? "import.confirmDatabase"
    : "import.confirmReplace";
}

/** 4. What the import will do: the job of every backup, previewed in one call. */
export function ImportPreview({
  engine,
  target,
  result,
  folder,
  destinations,
  onBack,
  onStarted,
}: {
  engine: EngineInfo;
  target: TargetSummary;
  result: InspectResult;
  folder: string;
  destinations: Destination[];
  onBack: () => void;
  /** The job started: follow it. */
  onStarted: (job: Job, plan: JobPlan) => void;
}) {
  const { t, lang } = useI18n();
  const [overwrite, setOverwrite] = useState(false);
  const [state, setState] = useState<PreviewState>({ loading: true, plan: null, error: null });
  const [typed, setTyped] = useState("");
  const [starting, setStarting] = useState(false);
  const [startError, setStartError] = useState<unknown>(null);

  const load = useCallback(
    (signal: AbortSignal) => {
      setState({ loading: true, plan: null, error: null });
      const items = importItems(result, destinations, overwrite);
      previewJob(engine, target.name, items, signal, jobOptions(folder)).then(
        (plan) => !signal.aborted && setState({ loading: false, plan, error: null }),
        (e: unknown) =>
          !signal.aborted &&
          setState({
            loading: false,
            plan: null,
            error: e instanceof NoJobsError ? t("import.jobsMissing") : e,
          }),
      );
    },
    [engine, target.name, result, destinations, folder, overwrite, t],
  );

  const inFlight = useRef<AbortController | null>(null);
  const reload = useCallback(() => {
    inFlight.current?.abort();
    const controller = new AbortController();
    inFlight.current = controller;
    load(controller.signal);
  }, [load]);
  useEffect(() => {
    reload();
    return () => inFlight.current?.abort();
  }, [reload]);

  const { plan } = state;
  const confirmation = plan?.confirmation ?? "";
  const ready = !state.loading && importReady(plan) && (!confirmation || typed === confirmation);

  const start = async () => {
    if (!plan || !ready || starting) {
      return;
    }
    setStarting(true);
    setStartError(null);
    try {
      const job = await startJob(
        engine,
        target.name,
        startItems(importItems(result, destinations, overwrite), plan),
        plan,
        confirmation ? typed : undefined,
        jobOptions(folder),
      );
      onStarted(job, plan);
    } catch (e) {
      setStartError(e);
      // 412: a check fails now or the plan changed since the preview: preview it again.
      if (e instanceof ApiError && e.status === 412) {
        reload();
      }
    } finally {
      setStarting(false);
    }
  };

  const uploads = plan?.items.flatMap((it) => it.uploads ?? []) ?? [];
  const rejected = plan?.items.flatMap((it) => it.rejected ?? []) ?? [];

  return (
    <div className="wizard-body" aria-busy={state.loading}>
      <p className="muted">{t("import.previewLead")}</p>
      {state.loading && (
        <div aria-hidden="true">
          <div className="skeleton skeleton-row" />
          <div className="skeleton skeleton-row" />
        </div>
      )}
      {state.error !== null && <ErrorBanner error={state.error} />}
      {startError !== null && <ErrorBanner error={startError} />}

      {plan && (
        <section className="plan" aria-labelledby="import-upload-title">
          <h3 id="import-upload-title">{t("import.uploadTitle", { bucket: target.bucket })}</h3>
          <ul className="item-list">
            {uploads.map((item) => (
              <li key={item.path} className="item">
                <div className="item-row">
                  <span className="item-title mono">{baseName(item.path)}</span>
                  {item.replaces && (
                    <span className="badge badge-warning">{t("import.replaces")}</span>
                  )}
                  <span className="muted">{formatBytes(item.size, lang)}</span>
                </div>
                <div className="mono muted item-detail">
                  s3://{item.bucket}/{item.key} · {t("import.parts", { parts: item.parts })}
                </div>
              </li>
            ))}
          </ul>
          {rejected.map((r) => (
            <p key={`${r.path ?? ""}${r.key ?? ""}`} className="field-error">
              {r.path ? `${baseName(r.path)}: ` : ""}
              {r.reason}
            </p>
          ))}
          {(plan.warnings ?? []).map((w) => (
            <p key={w} className="hint">
              {w}
            </p>
          ))}
          {(overwrite || rejected.length > 0) && (
            <div className="check">
              <input
                id="import-overwrite"
                type="checkbox"
                checked={overwrite}
                onChange={(e) => setOverwrite(e.target.checked)}
                aria-describedby="import-overwrite-hint"
              />
              <div>
                <label htmlFor="import-overwrite">{t("import.overwrite")}</label>
                <p id="import-overwrite-hint" className="hint">
                  {t("import.overwriteHint")}
                </p>
              </div>
            </div>
          )}
        </section>
      )}

      {plan?.items.map((item) => (
        <section
          key={item.database}
          className="plan-section"
          aria-label={t("import.restoreTitle", { db: item.database })}
        >
          <PlanPreviewView
            preview={itemPreview(plan, item)}
            title={t("import.restoreTitle", { db: item.database })}
            // One confirmation for the whole job, below.
            showConfirm={false}
            autoFocus={false}
          />
          {!item.ready && <p className="field-error">{t("import.restoreBlocked")}</p>}
          {!item.ready &&
            !destinations.find((d) => d.database === item.database)?.replace &&
            item.checks.some((c) => c.step === "database" && c.status === "failed") && (
              <p className="hint">{t("replace.exists", { db: item.database })}</p>
            )}
        </section>
      ))}

      {plan && confirmation && (
        <div className="form-field confirm-import">
          <label htmlFor="import-confirm">{t("import.confirmLabel", { name: confirmation })}</label>
          <input
            id="import-confirm"
            className="input mono"
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            aria-describedby="import-confirm-hint"
          />
          <p id="import-confirm-hint" className="hint">
            {/* The target name: a protected instance. Anything else: objects the upload replaces. */}
            {t(confirmHint(confirmation, target.name, destinations))}
          </p>
        </div>
      )}

      <div className="wizard-nav">
        <button type="button" className="btn btn-outline" onClick={onBack}>
          <ArrowLeft size={16} aria-hidden="true" />
          {t("import.back")}
        </button>
        <button
          type="button"
          className="btn btn-outline"
          onClick={() => {
            setStartError(null);
            reload();
          }}
          disabled={state.loading}
        >
          <RefreshCw size={16} aria-hidden="true" />
          {t("import.previewAgain")}
        </button>
        <button
          type="button"
          className="btn btn-primary"
          onClick={start}
          disabled={!ready}
          aria-disabled={starting || undefined}
        >
          <Play size={16} aria-hidden="true" />
          {t("import.start")}
        </button>
      </div>
    </div>
  );
}

/** The upload of a database: bytes, speed and time left. */
function UploadProgress({ item }: { item: ExportItemRun }) {
  const { t, lang } = useI18n();
  const transfer = item.transfer;
  if (!transfer) {
    return null;
  }
  const percent = percentOf(transfer.done, transfer.total);
  let meta = t("import.sent", {
    done: formatBytes(transfer.done, lang),
    total: formatBytes(transfer.total, lang),
  });
  if (transfer.bytesPerSecond > 0) {
    meta += ` · ${t("import.rate", { rate: formatBytes(transfer.bytesPerSecond, lang) })}`;
    if (transfer.etaSeconds !== undefined) {
      meta += ` · ${t("import.eta", { time: formatDuration(transfer.etaSeconds) })}`;
    }
  }
  return (
    <div className="task">
      <div className="task-row">
        <span className="muted">{t("import.uploadStep")}</span>
        <span className="muted">{percent}%</span>
      </div>
      <div
        className="progress"
        role="progressbar"
        aria-label={t("import.uploading", { name: item.database })}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent}
      >
        <div className="progress-bar" style={{ width: `${percent}%` }} />
      </div>
      <div className="task-meta muted">{meta}</div>
    </div>
  );
}

/** 5. Each database of the job: its steps, the upload and the native task. */
export function ImportProgress({
  run,
  onCancel,
}: {
  run: RunState;
  /** Asks the engine to stop the job. */
  onCancel: () => void;
}) {
  const { t } = useI18n();
  const running = run.phase === "running";
  return (
    <div className="wizard-body" aria-busy={running}>
      {run.items.length === 0 && <p className="muted">{t("import.starting")}</p>}
      <ul className="task-list">
        {run.items.map((item) => (
          <li key={item.database} className="export-item">
            <div className="export-preview-head">
              <h3 className="mono">{t("import.restoreTitle", { db: item.database })}</h3>
              <span className={`badge ${STATE_BADGE[item.state]}`}>
                {t(`export.state.${item.state}`)}
              </span>
            </div>
            {item.steps.length > 0 && <StepList steps={item.steps} />}
            {item.transfer && item.state === "running" && !item.task && (
              <UploadProgress item={item} />
            )}
            {item.task && <TaskProgress task={item.task} />}
          </li>
        ))}
      </ul>
      {running && <p className="hint">{t("import.stopHint")}</p>}
      <div className="wizard-nav">
        {running && (
          <>
            <button type="button" className="btn btn-outline" onClick={() => run.abort?.()}>
              <Square size={16} aria-hidden="true" />
              {t("import.stopFollowing")}
            </button>
            <button type="button" className="btn btn-outline" onClick={onCancel}>
              {t("import.cancel")}
            </button>
          </>
        )}
      </div>
    </div>
  );
}

/** The post report of a restored database, or why it is not online yet. */
function DatabaseResult({
  engine,
  target,
  bucket,
  destination,
  item,
  onDropOld,
}: {
  engine: EngineInfo;
  target: string;
  bucket: string;
  destination: Destination;
  item?: ExportItemRun;
  onDropOld: (database: string) => void;
}) {
  const { t } = useI18n();
  const [report, setReport] = useState<DatabaseReport | null>(null);
  const [error, setError] = useState("");
  const asked = useRef(false);

  useEffect(() => {
    if (destination.noRecovery || asked.current) {
      return;
    }
    asked.current = true;
    getDatabaseReport(engine, target, destination.database).then(setReport, (e: unknown) =>
      setError(errorText(e)),
    );
  }, [engine, target, destination]);

  const title = t("import.resultTitle", { db: destination.database });
  if (destination.noRecovery) {
    return (
      <section className="plan" aria-label={title}>
        <h3>{title}</h3>
        <p className="hint">{t("import.leftRestoring")}</p>
      </section>
    );
  }
  return (
    <section className="plan" aria-label={title} aria-busy={!report && !error}>
      <h3>{title}</h3>
      {destination.replace && (
        <ReplaceOutcome
          database={destination.database}
          mode={destination.replace}
          texts={[item?.result, ...(item?.steps ?? []).flatMap((s) => [s.label, s.detail])]}
          bucket={bucket}
          structured={item?.replace}
          onDropOld={onDropOld}
        />
      )}
      {!report && !error && <div className="skeleton skeleton-row" aria-hidden="true" />}
      {error && (
        <div className="banner banner-fail" role="alert">
          <span className="banner-text">{error}</span>
        </div>
      )}
      {report && (
        <>
          <div className={report.ok ? "banner banner-ok" : "banner banner-fail"} role="status">
            {report.ok ? t("import.reportOk") : t("import.reportFailed")}
          </div>
          <StepList steps={report.findings} />
          {report.orphanUsers.length > 0 && (
            <div className="orphans">
              <strong>{t("import.orphans", { count: report.orphanUsers.length })}</strong>
              <ul className="orphan-list">
                {report.orphanUsers.map((u) => (
                  <li key={u} className="mono">
                    {u}
                  </li>
                ))}
              </ul>
              <p className="hint">{t("import.orphansFix")}</p>
              <pre className="task-info">
                {orphanFixSql(destination.database, report.orphanUsers)}
              </pre>
            </div>
          )}
          {report.objects.length > 0 && (
            <p className="muted">{report.objects.map((o) => `${o.type} ${o.count}`).join(" · ")}</p>
          )}
        </>
      )}
    </section>
  );
}

/** 6. What each database came to: its post-restore report, or why it did not. */
export function ImportResult({
  engine,
  target,
  destinations,
  run,
  onAgain,
}: {
  engine: EngineInfo;
  target: TargetSummary;
  destinations: Destination[];
  run: RunState;
  onAgain: () => void;
}) {
  const { t } = useI18n();
  const count = tally(run.items);
  // The old database a swap kept, whose drop plan is open.
  const [dropping, setDropping] = useState<string | null>(null);
  const stopped = run.phase === "stopped";
  const ok = run.phase === "done";
  return (
    <div className="wizard-body">
      {run.error !== undefined && <ErrorBanner error={run.error} />}
      {stopped ? (
        <div className="banner banner-warn" role="status">
          <span className="banner-text">{t("import.stoppedFollowing")}</span>
        </div>
      ) : (
        <div className={ok ? "banner banner-ok" : "banner banner-warn"} role="status">
          <span className="banner-text">
            {ok
              ? t("import.done", { count: destinations.length })
              : t("import.result.summary", count)}
          </span>
        </div>
      )}
      {destinations.map((d) => {
        const item = run.items.find((i) => i.database === d.database);
        if (item?.state === "succeeded") {
          return (
            <DatabaseResult
              key={d.database}
              engine={engine}
              target={target.name}
              bucket={target.bucket}
              destination={d}
              item={item}
              onDropOld={setDropping}
            />
          );
        }
        return (
          <section
            key={d.database}
            className="plan"
            aria-label={t("import.resultTitle", { db: d.database })}
          >
            <div className="export-preview-head">
              <h3>{t("import.resultTitle", { db: d.database })}</h3>
              {item && (
                <span className={`badge ${STATE_BADGE[item.state]}`}>
                  {t(`export.state.${item.state}`)}
                </span>
              )}
            </div>
            {item && item.steps.length > 0 && <StepList steps={item.steps} />}
            {item?.error !== undefined && <ErrorBanner error={item.error} />}
          </section>
        );
      })}
      <div className="wizard-nav">
        {run.jobId && (
          <a className="btn btn-outline" href={`#activity/${encodeURIComponent(run.jobId)}`}>
            {t("import.activity")}
          </a>
        )}
        <a className="btn btn-outline" href={databasesHref(target.name)}>
          <Database size={16} aria-hidden="true" />
          {t("import.openDatabases")}
        </a>
        <button type="button" className="btn btn-primary" onClick={onAgain}>
          {t("import.again")}
        </button>
      </div>
      {dropping && (
        <PlanDialog
          engine={engine}
          target={target.name}
          operation="drop"
          request={{ database: dropping }}
          onClose={() => setDropping(null)}
        />
      )}
    </div>
  );
}
