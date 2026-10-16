import { isActive, type Lifecycle, type Task } from "./api";
import { useI18n } from "./i18n";

/** Badge tone of a task lifecycle. */
export const LIFECYCLE_BADGE: Record<Lifecycle, string> = {
  CREATED: "badge-running",
  IN_PROGRESS: "badge-running",
  SUCCESS: "badge-ok",
  ERROR: "badge-failed",
  CANCEL_REQUESTED: "badge-warning",
  CANCELLED: "badge-failed",
};

/** Server time 2006-01-02T15:04:05 shown as 2006-01-02 15:04. */
export function shortTime(value?: string): string {
  return value ? value.slice(0, 16).replace("T", " ") : "";
}

/** One native task: id, type, database, lifecycle, progress while it runs, and its log. */
export function TaskProgress({ task }: { task: Task }) {
  const { t } = useI18n();
  return (
    <div className="task">
      <div className="task-row">
        <span className="mono muted">#{task.id}</span>
        <span className="mono">{task.type}</span>
        <span className="task-db">{task.database}</span>
        <span className={`badge ${LIFECYCLE_BADGE[task.lifecycle] ?? "badge-skipped"}`}>
          {t(`lifecycle.${task.lifecycle}`)}
        </span>
      </div>
      {isActive(task) && (
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
      )}
      {task.updatedAt && (
        <div className="task-meta muted">
          {t("tasks.updated", { time: shortTime(task.updatedAt) })}
          {isActive(task) && ` · ${task.percent}%`}
        </div>
      )}
      {task.info && (
        <details className="task-details">
          <summary>{t("tasks.details")}</summary>
          <pre className="task-info">{task.info.replaceAll("\r\n", "\n").trim()}</pre>
        </details>
      )}
    </div>
  );
}
