import type { EngineInfo } from "./engine";
import { createSseParser } from "./sse";

export type StepStatus = "running" | "ok" | "warning" | "failed" | "skipped";

export interface StepEvent {
  step: string;
  label: string;
  status: StepStatus;
  detail?: string;
}

/** A configured target as the engine exposes it: no password reference. */
export interface TargetSummary {
  name: string;
  instance: string;
  region: string;
  profile: string;
  accountId: string;
  bucket: string;
  sqlUser: string;
  connectMode: "external" | "direct" | "tunnel";
  localPort?: number;
  protected: boolean;
}

/** GET /api/v1/targets: an empty list with no error means "no configuration yet". */
export interface TargetList {
  configPath: string;
  error?: string;
  targets: TargetSummary[];
}

export type Lifecycle =
  | "CREATED"
  | "IN_PROGRESS"
  | "SUCCESS"
  | "ERROR"
  | "CANCEL_REQUESTED"
  | "CANCELLED";

/** A native backup/restore task (msdb.dbo.rds_fn_task_status). */
export interface Task {
  id: number;
  type: string;
  database: string;
  percent: number;
  lifecycle: Lifecycle;
  info?: string;
  /** Server wall-clock time without a zone: 2006-01-02T15:04:05. */
  createdAt?: string;
  updatedAt?: string;
  s3Object?: string;
}

/** The task has not reached a final state yet. */
export function isActive(task: Task): boolean {
  return (
    task.lifecycle === "CREATED" ||
    task.lifecycle === "IN_PROGRESS" ||
    task.lifecycle === "CANCEL_REQUESTED"
  );
}

/** The message of a failed call, without the "Error: " prefix of String(error). */
export function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function authHeaders(engine: EngineInfo): HeadersInit {
  return { Authorization: `Bearer ${engine.token}` };
}

async function failure(response: Response): Promise<Error> {
  return new Error((await response.text()).trim() || `HTTP ${response.status}`);
}

export async function listTargets(engine: EngineInfo): Promise<TargetList> {
  const response = await fetch(`${engine.url}/api/v1/targets`, { headers: authHeaders(engine) });
  if (!response.ok) {
    throw await failure(response);
  }
  return (await response.json()) as TargetList;
}

/** The native tasks of a configured target, newest first. */
export async function listTasks(
  engine: EngineInfo,
  name: string,
  signal?: AbortSignal,
): Promise<Task[]> {
  const response = await fetch(`${engine.url}/api/v1/targets/${encodeURIComponent(name)}/tasks`, {
    headers: authHeaders(engine),
    signal,
  });
  if (!response.ok) {
    throw await failure(response);
  }
  return ((await response.json()) as { tasks: Task[] }).tasks;
}

/**
 * Runs the read-only checks of a configured target, reporting each step as it
 * starts and ends. onNotice receives warnings sent before the steps (e.g. no
 * SQL password in the engine environment).
 */
export async function checkTarget(
  engine: EngineInfo,
  name: string,
  onStep: (event: StepEvent) => void,
  onNotice: (message: string) => void,
  signal?: AbortSignal,
): Promise<boolean> {
  const response = await fetch(`${engine.url}/api/v1/targets/${encodeURIComponent(name)}/checks`, {
    method: "POST",
    headers: authHeaders(engine),
    signal,
  });
  if (!response.ok || !response.body) {
    throw await failure(response);
  }
  let passed = false;
  const parse = createSseParser((message) => {
    if (message.event === "step") {
      onStep(JSON.parse(message.data) as StepEvent);
    } else if (message.event === "notice") {
      onNotice((JSON.parse(message.data) as { message: string }).message);
    } else if (message.event === "done") {
      passed = (JSON.parse(message.data) as { ok: boolean }).ok;
    }
  });
  const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
  for (;;) {
    const { value, done } = await reader.read();
    if (done) {
      return passed;
    }
    parse(value);
  }
}

/** A user database of an instance (sys.databases); sizes in MiB. */
export interface Database {
  name: string;
  /** state_desc: ONLINE, RESTORING, RECOVERING, RECOVERY_PENDING, SUSPECT, EMERGENCY, OFFLINE. */
  state: string;
  /** FULL, SIMPLE or BULK_LOGGED. */
  recovery: string;
  compatibility: number;
  /** Absent while the database is offline. */
  collation?: string;
  /** Absent when no login has the owner SID. */
  owner?: string;
  userAccess: string;
  readOnly: boolean;
  /** Server wall-clock time without a zone: 2006-01-02T15:04:05. */
  createdAt: string;
  dataMB: number;
  logMB: number;
}

/** The user databases of a configured target, by name. */
export async function listDatabases(
  engine: EngineInfo,
  name: string,
  signal?: AbortSignal,
): Promise<Database[]> {
  const response = await fetch(
    `${engine.url}/api/v1/targets/${encodeURIComponent(name)}/databases`,
    { headers: authHeaders(engine), signal },
  );
  if (!response.ok) {
    throw await failure(response);
  }
  return ((await response.json()) as { databases: Database[] }).databases;
}

/** GET /api/v1/targets/{name}/overview: the AWS part always, the SQL part when sql is "ok". */
export interface Overview {
  target: string;
  instance: string;
  status: string;
  engine: string;
  engineVersion: string;
  class: string;
  allocatedGiB: number;
  /** Storage autoscaling limit; absent without autoscaling. */
  maxAllocatedGiB?: number;
  /** Free storage from CloudWatch; freeError says why it is missing. */
  freeBytes?: number;
  freeError?: string;
  sql: "ok" | "skipped" | "failed";
  /** Why the SQL part is skipped or failed. */
  sqlDetail?: string;
  userDatabases?: number;
  /** Native tasks queued, running or being cancelled. */
  activeTasks?: number;
}

/** The state of a configured target's RDS instance. */
export async function getOverview(
  engine: EngineInfo,
  name: string,
  signal?: AbortSignal,
): Promise<Overview> {
  const response = await fetch(
    `${engine.url}/api/v1/targets/${encodeURIComponent(name)}/overview`,
    { headers: authHeaders(engine), signal },
  );
  if (!response.ok) {
    throw await failure(response);
  }
  return (await response.json()) as Overview;
}
