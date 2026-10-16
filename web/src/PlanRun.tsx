import { TriangleAlert } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { ApiError, isActive, type StepEvent, type Task } from "./api";
import type { EngineInfo } from "./engine";
import { ErrorBanner } from "./Feedback";
import { type Translate, useI18n } from "./i18n";
import { stepLabel } from "./PlanPreview";
import {
  type ExecuteRequest,
  executePlan,
  type Operation,
  type PlanDone,
  type PlanPreview,
  type PlanRequest,
  planHint,
  s3URI,
  upsertStep,
} from "./plans";
import { StepList } from "./StepList";
import { TaskProgress } from "./TaskProgress";

/** An execution: its steps, the followed task, the outcome. */
export interface PlanRunState {
  status: "running" | "done" | "failed";
  steps: StepEvent[];
  notices: string[];
  task?: Task;
  done?: PlanDone;
  /** The stream broke: the task may still run. */
  error?: unknown;
}

/** What the user reads when a plan succeeded. */
export function doneText(
  t: Translate,
  operation: Operation,
  request: PlanRequest,
  done: PlanDone,
): string {
  const database = request.database ?? "";
  switch (operation) {
    case "restore":
      return request.noRecovery
        ? t("plan.done.restoreNoRecovery", { database })
        : t("plan.done.restore");
    case "export":
      return t("plan.done.export", { uri: s3URI(done.task?.s3Object) || (request.key ?? "") });
    case "finish":
      return t("plan.done.finish", { database });
    case "cancel":
      return t("plan.done.cancel");
    case "drop":
      return t("plan.done.drop", { database });
  }
}

/**
 * Executes previewed plans of one operation and keeps the state of the
 * current run. execute answers refused when the engine said no before
 * running anything (412: the plan changed, 422: confirmation or password):
 * the run is cleared and the preview is what to show again. announcement is
 * the text of a polite live region: the lifecycle changes and the outcome,
 * never every percent. Unmounting stops following the task, not the task.
 */
export function usePlanRun(engine: EngineInfo, target: string, operation: Operation) {
  const { t } = useI18n();
  const [run, setRun] = useState<PlanRunState | null>(null);
  const [announcement, setAnnouncement] = useState("");
  const inFlight = useRef<AbortController | null>(null);

  useEffect(() => () => inFlight.current?.abort(), []);

  const execute = useCallback(
    async (preview: PlanPreview, confirm: string): Promise<{ refused?: ApiError }> => {
      inFlight.current?.abort();
      const controller = new AbortController();
      inFlight.current = controller;
      setRun({ status: "running", steps: [], notices: [] });
      setAnnouncement(t("plan.running"));
      const body: ExecuteRequest = { ...preview.request, digest: preview.digest };
      if (preview.confirmation) {
        body.confirm = confirm;
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
        if (!controller.signal.aborted) {
          setRun(
            (r) =>
              r && { ...r, status: done.ok ? "done" : "failed", done, task: done.task ?? r.task },
          );
          if (done.ok) {
            setAnnouncement(doneText(t, operation, preview.request, done));
          }
        }
        return {};
      } catch (e) {
        if (controller.signal.aborted) {
          return {};
        }
        if (e instanceof ApiError) {
          setRun(null);
          return { refused: e };
        }
        setRun((r) => r && { ...r, status: "failed", error: e });
        return {};
      } finally {
        if (inFlight.current === controller) {
          inFlight.current = null;
        }
      }
    },
    [engine, target, operation, t],
  );

  /** Stops following: the engine finishes the statement and the task runs on. */
  const abort = useCallback(() => inFlight.current?.abort(), []);
  const reset = useCallback(() => {
    inFlight.current?.abort();
    setRun(null);
  }, []);
  return { run, announcement, execute, abort, reset };
}

/**
 * A run, inline: the pre-flight warnings, the steps, the RDS task with its
 * progress, the hint that closing keeps the task running, the outcome.
 */
export function PlanRunView({
  run,
  target,
  operation,
  request,
  strategy,
}: {
  run: PlanRunState;
  target: string;
  operation: Operation;
  /** The request of the preview that runs (labels, outcome). */
  request: PlanRequest;
  /** The resolved replace strategy of the preview, when there is one. */
  strategy?: string;
}) {
  const { t } = useI18n();
  return (
    <section className="plan-run" aria-label={t("plan.progress")}>
      {run.notices.map((n) => (
        <div key={n} className="banner banner-warn">
          <TriangleAlert size={16} aria-hidden="true" />
          <span className="banner-text">{n}</span>
        </div>
      ))}
      <StepList steps={run.steps} label={(s) => stepLabel(t, s, request, strategy)} />
      {run.task && <TaskProgress task={run.task} />}
      {run.status === "running" && run.task && isActive(run.task) && (
        <p className="hint">{t("plan.runningHint", { id: run.task.id, target })}</p>
      )}
      {run.status === "done" && run.done && (
        <div className="banner banner-ok">
          <span className="banner-text">{doneText(t, operation, request, run.done)}</span>
        </div>
      )}
      {run.status === "failed" && <RunFailure run={run} />}
    </section>
  );
}

function RunFailure({ run }: { run: PlanRunState }) {
  const { t } = useI18n();
  // The call failed (refused, or the stream broke): the engine's message, then what to do.
  if (run.error !== undefined) {
    return <ErrorBanner error={run.error} fallback={planHint(run.error)} />;
  }
  const task = run.done?.task;
  const text =
    task && task.lifecycle !== "SUCCESS" && !run.done?.error
      ? t("plan.failed.task", { lifecycle: t(`lifecycle.${task.lifecycle}`) })
      : t("plan.failed.step", { error: run.done?.error ?? "" });
  return (
    <div className="banner banner-fail" role="alert">
      <span className="banner-text">{text}</span>
    </div>
  );
}

/** The polite live region of a run (render it always, outside conditional content). */
export function PlanAnnouncer({ text }: { text: string }) {
  return (
    <div className="sr-only" role="status" aria-live="polite">
      {text}
    </div>
  );
}
