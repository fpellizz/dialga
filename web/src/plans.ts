import { authHeaders, failure, type StepEvent, type Task } from "./api";
import type { EngineInfo } from "./engine";
import { readEventStream } from "./sse";

// Client of the native operations (engine api/ops.go): a plan to preview,
// the same plan to execute over SSE, the post-restore report.

export type Operation = "restore" | "export" | "finish" | "drop" | "cancel";

/** One action of a plan (core.Action). */
export interface Action {
  id: string;
  title: string;
  kind: "sql" | "aws";
  risk: "change" | "destructive";
  /** The T-SQL or the AWS call, as the dry-run prints it. */
  statement: string;
}

/** What an operation will do (core.Plan). */
export interface Plan {
  operation: string;
  target: string;
  instance: string;
  protected: boolean;
  /** What a destructive plan removes (the database name). */
  subject?: string;
  actions: Action[];
}

/** The body of POST .../plans/{operation}. */
export interface PlanRequest {
  database?: string;
  key?: string;
  differential?: boolean;
  noRecovery?: boolean;
  taskId?: number;
}

/** POST .../plans/{operation}: nothing is changed. */
export interface PlanPreview {
  plan: Plan;
  /** The read-only pre-flight checks; one failed makes ready false. */
  checks: StepEvent[];
  ready: boolean;
  /** Text to retype to execute it, if any. */
  confirmation?: string;
  digest: string;
  /** The dry-run text, as the CLI prints it. */
  preview: string;
  /** The body with the defaults resolved: what execute must send. */
  request: PlanRequest;
}

export interface ExecuteRequest extends PlanRequest {
  confirm?: string;
  /** The digest of the preview: 412 when the plan changed since. */
  digest?: string;
  /** Return once the native task is started instead of following it. */
  noWait?: boolean;
}

/** The final "done" of an execution. */
export interface PlanDone {
  ok: boolean;
  error?: string;
  /** The native task the operation started, last state read. */
  task?: Task;
}

export interface PlanHandlers {
  /** Pre-flight warnings, before the steps. */
  onNotice?: (message: string) => void;
  onStep: (event: StepEvent) => void;
  /** Every change of the followed native task. */
  onTask?: (task: Task) => void;
}

function planUrl(engine: EngineInfo, target: string, operation: Operation): string {
  return `${engine.url}/api/v1/targets/${encodeURIComponent(target)}/plans/${operation}`;
}

/** The plan of an operation after its pre-flight checks (read-only). */
export async function previewPlan(
  engine: EngineInfo,
  target: string,
  operation: Operation,
  request: PlanRequest,
  signal?: AbortSignal,
): Promise<PlanPreview> {
  const response = await fetch(planUrl(engine, target, operation), {
    method: "POST",
    headers: { ...authHeaders(engine), "Content-Type": "application/json" },
    body: JSON.stringify(request),
    signal,
  });
  if (!response.ok) {
    throw await failure(response);
  }
  return (await response.json()) as PlanPreview;
}

/**
 * Executes the plan the preview showed. The engine builds it again and
 * answers 412 when a check fails or the digest changed, 422 without the
 * confirmation (HttpError.status); then it streams the steps and the task.
 */
export async function executePlan(
  engine: EngineInfo,
  target: string,
  operation: Operation,
  request: ExecuteRequest,
  handlers: PlanHandlers,
  signal?: AbortSignal,
): Promise<PlanDone> {
  const response = await fetch(`${planUrl(engine, target, operation)}/execute`, {
    method: "POST",
    headers: { ...authHeaders(engine), "Content-Type": "application/json" },
    body: JSON.stringify(request),
    signal,
  });
  if (!response.ok || !response.body) {
    throw await failure(response);
  }
  let done: PlanDone = { ok: false, error: "the execution ended without an outcome" };
  await readEventStream(response.body, (message) => {
    const data = JSON.parse(message.data);
    if (message.event === "notice") {
      handlers.onNotice?.((data as { message: string }).message);
    } else if (message.event === "step") {
      handlers.onStep(data as StepEvent);
    } else if (message.event === "task") {
      handlers.onTask?.(data as Task);
    } else if (message.event === "done") {
      done = data as PlanDone;
    }
  });
  return done;
}

/** A file of a restored database (sys.master_files). */
export interface ReportFile {
  type: string;
  name: string;
  sizeMB: number;
  maxSizeMB?: number;
  physicalName: string;
}

/** GET .../databases/{db}/report: the post-restore report (read-only). */
export interface DatabaseReport {
  target: string;
  instance: string;
  /** False when a finding failed (not online, the login cannot enter). */
  ok: boolean;
  /** database, access, orphans, windows. */
  findings: StepEvent[];
  database: {
    name: string;
    state: string;
    recoveryModel: string;
    compatibilityLevel: number;
    collation?: string;
    owner?: string;
    userAccess: string;
    readOnly: boolean;
  };
  files: ReportFile[];
  access: boolean;
  orphanUsers: string[];
  windowsPrincipals: number;
  objects: { type: string; count: number }[];
}

export async function getDatabaseReport(
  engine: EngineInfo,
  target: string,
  database: string,
  signal?: AbortSignal,
): Promise<DatabaseReport> {
  const response = await fetch(
    `${engine.url}/api/v1/targets/${encodeURIComponent(target)}/databases/${encodeURIComponent(database)}/report`,
    { headers: authHeaders(engine), signal },
  );
  if (!response.ok) {
    throw await failure(response);
  }
  return (await response.json()) as DatabaseReport;
}
