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

export type Operation =
  | "restore"
  | "export"
  | "finish"
  | "cancel"
  | "drop"
  | "grant"
  | "revoke"
  | "fix-orphan";

/** The level of access a grant gives (the roles of the database). */
export type AccessLevel = "read" | "readwrite" | "owner";

/** What a revoke takes away. */
export type RemoveWhat = "level" | "backup" | "user";

/**
 * What a restore does when the database already exists: "" refuses (the
 * default), "auto" lets the engine choose, "swap" restores beside it and
 * keeps the old one, "drop" drops it first.
 */
export type ReplaceMode = "" | "auto" | "swap" | "drop";

/** What a replace did or kept, filling in as its steps succeed (core.ReplaceOutcome). */
export interface ReplaceOutcomeInfo {
  strategy: string;
  /** The old database a swap kept: present once its rename succeeded. */
  oldDatabase?: string;
  /** The S3 key of the safety backup: present once it finished. */
  backupKey?: string;
}

/** How a restore over an existing database resolved, and the names it uses (ops.Replacement). */
export interface ReplacePlan {
  strategy: string;
  reason?: string;
  backupFirst?: boolean;
  stamp?: string;
  temp?: string;
  old?: string;
  backupKey?: string;
  result?: string;
}

/** The parameters of an operation (POST .../plans/{operation}). */
export interface PlanRequest {
  database?: string;
  /** S3 key: required by restore, optional for export (the engine picks a default). */
  key?: string;
  differential?: boolean;
  noRecovery?: boolean;
  taskId?: number;
  /** Restore over an existing database. */
  replace?: ReplaceMode;
  /** Drop only: back the current database up to S3 first (the engine default is true). */
  backupFirst?: boolean;
  /** Set by the engine's preview (yyyymmddhhmmss): sent back on execute so the names match. */
  stamp?: string;
  /** Database access: the SQL login (grant, revoke). */
  login?: string;
  /** fix-orphan: the user without a login. */
  user?: string;
  /** grant: the level of access. */
  level?: AccessLevel;
  /** grant: also let the login back the database up. */
  backup?: boolean;
  /** grant: the login does not exist and is created (its password goes to execute only). */
  createLogin?: boolean;
  /** revoke: what to take away. */
  remove?: RemoveWhat;
}

/** The body of .../execute: the previewed request, the confirmation and the digest. */
export interface ExecuteRequest extends PlanRequest {
  confirm?: string;
  digest?: string;
  noWait?: boolean;
  /**
   * grant with createLogin: the password of the new login. Execute only: the
   * engine refuses a preview that carries it, and it is never kept.
   */
  loginPassword?: string;
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
  /** How a restore over an existing database resolved. */
  replace?: ReplacePlan;
  /** The request with the defaults resolved (the export key): what execute sends back. */
  request: PlanRequest;
}

