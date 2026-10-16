import { useCallback, useEffect, useState } from "react";
import { errorText } from "./api";
import type { EngineInfo } from "./engine";
import { isJobActive, type JobSummary, listJobs } from "./jobs";

/** How often the job list refreshes: always (sidebar badge), faster on Activity while a job runs. */
export const JOBS_POLL_MS = 15_000;
export const JOBS_POLL_FAST_MS = 3_000;

export interface JobList {
  /** null until the first answer. */
  jobs: JobSummary[] | null;
  error: string;
  refresh: () => void;
}

/**
 * The jobs of the engine, polled while the app is open: one cheap local
 * request every 15 seconds, every 3 when fast (the Activity page is open)
 * and a job is queued or running.
 */
export function useJobList(engine: EngineInfo | null, fast: boolean): JobList {
  const [jobs, setJobs] = useState<JobSummary[] | null>(null);
  const [error, setError] = useState("");
  const [tick, setTick] = useState(0);
  const refresh = useCallback(() => setTick((n) => n + 1), []);

  // biome-ignore lint/correctness/useExhaustiveDependencies: tick is the refresh trigger.
  useEffect(() => {
    if (!engine) {
      return;
    }
    const controller = new AbortController();
    listJobs(engine, controller.signal).then(
      (list) => {
        if (!controller.signal.aborted) {
          setJobs(list);
          setError("");
        }
      },
      (e: unknown) => {
        if (!controller.signal.aborted) {
          setError(errorText(e));
        }
      },
    );
    return () => controller.abort();
  }, [engine, tick]);

  const active = jobs?.some((j) => isJobActive(j.state)) ?? false;
  useEffect(() => {
    if (!engine) {
      return;
    }
    const id = setInterval(refresh, fast && active ? JOBS_POLL_FAST_MS : JOBS_POLL_MS);
    return () => clearInterval(id);
  }, [engine, fast, active, refresh]);

  return { jobs, error, refresh };
}
