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

/* First run and configuration (engine firstrun.go). */

export interface InstallHint {
  distro: "fedora" | "debian";
  command: string;
}

/** A program the engine runs: session-manager-plugin or the container runtime. */
export interface Prerequisite {
  id: "session-manager-plugin" | "container-runtime";
  program: string;
  found: boolean;
  path?: string;
  version?: string;
  purpose: string;
  install: InstallHint[];
}

export interface AwsProfile {
  name: string;
  region?: string;
}

/** An RDS SQL Server instance a profile sees. */
export interface DiscoveredInstance {
  identifier: string;
  endpoint?: string;
  port?: number;
  engine: string;
  engineVersion: string;
  class: string;
  status: string;
  masterUser?: string;
  optionGroup?: string;
  /** The option group has SQLSERVER_BACKUP_RESTORE. */
  backupRestore: boolean;
}

export interface InstanceDiscovery {
  profile: string;
  region: string;
  accountId: string;
  instances: DiscoveredInstance[];
}

/** POST /api/v1/config/targets: never a password, only where to find it. */
export interface TargetInput {
  name: string;
  accountId: string;
  region: string;
  profile: string;
  instance: string;
  bucket: string;
  sqlUser: string;
  /** "prompt" or "env:VAR". */
  passwordSource: string;
  connectMode: "external" | "direct" | "tunnel";
  localPort?: number;
  protected: boolean;
  replace?: boolean;
}

async function getJSON<T>(engine: EngineInfo, path: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(`${engine.url}${path}`, { headers: authHeaders(engine), signal });
  if (!response.ok) {
    throw await failure(response);
  }
  return (await response.json()) as T;
}

export async function getPrerequisites(engine: EngineInfo): Promise<Prerequisite[]> {
  return (await getJSON<{ prerequisites: Prerequisite[] }>(engine, "/api/v1/prerequisites"))
    .prerequisites;
}

export async function listProfiles(engine: EngineInfo): Promise<AwsProfile[]> {
  return (await getJSON<{ profiles: AwsProfile[] }>(engine, "/api/v1/discovery/profiles")).profiles;
}

/** The SQL Server instances of a profile in a region, with the account it points to. */
export function discoverInstances(
  engine: EngineInfo,
  profile: string,
  region: string,
  signal?: AbortSignal,
): Promise<InstanceDiscovery> {
  const query = new URLSearchParams({ profile, region });
  return getJSON<InstanceDiscovery>(engine, `/api/v1/discovery/instances?${query}`, signal);
}

/** Adds a target to the configuration file (or replaces it with replace). */
export async function saveTarget(engine: EngineInfo, input: TargetInput): Promise<TargetSummary> {
  const response = await fetch(`${engine.url}/api/v1/config/targets`, {
    method: "POST",
    headers: { ...authHeaders(engine), "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
  if (!response.ok) {
    throw await failure(response);
  }
  return (await response.json()) as TargetSummary;
}

/** Removes a target; confirm repeats its name. */
export async function removeTarget(engine: EngineInfo, name: string): Promise<void> {
  const query = new URLSearchParams({ confirm: name });
  const response = await fetch(
    `${engine.url}/api/v1/config/targets/${encodeURIComponent(name)}?${query}`,
    { method: "DELETE", headers: authHeaders(engine) },
  );
  if (!response.ok) {
    throw await failure(response);
  }
}
