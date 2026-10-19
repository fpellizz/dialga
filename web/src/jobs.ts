import { failure as apiFailure, type Lifecycle, type StepEvent } from "./api";
import type { EngineInfo } from "./engine";
import { createSseParser } from "./sse";

/* Import/export jobs: GET /api/v1/jobs... (engine internal/jobs, api/jobs.go). */

export type JobState = "queued" | "running" | "succeeded" | "failed" | "cancelled" | "interrupted";

export type JobStepStatus =
  | "pending"
  | "waiting"
  | "running"
  | "succeeded"
  | "failed"
  | "skipped"
  | "cancelled"
  | "interrupted";

/** An S3 upload or download; speed and ETA computed by the engine. */
export interface TransferProgress {
  direction: "upload" | "download";
  bytes: number;
  total: number;
  bytesPerSecond: number;
  /** Absent while the speed is not known. */
  etaSeconds?: number;
}

/** The last known state of a native RDS task. */
export interface TaskProgress {
  id: number;
  type?: string;
  lifecycle: Lifecycle;
  percent: number;
}

/** What resuming the upload of a database needs. */
export interface MultipartUpload {
  bucket: string;
  key: string;
  uploadId: string;
  size: number;
  partSize: number;
  partsDone: number[];
}

export interface JobStep {
  id: string;
  title: string;
  resource?: string;
  status: JobStepStatus;
  startedAt?: string;
  endedAt?: string;
  detail?: string;
}

/** The checksum of the file a database uploaded or downloaded. */
export interface JobChecksum {
  algorithm: string;
  /** Base64 of the checksum of the whole object. */
  value?: string;
  /** The checksum computed here matches the one S3 keeps. */
  verified: boolean;
  /** Why it was not verified. */
  note?: string;
}

/** One database of a job. */
export interface JobItem {
  name: string;
  state: JobState;
  /** The current step, or the last one reached. */
  step?: string;
  percent: number;
  steps: JobStep[];
  transfer?: TransferProgress;
  task?: TaskProgress;
  taskIds?: number[];
  taskLeftRunning?: boolean;
  upload?: MultipartUpload;
  checksum?: JobChecksum;
  error?: string;
  result?: string;
}

export interface JobEvent {
  seq: number;
  time: string;
  type: string;
  item?: string;
  step?: string;
  message?: string;
  level?: "info" | "warn" | "error";
  transfer?: TransferProgress;
  task?: TaskProgress;
}

/** An import, an export, or one operation of the Databases page or the CLI. */
export type JobKind = "import" | "export" | "operation";

/** What a job of kind "operation" does. */
export type JobOperation = "restore" | "export" | "drop" | "finish" | "cancel" | "post";

/** Where the job started: this app or the dialga CLI. */
export type JobOrigin = "app" | "cli";

/** A Run the engine refused before running anything: the job is recorded as failed. */
export interface JobRefusal {
  /** The HTTP status of the refusal: 412, 422, 428 or 502. */
  status: number;
  reason: string;
  checks?: StepEvent[];
}

export interface JobSummary {
  id: string;
  kind: JobKind;
  operation?: JobOperation;
  origin: JobOrigin;
  refusal?: JobRefusal;
  target: string;
  instance: string;
  state: JobState;
  createdAt: string;
  updatedAt: string;
  endedAt?: string;
  cancelRequested?: boolean;
  databases: string[];
  succeeded: number;
  failed: number;
}

export interface Job {
  id: string;
  kind: JobKind;
  operation?: JobOperation;
  origin: JobOrigin;
  refusal?: JobRefusal;
  target: string;
  instance: string;
  maxTasks: number;
  state: JobState;
  createdAt: string;
  updatedAt: string;
  endedAt?: string;
  cancelRequested?: boolean;
  items: JobItem[];
  /** The log: no transfer progress, no task percentages. */
  events: JobEvent[];
}

