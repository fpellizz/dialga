import { Activity, ArrowLeft, Ban, ClipboardCopy, Play, RefreshCw } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { errorText, listTargets, passwordNeeded } from "./api";
import { formatBytes } from "./databases";
import type { EngineInfo } from "./engine";
import { type Translate, useI18n } from "./i18n";
import {
  applyEvent,
  cancelJob,
  filterCounts,
  followJob,
  formatClock,
  formatDateTime,
  formatDuration,
  getJob,
  HttpError,
  isFinalEvent,
  isJobActive,
  JOB_FILTERS,
  type Job,
  type JobFilter,
  type JobItem,
  type JobOperation,
  type JobRefusal,
  type JobShownState,
  type JobState,
  type JobStepStatus,
  type JobSummary,
  lastSeq,
  logText,
  matchesFilter,
  resumeJob,
  resumeNeeds,
  shownState,
  sortJobs,
} from "./jobs";
import { PasswordNeededBanner } from "./PasswordProvider";
import type { JobList } from "./useJobs";

const STATE_BADGE: Record<JobShownState, string> = {
  refused: "badge-warning",
  queued: "badge-skipped",
  running: "badge-running",
  succeeded: "badge-ok",
  failed: "badge-failed",
  cancelled: "badge-warning",
  interrupted: "badge-warning",
};

const STEP_BADGE: Record<JobStepStatus, string> = {
  pending: "badge-skipped",
  waiting: "badge-skipped",
  running: "badge-running",
  succeeded: "badge-ok",
  failed: "badge-failed",
  skipped: "badge-skipped",
  cancelled: "badge-warning",
  interrupted: "badge-warning",
};

/** The event stream reconnects after 1 s, doubling up to 10 s while it fails. */
export const RECONNECT_MS = 1_000;
const RECONNECT_MAX_MS = 10_000;

function jobIdFromHash(hash: string): string {
  return hash.startsWith("#activity/") ? decodeURIComponent(hash.slice("#activity/".length)) : "";
}

function useJobIdFromHash(): string {
  const [id, setId] = useState(() => jobIdFromHash(window.location.hash));
  useEffect(() => {
    const onHash = () => setId(jobIdFromHash(window.location.hash));
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  return id;
}

const OPERATION_LABEL = {
  restore: "activity.op.restore",
  export: "activity.op.export",
  drop: "activity.op.drop",
  finish: "activity.op.finish",
  cancel: "activity.op.cancel",
  post: "activity.op.post",
} as const;

/** Import, Export, or the operation of a single job: Restore, Drop, Cancel task... */
function jobTitle(
  t: Translate,
  job: { kind: string; operation?: JobOperation; target: string },
): string {
  let kind: string = job.kind;
  if (job.kind === "operation" && job.operation) {
    kind = t(OPERATION_LABEL[job.operation]);
  } else if (job.kind === "import" || job.kind === "export") {
    kind = t(`activity.kind.${job.kind}`);
  }
  return t("activity.jobTitle", { kind, target: job.target });
}

const FILTER_KEY = "dialga.activity.filter";

/** The chip of the list, kept for the session (sessionStorage may be blocked). */
function useJobFilter(): [JobFilter, (filter: JobFilter) => void] {
  const [filter, setFilter] = useState<JobFilter>(() => {
    try {
      const saved = sessionStorage.getItem(FILTER_KEY);
      return JOB_FILTERS.find((f) => f === saved) ?? "all";
    } catch {
      return "all";
    }
  });
  const choose = (next: JobFilter) => {
    setFilter(next);
    try {
      sessionStorage.setItem(FILTER_KEY, next);
    } catch {
      // The chip is just not remembered.
    }
  };
  return [filter, choose];
}

const REFUSAL_LABEL: Record<
  number,
  "activity.refusal.412" | "activity.refusal.422" | "activity.refusal.428" | "activity.refusal.502"
> = {
  412: "activity.refusal.412",
  422: "activity.refusal.422",
  428: "activity.refusal.428",
  502: "activity.refusal.502",
};

/** "HTTP 412 · Precondition failed": the status of a refusal, short. */
function refusalStatus(t: Translate, refusal: JobRefusal): string {
  const code = t("activity.refusal.status", { status: refusal.status });
  const key = REFUSAL_LABEL[refusal.status];
  return key ? `${code} · ${t(key)}` : code;
}

function OriginBadge({ origin }: { origin: string }) {
  const { t } = useI18n();
  return origin === "cli" ? (
    <span className="badge badge-skipped">{t("activity.origin.cli")}</span>
  ) : null;
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal.addEventListener(
      "abort",
      () => {
        clearTimeout(timer);
        resolve();
      },
      { once: true },
    );
  });
}