export interface PlanDone {
  ok: boolean;
  /** The job of the operation in Activity. */
  jobId?: string;
  error?: string;
  task?: Task;
  /** A replace: the text naming the old database it kept, or the S3 key of the safety backup. */
  result?: string;
  /** The structured outcome of a replace. */
  replace?: ReplaceOutcomeInfo;
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
  /** The operation runs as this job (first event): it goes on if the client leaves. */
  onJob?: (id: string) => void;
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
    if (message.event === "job") {
      handlers.onJob?.((data as { id: string }).id);
    } else if (message.event === "notice") {
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

/**
 * The steps with event in place of the one it updates, or appended. A step
 * is its id and its label: the engine repeats an id for the same kind of
 * action (add-role twice, with the role in the label), and those are
 * separate steps. A label that is missing on either side matches.
 */
export function upsertStep(steps: StepEvent[], event: StepEvent): StepEvent[] {
  let index = -1;
  for (let i = steps.length - 1; i >= 0; i--) {
    const s = steps[i];
    if (s && s.step === event.step && (!s.label || !event.label || s.label === event.label)) {
      index = i;
      break;
    }
  }
  return index === -1 ? [...steps, event] : steps.map((s, i) => (i === index ? event : s));
}

/** A key for a list of steps that can repeat an id: the id and its occurrence. */
export function stepKeys(steps: readonly { step: string }[]): string[] {
  const seen = new Map<string, number>();
  return steps.map((s) => {
    const n = (seen.get(s.step) ?? 0) + 1;
    seen.set(s.step, n);
    return `${s.step}#${n}`;
  });
}

/**
 * "swap" when the steps include the rename of the restored database, "drop"
 * for a replace that drops first, "" otherwise: a job does not say it.
 */
export function replaceStrategy(ids: readonly string[]): string {
  const plain = ids.map((id) => id.slice(id.lastIndexOf("/") + 1).replace(/#[0-9]+$/, ""));
  if (plain.includes("rename-new")) {
    return "swap";
  }
  return plain.includes("drop") && plain.includes("restore") ? "drop" : "";
}

/** The plan can run: no check failed, the values were not edited, the confirmation matches. */
export function canExecute(preview: PlanPreview, typed: string, stale: boolean): boolean {
  return preview.ready && !stale && (!preview.confirmation || typed === preview.confirmation);
}

/** The request of a database access operation (grant, revoke, fix-orphan). */
export function isAccessRequest(request: PlanRequest): boolean {
  return !!(request.login || request.user || request.level || request.remove);
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
  const login = request.login?.trim();
  const user = request.user?.trim();
  if (login) {
    out.login = login;
  }
  if (user) {
    out.user = user;
  }
  if (request.level) {
    out.level = request.level;
  }
  if (request.backup) {
    out.backup = true;
  }
  if (request.createLogin) {
    out.createLogin = true;
  }
  if (request.remove) {
    out.remove = request.remove;
  }
  // The engine refuses a replace with a differential or a restore left restoring.
  if (request.replace && !request.differential && !request.noRecovery) {
    out.replace = request.replace;
    if (request.replace === "drop") {
      out.backupFirst = request.backupFirst !== false;
    }
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
    x.taskId === y.taskId &&
    x.login === y.login &&
    x.user === y.user &&
    x.level === y.level &&
    !!x.backup === !!y.backup &&
    !!x.createLogin === !!y.createLogin &&
    x.remove === y.remove &&
    (x.replace ?? "") === (y.replace ?? "") &&
    x.backupFirst === y.backupFirst
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
      replace: "plancheck.replace",
      sessions: "plancheck.sessions",
      names: "plancheck.names",
      "wait-backup": "planaction.waitBackup",
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

/** The name is one of the existing databases (SQL Server names are case-insensitive by default). */
export function databaseExists(existing: readonly string[] | undefined, name: string): boolean {
  const wanted = name.trim().toLowerCase();
  return wanted !== "" && !!existing?.some((n) => n.toLowerCase() === wanted);
}

/** The first match of a pattern in the texts, or undefined. */
function findIn(texts: readonly (string | undefined)[], pattern: RegExp): string | undefined {
  for (const text of texts) {
    const found = text?.match(pattern)?.[0];
    if (found) {
      return found;
    }
  }
  return undefined;
}

function escapeRegExp(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * What a replace left behind: the old database a swap kept and the safety
 * backup a drop took (an S3 URI when the bucket is known). The engine's
 * structured outcome wins; the texts the run reported (step labels and
 * details, the result line) are the fallback for an older engine, where the
 * names are <db>_old_<ts> and <prefix>/<db>_before_replace_<ts>.bak.
 */
export function replaceOutcome(
  database: string,
  texts: readonly (string | undefined)[],
  bucket?: string,
  structured?: ReplaceOutcomeInfo,
): { kept?: string; backup?: string } {
  const db = escapeRegExp(database);
  const kept = structured?.oldDatabase ?? findIn(texts, new RegExp(`${db}_old_[A-Za-z0-9]+`));
  const found =
    structured?.backupKey ??
    findIn(
      texts,
      new RegExp(`(?:s3://[^/\\s]+/)?[A-Za-z0-9._/+=@-]*${db}_before_replace_[A-Za-z0-9]+\\.bak`),
    );
  const key = found?.replace(/^\/+/, "");
  const backup = key && !key.startsWith("s3://") && bucket ? `s3://${bucket}/${key}` : key;
  return { kept, backup };
}
