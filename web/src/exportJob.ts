/**
 * The export as one engine job (POST /api/v1/jobs, kind "export": backup,
 * wait and download per database, run by the engine and shown in
 * Activity). Used when the engine has the route; an older engine answers
 * 404 or 405 and the wizard chains the plan calls instead (exportRun.ts).
 * Shapes as in api.JobRequest, pipeline.Plan and jobs.Job.
 */
import type { StepEvent, StepStatus, Task } from "./api";
import type { EngineInfo } from "./engine";
import type { ExportItemRun, ItemState } from "./exportRun";
import { HttpError } from "./plans";
import { createSseParser } from "./sse";

export interface JobItemRequest {
  database: string;
  key?: string;
  destination?: string;
  overwrite?: boolean;
}

/** pipeline.ItemPlan of an export. */
export interface JobItemPlan {
  database: string;
  /** The local destination, "" when the backup stays in S3. */
  file: string;
  /** The S3 key, defaults resolved. */
  key: string;
  ready: boolean;
  checks: StepEvent[];
  actions: { id: string; title: string; statement: string }[];
}

/** pipeline.Plan: the dry run of the job. */
export interface JobPlan {
  kind: string;
  target: string;
  instance: string;
  protected: boolean;
  ready: boolean;
  confirmation?: string;
  digest: string;
  warnings?: string[];
  items: JobItemPlan[];
  preview: string;
}

interface JobStepInfo {
  id: string;
  title: string;
  status: string;
  detail?: string;
}

/** jobs.Item. */
interface JobItem {
  name: string;
  state: string;
  step?: string;
  percent: number;
  steps: JobStepInfo[];
  transfer?: {
    direction: string;
    bytes: number;
    total: number;
    bytesPerSecond: number;
    etaSeconds?: number;
  };
  task?: { id: number; type?: string; lifecycle: Task["lifecycle"]; percent: number };
  taskLeftRunning?: boolean;
  error?: string;
  result?: string;
}

/** jobs.Job. */
export interface Job {
  id: string;
  kind: string;
  target: string;
  state: string;
  items: JobItem[];
}

function headers(engine: EngineInfo): HeadersInit {
  return { Authorization: `Bearer ${engine.token}`, "Content-Type": "application/json" };
}

async function httpError(response: Response): Promise<HttpError> {
  return new HttpError(
    response.status,
    (await response.text()).trim() || `HTTP ${response.status}`,
  );
}

/** The engine has no POST /api/v1/jobs: answered when the route is missing. */
export class NoJobsError extends Error {}

/**
 * The dry run of an export job: the checks of each database and the
 * actions, nothing started. NoJobsError when the engine has no job route.
 */
export async function previewJob(
  engine: EngineInfo,
  target: string,
  items: JobItemRequest[],
  signal?: AbortSignal,
): Promise<JobPlan> {
  const response = await fetch(`${engine.url}/api/v1/jobs`, {
    method: "POST",
    headers: headers(engine),
    body: JSON.stringify({ kind: "export", target, items, dryRun: true }),
    signal,
  });
  if (response.status === 404 || response.status === 405) {
    // 404 from the mux of an engine without the route, not "unknown target": that one has text.
    const body = (await response.text()).trim();
    if (response.status === 405 || body === "404 page not found" || body === "") {
      throw new NoJobsError(body || `HTTP ${response.status}`);
    }
    throw new HttpError(response.status, body);
  }
  if (!response.ok) {
    throw await httpError(response);
  }
  return (await response.json()) as JobPlan;
}

/** Starts the previewed job (its digest): 201 with the first snapshot. */
export async function startJob(
  engine: EngineInfo,
  target: string,
  items: JobItemRequest[],
  plan: JobPlan,
  confirm?: string,
): Promise<Job> {
  const response = await fetch(`${engine.url}/api/v1/jobs`, {
    method: "POST",
    headers: headers(engine),
    body: JSON.stringify({ kind: "export", target, items, digest: plan.digest, confirm }),
  });
  if (response.status === 412) {
    // {reason, plan}: a pre-flight check failed or the plan changed since the preview.
    const text = await response.text();
    let reason = text.trim();
    try {
      reason = (JSON.parse(text) as { reason?: string }).reason ?? reason;
    } catch {
      // Plain text: keep it.
    }
    throw new HttpError(412, reason || "HTTP 412");
  }
  if (!response.ok) {
    throw await httpError(response);
  }
  return (await response.json()) as Job;
}

export async function getJob(engine: EngineInfo, id: string, signal?: AbortSignal): Promise<Job> {
  const response = await fetch(`${engine.url}/api/v1/jobs/${encodeURIComponent(id)}`, {
    headers: headers(engine),
    signal,
  });
  if (!response.ok) {
    throw await httpError(response);
  }
  return (await response.json()) as Job;
}

