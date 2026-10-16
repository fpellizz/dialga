import { LoaderCircle, Play, RefreshCw, ShieldAlert, TriangleAlert } from "lucide-react";
import {
  type FormEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import { errorText, isActive, type StepEvent, type Task } from "./api";
import type { EngineInfo } from "./engine";
import { messages, type Translate, useI18n } from "./i18n";
import { Modal } from "./Modal";
import {
  canExecute,
  cleanRequest,
  type ExecuteRequest,
  errorHint,
  executePlan,
  HttpError,
  isDestructive,
  type Operation,
  type PlanDone,
  type PlanPreview,
  type PlanRequest,
  planStepKey,
  previewPlan,
  s3URI,
  sameRequest,
  upsertStep,
} from "./plans";
import { StepList } from "./StepList";
import { TaskProgress } from "./TaskProgress";

/** How the dialog ended, for the page behind it. */
export interface PlanResult {
  /** An execution started: what the page shows may have changed. */
  changed: boolean;
  /** The task the dialog was following when it closed: it keeps running. */
  running?: Task;
}

/** An execution: its steps, the followed task, the outcome. */
interface Run {
  status: "running" | "done" | "failed";
  steps: StepEvent[];
  notices: string[];
  task?: Task;
  done?: PlanDone;
  /** The stream broke: the task may still run. */
  error?: unknown;
}

/**
 * The dialog of a native operation: the preview (pre-flight checks, the
 * exact T-SQL, the confirmation the plan asks for), then the execution
 * streamed from the engine with the progress of the RDS task.
 *
 * fields shows inputs before the preview: "export" the S3 key (filled with
 * the engine default), "restore" the database, the key and the options.
 * Editing them after a preview asks for a new one before running.
 */
export function PlanDialog({
  engine,
  target,
  operation,
  request,
  fields,
  onClose,
  onShowReport,
}: {
  engine: EngineInfo;
  target: string;
  operation: Operation;
  request: PlanRequest;
  fields?: "export" | "restore";
  onClose: (result: PlanResult) => void;
  /** Offered after a restore: open the post-restore report of the database. */
  onShowReport?: (database: string) => void;
}) {
  const { t, lang } = useI18n();
  const [params, setParams] = useState<PlanRequest>(request);
  const [preview, setPreview] = useState<PlanPreview | null>(null);
  // The parameters of the preview shown, as the user sees them in the fields.
  const [previewed, setPreviewed] = useState<PlanRequest | null>(null);
  const [loading, setLoading] = useState(false);
  const [previewError, setPreviewError] = useState<unknown>(null);
  const [executeError, setExecuteError] = useState<unknown>(null);
  const [changed, setChanged] = useState(false);
  const [typed, setTyped] = useState("");
  const [run, setRun] = useState<Run | null>(null);
  const [announcement, setAnnouncement] = useState("");
  const previewAbort = useRef<AbortController | null>(null);
  const runAbort = useRef<AbortController | null>(null);
  const confirmInput = useRef<HTMLInputElement>(null);
  const ids = useId();

  const doPreview = useCallback(
    async (p: PlanRequest) => {
      previewAbort.current?.abort();
      const controller = new AbortController();
      previewAbort.current = controller;
      setLoading(true);
      setPreviewError(null);
      setExecuteError(null);
      try {
        const result = await previewPlan(
          engine,
          target,
          operation,
          cleanRequest(p),
          controller.signal,
        );
        if (controller.signal.aborted) {
          return;
        }
        // An empty export key is resolved by the engine: show the default.
        const shown = p.key?.trim() || !result.request.key ? p : { ...p, key: result.request.key };
        setPreview(result);
        setParams(shown);
        setPreviewed(shown);
        setTyped("");
      } catch (e) {
        if (!controller.signal.aborted) {
          setPreview(null);
          setPreviewed(null);
          setPreviewError(e);
        }
      } finally {
        if (previewAbort.current === controller) {
          previewAbort.current = null;
          setLoading(false);
        }
      }
    },
    [engine, target, operation],
  );

  // biome-ignore lint/correctness/useExhaustiveDependencies: the first preview only, on open.
  useEffect(() => {
    if (fields !== "restore" || (request.database && request.key)) {
      void doPreview(request);
    }
    return () => {
      previewAbort.current?.abort();
      runAbort.current?.abort();
    };
  }, []);

  // The plan asks to retype something: that is what the user does next.
  useEffect(() => {
    if (preview?.confirmation) {
      confirmInput.current?.focus();
    }
  }, [preview]);

  const stale = preview !== null && previewed !== null && !sameRequest(params, previewed);
  const database = preview?.request.database ?? params.database ?? "";
  const label = (s: StepEvent) => stepLabel(t, s, preview?.request ?? params);

  async function execute() {
    if (!preview) {
      return;
    }
    const controller = new AbortController();
    runAbort.current = controller;
    setExecuteError(null);
    setChanged(false);
    setRun({ status: "running", steps: [], notices: [] });
    setAnnouncement(t("plan.running"));
    const body: ExecuteRequest = { ...preview.request, digest: preview.digest };
    if (preview.confirmation) {
      body.confirm = typed;
    }
    let lifecycle = "";
    try {
      const done = await executePlan(
        engine,
        target,
        operation,
        body,
        {
          onNotice: (message) => setRun((r) => r && { ...r, notices: [...r.notices, message] }),
          onStep: (event) => setRun((r) => r && { ...r, steps: upsertStep(r.steps, event) }),
          onTask: (task) => {
            setRun((r) => r && { ...r, task });
            // Announce the lifecycle changes only, not every percent.
            if (task.lifecycle !== lifecycle) {
              lifecycle = task.lifecycle;
              setAnnouncement(
                t("plan.announce.task", {
                  id: task.id,
                  lifecycle: t(`lifecycle.${task.lifecycle}`),
                }),
              );
            }
          },
        },
        controller.signal,
      );
      if (controller.signal.aborted) {
        return;
      }
      setRun(
        (r) => r && { ...r, status: done.ok ? "done" : "failed", done, task: done.task ?? r.task },
      );
      if (done.ok) {
        setAnnouncement(doneText(t, operation, preview, done));
      }
    } catch (e) {
      if (controller.signal.aborted) {
        return;
      }
      if (e instanceof HttpError) {
        // Refused before anything ran: back to the preview.
        setRun(null);
        if (e.status === 412) {
          setChanged(true);
          void doPreview(previewed ?? params);
        } else {
          setExecuteError(e);
        }
        return;
      }
      setRun((r) => r && { ...r, status: "failed", error: e });
    } finally {
      if (runAbort.current === controller) {
        runAbort.current = null;
      }
    }
  }

  function close() {
    const following = run?.status === "running";
    previewAbort.current?.abort();
    // Stops following the task; the engine finishes the statement and the task runs on.
    runAbort.current?.abort();
    onClose({
      changed: run !== null,
      running: following && run?.task && isActive(run.task) ? run.task : undefined,
    });
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    if (formComplete(fields, params)) {
      void doPreview(params);
    }
  }

  const title = t(`plan.title.${operation}`, {
    database: request.database ?? "",
    id: request.taskId ?? "",
  });
  const runnable = preview !== null && !loading && canExecute(preview, typed, stale);
  // Known before the preview for a drop: the button does not change colour when it arrives.
  const destructive = preview ? isDestructive(preview.plan) : operation === "drop";

  // "Cancel" next to "Cancel task" would read as the same thing.
  const dismiss = operation === "cancel" ? t("plan.close") : t("plan.cancel");
  const [confirmBefore, confirmAfter] = messages[lang]["plan.confirm.label"].split("{text}");
  let footer: ReactNode;
  if (run && run.status !== "running") {
    footer = (
      <>
        {run.status === "done" &&
          operation === "restore" &&
          !preview?.request.noRecovery &&
          onShowReport && (
            <button
              type="button"
              className="btn btn-outline"
              onClick={() => onShowReport(database)}
            >
              {t("plan.openReport")}
            </button>
          )}
        {run.status === "failed" && (
          <button
            type="button"
            className="btn btn-ghost"
            onClick={() => {
              // What failed may have changed the instance: a new preview, from the start.
              setRun(null);
              void doPreview(previewed ?? params);
            }}
          >
            <RefreshCw size={16} aria-hidden="true" />
            {t("plan.previewAgain")}
          </button>
        )}
        <button type="button" className="btn btn-primary" onClick={close}>
          {t("plan.close")}
        </button>
      </>
    );
  } else if (run) {
    footer = (
      <button type="button" className="btn btn-outline" onClick={close}>
        {t("plan.close")}
      </button>
    );
  } else {
    footer = (
      <>
        <button type="button" className="btn btn-outline" onClick={close}>
          {dismiss}
        </button>
        {(preview || previewError) && (
          <button
            type="button"
            className="btn btn-ghost"
            onClick={() => void doPreview(params)}
            disabled={loading || !formComplete(fields, params)}
          >
            <RefreshCw size={16} className={loading ? "spin" : undefined} aria-hidden="true" />
            {t("plan.previewAgain")}
          </button>
        )}
        {fields === "restore" && !preview && !previewError ? (
          <button
            type="submit"
            form={`${ids}-form`}
            className="btn btn-primary"
            disabled={loading || !formComplete(fields, params)}
          >
            {t("plan.preview")}
          </button>
        ) : (
          <button
            type="button"
            className={destructive ? "btn btn-destructive" : "btn btn-primary"}
            onClick={() => void execute()}
            disabled={!runnable}
          >
            <Play size={16} aria-hidden="true" />
            {t(`plan.run.${operation}`)}
          </button>
        )}
      </>
    );
  }

  return (
    <Modal title={title} onClose={close} busy={loading} footer={footer} className="plan-dialog">
      {fields && !run && (
        <form
          id={`${ids}-form`}
          className="plan-form"
          onSubmit={submit}
          // Enter previews: the submit button sits in the footer, outside the form.
          onKeyDown={(e) => {
            if (
              e.key === "Enter" &&
              e.target instanceof HTMLInputElement &&
              e.target.type !== "checkbox"
            ) {
              submit(e);
            }
          }}
        >
          {fields === "restore" && (
            <label className="field-inline">
              <span>{t("plan.field.database")}</span>
              <input
                className="input mono"
                name="database"
                value={params.database ?? ""}
                autoComplete="off"
                spellCheck={false}
                disabled={loading}
                onChange={(e) => {
                  const database = e.currentTarget.value;
                  setParams((p) => ({ ...p, database }));
                }}
              />
            </label>
          )}
          <div className="field-inline">
            <label className="field-inline">
              <span>{t("plan.field.key")}</span>
              <input
                className="input mono"
                name="key"
                value={params.key ?? ""}
                autoComplete="off"
                spellCheck={false}
                disabled={loading}
                aria-describedby={`${ids}-key-hint`}
                onChange={(e) => {
                  const key = e.currentTarget.value;
                  setParams((p) => ({ ...p, key }));
                }}
              />
            </label>
            <span id={`${ids}-key-hint`} className="hint">
              {t(fields === "export" ? "plan.field.keyHint.export" : "plan.field.keyHint.restore")}
            </span>
          </div>
          {fields === "restore" && (
            <div className="plan-options">
              <label className="check">
                <input
                  type="checkbox"
                  name="differential"
                  checked={!!params.differential}
                  disabled={loading}
                  onChange={(e) => {
                    const differential = e.currentTarget.checked;
                    setParams((p) => ({ ...p, differential }));
                  }}
                />
                {t("plan.field.differential")}
              </label>
              <label className="check">
                <input
                  type="checkbox"
                  name="noRecovery"
                  checked={!!params.noRecovery}
                  disabled={loading}
                  onChange={(e) => {
                    const noRecovery = e.currentTarget.checked;
                    setParams((p) => ({ ...p, noRecovery }));
                  }}
                />
                {t("plan.field.noRecovery")}
              </label>
            </div>
          )}
          {stale && (
            <div className="banner banner-warn">
              <TriangleAlert size={16} aria-hidden="true" />
              <span className="banner-text">{t("plan.stale")}</span>
            </div>
          )}
        </form>
      )}

      {changed && !run && (
        <div className="banner banner-warn" role="status">
          <TriangleAlert size={16} aria-hidden="true" />
          <span className="banner-text">{t("plan.changed")}</span>
        </div>
      )}
      {loading && (
        <div className="plan-loading">
          <LoaderCircle size={16} className="spin tone-info" aria-hidden="true" />
          <span>{t("plan.previewing")}</span>
        </div>
      )}
      {previewError !== null && !loading && <ErrorBanner error={previewError} />}

      {preview && !run && !loading && (
        <>
          {preview.plan.protected && (
            <div className="banner banner-warn">
              <ShieldAlert size={16} aria-hidden="true" />
              <span className="banner-text">
                <strong>{t("plan.protected")}</strong> ·{" "}
                {t("plan.confirm.protected", { target: preview.plan.target })}
              </span>
            </div>
          )}
          <section aria-labelledby={`${ids}-checks`}>
            <h3 id={`${ids}-checks`} className="plan-heading">
              {t("plan.checks")}
            </h3>
            <StepList steps={preview.checks} label={label} />
          </section>
          {!preview.ready && (
            <div className="banner banner-fail">
              <span className="banner-text">{t("plan.notReady")}</span>
            </div>
          )}
          {fields === "restore" &&
            preview.request.key &&
            preview.request.key !== params.key?.trim() && (
              <p className="hint">{t("plan.resolved", { key: preview.request.key })}</p>
            )}
          <section aria-labelledby={`${ids}-actions`}>
            <h3 id={`${ids}-actions`} className="plan-heading">
              {t("plan.actions")}
            </h3>
            <ol className="plan-actions">
              {preview.plan.actions.map((a) => (
                <li key={a.id} className="plan-action">
                  <div className="plan-action-head">
                    <span className="step-label">
                      {label({ step: a.id, label: a.title, status: "ok" })}
                    </span>
                    {a.risk === "destructive" && (
                      <span className="badge badge-failed">{t("plan.destructive")}</span>
                    )}
                  </div>
                  <details className="task-details">
                    <summary>{t("plan.details")}</summary>
                    <pre className="statement">{a.statement}</pre>
                  </details>
                </li>
              ))}
            </ol>
          </section>
          {preview.confirmation && preview.ready && (
            <div className="plan-confirm">
              {destructive && !preview.plan.protected && (
                <p className="plan-warning">
                  {t("plan.confirm.drop", { database, target: preview.plan.target })}
                </p>
              )}
              <label className="field-inline">
                <span>
                  {confirmBefore}
                  <code className="confirm-text">{preview.confirmation}</code>
                  {confirmAfter}
                </span>
                <input
                  ref={confirmInput}
                  className="input mono"
                  name="confirm"
                  value={typed}
                  autoComplete="off"
                  spellCheck={false}
                  onChange={(e) => setTyped(e.currentTarget.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && runnable) {
                      e.preventDefault();
                      void execute();
                    }
                  }}
                />
              </label>
            </div>
          )}
          {executeError !== null && <ErrorBanner error={executeError} />}
        </>
      )}

      {run && (
        <section className="plan-run" aria-label={t("plan.progress")}>
          {run.notices.map((n) => (
            <div key={n} className="banner banner-warn">
              <TriangleAlert size={16} aria-hidden="true" />
              <span className="banner-text">{n}</span>
            </div>
          ))}
          <StepList steps={run.steps} label={label} />
          {run.task && <TaskProgress task={run.task} />}
          {run.status === "running" && run.task && isActive(run.task) && (
            <p className="hint">{t("plan.runningHint", { id: run.task.id, target })}</p>
          )}
          {run.status === "done" && preview && run.done && (
            <div className="banner banner-ok">
              <span className="banner-text">{doneText(t, operation, preview, run.done)}</span>
            </div>
          )}
          {run.status === "failed" && <RunFailure run={run} />}
        </section>
      )}
      <div className="sr-only" role="status" aria-live="polite">
        {announcement}
      </div>
    </Modal>
  );
}