/** Imports and exports: the list (#activity) or one job (#activity/<id>). */
export function ActivityPage({
  engine,
  engineError,
  jobs,
}: {
  engine: EngineInfo | null;
  engineError: string;
  jobs: JobList;
}) {
  const id = useJobIdFromHash();
  if (id) {
    return (
      <JobDetail
        key={id}
        engine={engine}
        engineError={engineError}
        id={id}
        onChange={jobs.refresh}
      />
    );
  }
  return <JobListView engineError={engineError} jobs={jobs} />;
}

/**
 * The jobs that ended since the previous list, as one sentence for the live
 * region: only the end of a job is announced, never its progress.
 */
function useEndAnnouncement(list: JobSummary[] | null, t: Translate): string {
  const previous = useRef<Map<string, JobState> | null>(null);
  const [text, setText] = useState("");
  useEffect(() => {
    if (!list) {
      return;
    }
    const before = previous.current;
    previous.current = new Map(list.map((j) => [j.id, j.state]));
    if (!before) {
      return;
    }
    const ended = list.filter((j) => {
      const was = before.get(j.id);
      return was !== undefined && isJobActive(was) && !isJobActive(j.state);
    });
    if (ended.length > 0) {
      setText(
        ended
          .map((j) =>
            t("activity.announce", {
              title: jobTitle(t, j),
              state: t(`jobState.${shownState(j)}`),
            }),
          )
          .join(" "),
      );
    }
  }, [list, t]);
  return text;
}

function JobListView({ engineError, jobs }: { engineError: string; jobs: JobList }) {
  const { t } = useI18n();
  const all = jobs.jobs ? sortJobs(jobs.jobs) : null;
  const announcement = useEndAnnouncement(jobs.jobs, t);
  const [filter, setFilter] = useJobFilter();
  const list = all?.filter((j) => matchesFilter(j, filter)) ?? null;
  const active = list?.filter((j) => isJobActive(j.state)) ?? [];
  const history = list?.filter((j) => !isJobActive(j.state)) ?? [];
  const counts = filterCounts(all ?? []);
  const error = engineError || (all ? "" : jobs.error);
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>{t("activity.title")}</h1>
          <p className="lead">{t("activity.lead")}</p>
        </div>
        <div className="toolbar">
          <button
            type="button"
            className="btn btn-outline"
            onClick={jobs.refresh}
            disabled={!!engineError}
          >
            <RefreshCw size={16} aria-hidden="true" />
            {t("instances.refresh")}
          </button>
        </div>
      </header>

      {error && (
        <div className="banner banner-fail" role="alert">
          <span className="banner-text">{error}</span>
        </div>
      )}
      {!list && !error && (
        <div aria-hidden="true">
          <div className="skeleton skeleton-row" />
          <div className="skeleton skeleton-row" />
        </div>
      )}
      {all && all.length > 0 && (
        <fieldset className="field job-filter">
          <legend className="sr-only">{t("activity.filter.label")}</legend>
          <div className="segmented">
            {JOB_FILTERS.map((f) => (
              <label key={f} className="segment">
                <input
                  type="radio"
                  name="job-filter"
                  value={f}
                  checked={filter === f}
                  onChange={() => setFilter(f)}
                />
                <span>
                  {t("activity.filter.count", {
                    label: t(`activity.filter.${f}`),
                    count: counts[f],
                  })}
                </span>
              </label>
            ))}
          </div>
        </fieldset>
      )}
      {all?.length === 0 && (
        <section className="empty" role="status">
          <Activity size={20} aria-hidden="true" />
          <h2>{t("activity.empty.title")}</h2>
          <p>{t("activity.empty.hint")}</p>
        </section>
      )}
      {all && all.length > 0 && list?.length === 0 && (
        <p className="muted" role="status">
          {t("activity.filter.none")}
        </p>
      )}
      {active.length > 0 && (
        <section className="job-section" aria-labelledby="jobs-running">
          <h2 id="jobs-running">{t("activity.running")}</h2>
          <JobRows list={active} />
        </section>
      )}
      {history.length > 0 && (
        <section className="job-section" aria-labelledby="jobs-history">
          <h2 id="jobs-history">{t("activity.history")}</h2>
          <JobRows list={history} />
        </section>
      )}
      {all && <p className="hint">{t("activity.previewsNote")}</p>}
      <div className="sr-only" role="status">
        {announcement}
      </div>
    </div>
  );
}

