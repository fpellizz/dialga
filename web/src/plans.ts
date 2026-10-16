/**
 * The native operations of the engine (restore, export, finish, cancel,
 * drop): a plan to preview, the same plan to execute over Server-Sent
 * Events, and the post-restore report of a database. Pure helpers of the
 * plan dialog live here too.
 */
import { ApiError, authHeaders, failure, type Lifecycle, type StepEvent, type Task } from "./api";
import type { EngineInfo } from "./engine";
import type { Hint } from "./hints";
import type { MessageKey } from "./i18n";
import { readEventStream } from "./sse";

export type Operation = "restore" | "export" | "finish" | "cancel" | "drop";

/** The parameters of an operation (POST .../plans/{operation}). */
export interface PlanRequest {
  database?: string;
  /** S3 key: required by restore, optional for export (the engine picks a default). */
  key?: string;
  differential?: boolean;
  noRecovery?: boolean;
  taskId?: number;
}

/** The body of .../execute: the previewed request, the confirmation and the digest. */
export interface ExecuteRequest extends PlanRequest {
  confirm?: string;
  digest?: string;
  noWait?: boolean;
}

export interface PlanAction {
  id: string;
  title: string;
  kind: "sql" | "aws";
  risk: "change" | "destructive";
  /** The exact T-SQL the action runs. */
  statement: string;
}

export interface Plan {
  operation: string;
  target: string;
  instance: string;
  protected: boolean;
  subject?: string;
  actions: PlanAction[];
}

export interface PlanPreview {
  plan: Plan;
  /** Read-only pre-flight checks: ok, warning or failed. */
  checks: StepEvent[];
  ready: boolean;
  /** The text to retype before executing; absent when none. */
  confirmation?: string;
  digest: string;
  preview: string;
  /** The request with the defaults resolved (the export key): what execute sends back. */
  request: PlanRequest;
}

export interface PlanDone {
  ok: boolean;
  error?: string;
  task?: Task;
}

export interface DatabaseInfo {
  name: string;
  state: string;
  recoveryModel: string;
  compatibilityLevel: number;
  collation?: string;
  owner?: string;
  userAccess: string;
  readOnly: boolean;
}

export interface DatabaseFile {
  type: string;
  name: string;
  sizeMB: number;
  /** Absent when the file grows without a limit. */
  maxSizeMB?: number;
  physicalName: string;
}

export interface ObjectCount {
  type: string;
  count: number;
}

/** GET .../databases/{db}/report: the post-restore report. */
export interface DatabaseReport {
  target: string;
  instance: string;
  ok: boolean;
  /** Verdicts: database, access, orphans, windows. */
  findings: StepEvent[];
  database: DatabaseInfo;
  files: DatabaseFile[];
  access: boolean;
  orphanUsers: string[];
  windowsPrincipals: number;
  objects: ObjectCount[];
}

function planURL(engine: EngineInfo, target: string, operation: Operation): string {
  return `${engine.url}/api/v1/targets/${encodeURIComponent(target)}/plans/${operation}`;
}

function jsonHeaders(engine: EngineInfo): HeadersInit {
  return { ...authHeaders(engine), "Content-Type": "application/json" };
}

/** Runs the pre-flight checks and answers the plan. Nothing is changed. */
export async function previewPlan(
  engine: EngineInfo,
  target: string,
  operation: Operation,
  request: PlanRequest,
  signal?: AbortSignal,
): Promise<PlanPreview> {
  const response = await fetch(planURL(engine, target, operation), {
    method: "POST",
    headers: jsonHeaders(engine),
    body: JSON.stringify(request),
    signal,
  });
  if (!response.ok) {
    throw await failure(response);
  }
  return (await response.json()) as PlanPreview;
}

export interface ExecuteHandlers {
  /** Pre-flight warnings, sent before the actions. */
  onNotice: (message: string) => void;
  /** An action, or the wait for the task, starts or ends. */
  onStep: (event: StepEvent) => void;
  /** The followed task changed. */
  onTask: (task: Task) => void;
}

/**
 * Executes the plan and streams its progress. A refusal before the stream
 * (412 plan changed or pre-flight failed, 422 confirmation) is an ApiError;
 * a stream that ends without "done" is an Error: the task may still run.
 */