/** An answer of the engine that is not 2xx, with its status. */
export class HttpError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
  }
}

function headers(engine: EngineInfo, extra?: Record<string, string>): HeadersInit {
  return { Authorization: `Bearer ${engine.token}`, ...extra };
}

async function failure(response: Response): Promise<Error> {
  // 428: the SQL password is needed, with the target; the dialog asks for it.
  if (response.status === 428) {
    return apiFailure(response);
  }
  const text = (await response.text()).trim();
  return new HttpError(text || `HTTP ${response.status}`, response.status);
}

const path = (id: string) => `/api/v1/jobs/${encodeURIComponent(id)}`;

export async function listJobs(engine: EngineInfo, signal?: AbortSignal): Promise<JobSummary[]> {
  const response = await fetch(`${engine.url}/api/v1/jobs`, { headers: headers(engine), signal });
  if (!response.ok) {
    throw await failure(response);
  }
  return ((await response.json()) as { jobs: JobSummary[] }).jobs;
}

export async function getJob(engine: EngineInfo, id: string, signal?: AbortSignal): Promise<Job> {
  const response = await fetch(`${engine.url}${path(id)}`, { headers: headers(engine), signal });
  if (!response.ok) {
    throw await failure(response);
  }
  return (await response.json()) as Job;
}

/** Asks the job to stop; it ends with a job.cancelled event once its steps have returned. */
export async function cancelJob(engine: EngineInfo, id: string): Promise<JobSummary> {
  const response = await fetch(`${engine.url}${path(id)}/cancel`, {
    method: "POST",
    headers: headers(engine),
  });
  if (!response.ok) {
    throw await failure(response);
  }
  return (await response.json()) as JobSummary;
}

/**
 * Resumes an interrupted job (same id): the answer is the job queued again,
 * to follow with followJob. confirm is the target name of a protected target.
 */
export async function resumeJob(engine: EngineInfo, id: string, confirm?: string): Promise<Job> {
  const response = await fetch(`${engine.url}${path(id)}/resume`, {
    method: "POST",
    headers: headers(engine, { "Content-Type": "application/json" }),
    body: JSON.stringify(confirm ? { confirm } : {}),
  });
  if (!response.ok) {
    throw await failure(response);
  }
  return (await response.json()) as Job;
}

const FINAL = new Set(["job.succeeded", "job.failed", "job.cancelled", "job.interrupted"]);

/** The event that ends a job (and its event stream). */
export function isFinalEvent(event: JobEvent): boolean {
  return FINAL.has(event.type);
}

export function isJobActive(state: JobState): boolean {
  return state === "queued" || state === "running";
}

/** The state to show: a refused job has the state failed, but never ran. */
export type JobShownState = JobState | "refused";

export function shownState(job: { state: JobState; refusal?: JobRefusal }): JobShownState {
  return job.refusal ? "refused" : job.state;
}

export type JobFilter = "all" | "running" | "failed" | "refused";

export const JOB_FILTERS: JobFilter[] = ["all", "running", "failed", "refused"];

/** Running: queued or running; Failed: failed while running; Refused: never ran. */
export function matchesFilter(job: JobSummary, filter: JobFilter): boolean {
  switch (filter) {
    case "running":
      return isJobActive(job.state);
    case "failed":
      return job.state === "failed" && !job.refusal;
    case "refused":
      return !!job.refusal;
    default:
      return true;
  }
}

export function filterCounts(list: JobSummary[]): Record<JobFilter, number> {
  return {
    all: list.length,
    running: list.filter((j) => matchesFilter(j, "running")).length,
    failed: list.filter((j) => matchesFilter(j, "failed")).length,
    refused: list.filter((j) => matchesFilter(j, "refused")).length,
  };
}

/** The refusals the engine records as jobs, when a Run is refused. */
const RECORDED = new Set([412, 422, 428, 502]);

