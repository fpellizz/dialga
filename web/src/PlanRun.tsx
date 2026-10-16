import { TriangleAlert } from "lucide-react";
import type { StepEvent, Task } from "./api";
import { useI18n } from "./i18n";
import type { PlanDone } from "./plans";
import { StepList } from "./StepList";
import { BADGE } from "./TasksPanel";

/** The native task a plan started, with the progress bar of TasksPanel. */
export function TaskProgress({ task }: { task: Task }) {
  const { t } = useI18n();
  return (
    <div className="task">
      <div className="task-row">
        <span className="mono muted">#{task.id}</span>
        <span className="mono">{task.type}</span>
        <span className="task-db">{task.database}</span>
        <span className={`badge ${BADGE[task.lifecycle] ?? "badge-skipped"}`}>
          {t(`lifecycle.${task.lifecycle}`)}
        </span>
      </div>
      <div
        className="progress"
        role="progressbar"
        aria-label={`${task.type} ${task.database}`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={task.percent}
      >
        <div className="progress-bar" style={{ width: `${task.percent}%` }} />
      </div>
      <div className="task-meta muted">{task.percent}%</div>
    </div>
  );
}

/** A plan while and after it runs: warnings, steps, the task, the outcome. */
export function PlanRun({
  notices = [],
  steps,
  task,
  done,
  error,
}: {
  notices?: string[];
  steps: StepEvent[];
  task?: Task | null;
  done?: PlanDone | null;
  /** The request failed before the stream (412, 422, 502...). */
  error?: string;
}) {
  const { t } = useI18n();
  return (
    <div className="plan-run">
      {notices.map((notice) => (
        <div key={notice} className="banner banner-warn" role="status">
          <TriangleAlert size={16} aria-hidden="true" />
          <span className="banner-text">{notice}</span>
        </div>
      ))}
      {steps.length > 0 && <StepList steps={steps} />}
      {task && <TaskProgress task={task} />}
      {error && (
        <div className="banner banner-fail" role="alert">
          <span className="banner-text">{error}</span>
        </div>
      )}
      {done && !done.ok && done.error && (
        <div className="banner banner-fail" role="alert">
          <span className="banner-text">{done.error}</span>
        </div>
      )}
      {done && !done.ok && !done.error && (
        <div className="banner banner-fail" role="alert">
          {t("plan.failed")}
        </div>
      )}
    </div>
  );
}
