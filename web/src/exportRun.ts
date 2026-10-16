/**
 * The execution of an export, behind one small interface: today it chains
 * the engine calls that exist (the export plan with the wait for its task,
 * then the download); a job of kind "export" (POST /api/v1/jobs, backup +
 * wait + download as one job, followed over its event stream and shown in
 * Activity) can replace runExports with the same ExportItemRun model, which
 * mirrors the engine's jobs.Item.
 */
import type { StepEvent, Task } from "./api";
import type { EngineInfo } from "./engine";
import { localPath } from "./exportPlan";
import { executePlan, HttpError, type PlanPreview, s3URI, upsertStep } from "./plans";
import { createSseParser } from "./sse";

export type ItemState = "queued" | "running" | "succeeded" | "failed" | "cancelled";
export type ItemStep = "backup" | "download";

/** What a download moved and checked (transfer.Result). */
export interface TransferResult {
  path: string;
  bucket: string;
  key: string;
  size: number;
  checksumAlgorithm?: string;
  checksum?: string;
  /** The checksum computed locally matches the one S3 keeps. */
  verified: boolean;
  /** Why the checksum was not verified. */
  note?: string;
  seconds: number;
  bytesPerSecond: number;
}

/** The bytes of a running transfer (transfer.Progress). */
export interface TransferProgress {
  done: number;
  total: number;
  bytesPerSecond: number;
  etaSeconds?: number;
}

/** One database of an export, as the progress and result steps show it. */
export interface ExportItemRun {
  database: string;
  /** The S3 key the backup writes (resolved by the preview). */
  key: string;
  state: ItemState;
  step?: ItemStep;
  /** The steps the engine reported: the backup, the wait, the download. */
  steps: StepEvent[];
  task?: Task;
  transfer?: TransferProgress;
  /** s3://bucket/key once the backup succeeded. */
  s3Uri?: string;
  /** What the download moved and checked (plan calls). */
  download?: TransferResult;
  /** The local file and what the engine said about it (job). */
  localFile?: string;
  note?: string;
  /** The item stopped while its native task was still running. */
  taskLeftRunning?: boolean;
  error?: unknown;
}

/** One preview per database, or why it failed (the preview step of the wizard). */
export interface PreviewState {
  database: string;
  preview?: PlanPreview;
  error?: unknown;
}

export interface ExportSpec {
  target: string;
  /** One previewed plan per database: execute sends back its request and digest. */
  previews: PlanPreview[];
  /** The text a protected target asks to retype (its name). */
  confirm?: string;
  download?: { folder: string; overwrite: boolean };
}

/** The first state of the items: all queued. */
export function queuedItems(spec: ExportSpec): ExportItemRun[] {
  return spec.previews.map((p) => ({
    database: p.request.database ?? "",
    key: p.request.key ?? "",
    state: "queued",
    steps: [],
  }));
}

/** An export or a download stopped because the user stopped following it. */
function aborted(signal: AbortSignal): boolean {
  return signal.aborted;
}

/** The answer of the engine at the end of a download (api.TransferDone). */
interface TransferDone {
  ok: boolean;
  error?: string;
  results: TransferResult[];
}

interface DownloadBatch {
  confirmation: string;
  rejected: { path?: string; key?: string; reason: string }[];
}

/**
 * Downloads one object to a local folder (POST .../downloads, apply) and
 * streams its progress. A refusal before the stream is an HttpError.
 */
export async function downloadBackup(
  engine: EngineInfo,
  target: string,
  body: { key: string; dest: string; overwrite: boolean; confirm?: string },
  handlers: { onStep: (e: StepEvent) => void; onProgress: (p: TransferProgress) => void },
  signal: AbortSignal,
): Promise<TransferDone> {
  const response = await fetch(
    `${engine.url}/api/v1/targets/${encodeURIComponent(target)}/downloads`,
    {
      method: "POST",
      headers: { Authorization: `Bearer ${engine.token}`, "Content-Type": "application/json" },
      body: JSON.stringify({ ...body, apply: true }),
      signal,
    },
  );
  if (!response.ok || !response.body) {
    throw new HttpError(
      response.status,
      (await response.text()).trim() || `HTTP ${response.status}`,
    );
  }
  const out: { done?: TransferDone; rejected?: string } = {};
  const parse = createSseParser((message) => {
    const data = JSON.parse(message.data);
    if (message.event === "plan") {
      const batch = data as DownloadBatch;
      if (batch.rejected?.length) {
        out.rejected = batch.rejected.map((r) => r.reason).join("; ");
      }
    } else if (message.event === "step") {
      handlers.onStep(data as StepEvent);
    } else if (message.event === "progress") {
      handlers.onProgress(data as TransferProgress);
    } else if (message.event === "done") {
      out.done = data as TransferDone;
    }
  });
  const reader = response.body.pipeThrough(new TextDecoderStream()).getReader();
  for (;;) {
    const { value, done } = await reader.read();
    if (done) {
      break;
    }
    parse(value);
  }
  if (!out.done) {
    throw new Error("the engine closed the stream before the end");
  }
  if (!out.done.ok && !out.done.error && out.rejected) {
    out.done.error = out.rejected;
  }
  return out.done;
}