function formComplete(fields: "export" | "restore" | undefined, p: PlanRequest): boolean {
  return fields !== "restore" || (!!p.database?.trim() && !!p.key?.trim());
}

/** The label of a check, an action or the wait, in the user's language. */
function stepLabel(t: Translate, s: StepEvent, request: PlanRequest): string {
  const vars = {
    database: request.database ?? "",
    key: request.key ?? "",
    id: request.taskId ?? "",
  };
  switch (s.step) {
    case "restore":
    case "export":
    case "finish":
    case "cancel":
    case "drop":
      return t(`planaction.${s.step}`, vars);
  }
  const key = planStepKey(s.step);
  return key ? t(key) : s.label;
}

function doneText(
  t: Translate,
  operation: Operation,
  preview: PlanPreview,
  done: PlanDone,
): string {
  const database = preview.request.database ?? "";
  switch (operation) {
    case "restore":
      return preview.request.noRecovery
        ? t("plan.done.restoreNoRecovery", { database })
        : t("plan.done.restore");
    case "export":
      return t("plan.done.export", {
        uri: s3URI(done.task?.s3Object) || (preview.request.key ?? ""),
      });
    case "finish":
      return t("plan.done.finish", { database });
    case "cancel":
      return t("plan.done.cancel");
    case "drop":
      return t("plan.done.drop", { database });
  }
}

function RunFailure({ run }: { run: Run }) {
  const { t } = useI18n();
  let text: string;
  if (run.error !== undefined) {
    text = errorText(run.error);
  } else if (run.done?.task && run.done.task.lifecycle !== "SUCCESS" && !run.done.error) {
    text = t("plan.failed.task", { lifecycle: t(`lifecycle.${run.done.task.lifecycle}`) });
  } else {
    text = t("plan.failed.step", { error: run.done?.error ?? "" });
  }
  return (
    <div className="banner banner-fail" role="alert">
      <span className="banner-text">{text}</span>
      {run.error !== undefined && <span className="banner-hint">{t("plan.hint.network")}</span>}
    </div>
  );
}

function ErrorBanner({ error }: { error: unknown }) {
  const { t } = useI18n();
  const hint = errorHint(error);
  return (
    <div className="banner banner-fail" role="alert">
      <span className="banner-text">{errorText(error)}</span>
      {hint && <span className="banner-hint">{t(hint)}</span>}
    </div>
  );
}