/** The databases of a job, the first three. */
function names(databases: string[]): string {
  const shown = databases.slice(0, 3).join(", ");
  return databases.length > 3 ? `${shown}, +${databases.length - 3}` : shown;
}

function JobRows({ list }: { list: JobSummary[] }) {
  const { t } = useI18n();
  return (
    <ul className="job-list">
      {list.map((job) => {
        const count = job.databases.length;
        const done = job.succeeded + job.failed;
        return (
          <li key={job.id}>
            <a className="job-row" href={`#activity/${encodeURIComponent(job.id)}`}>
              <div className="job-row-head">
                <span className="job-title">{jobTitle(t, job)}</span>
                <OriginBadge origin={job.origin} />
                <span className={`badge ${STATE_BADGE[shownState(job)]}`}>
                  {t(`jobState.${shownState(job)}`)}
                </span>
              </div>
              {job.refusal && (
                <div className="job-meta">
                  <span className="tone-warning">{job.refusal.reason}</span>
                  <span className="muted">{refusalStatus(t, job.refusal)}</span>
                </div>
              )}
              <div className="job-meta muted">
                {count > 0 && (
                  <span>
                    {count === 1 ? t("activity.databasesOne") : t("activity.databases", { count })}
                  </span>
                )}
                {count > 0 && <span className="mono">{names(job.databases)}</span>}
                {job.failed > 0 && (
                  <span className="tone-destructive">
                    {t("activity.failedCount", { count: job.failed })}
                  </span>
                )}
                <span>{t("activity.created", { time: formatDateTime(job.createdAt) })}</span>
                {job.endedAt && (
                  <span>{t("activity.endedAt", { time: formatDateTime(job.endedAt) })}</span>
                )}
              </div>
              {isJobActive(job.state) && (
                <div
                  className="progress"
                  role="progressbar"
                  aria-label={t("activity.progress", { done, count })}
                  aria-valuemin={0}
                  aria-valuemax={count}
                  aria-valuenow={done}
                >
                  <div
                    className="progress-bar"
                    style={{ width: `${count ? (done * 100) / count : 0}%` }}
                  />
                </div>
              )}
            </a>
          </li>
        );
      })}
    </ul>
  );
}

type Connection = "idle" | "live" | "reconnecting";

