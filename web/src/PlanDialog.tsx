import { LoaderCircle, Play, RefreshCw, TriangleAlert } from "lucide-react";
import {
  type FormEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import { isActive, passwordNeeded, type Task } from "./api";
import type { EngineInfo } from "./engine";
import { ErrorBanner } from "./Feedback";
import { useI18n } from "./i18n";
import { Modal } from "./Modal";
import { usePassword } from "./PasswordProvider";
import { PlanPreviewView } from "./PlanPreview";
import { PlanAnnouncer, PlanRunView, usePlanRun } from "./PlanRun";
import {
  canExecute,
  cleanRequest,
  databaseExists,
  isDestructive,
  type Operation,
  type PlanPreview,
  type PlanRequest,
  planHint,
  previewPlan,
  sameRequest,
} from "./plans";
import { ReplaceChoice } from "./ReplaceChoice";
import { ReplaceOutcome } from "./ReplaceOutcome";

/** How the dialog ended, for the page behind it. */
export interface PlanResult {
  /** An execution started: what the page shows may have changed. */
  changed: boolean;
  /** The task the dialog was following when it closed: it keeps running. */
  running?: Task;
  /** The job of the operation it was following when it closed, in Activity. */
  jobId?: string;
}

/**
 * The dialog of a native operation: the preview (PlanPreviewView: pre-flight
 * checks, the exact T-SQL, the confirmation the plan asks for), then the
 * execution streamed from the engine (PlanRunView, with the RDS task).
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
  existing,
  bucket,
  onClose,
  onShowReport,
  onDropOld,
}: {
  engine: EngineInfo;
  target: string;
  operation: Operation;
  request: PlanRequest;
  fields?: "export" | "restore";
  /** The databases of the target, when the page knows them: a restore over one offers to replace it. */
  existing?: readonly string[];
  /** The bucket of the target: names the S3 URI of the safety backup of a replace. */
  bucket?: string;
  onClose: (result: PlanResult) => void;
  /** Offered after a restore: open the post-restore report of the database. */
  onShowReport?: (database: string) => void;
  /** Offered after a swap: drop the old database it kept. */
  onDropOld?: (database: string) => void;
}) {
  const { t } = useI18n();
  const [params, setParams] = useState<PlanRequest>(request);
  const [preview, setPreview] = useState<PlanPreview | null>(null);
  // The parameters of the preview shown, as the user sees them in the fields.
  const [previewed, setPreviewed] = useState<PlanRequest | null>(null);
  const [loading, setLoading] = useState(false);
  const [previewError, setPreviewError] = useState<unknown>(null);
  const [executeError, setExecuteError] = useState<unknown>(null);
  const [changed, setChanged] = useState(false);
  const [typed, setTyped] = useState("");
  const {
    run,
    announcement,
    execute: start,
    abort,
    cancel,
    reset,
  } = usePlanRun(engine, target, operation);
  const previewAbort = useRef<AbortController | null>(null);
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
    return () => previewAbort.current?.abort();
  }, []);

  // The SQL password was saved while the preview waited for it: preview again.
  const { epoch } = usePassword();
  const seenEpoch = useRef(epoch);
  // biome-ignore lint/correctness/useExhaustiveDependencies: epoch is the trigger.
  useEffect(() => {
    if (epoch === seenEpoch.current) {
      return;
    }
    seenEpoch.current = epoch;
    if (passwordNeeded(previewError)) {
      void doPreview(params);
    }
  }, [epoch]);

  const stale = preview !== null && previewed !== null && !sameRequest(params, previewed);
  const runnable = preview !== null && !loading && canExecute(preview, typed, stale);

  async function execute() {
    if (!preview || !runnable) {
      return;
    }
    setExecuteError(null);
    setChanged(false);
    const { refused } = await start(preview, typed);
    if (refused?.status === 412) {
      // The plan changed, or a check fails now: show the new preview.
      setChanged(true);
      void doPreview(previewed ?? params);
    } else if (refused) {
      setExecuteError(refused);
    }
  }

  function close() {
    const following = run?.status === "running";
    previewAbort.current?.abort();
    // Stops following the task; the engine finishes the statement and the task runs on.
    abort();
    onClose({
      changed: run !== null,
      running: following && run?.task && isActive(run.task) ? run.task : undefined,
      jobId: following ? run?.jobId : undefined,
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
  // Known before the preview for a drop: the button does not change colour when it arrives.
  const destructive = preview ? isDestructive(preview.plan) : operation === "drop";
  // "Cancel" next to "Cancel task" would read as the same thing.
  const dismiss = operation === "cancel" ? t("plan.close") : t("plan.cancel");
  const database = preview?.request.database ?? params.database ?? "";
  // The list of the page, or the pre-flight that refused an existing database.
  const exists =
    fields === "restore" &&
    // The engine refuses a replace with a differential or a restore left restoring.
    !params.differential &&
    !params.noRecovery &&
    (!!params.replace ||
      databaseExists(existing, params.database ?? "") ||
      (!!preview &&
        !preview.request.replace &&
        preview.checks.some((c) => c.step === "database" && c.status === "failed")));

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
              reset();
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
      <>
        {run.jobId && operation !== "cancel" && (
          <button
            type="button"
            className="btn btn-outline"
            aria-disabled={run.cancelling}
            onClick={() => !run.cancelling && void cancel()}
          >
            {t("plan.cancelOperation")}
          </button>
        )}
        <button type="button" className="btn btn-outline" onClick={close}>
          {t("plan.close")}
        </button>
      </>
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
          {exists && (
            <ReplaceChoice
              database={params.database?.trim() ?? ""}
              mode={params.replace ?? ""}
              backupFirst={params.backupFirst !== false}
              disabled={loading}
              onChange={({ replace, backupFirst }) =>
                setParams((p) => ({ ...p, replace, backupFirst }))
              }
            />
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
      {previewError !== null && !loading && (
        <ErrorBanner error={previewError} fallback={planHint(previewError)} />
      )}

      {preview && !run && !loading && (
        <>
          {fields === "restore" &&
            preview.request.key &&
            preview.request.key !== params.key?.trim() && (
              <p className="hint">{t("plan.resolved", { key: preview.request.key })}</p>
            )}
          <PlanPreviewView
            preview={preview}
            confirm={typed}
            onConfirmChange={setTyped}
            onSubmit={() => void execute()}
          />
          {executeError !== null && (
            <ErrorBanner error={executeError} fallback={planHint(executeError)} recorded />
          )}
        </>
      )}

      {run && (
        <PlanRunView
          run={run}
          target={target}
          operation={operation}
          request={preview?.request ?? params}
          strategy={preview?.replace?.strategy}
        />
      )}
      {run?.status === "done" && operation === "restore" && preview?.request.replace && (
        <ReplaceOutcome
          database={database}
          mode={preview.request.replace}
          structured={run.done?.replace}
          texts={[
            run.done?.result,
            preview.replace?.old,
            preview.replace?.backupKey,
            ...run.steps.flatMap((s) => [s.label, s.detail]),
            ...run.notices,
          ]}
          bucket={bucket}
          onDropOld={onDropOld}
        />
      )}
      <PlanAnnouncer text={announcement} />
    </Modal>
  );
}

function formComplete(fields: "export" | "restore" | undefined, p: PlanRequest): boolean {
  return fields !== "restore" || (!!p.database?.trim() && !!p.key?.trim());
}