/**
 * Runs the exports one after another: the backup (the plan, then the wait
 * for its native task), then the download when asked. A failure stops that
 * database only; aborting stops following (the running RDS task goes on)
 * and leaves the databases not started as cancelled.
 */
export async function runExports(
  engine: EngineInfo,
  spec: ExportSpec,
  onChange: (items: ExportItemRun[]) => void,
  signal: AbortSignal,
): Promise<ExportItemRun[]> {
  let items = queuedItems(spec);
  const update = (index: number, change: (item: ExportItemRun) => ExportItemRun) => {
    items = items.map((item, i) => (i === index ? change(item) : item));
    onChange(items);
  };
  onChange(items);

  for (const [index, preview] of spec.previews.entries()) {
    if (aborted(signal)) {
      update(index, (it) => ({ ...it, state: "cancelled" }));
      continue;
    }
    update(index, (it) => ({ ...it, state: "running", step: "backup" }));
    try {
      const done = await executePlan(
        engine,
        spec.target,
        "export",
        {
          ...preview.request,
          digest: preview.digest,
          ...(preview.confirmation ? { confirm: spec.confirm ?? "" } : {}),
        },
        {
          onNotice: () => {},
          onStep: (e) => update(index, (it) => ({ ...it, steps: upsertStep(it.steps, e) })),
          onTask: (task) => update(index, (it) => ({ ...it, task })),
        },
        signal,
      );
      if (!done.ok) {
        update(index, (it) => ({
          ...it,
          state: "failed",
          task: done.task ?? it.task,
          error: new Error(done.error || failedTask(done.task)),
        }));
        continue;
      }
      const s3Uri = s3URI(done.task?.s3Object) || previewURI(preview);
      update(index, (it) => ({ ...it, task: done.task ?? it.task, s3Uri }));
      if (spec.download) {
        update(index, (it) => ({ ...it, step: "download" }));
        const result = await downloadBackup(
          engine,
          spec.target,
          {
            key: preview.request.key ?? "",
            // The file, not the folder: a folder that does not exist would become the file name.
            dest: localPath(spec.download.folder, preview.request.key ?? ""),
            overwrite: spec.download.overwrite,
            ...(preview.plan.protected ? { confirm: spec.confirm ?? "" } : {}),
          },
          {
            onStep: (e) =>
              update(index, (it) => ({ ...it, steps: upsertStep(it.steps, downloadStep(e)) })),
            onProgress: (transfer) => update(index, (it) => ({ ...it, transfer })),
          },
          signal,
        );
        const download = result.results[0];
        if (!result.ok || !download) {
          update(index, (it) => ({
            ...it,
            state: "failed",
            error: new Error(result.error || "the download did not complete"),
          }));
          continue;
        }
        update(index, (it) => ({ ...it, download }));
      }
      update(index, (it) => ({ ...it, state: "succeeded" }));
    } catch (e) {
      update(index, (it) => ({
        ...it,
        state: aborted(signal) ? "cancelled" : "failed",
        error: aborted(signal) ? undefined : e,
      }));
    }
  }
  return items;
}

/** s3://bucket/key of a previewed export, from its destination check or its action. */
export function previewURI(preview: PlanPreview): string {
  const texts = [
    ...preview.checks.filter((c) => c.step === "destination").map((c) => c.detail ?? ""),
    ...preview.plan.actions.map((a) => a.title),
  ];
  for (const text of texts) {
    const m = /s3:\/\/\S+/.exec(text);
    if (m) {
      return m[0];
    }
  }
  return preview.request.key ?? "";
}

/** The download steps carry their own ids, apart from the backup ones. */
function downloadStep(e: StepEvent): StepEvent {
  return { ...e, step: `download:${e.step}` };
}

function failedTask(task?: Task): string {
  return task ? `task ${task.id} ended in ${task.lifecycle}` : "the backup did not complete";
}

/** Counts of an export, for the result and the announcements. */
export function tally(items: ExportItemRun[]): {
  succeeded: number;
  failed: number;
  cancelled: number;
  running: number;
} {
  const count = (state: ItemState) => items.filter((i) => i.state === state).length;
  return {
    succeeded: count("succeeded"),
    failed: count("failed"),
    cancelled: count("cancelled"),
    running: count("running") + count("queued"),
  };
}
