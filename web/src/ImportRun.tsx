import { ArrowLeft, Database, LoaderCircle, Play, RefreshCw, Square } from "lucide-react";
import {
  type Dispatch,
  type SetStateAction,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import { ApiError, errorText, type StepEvent, type TargetSummary, type Task } from "./api";
import { databasesHref, formatBytes } from "./databases";
import type { EngineInfo } from "./engine";
import { useI18n } from "./i18n";
import {
  baseName,
  blockingChecks,
  confirmations,
  type Destination,
  formatDuration,
  orphanFixSql,
  percentOf,
  previewReady,
  restoreRequests,
  SOURCE_CHECK,
  uploadRequest,
} from "./importWizard";
import type { InspectResult } from "./inspect";
import { PlanPreviewView } from "./PlanPreview";
import { PlanRunView } from "./PlanRun";
import {
  type DatabaseReport,
  executePlan,
  getDatabaseReport,
  type PlanDone,
  type PlanRequest,
  type PlanPreview as Preview,
  previewPlan,
} from "./plans";
import { StepList } from "./StepList";
import {
  planUploads,
  runUploads,
  type UploadBatch,
  type UploadDone,
  type UploadItem,
  type UploadProgress,
} from "./uploads";

/** The restore of one backup while and after it runs. */
export interface RestoreRun {
  database: string;
  notices: string[];
  steps: StepEvent[];
  task: Task | null;
  done: PlanDone | null;
  /** The call failed (refused, or the stream broke): the error itself, for its hint. */
  error?: unknown;
  /** The previewed request (labels and outcome of the run view). */
  request: PlanRequest;
}

/** The execution of an import: the upload, then the restores in order. */
export interface RunState {
  phase: "idle" | "upload" | "restore" | "done" | "failed" | "stopped";
  items?: UploadItem[];
  uploadSteps: StepEvent[];
  /** The last progress of each file, by path. */
  progress: Record<string, UploadProgress>;
  uploadDone: UploadDone | null;
  uploadError: string;
  restores: RestoreRun[];
  /** A restore answered 412: the plan changed or a check fails now. */
  planChanged?: boolean;
  /** Stops the upload, or stops following a restore (the task goes on). */
  abort?: () => void;
}

export type RunSetter = Dispatch<SetStateAction<RunState>>;

/** The engine has no upload endpoint yet (mux 404, not an unknown target). */
function uploadMissing(e: unknown): boolean {
  return e instanceof ApiError && e.status === 404 && e.message === "404 page not found";
}

function upsert(steps: StepEvent[], event: StepEvent): StepEvent[] {
  const index = steps.findIndex((s) => s.step === event.step);
  return index === -1 ? [...steps, event] : steps.map((s, i) => (i === index ? event : s));
}

interface PreviewState {
  loading: boolean;
  upload: UploadBatch | null;
  uploadError: string;
  restores: (Preview | null)[];
  restoreErrors: string[];
}

/** 4. What the import will do: the upload plan and each restore plan. */
export function ImportPreview({
  engine,
  target,
  result,
  folder,
  destinations,
  onBack,
  onStart,
}: {
  engine: EngineInfo;
  target: TargetSummary;
  result: InspectResult;
  folder: string;
  destinations: Destination[];
  onBack: () => void;
  /** Gets the import to run: it reports into setRun and resolves true when all went well. */
  onStart: (start: (setRun: RunSetter) => Promise<boolean>) => void;
}) {
  const { t, lang } = useI18n();
  const [overwrite, setOverwrite] = useState(false);
  const [state, setState] = useState<PreviewState>({
    loading: true,
    upload: null,
    uploadError: "",
    restores: [],
    restoreErrors: [],
  });
  const [typed, setTyped] = useState<Record<string, string>>({});

  const load = useCallback(
    (signal: AbortSignal) => {
      setState({ loading: true, upload: null, uploadError: "", restores: [], restoreErrors: [] });
      const requests = restoreRequests(result, destinations, folder);
      const upload = planUploads(
        engine,
        target.name,
        uploadRequest(result, folder, overwrite),
        signal,
      ).then(
        (batch) => ({ batch, error: "" }),
        (e: unknown) => ({
          batch: null,
          error: uploadMissing(e) ? t("import.uploadMissing") : errorText(e),
        }),
      );
      const restores = Promise.all(
        requests.map((request) =>
          previewPlan(engine, target.name, "restore", request, signal).then(
            (preview) => ({ preview, error: "" }),
            (e: unknown) => ({ preview: null, error: errorText(e) }),
          ),
        ),
      );
      Promise.all([upload, restores]).then(([u, r]) => {
        if (!signal.aborted) {
          setState({
            loading: false,
            upload: u.batch,
            uploadError: u.error,
            restores: r.map((x) => x.preview),
            restoreErrors: r.map((x) => x.error),
          });
        }
      });
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

  const previews = state.restores.filter((p): p is Preview => p !== null);
  const complete = previews.length === destinations.length && state.restoreErrors.every((e) => !e);
  const needed = confirmations(state.upload, previews);
  const confirmed = needed.every((c) => typed[c] === c);
  const ready = !state.loading && complete && previewReady(state.upload, previews) && confirmed;

  const start = () =>
    onStart(async (setRun) => {
      const upload = state.upload as UploadBatch;
      const controller = new AbortController();
      setRun({
        phase: "upload",
        items: upload.items,
        uploadSteps: [],
        progress: {},
        uploadDone: null,
        uploadError: "",
        restores: destinations.map((d, i) => ({
          database: d.database,
          notices: [],
          steps: [],
          task: null,
          done: null,
          request: state.restores[i]?.request ?? { database: d.database },
        })),
        abort: () => controller.abort(),
      });
      const set = setRun;
      try {
        const done = await runUploads(
          engine,
          target.name,
          {
            ...uploadRequest(result, folder, overwrite),
            confirm: upload.confirmation || undefined,
          },
          {
            onStep: (e) => set((r) => ({ ...r, uploadSteps: upsert(r.uploadSteps, e) })),
            onProgress: (p) => set((r) => ({ ...r, progress: { ...r.progress, [p.path]: p } })),
          },
          controller.signal,
        );
        set((r) => ({ ...r, uploadDone: done, phase: done.ok ? "restore" : "failed" }));
        if (!done.ok) {
          return false;
        }
      } catch (e) {
        const stopped = controller.signal.aborted;
        set((r) => ({
          ...r,
          phase: stopped ? "stopped" : "failed",
          uploadError: stopped ? "" : errorText(e),
        }));
        return false;
      }
      for (const [i, preview] of previews.entries()) {
        const change = (update: (x: RestoreRun) => RestoreRun) =>
          set((r) => ({ ...r, restores: r.restores.map((x, j) => (j === i ? update(x) : x)) }));
        try {
          const done = await executePlan(
            engine,
            target.name,
            "restore",
            {
              ...preview.request,
              confirm: preview.confirmation || undefined,
              digest: preview.digest,
            },
            {
              onNotice: (n) => change((x) => ({ ...x, notices: [...x.notices, n] })),
              onStep: (e) => change((x) => ({ ...x, steps: upsert(x.steps, e) })),
              onTask: (task) => change((x) => ({ ...x, task })),
            },
            controller.signal,
          );
          change((x) => ({ ...x, done, task: done.task ?? x.task }));
          if (!done.ok) {
            set((r) => ({ ...r, phase: "failed" }));
            return false;
          }
        } catch (e) {
          const stopped = controller.signal.aborted;
          change((x) => ({ ...x, error: stopped ? undefined : e }));
          set((r) => ({
            ...r,
            phase: stopped ? "stopped" : "failed",
            planChanged: e instanceof ApiError && e.status === 412,
          }));
          return false;
        }
      }
      set((r) => ({ ...r, phase: "done", abort: undefined }));
      return true;
    });

  return (
    <div className="wizard-body" aria-busy={state.loading}>
      <p className="muted">{t("import.previewLead")}</p>
      {state.loading && (
        <div aria-hidden="true">
          <div className="skeleton skeleton-row" />
          <div className="skeleton skeleton-row" />
        </div>
      )}

      {!state.loading && (
        <section className="plan" aria-labelledby="import-upload-title">
          <h3 id="import-upload-title">{t("import.uploadTitle", { bucket: target.bucket })}</h3>
          {state.uploadError && (
            <div className="banner banner-fail" role="alert">
              <span className="banner-text">{state.uploadError}</span>
            </div>
          )}
          {state.upload && (
            <>
              <ul className="item-list">
                {state.upload.items.map((item) => (
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
              {state.upload.rejected.map((r) => (
                <p key={`${r.path ?? ""}${r.key ?? ""}`} className="field-error">
                  {r.path ? `${baseName(r.path)}: ` : ""}
                  {r.reason}
                </p>
              ))}
              {(state.upload.warnings ?? []).map((w) => (
                <p key={w} className="hint">
                  {w}
                </p>
              ))}
            </>
          )}
          {(overwrite || (state.upload?.rejected.length ?? 0) > 0) && (
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

      {!state.loading &&
        destinations.map((d, i) => {
          const preview = state.restores[i];
          const error = state.restoreErrors[i];
          return (
            <section
              key={d.database}
              className="plan-section"
              aria-label={t("import.restoreTitle", { db: d.database })}
            >
              {error && (
                <>
                  <h3>{t("import.restoreTitle", { db: d.database })}</h3>
                  <div className="banner banner-fail" role="alert">
                    <span className="banner-text">{error}</span>
                  </div>
                </>
              )}
              {preview && (
                <PlanPreviewView
                  preview={preview}
                  title={t("import.restoreTitle", { db: d.database })}
                  pending={{ [SOURCE_CHECK]: t("import.sourcePending") }}
                  // One confirmation for the upload and every restore, below.
                  showConfirm={false}
                  autoFocus={false}
                />
              )}
              {preview && blockingChecks(preview).length > 0 && (
                <p className="field-error">{t("import.restoreBlocked")}</p>
              )}
            </section>
          );
        })}

      {!state.loading &&
        needed.map((c) => (
          <div key={c} className="form-field confirm-import">
            <label htmlFor={`import-confirm-${c}`}>{t("import.confirmLabel", { name: c })}</label>
            <input
              id={`import-confirm-${c}`}
              className="input mono"
              value={typed[c] ?? ""}
              onChange={(e) => setTyped((x) => ({ ...x, [c]: e.target.value }))}
              autoComplete="off"
              spellCheck={false}
              aria-describedby={`import-confirm-${c}-hint`}
            />
            <p id={`import-confirm-${c}-hint`} className="hint">
              {/* The target name: a protected instance. Anything else: objects the upload replaces. */}
              {t(c === target.name ? "import.confirmHint" : "import.confirmReplace")}
            </p>
          </div>
        ))}

      <div className="wizard-nav">
        <button type="button" className="btn btn-outline" onClick={onBack}>
          <ArrowLeft size={16} aria-hidden="true" />
          {t("import.back")}
        </button>
        <button type="button" className="btn btn-outline" onClick={reload} disabled={state.loading}>
          <RefreshCw size={16} aria-hidden="true" />
          {t("import.previewAgain")}
        </button>
        <button type="button" className="btn btn-primary" onClick={start} disabled={!ready}>
          <Play size={16} aria-hidden="true" />
          {t("import.start")}
        </button>
      </div>
    </div>
  );
}

/** One file of the upload, with bytes, speed and time left. */
function FileProgress({
  item,
  progress,
  done,
}: {
  item: UploadItem;
  progress?: UploadProgress;
  done: boolean;
}) {
  const { t, lang } = useI18n();
  const sent = done ? item.size : (progress?.done ?? 0);
  const total = progress?.total || item.size;
  const percent = percentOf(sent, total);
  let meta = t("import.sent", { done: formatBytes(sent, lang), total: formatBytes(total, lang) });
  if (!done && progress && progress.bytesPerSecond > 0) {
    meta += ` · ${t("import.rate", { rate: formatBytes(progress.bytesPerSecond, lang) })}`;
    if (progress.etaSeconds !== undefined) {
      meta += ` · ${t("import.eta", { time: formatDuration(progress.etaSeconds) })}`;
    }
  }
  return (
    <li className="task">
      <div className="task-row">
        <span className="task-db mono">{baseName(item.path)}</span>
        <span className="muted">{percent}%</span>
      </div>
      <div
        className="progress"
        role="progressbar"
        aria-label={t("import.uploading", { name: baseName(item.path) })}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent}
      >
        <div className="progress-bar" style={{ width: `${percent}%` }} />
      </div>
      <div className="task-meta muted">{meta}</div>
    </li>
  );
}

/** 5. The upload, then each restore with its native task. */
export function ImportProgress({
  target,
  run,
  onBackToPreview,
  onResult,
}: {
  target: TargetSummary;
  run: RunState;
  onBackToPreview: () => void;
  onResult: () => void;
}) {
  const { t } = useI18n();
  const uploaded = run.uploadDone?.ok === true;
  return (
    <div className="wizard-body" aria-busy={run.phase === "upload" || run.phase === "restore"}>
      <section className="plan" aria-labelledby="import-run-upload">
        <h3 id="import-run-upload">{t("import.uploadTitle", { bucket: target.bucket })}</h3>
        <ul className="task-list">
          {(run.items ?? []).map((item) => (
            <FileProgress
              key={item.path}
              item={item}
              progress={run.progress[item.path]}
              done={uploaded}
            />
          ))}
        </ul>
        {run.uploadSteps.length > 0 && <StepList steps={run.uploadSteps} />}
        {run.uploadError && (
          <div className="banner banner-fail" role="alert">
            <span className="banner-text">{run.uploadError}</span>
          </div>
        )}
        {run.uploadDone && !run.uploadDone.ok && (
          <div className="banner banner-fail" role="alert">
            <span className="banner-text">{run.uploadDone.error || t("import.uploadFailed")}</span>
          </div>
        )}
        {uploaded && (
          <p className="tone-success" role="status">
            {t("import.uploaded")}
          </p>
        )}
      </section>

      {run.restores.map((r, i) => (
        <section
          key={r.database}
          className="plan"
          aria-label={t("import.restoreTitle", { db: r.database })}
        >
          <h3>{t("import.restoreTitle", { db: r.database })}</h3>
          {r.steps.length === 0 && !r.done && r.error === undefined && (
            <p className="muted">
              {run.phase === "restore" && !run.restores.slice(0, i).some((x) => !x.done) ? (
                <>
                  <LoaderCircle size={16} className="spin inline-icon" aria-hidden="true" />
                  {t("import.restoreStarting")}
                </>
              ) : (
                t("import.restoreWaiting")
              )}
            </p>
          )}
          <PlanRunView
            run={{
              status: r.done
                ? r.done.ok
                  ? "done"
                  : "failed"
                : r.error !== undefined
                  ? "failed"
                  : "running",
              steps: r.steps,
              notices: r.notices,
              task: r.task ?? undefined,
              done: r.done ?? undefined,
              error: r.error,
            }}
            target={target.name}
            operation="restore"
            request={r.request}
          />
        </section>
      ))}

      {run.phase === "stopped" && (
        <div className="banner banner-warn" role="status">
          <span className="banner-text">
            {uploaded ? t("import.stoppedFollowing") : t("import.stoppedUpload")}
          </span>
        </div>
      )}
      {run.planChanged && (
        <div className="banner banner-warn" role="status">
          <span className="banner-text">{t("import.planChanged")}</span>
        </div>
      )}

      <div className="wizard-nav">
        {(run.phase === "upload" || run.phase === "restore") && (
          <button type="button" className="btn btn-outline" onClick={() => run.abort?.()}>
            <Square size={16} aria-hidden="true" />
            {run.phase === "upload" ? t("import.cancelUpload") : t("import.stopFollowing")}
          </button>
        )}
        {(run.phase === "failed" || run.phase === "stopped") && (
          <button type="button" className="btn btn-outline" onClick={onBackToPreview}>
            <ArrowLeft size={16} aria-hidden="true" />
            {t("import.backToPreview")}
          </button>
        )}
        {run.phase === "done" && (
          <button type="button" className="btn btn-primary" onClick={onResult}>
            {t("import.showResult")}
          </button>
        )}
      </div>
    </div>
  );
}

/** The post report of a restored database, or why it is not online yet. */
function DatabaseResult({
  engine,
  target,
  destination,
}: {
  engine: EngineInfo;
  target: string;
  destination: Destination;
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

/** 6. The post-restore report of each database, and where to go next. */
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
  return (
    <div className="wizard-body">
      {run.phase === "done" && (
        <div className="banner banner-ok" role="status">
          {t("import.done", { count: destinations.length })}
        </div>
      )}
      {destinations.map((d) => (
        <DatabaseResult key={d.database} engine={engine} target={target.name} destination={d} />
      ))}
      <div className="wizard-nav">
        <a className="btn btn-outline" href={databasesHref(target.name)}>
          <Database size={16} aria-hidden="true" />
          {t("import.openDatabases")}
        </a>
        <button type="button" className="btn btn-primary" onClick={onAgain}>
          {t("import.again")}
        </button>
      </div>
    </div>
  );
}
