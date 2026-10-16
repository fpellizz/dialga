import { RefreshCw } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { isActive, type Lifecycle, listTasks, type Task } from "./api";
import type { EngineInfo } from "./engine";
import { ErrorBanner } from "./Feedback";
import { useI18n } from "./i18n";

/** While a task is running the list refreshes itself (RDS updates every ~2 min). */
const POLL_MS = 30_000;

const BADGE: Record<Lifecycle, string> = {
  CREATED: "badge-running",
  IN_PROGRESS: "badge-running",
  SUCCESS: "badge-ok",
  ERROR: "badge-failed",
  CANCEL_REQUESTED: "badge-warning",
  CANCELLED: "badge-failed",
};

/** Server time 2006-01-02T15:04:05 shown as 2006-01-02 15:04. */
function shortTime(value?: string): string {
  return value ? value.slice(0, 16).replace("T", " ") : "";
}

/** The recent native backup/restore tasks of a target. */
export function TasksPanel({ engine, target }: { engine: EngineInfo; target: string }) {
  const { t } = useI18n();
  const [tasks, setTasks] = useState<Task[] | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);
  const inFlight = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    if (inFlight.current) {
      return;
    }
    const controller = new AbortController();
    inFlight.current = controller;
    setLoading(true);
    try {
      const list = await listTasks(engine, target, controller.signal);
      if (!controller.signal.aborted) {
        setTasks(list);
        setError(null);
      }
    } catch (e) {
      if (!controller.signal.aborted) {
        setError(e);
      }
    } finally {
      // A request aborted by the cleanup must not clear the one that replaced it.
      if (inFlight.current === controller) {
        inFlight.current = null;
        setLoading(false);
      }
    }
  }, [engine, target]);

  useEffect(() => {
    load();
    return () => {
      inFlight.current?.abort();
      inFlight.current = null;
    };
  }, [load]);

  const polling = tasks?.some(isActive) ?? false;
  useEffect(() => {
    if (!polling) {
      return;
    }
    const id = setInterval(load, POLL_MS);
    return () => clearInterval(id);
  }, [polling, load]);

  const titleId = `tasks-${target}`;
  return (
    <section className="tasks" aria-labelledby={titleId} aria-busy={loading}>
      <div className="tasks-head">
        <h3 id={titleId}>{t("tasks.title")}</h3>
        <button
          type="button"
          className="btn btn-ghost btn-sm"
          onClick={load}
          aria-disabled={loading || undefined}
          aria-label={t("tasks.refresh")}
          title={t("tasks.refresh")}
        >
          <RefreshCw size={14} className={loading ? "spin" : undefined} aria-hidden="true" />
        </button>
      </div>

      {error !== null && <ErrorBanner error={error} />}
      {tasks === null && error === null && (
        <div aria-hidden="true">
          <div className="skeleton skeleton-row" />
          <div className="skeleton skeleton-row" />
        </div>
      )}
      {tasks?.length === 0 && <p className="muted">{t("tasks.empty")}</p>}
      {tasks && tasks.length > 0 && (
        <ul className="task-list">
          {tasks.map((task) => (
            <li key={task.id} className="task">
              <div className="task-row">
                <span className="mono muted">#{task.id}</span>
                <span className="mono">{task.type}</span>
                <span className="task-db">{task.database}</span>
                <span className={`badge ${BADGE[task.lifecycle] ?? "badge-skipped"}`}>
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
              <div className="task-meta muted">
                {t("tasks.updated", { time: shortTime(task.updatedAt) })}
                {isActive(task) && ` · ${task.percent}%`}
              </div>
              {task.info && (
                <details className="task-details">
                  <summary>{t("tasks.details")}</summary>
                  <pre className="task-info">{task.info.replaceAll("\r\n", "\n").trim()}</pre>
                </details>
              )}
            </li>
          ))}
        </ul>
      )}
      {polling && <p className="hint">{t("tasks.polling")}</p>}
    </section>
  );
}
