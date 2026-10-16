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

/** A call the engine answered with an error: its message and HTTP status. */
export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

async function failure(response: Response): Promise<Error> {
  return new ApiError(response.status, (await response.text()).trim() || `HTTP ${response.status}`);
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
  /** Daily automated backup window, UTC: "04:29-04:59". */
  backupWindow?: string;
  /** Weekly maintenance window, UTC: "sun:05:00-sun:05:30". */
  maintenanceWindow?: string;
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
  /** Found but too old for what needs it (the integrated tunnel). */
  outdated?: boolean;
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

export type TunnelStatus = "connecting" | "ready" | "reconnecting" | "closed";

/** The engine's shared tunnel of a tunnel-mode target. */
export interface TunnelState {
  target: string;
  status: TunnelStatus;
  /** Port on 127.0.0.1. */
  localPort?: number;
  /** host:port of the RDS instance. */
  endpoint?: string;
  /** Instance id of the bridge of the session. */
  bridge?: string;
  /** The bridge and endpoint when ready, the failure when reconnecting or closed. */
  detail?: string;
  /** When status began; absent for a tunnel the engine never opened. */
  since?: string;
  /** SQL sessions going through it now. */
  users: number;
  /** Opened on request: no idle close. */
  keepOpen: boolean;
  /** Closed because the last attempt to open it failed. */
  failed?: boolean;
}

function tunnelURL(engine: EngineInfo, name: string): string {
  return `${engine.url}/api/v1/targets/${encodeURIComponent(name)}/tunnel`;
}

/** The shared tunnels of every tunnel-mode target, open or not. */
export async function listTunnels(
  engine: EngineInfo,
  signal?: AbortSignal,
): Promise<TunnelState[]> {
  const response = await fetch(`${engine.url}/api/v1/tunnels`, {
    headers: authHeaders(engine),
    signal,
  });
  if (!response.ok) {
    throw await failure(response);
  }
  return ((await response.json()) as { tunnels: TunnelState[] }).tunnels;
}

/** Opens the tunnel and keeps it open; resolves once it is ready. */
export async function openTunnel(engine: EngineInfo, name: string): Promise<TunnelState> {
  const response = await fetch(tunnelURL(engine, name), {
    method: "POST",
    headers: authHeaders(engine),
  });
  if (!response.ok) {
    throw await failure(response);
  }
  return (await response.json()) as TunnelState;
}

/** Closes the tunnel now, even while sessions use it. */
export async function closeTunnel(engine: EngineInfo, name: string): Promise<TunnelState> {
  const response = await fetch(tunnelURL(engine, name), {
    method: "DELETE",
    headers: authHeaders(engine),
  });
  if (!response.ok) {
    throw await failure(response);
  }
  return (await response.json()) as TunnelState;
}

/**
 * Follows the state of the tunnel: the current one first, then every change.
 * Resolves when the stream ends (the engine stopped), rejects when it fails.
 */
export async function watchTunnel(
  engine: EngineInfo,
  name: string,
  onState: (state: TunnelState) => void,
  signal: AbortSignal,
): Promise<void> {
  const response = await fetch(`${tunnelURL(engine, name)}/events`, {
    headers: authHeaders(engine),
    signal,
  });
  if (!response.ok || !response.body) {
    throw await failure(response);
  }
  const parse = createSseParser((message) => {
    if (message.event === "state") {
      onState(JSON.parse(message.data) as TunnelState);
    }
  });
  const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
  for (;;) {
    const { value, done } = await reader.read();
    if (done) {
      return;
    }
    parse(value);
  }
}