/** Asks the engine to stop the job (202): it ends with job.cancelled. */
export async function cancelJob(engine: EngineInfo, id: string): Promise<void> {
  const response = await fetch(`${engine.url}/api/v1/jobs/${encodeURIComponent(id)}/cancel`, {
    method: "POST",
    headers: headers(engine),
  });
  if (!response.ok && response.status !== 409) {
    throw await httpError(response);
  }
}

const ENDED = new Set(["succeeded", "failed", "cancelled", "interrupted"]);

/**
 * Follows a job until it ends: every event of its stream asks for a fresh
 * snapshot (at most one at a time), mapped onto the items of the wizard.
 * Aborting stops following; the job goes on in the engine.
 */
export async function followJob(
  engine: EngineInfo,
  job: Job,
  plan: JobPlan,
  onChange: (items: ExportItemRun[]) => void,
  signal: AbortSignal,
  bucket?: string,
): Promise<ExportItemRun[]> {
  let items = mapJob(job, plan, bucket);
  onChange(items);
  let reading: Promise<void> | null = null;
  let again = false;
  const refresh = async () => {
    if (reading) {
      again = true;
      return reading;
    }
    reading = (async () => {
      do {
        again = false;
        const snapshot = await getJob(engine, job.id, signal);
        items = mapJob(snapshot, plan, bucket);
        onChange(items);
      } while (again && !signal.aborted);
    })().finally(() => {
      reading = null;
    });
    return reading;
  };

  const response = await fetch(`${engine.url}/api/v1/jobs/${encodeURIComponent(job.id)}/events`, {
    headers: headers(engine),
    signal,
  });
  if (!response.ok || !response.body) {
    throw await httpError(response);
  }
  let ended = false;
  const parse = createSseParser((message) => {
    if (message.event.startsWith("job.") && ENDED.has(message.event.slice(4))) {
      ended = true;
    }
    void refresh().catch(() => {});
  });
  const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
  while (!ended) {
    const { value, done } = await reader.read();
    if (done) {
      break;
    }
    parse(value);
  }
  await reader.cancel().catch(() => {});
  await refresh();
  return items;
}

const STEP_STATUS: Record<string, StepStatus> = {
  pending: "skipped",
  waiting: "running",
  running: "running",
  succeeded: "ok",
  failed: "failed",
  skipped: "skipped",
  cancelled: "skipped",
  interrupted: "failed",
};

const ITEM_STATE: Record<string, ItemState> = {
  queued: "queued",
  running: "running",
  succeeded: "succeeded",
  failed: "failed",
  cancelled: "cancelled",
  interrupted: "failed",
};

/**
 * A job snapshot as the progress and result steps show it. The S3 URI is
 * s3://<bucket>/<planned key> (the key is stable between preview and start).
 */
export function mapJob(job: Job, plan: JobPlan, bucket?: string): ExportItemRun[] {
  return job.items.map((item) => {
    const planned = plan.items.find((p) => p.database === item.name);
    const s3 = bucket && planned?.key ? `s3://${bucket}/${planned.key}` : undefined;
    const downloadStep = item.steps.find((s) => s.id === "download");
    const run: ExportItemRun = {
      database: item.name,
      key: planned?.key ?? "",
      state: ITEM_STATE[item.state] ?? "running",
      step: item.step === "download" ? "download" : "backup",
      steps: item.steps
        .filter((s) => s.status !== "pending")
        .map((s) => ({
          step: s.id === "download" ? "download:download" : s.id,
          label: s.title,
          status: STEP_STATUS[s.status] ?? "running",
          detail: s.detail,
        })),
      task: item.task && {
        id: item.task.id,
        type: item.task.type ?? "BACKUP_DB",
        database: item.name,
        percent: item.task.percent,
        lifecycle: item.task.lifecycle,
      },
      transfer: item.transfer && {
        done: item.transfer.bytes,
        total: item.transfer.total,
        bytesPerSecond: item.transfer.bytesPerSecond,
        etaSeconds: item.transfer.etaSeconds,
      },
    };
    if (s3 && (item.state === "succeeded" || downloadStep)) {
      run.s3Uri = s3;
    }
    if (item.state === "succeeded" && planned?.file) {
      run.localFile = planned.file;
      run.note = downloadStep?.detail;
    }
    if (item.error) {
      run.error = new Error(item.error);
    }
    if (item.taskLeftRunning) {
      run.taskLeftRunning = true;
    }
    return run;
  });
}
