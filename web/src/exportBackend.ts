/**
 * The two ways the wizard exports, behind one preview and one run:
 *   - "job": one engine job (POST /api/v1/jobs): the engine runs backup,
 *     wait and download, the job shows in Activity and survives the page;
 *   - "plans": the export plan of each database, then its download, chained
 *     by the page (an engine without the job route).
 * The first preview finds out which one the engine offers.
 */
import type { StepEvent } from "./api";
import type { EngineInfo } from "./engine";
import {
  followJob,
  type JobItemRequest,
  type JobPlan,
  NoJobsError,
  previewJob,
  startJob,
} from "./exportJob";
import { localPath } from "./exportPlan";
import { type ExportItemRun, previewURI, runExports } from "./exportRun";
import { cleanRequest, type PlanPreview, previewPlan } from "./plans";

export interface ExportPreviewItem {
  database: string;
  /** The S3 key, defaults resolved. */
  key: string;
  s3Uri: string;
  /** Where the download writes, when there is one. */
  localFile?: string;
  ready: boolean;
  checks: StepEvent[];
  statements: string[];
  /** The preview of this database failed (plans). */
  error?: unknown;
}

export interface ExportPreview {
  via: "job" | "plans";
  target: string;
  protected: boolean;
  /** The text to retype before running (the target name when protected). */
  confirmation?: string;
  ready: boolean;
  items: ExportPreviewItem[];
  download?: { folder: string; overwrite: boolean };
  /** What run sends back. */
  job?: { plan: JobPlan; request: JobItemRequest[] };
  plans?: PlanPreview[];
}

export interface ExportChoice {
  database: string;
  /** "" for the engine default. */
  key: string;
}

/** Whether an engine (by URL) has the job route: learned at the first preview. */
const jobRoute = new Map<string, boolean>();

/** For the tests: forget what the engines offered. */
export function resetBackends(): void {
  jobRoute.clear();
}

/** The dry run of the export of the chosen databases. Nothing is changed. */
export async function previewExport(
  engine: EngineInfo,
  target: string,
  choices: ExportChoice[],
  download?: { folder: string; overwrite: boolean },
): Promise<ExportPreview> {
  if (jobRoute.get(engine.url) !== false) {
    const request: JobItemRequest[] = choices.map((c) => ({
      database: c.database,
      ...(c.key.trim() ? { key: c.key.trim() } : {}),
      ...(download ? { destination: download.folder, overwrite: download.overwrite } : {}),
    }));
    try {
      const plan = await previewJob(engine, target, request);
      jobRoute.set(engine.url, true);
      return fromJob(target, plan, request, download);
    } catch (e) {
      if (!(e instanceof NoJobsError)) {
        throw e;
      }
      jobRoute.set(engine.url, false);
    }
  }
  return previewPlans(engine, target, choices, download);
}

function fromJob(
  target: string,
  plan: JobPlan,
  request: JobItemRequest[],
  download?: { folder: string; overwrite: boolean },
): ExportPreview {
  return {
    via: "job",
    target,
    protected: plan.protected,
    confirmation: plan.confirmation || undefined,
    ready: plan.ready,
    download,
    items: plan.items.map((it) => {
      const uri = it.checks.find((c) => c.step === "destination")?.detail ?? "";
      return {
        database: it.database,
        key: it.key,
        s3Uri: /s3:\/\/\S+/.exec(uri)?.[0] ?? it.key,
        localFile: it.file || undefined,
        ready: it.ready,
        checks: it.checks,
        statements: it.actions.map((a) => a.statement).filter(Boolean),
      };
    }),
    // Start what was previewed: the resolved key (a default carries a timestamp) and file.
    job: {
      plan,
      request: request.map((r) => {
        const planned = plan.items.find((it) => it.database === r.database);
        return {
          ...r,
          ...(planned?.key ? { key: planned.key } : {}),
          ...(planned?.file ? { destination: planned.file } : {}),
        };
      }),
    },
  };
}

async function previewPlans(
  engine: EngineInfo,
  target: string,
  choices: ExportChoice[],
  download?: { folder: string; overwrite: boolean },
): Promise<ExportPreview> {
  const items: ExportPreviewItem[] = [];
  const plans: PlanPreview[] = [];
  let protectedTarget = false;
  let confirmation: string | undefined;
  // One after the other: each preview opens its own SQL session.
  for (const choice of choices) {
    try {
      const preview = await previewPlan(
        engine,
        target,
        "export",
        cleanRequest({ database: choice.database, key: choice.key }),
      );
      plans.push(preview);
      protectedTarget ||= preview.plan.protected;
      confirmation ??= preview.confirmation;
      const key = preview.request.key ?? "";
      items.push({
        database: choice.database,
        key,
        s3Uri: previewURI(preview),
        localFile: download ? localPath(download.folder, key) : undefined,
        ready: preview.ready,
        checks: preview.checks,
        statements: preview.plan.actions.map((a) => a.statement),
      });
    } catch (error) {
      items.push({
        database: choice.database,
        key: choice.key,
        s3Uri: "",
        ready: false,
        checks: [],
        statements: [],
        error,
      });
    }
  }
  return {
    via: "plans",
    target,
    protected: protectedTarget,
    confirmation,
    ready: items.length > 0 && items.every((i) => i.ready),
    items,
    download,
    plans,
  };
}

/**
 * Runs a ready preview. With a job, onJob receives its id first (Activity,
 * cancel); aborting stops following, the job goes on in the engine. With
 * plan calls, aborting stops after the running backup (its RDS task goes on).
 */
export async function runExport(
  engine: EngineInfo,
  preview: ExportPreview,
  confirm: string | undefined,
  onChange: (items: ExportItemRun[]) => void,
  signal: AbortSignal,
  onJob?: (id: string) => void,
): Promise<ExportItemRun[]> {
  if (preview.via === "job" && preview.job) {
    const job = await startJob(
      engine,
      preview.target,
      preview.job.request,
      preview.job.plan,
      confirm,
    );
    onJob?.(job.id);
    return followJob(engine, job, preview.job.plan, onChange, signal);
  }
  return runExports(
    engine,
    {
      target: preview.target,
      previews: preview.plans ?? [],
      confirm,
      download: preview.download,
    },
    onChange,
    signal,
  );
}