function JobDetail({
  engine,
  engineError,
  id,
  onChange,
}: {
  engine: EngineInfo | null;
  engineError: string;
  id: string;
  onChange: () => void;
}) {
  const { t } = useI18n();
  const [job, setJob] = useState<Job | null>(null);
  const [error, setError] = useState("");
  const [notFound, setNotFound] = useState(false);
  /** Where the event stream starts: set once the snapshot shows the job running. */
  const [start, setStart] = useState<number | null>(null);
  const [connection, setConnection] = useState<Connection>("idle");
  const [announcement, setAnnouncement] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [cancelling, setCancelling] = useState(false);
  const [cancelError, setCancelError] = useState("");
  const [copied, setCopied] = useState<"" | "ok" | "failed">("");
  const [resuming, setResuming] = useState(false);
  const [resumeBusy, setResumeBusy] = useState(false);
  const [resumeError, setResumeError] = useState("");
  /** Whether the target asks to retype its name; null until known. */
  const [protectedTarget, setProtectedTarget] = useState<boolean | null>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const [focusHeading, setFocusHeading] = useState(false);
  const cancellingBanner = useRef<HTMLDivElement>(null);
  const [focusBanner, setFocusBanner] = useState(false);
  // The end announcement is built from the latest language and title.
  const tRef = useRef(t);
  tRef.current = t;
  const titleRef = useRef("");
  titleRef.current = job ? jobTitle(t, job) : "";

  useEffect(() => {
    if (!engine) {
      return;
    }
    const controller = new AbortController();
    getJob(engine, id, controller.signal).then(
      (snapshot) => {
        if (!controller.signal.aborted) {
          setJob(snapshot);
          if (isJobActive(snapshot.state)) {
            setStart(lastSeq(snapshot));
          }
        }
      },
      (e: unknown) => {
        if (controller.signal.aborted) {
          return;
        }
        if (e instanceof HttpError && e.status === 404) {
          setNotFound(true);
        } else {
          setError(errorText(e));
        }
      },
    );
    return () => controller.abort();
  }, [engine, id]);

  // The live events: replayed after the last one seen, reconnecting with
  // Last-Event-ID until the final job.* event.
  useEffect(() => {
    if (!engine || start === null) {
      return;
    }
    const controller = new AbortController();
    const { signal } = controller;
    let after = start;
    (async () => {
      let delay = RECONNECT_MS;
      while (!signal.aborted) {
        setConnection("live");
        try {
          const ended = await followJob(
            engine,
            id,
            after,
            (event) => {
              if (event.seq <= after) {
                return;
              }
              after = event.seq;
              delay = RECONNECT_MS;
              setJob((j) => (j ? applyEvent(j, event) : j));
              if (isFinalEvent(event)) {
                const tr = tRef.current;
                const state = event.type.slice("job.".length) as JobState;
                setAnnouncement(
                  tr("activity.announce", {
                    title: titleRef.current,
                    state: tr(`jobState.${state}`),
                  }),
                );
              }
            },
            signal,
          );
          if (ended) {
            setConnection("idle");
            // The snapshot after the end also has what no event carries
            // (the native tasks left running).
            const final = await getJob(engine, id, signal).catch(() => null);
            if (final && !signal.aborted) {
              setJob(final);
            }
            onChange();
            return;
          }
        } catch (e) {
          if (signal.aborted) {
            return;
          }
          if (e instanceof HttpError && e.status === 404) {
            setNotFound(true);
            setConnection("idle");
            return;
          }
        }
        setConnection("reconnecting");
        await sleep(delay, signal);
        delay = Math.min(delay * 2, RECONNECT_MAX_MS);
      }
    })();
    return () => controller.abort();
  }, [engine, id, start, onChange]);

  const confirmCancel = async () => {
    if (!engine) {
      return;
    }
    setCancelling(true);
    setCancelError("");
    try {
      await cancelJob(engine, id);
      setJob((j) => (j ? { ...j, cancelRequested: true } : j));
      setConfirming(false);
      onChange();
      setFocusBanner(true);
    } catch (e) {
      setCancelError(errorText(e));
    } finally {
      setCancelling(false);
    }
  };

  // Resume asks to retype the name of a protected target, as Start does.
  const interrupted = job?.state === "interrupted";
  const jobTarget = job?.target;
  useEffect(() => {
    if (!engine || !interrupted) {
      return;
    }
    let live = true;
    listTargets(engine).then(
      (list) =>
        live &&
        setProtectedTarget(list.targets.find((x) => x.name === jobTarget)?.protected ?? false),
      () => live && setProtectedTarget(false),
    );
    return () => {
      live = false;
    };
  }, [engine, interrupted, jobTarget]);

  const confirmResume = async (confirm: string) => {
    if (!engine) {
      return;
    }
    setResumeBusy(true);
    setResumeError("");
    try {
      const snapshot = await resumeJob(engine, id, confirm || undefined);
      setJob(snapshot);
      setResuming(false);
      setAnnouncement("");
      setStart(lastSeq(snapshot));
      onChange();
      setFocusHeading(true);
    } catch (e) {
      // The targets could not be read, or the target became protected:
      // the engine asks for the name, so ask for it here too.
      if (e instanceof HttpError && e.status === 422 && e.message.startsWith("not confirmed")) {
        setProtectedTarget(true);
      }
      setResumeError(errorText(e));
    } finally {
      setResumeBusy(false);
    }
  };

  // The Resume button is gone: the focus goes to the title of the job.
  useEffect(() => {
    if (focusHeading) {
      heading.current?.focus();
      setFocusHeading(false);
    }
  }, [focusHeading]);

  // The Cancel button is gone: the focus goes to the banner that replaced it.
  useEffect(() => {
    if (focusBanner) {
      cancellingBanner.current?.focus();
      setFocusBanner(false);
    }
  }, [focusBanner]);

  const copyLog = async () => {
    if (!job) {
      return;
    }
    try {
      await navigator.clipboard.writeText(logText(job));
      setCopied("ok");
    } catch {
      setCopied("failed");
    }
  };

  const back = (
    <a href="#activity" className="btn btn-ghost btn-sm back-link">
      <ArrowLeft size={14} aria-hidden="true" />
      {t("activity.back")}
    </a>
  );
  if (notFound || error || engineError || !job) {
    const message = engineError || error || (notFound ? t("activity.notFound") : "");
    return (
      <div className="page">
        {back}
        {message ? (
          <div className="banner banner-fail" role="alert">
            <span className="banner-text">{message}</span>
          </div>
        ) : (
          <div aria-hidden="true">
            <div className="skeleton skeleton-line" />
            <div className="skeleton skeleton-row" />
            <div className="skeleton skeleton-row" />
          </div>
        )}
      </div>
    );
  }

  const active = isJobActive(job.state);
  const title = jobTitle(t, job);
  return (
    <div className="page">
      {back}
      <header className="page-head">
        <div className="job-head">
          <h1 ref={heading} tabIndex={-1}>
            {title}
          </h1>
          <OriginBadge origin={job.origin} />
          <span className={`badge ${STATE_BADGE[shownState(job)]}`}>
            {t(`jobState.${shownState(job)}`)}
          </span>
        </div>
        {job.state === "interrupted" && !resuming && (
          <div className="toolbar">
            <button
              type="button"
              className="btn btn-primary"
              onClick={() => setResuming(true)}
              disabled={protectedTarget === null}
            >
              <Play size={16} aria-hidden="true" />
              {t("activity.resume")}
            </button>
          </div>
        )}
        {active && !job.cancelRequested && !confirming && (
          <div className="toolbar">
            <button type="button" className="btn btn-outline" onClick={() => setConfirming(true)}>
              <Ban size={16} aria-hidden="true" />
              {t("activity.cancel")}
            </button>
          </div>
        )}
      </header>

      <dl className="facts">
        <dt>{t("activity.fact.instance")}</dt>
        <dd>{job.instance}</dd>
        <dt>{t("activity.fact.created")}</dt>
        <dd>{formatDateTime(job.createdAt)}</dd>
        {job.endedAt && (
          <>
            <dt>{t("activity.fact.ended")}</dt>
            <dd>{formatDateTime(job.endedAt)}</dd>
          </>
        )}
        <dt>{t("activity.fact.origin")}</dt>
        <dd>{t(`activity.origin.${job.origin === "cli" ? "cli" : "app"}`)}</dd>
        <dt>{t("activity.fact.id")}</dt>
        <dd className="mono">{job.id}</dd>
      </dl>

      {confirming && active && (
        <CancelConfirm
          busy={cancelling}
          error={cancelError}
          onConfirm={confirmCancel}
          onKeep={() => {
            setConfirming(false);
            setCancelError("");
          }}
        />
      )}
      {active && job.cancelRequested && (
        <div className="banner banner-warn job-warn" ref={cancellingBanner} tabIndex={-1}>
          <span className="banner-text">{t("activity.cancelling")}</span>
        </div>
      )}
      {job.state === "interrupted" && resuming && (
        <ResumeConfirm
          target={job.target}
          protectedTarget={protectedTarget === true}
          busy={resumeBusy}
          error={resumeError}
          onConfirm={confirmResume}
          onKeep={() => {
            setResuming(false);
            setResumeError("");
          }}
        />
      )}
      {job.state === "interrupted" && <ResumeNeedsPanel job={job} />}
      {job.refusal && <RefusalPanel refusal={job.refusal} />}

      {job.items.length > 0 && (
        <section className="card" aria-labelledby="job-items">
          <h2 id="job-items">{t("activity.items")}</h2>
          <ul className="job-items">
            {job.items.map((item) => (
              <ItemCard key={item.name} item={item} />
            ))}
          </ul>
        </section>
      )}

      <section className="card" aria-labelledby="job-log">
        <div className="card-head">
          <h2 id="job-log">{t("activity.log")}</h2>
          <div className="head-actions">
            {active && connection !== "idle" && (
              <span className={connection === "live" ? "muted" : "tone-warning"}>
                {connection === "live" ? t("activity.live") : t("activity.reconnecting")}
              </span>
            )}
            <button type="button" className="btn btn-outline btn-sm" onClick={copyLog}>
              <ClipboardCopy size={14} aria-hidden="true" />
              {t("activity.copyLog")}
            </button>
          </div>
        </div>
        {copied && (
          <p className={copied === "ok" ? "hint" : "hint tone-destructive"} role="status">
            {copied === "ok" ? t("activity.copied") : t("activity.copyFailed")}
          </p>
        )}
        <ol className="job-log">
          {job.events.map((event) => {
            const where = [event.item, event.step].filter(Boolean).join("/");
            const tone =
              event.level === "error"
                ? "tone-destructive"
                : event.level === "warn"
                  ? "tone-warning"
                  : undefined;
            const task = event.task
              ? `${t("activity.task", { id: event.task.id })} ${t(`lifecycle.${event.task.lifecycle}`)} ${event.task.percent}%`
              : "";
            return (
              <li key={event.seq} className="job-log-line">
                <time className="mono muted" dateTime={event.time}>
                  {formatClock(event.time)}
                </time>
                <span className="mono job-log-type">{event.type}</span>
                {where && <span className="mono">{where}</span>}
                <span className={tone}>{[task, event.message].filter(Boolean).join(" · ")}</span>
              </li>
            );
          })}
        </ol>
      </section>

      <div className="sr-only" role="status">
        {announcement}
      </div>
    </div>
  );
}