export async function executePlan(
  engine: EngineInfo,
  target: string,
  operation: Operation,
  body: ExecuteRequest,
  handlers: ExecuteHandlers,
  signal?: AbortSignal,
): Promise<PlanDone> {
  const response = await fetch(`${planURL(engine, target, operation)}/execute`, {
    method: "POST",
    headers: jsonHeaders(engine),
    body: JSON.stringify(body),
    signal,
  });
  if (!response.ok || !response.body) {
    throw await failure(response);
  }
  // An object, not a let: TypeScript does not see assignments in the callback.
  const result: { done?: PlanDone } = {};
  await readEventStream(response.body, (message) => {
    const data = JSON.parse(message.data);
    if (message.event === "notice") {
      handlers.onNotice((data as { message: string }).message);
    } else if (message.event === "step") {
      handlers.onStep(data as StepEvent);
    } else if (message.event === "task") {
      handlers.onTask(data as Task);
    } else if (message.event === "done") {
      result.done = data as PlanDone;
    }
  });
  if (!result.done) {
    throw new Error("the engine closed the stream before the end");
  }
  return result.done;
}

/** The post-restore report of a database (read-only). */
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

/** The steps with event in place of the one with its id, or appended. */
export function upsertStep(steps: StepEvent[], event: StepEvent): StepEvent[] {
  const index = steps.findIndex((s) => s.step === event.step);
  return index === -1 ? [...steps, event] : steps.map((s, i) => (i === index ? event : s));
}

/** The plan can run: no check failed, the values were not edited, the confirmation matches. */
export function canExecute(preview: PlanPreview, typed: string, stale: boolean): boolean {
  return preview.ready && !stale && (!preview.confirmation || typed === preview.confirmation);
}

export function isDestructive(plan: Plan): boolean {
  return plan.actions.some((a) => a.risk === "destructive");
}

/** The parameters the user can edit, trimmed and without the empty ones. */
export function cleanRequest(request: PlanRequest): PlanRequest {
  const out: PlanRequest = {};
  const database = request.database?.trim();
  const key = request.key?.trim();
  if (database) {
    out.database = database;
  }
  if (key) {
    out.key = key;
  }
  if (request.differential) {
    out.differential = true;
  }
  if (request.noRecovery) {
    out.noRecovery = true;
  }
  if (request.taskId) {
    out.taskId = request.taskId;
  }
  return out;
}

/** Same parameters, whatever the order of the fields and the blanks around them. */
export function sameRequest(a: PlanRequest, b: PlanRequest): boolean {
  const x = cleanRequest(a);
  const y = cleanRequest(b);
  return (
    x.database === y.database &&
    x.key === y.key &&
    !!x.differential === !!y.differential &&
    !!x.noRecovery === !!y.noRecovery &&
    x.taskId === y.taskId
  );
}

/**
 * What to do after a refused plan call, by HTTP status: the fallback of
 * ErrorBanner (Feedback.tsx), after the hints of the message (hints.ts).
 */
export function planHint(error: unknown): Hint | undefined {
  if (!(error instanceof ApiError)) {
    return error instanceof Error ? { key: "plan.hint.network" } : undefined;
  }
  const key = (
    {
      400: "plan.hint.400",
      412: "plan.hint.412",
      422: "plan.hint.422",
      502: "plan.hint.502",
    } as Record<number, MessageKey>
  )[error.status];
  return key ? { key } : undefined;
}

/** Message key of a pre-flight check or report finding, when the UI knows it. */
export function planStepKey(step: string): MessageKey | undefined {
  return (
    {
      source: "plancheck.source",
      database: "plancheck.database",
      destination: "plancheck.destination",
      tasks: "plancheck.tasks",
      task: "plancheck.task",
      wait: "planaction.wait",
      access: "report.finding.access",
      orphans: "report.finding.orphans",
      windows: "report.finding.windows",
    } as Record<string, MessageKey>
  )[step];
}

/** s3://bucket/key from the ARN of a task (arn:aws:s3:::bucket/key). */
export function s3URI(arn?: string): string {
  return arn?.startsWith("arn:aws:s3:::")
    ? `s3://${arn.slice("arn:aws:s3:::".length)}`
    : (arn ?? "");
}

/** The task reached SUCCESS, ERROR or CANCELLED. */
export function isFinal(lifecycle: Lifecycle): boolean {
  return lifecycle === "SUCCESS" || lifecycle === "ERROR" || lifecycle === "CANCELLED";
}