/** A failed Run (or start) that the engine refused and recorded in Activity. */
export function recordedRefusal(error: unknown): boolean {
  const status = (error as { status?: unknown } | null)?.status;
  return typeof status === "number" && RECORDED.has(status);
}

/**
 * Streams the events of a job after seq `after` (Last-Event-ID), calling
 * onEvent for each, until the stream closes. Resolves true when the final
 * job.* event arrived, false when the stream closed before it (reconnect).
 */
export async function followJob(
  engine: EngineInfo,
  id: string,
  after: number,
  onEvent: (event: JobEvent) => void,
  signal?: AbortSignal,
): Promise<boolean> {
  const extra: Record<string, string> = after > 0 ? { "Last-Event-ID": String(after) } : {};
  const response = await fetch(`${engine.url}${path(id)}/events`, {
    headers: headers(engine, extra),
    signal,
  });
  if (!response.ok || !response.body) {
    throw await failure(response);
  }
  let ended = false;
  const parse = createSseParser((message) => {
    const event = JSON.parse(message.data) as JobEvent;
    ended = ended || isFinalEvent(event);
    onEvent(event);
  });
  const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
  for (;;) {
    const { value, done } = await reader.read();
    if (done) {
      return ended;
    }
    parse(value);
  }
}

/** The highest seq in the log of a job: where its event stream resumes. */
export function lastSeq(job: Job): number {
  return job.events.reduce((seq, event) => Math.max(seq, event.seq), 0);
}

/** Active jobs first, then the history; newest first in both. */
export function sortJobs(list: JobSummary[]): JobSummary[] {
  return [...list].sort((a, b) => {
    const active = Number(isJobActive(b.state)) - Number(isJobActive(a.state));
    return active || b.createdAt.localeCompare(a.createdAt) || b.id.localeCompare(a.id);
  });
}

function percentOf(transfer: TransferProgress): number {
  return transfer.total > 0
    ? Math.min(100, Math.floor((transfer.bytes * 100) / transfer.total))
    : 0;
}

const STEP_STATUS: Record<string, JobStepStatus> = {
  "step.waiting": "waiting",
  "step.started": "running",
  "step.succeeded": "succeeded",
  "step.failed": "failed",
  "step.skipped": "skipped",
  "step.cancelled": "cancelled",
  "step.interrupted": "interrupted",
};

/**
 * The job after one live event, the way the engine changes it. Progress
 * events update the databases but stay out of the log, like in the engine;
 * a task.status event is logged when the task or its lifecycle changes.
 */
export function applyEvent(job: Job, event: JobEvent): Job {
  const next: Job = { ...job, updatedAt: event.time };
  const index = event.item === undefined ? -1 : job.items.findIndex((i) => i.name === event.item);
  const item = index >= 0 ? job.items[index] : undefined;
  let logged = true;
  let changed: JobItem | undefined;

  const status = STEP_STATUS[event.type];
  if (status && item) {
    const steps = item.steps.map((s): JobStep => {
      if (s.id !== event.step) {
        return s;
      }
      const step: JobStep = { ...s, status };
      if (status === "running") {
        step.startedAt = event.time;
      } else if (status !== "waiting" && status !== "skipped") {
        step.endedAt = event.time;
      }
      if ((status === "succeeded" || status === "failed") && event.message) {
        step.detail = event.message;
      }
      return step;
    });
    changed = { ...item, steps };
    switch (status) {
      case "waiting":
        changed.step = event.step;
        break;
      case "running":
        Object.assign(changed, { step: event.step, state: "running", percent: 0 });
        changed.transfer = undefined;
        break;
      case "succeeded":
        changed.percent = 100;
        if (item.steps.at(-1)?.id === event.step) {
          changed.state = "succeeded";
        }
        break;
      case "failed":
        Object.assign(changed, { state: "failed", error: event.message });
        break;
      case "cancelled":
      case "interrupted":
        changed.state = status;
        break;
    }
  } else if (event.type === "upload.progress" && item && event.transfer) {
    logged = false;
    changed = { ...item, transfer: event.transfer, percent: percentOf(event.transfer) };
  } else if (event.type === "task.status" && item && event.task) {
    const prev = item.task;
    logged = !prev || prev.id !== event.task.id || prev.lifecycle !== event.task.lifecycle;
    const ids = item.taskIds ?? [];
    changed = {
      ...item,
      task: event.task,
      percent: event.task.percent,
      taskIds: ids.includes(event.task.id) ? ids : [...ids, event.task.id],
    };
  }

  switch (event.type) {
    case "job.started":
      next.state = "running";
      break;
    case "step.started":
      if (next.state === "queued") {
        next.state = "running";
      }
      break;
    case "job.cancelling":
      next.cancelRequested = true;
      break;
    default:
      if (isFinalEvent(event)) {
        next.state = event.type.slice("job.".length) as JobState;
        next.endedAt = event.time;
      }
  }
  if (changed) {
    next.items = job.items.map((it, i) => (i === index ? (changed as JobItem) : it));
  }
  if (logged) {
    next.events = [...job.events, event];
  }
  return next;
}