/** A Run the engine refused: why, the status, the checks that failed. */
function RefusalPanel({ refusal }: { refusal: JobRefusal }) {
  const { t } = useI18n();
  const failed = refusal.checks?.filter((c) => c.status === "failed") ?? [];
  return (
    <section className="banner banner-warn job-warn" aria-labelledby="refusal-title">
      <strong id="refusal-title">{t("activity.refusal.title")}</strong>
      <p className="banner-text">{refusal.reason}</p>
      <p className="banner-hint">
        {refusalStatus(t, refusal)}. {t("activity.refusal.hint")}
      </p>
      {failed.length > 0 && (
        <>
          <h3 id="refusal-checks">{t("activity.refusal.checks")}</h3>
          <ul aria-labelledby="refusal-checks">
            {failed.map((c) => (
              <li key={c.step}>
                <span>{c.label}</span>
                {c.detail && <span className="muted"> · {c.detail}</span>}
              </li>
            ))}
          </ul>
        </>
      )}
    </section>
  );
}

function CancelConfirm({
  busy,
  error,
  onConfirm,
  onKeep,
}: {
  busy: boolean;
  error: string;
  onConfirm: () => void;
  onKeep: () => void;
}) {
  const { t } = useI18n();
  return (
    <section
      className="confirm-cancel"
      aria-labelledby="cancel-title"
      aria-describedby="cancel-hint"
      onKeyDown={(e) => e.key === "Escape" && onKeep()}
    >
      <strong id="cancel-title">{t("activity.cancelTitle")}</strong>
      <p id="cancel-hint" className="hint">
        {t("activity.cancelHint")}
      </p>
      {error && (
        <div className="banner banner-fail" role="alert">
          <span className="banner-text">{error}</span>
        </div>
      )}
      <div className="actions actions-start">
        <button type="button" className="btn btn-destructive" onClick={onConfirm} disabled={busy}>
          {t("activity.cancel")}
        </button>
        {/* biome-ignore lint/a11y/noAutofocus: the safe choice takes the focus when the question appears */}
        <button type="button" className="btn btn-outline" onClick={onKeep} autoFocus>
          {t("activity.keepRunning")}
        </button>
      </div>
    </section>
  );
}