/** What resuming an interrupted database needs, as the journal keeps it. */
export interface ResumeNeeds {
  item: string;
  lastStep?: string;
  pending: string[];
  upload?: MultipartUpload;
  /** Parts of the whole upload, when its size and part size are known. */
  partsTotal?: number;
  taskIds: number[];
  taskLeftRunning: boolean;
}

export function resumeNeeds(job: Job): ResumeNeeds[] {
  return job.items
    .filter((it) => it.state !== "succeeded")
    .map((it) => {
      const step = it.steps.find((s) => s.id === it.step);
      const up = it.upload;
      return {
        item: it.name,
        lastStep: step?.title ?? it.step,
        pending: it.steps
          .filter((s) => s.status === "pending" || s.status === "interrupted")
          .map((s) => s.title),
        upload: up,
        partsTotal:
          up && up.size > 0 && up.partSize > 0 ? Math.ceil(up.size / up.partSize) : undefined,
        taskIds: it.taskIds ?? [],
        taskLeftRunning: it.taskLeftRunning ?? false,
      };
    });
}

const pad = (n: number) => String(n).padStart(2, "0");

/** 2026-10-16T09:00:00Z as 2026-10-16 11:00 in the local zone ("" if missing). */
export function formatDateTime(iso?: string): string {
  if (!iso) {
    return "";
  }
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return iso;
  }
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** The local time of day, 11:00:05. */
export function formatClock(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return iso;
  }
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

/** A duration for an ETA: 45 s, 12 min, 1 h 05 min. */
export function formatDuration(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  if (s < 60) {
    return `${s} s`;
  }
  const minutes = Math.round(s / 60);
  if (minutes < 60) {
    return `${minutes} min`;
  }
  return `${Math.floor(minutes / 60)} h ${pad(minutes % 60)} min`;
}

/** The log as plain text for the clipboard: stable, in English like the engine. */
export function logText(job: Job): string {
  const lines = [
    `Dialga job ${job.id}: ${job.operation ?? job.kind} on ${job.target} (instance ${job.instance}), ${job.state}`,
    `Created ${job.createdAt}${job.endedAt ? `, ended ${job.endedAt}` : ""}`,
    "",
  ];
  for (const event of job.events) {
    const where = [event.item, event.step].filter(Boolean).join("/");
    const parts = [event.time, event.type];
    if (where) {
      parts.push(where);
    }
    if (event.level && event.level !== "info") {
      parts.push(event.level.toUpperCase());
    }
    if (event.task) {
      parts.push(`task ${event.task.id} ${event.task.lifecycle} ${event.task.percent}%`);
    }
    if (event.message) {
      parts.push(event.message);
    }
    lines.push(parts.join("  "));
  }
  return `${lines.join("\n")}\n`;
}