function ResumeConfirm({
  target,
  protectedTarget,
  busy,
  error,
  onConfirm,
  onKeep,
}: {
  target: string;
  protectedTarget: boolean;
  busy: boolean;
  error: string;
  onConfirm: (confirm: string) => void;
  onKeep: () => void;
}) {
  const { t } = useI18n();
  const [typed, setTyped] = useState("");
  const ready = !protectedTarget || typed === target;
  return (
    <form
      className="confirm-resume"
      aria-labelledby="resume-confirm-title"
      aria-describedby="resume-confirm-hint"
      onSubmit={(e) => {
        e.preventDefault();
        if (ready && !busy) {
          onConfirm(protectedTarget ? typed : "");
        }
      }}
      onKeyDown={(e) => e.key === "Escape" && onKeep()}
    >
      <strong id="resume-confirm-title">{t("activity.resumeTitle")}</strong>
      <p id="resume-confirm-hint" className="hint">
        {t("activity.resumeHint")}
      </p>
      {protectedTarget && (
        <div className="form-field">
          <label htmlFor="resume-confirm">{t("activity.resumeConfirm", { target })}</label>
          <input
            id="resume-confirm"
            className="input mono"
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            // biome-ignore lint/a11y/noAutofocus: the field appears on request and must take the focus
            autoFocus
          />
        </div>
      )}
      {error && passwordNeeded(error) && (
        <PasswordNeededBanner target={passwordNeeded(error) ?? target} />
      )}
      {error && !passwordNeeded(error) && (
        <div className="banner banner-fail" role="alert">
          <span className="banner-text">{error}</span>
        </div>
      )}
      <div className="actions actions-start">
        <button type="submit" className="btn btn-primary" disabled={!ready || busy}>
          {t("activity.resume")}
        </button>
        <button
          type="button"
          className="btn btn-outline"
          onClick={onKeep}
          // biome-ignore lint/a11y/noAutofocus: without a field to type in, the question takes the focus
          autoFocus={!protectedTarget}
        >
          {t("activity.notNow")}
        </button>
      </div>
    </form>
  );
}

function ResumeNeedsPanel({ job }: { job: Job }) {
  const { t } = useI18n();
  const needs = resumeNeeds(job);
  return (
    <section className="banner banner-warn job-warn resume" aria-labelledby="resume-title">
      <strong id="resume-title">{t("activity.interrupted.title")}</strong>
      <p className="banner-hint">{t("activity.interrupted.hint")}</p>
      {needs.map((n) => (
        <dl key={n.item} className="facts resume-facts" aria-label={n.item}>
          <dt>{t("activity.resume.item")}</dt>
          <dd className="mono">{n.item}</dd>
          {n.lastStep && (
            <>
              <dt>{t("activity.resume.lastStep")}</dt>
              <dd>{n.lastStep}</dd>
            </>
          )}
          {n.pending.length > 0 && (
            <>
              <dt>{t("activity.resume.pending")}</dt>
              <dd>{n.pending.join(", ")}</dd>
            </>
          )}
          {n.upload && (
            <>
              <dt>{t("activity.resume.upload")}</dt>
              <dd className="mono">
                {n.partsTotal
                  ? t("activity.resume.uploadValue", {
                      bucket: n.upload.bucket,
                      key: n.upload.key,
                      id: n.upload.uploadId,
                      done: n.upload.partsDone.length,
                      total: n.partsTotal,
                    })
                  : t("activity.resume.uploadParts", {
                      bucket: n.upload.bucket,
                      key: n.upload.key,
                      id: n.upload.uploadId,
                      done: n.upload.partsDone.length,
                    })}
              </dd>
            </>
          )}
          {n.taskIds.length > 0 && (
            <>
              <dt>{t("activity.resume.tasks")}</dt>
              <dd>
                {n.taskIds.map((x) => `#${x}`).join(", ")}
                {n.taskLeftRunning && ` (${t("activity.resume.stillRunning")})`}
              </dd>
            </>
          )}
        </dl>
      ))}
    </section>
  );
}

function ItemCard({ item }: { item: JobItem }) {
  const { t, lang } = useI18n();
  const step = item.steps.find((s) => s.id === item.step);
  const running = isJobActive(item.state) && step?.status === "running";
  let progress = "";
  if (running && item.transfer) {
    const tr = item.transfer;
    progress = t("activity.transfer", {
      done: formatBytes(tr.bytes, lang),
      total: formatBytes(tr.total, lang),
    });
    if (tr.bytesPerSecond > 0) {
      progress += ` · ${t("activity.speed", { speed: formatBytes(tr.bytesPerSecond, lang) })}`;
    }
    if (tr.etaSeconds) {
      progress += ` · ${t("activity.eta", { eta: formatDuration(tr.etaSeconds) })}`;
    }
  } else if (running && item.task) {
    progress = `${t("activity.task", { id: item.task.id })} · ${t(`lifecycle.${item.task.lifecycle}`)} · ${item.task.percent}%`;
  }
  return (
    <li className="job-item">
      <div className="job-item-head">
        <span className="mono job-item-name">{item.name}</span>
        <span className={`badge ${STATE_BADGE[item.state]}`}>{t(`jobState.${item.state}`)}</span>
        {isJobActive(item.state) && step && <span className="muted">{step.title}</span>}
      </div>
      {progress && (
        <>
          <div
            className="progress"
            role="progressbar"
            aria-label={`${item.name}: ${step?.title ?? ""}`}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={item.percent}
          >
            <div className="progress-bar" style={{ width: `${item.percent}%` }} />
          </div>
          <p className="job-progress muted">{progress}</p>
        </>
      )}
      {item.taskLeftRunning && item.task && (
        <p className="tone-warning">{t("activity.taskLeftRunning", { id: item.task.id })}</p>
      )}
      {item.error && <p className="tone-destructive">{item.error}</p>}
      {item.result && <p className="muted">{item.result}</p>}
      <ol className="job-steps" aria-label={t("activity.steps", { name: item.name })}>
        {item.steps.map((s) => (
          <li key={s.id} className="job-step">
            <span className={`badge ${STEP_BADGE[s.status]}`}>{t(`jobStep.${s.status}`)}</span>
            <span>{s.title}</span>
            {s.detail && <span className="muted job-step-detail">{s.detail}</span>}
          </li>
        ))}
      </ol>
    </li>
  );
}
