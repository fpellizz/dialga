import { authHeaders, failure, type StepEvent } from "./api";
import type { EngineInfo } from "./engine";
import type { Plan } from "./plans";
import { readEventStream } from "./sse";

// Adapter over POST /api/v1/targets/{name}/uploads (engine transfers.go, the
// transfer agent's contract as of ee126f9): apply false answers the plan,
// apply true streams "plan", "step", "progress" and "done". The wizard only
// uses this file, so a change of that contract stays here.

export interface UploadRequest {
  /** Absolute paths on the machine of the engine. */
  files: string[];
  /** One file only: the whole key. */
  key?: string;
  /** Where the files go (<prefix>/<file name>); the import prefix when absent. */
  prefix?: string;
  /** Replace objects that already exist. */
  overwrite?: boolean;
}

export interface UploadItem {
  step: string;
  path: string;
  bucket: string;
  key: string;
  size: number;
  partSize: number;
  parts: number;
  checksum?: string;
  /** An object already has this key and will be replaced. */
  replaces: boolean;
}

export interface UploadRejection {
  path?: string;
  key?: string;
  reason: string;
}

/** The upload plan: nothing is changed until it runs. */
export interface UploadBatch {
  plan: Plan;
  /** Text to retype before running it (the target name of a protected target). */
  confirmation: string;
  items: UploadItem[];
  rejected: UploadRejection[];
  warnings?: string[];
}

export interface UploadProgress {
  step?: string;
  path: string;
  key: string;
  done: number;
  total: number;
  parts: number;
  partsTotal: number;
  bytesPerSecond: number;
  etaSeconds?: number;
}

export interface UploadResult {
  path: string;
  bucket: string;
  key: string;
  size: number;
  checksumAlgorithm?: string;
  checksum?: string;
  verified: boolean;
  note?: string;
  seconds: number;
  bytesPerSecond: number;
}

export interface UploadDone {
  ok: boolean;
  error?: string;
  results: UploadResult[];
}

function uploadsUrl(engine: EngineInfo, target: string): string {
  return `${engine.url}/api/v1/targets/${encodeURIComponent(target)}/uploads`;
}

/** The upload plan (apply false): keys, sizes, parts, rejections. */
export async function planUploads(
  engine: EngineInfo,
  target: string,
  request: UploadRequest,
  signal?: AbortSignal,
): Promise<UploadBatch> {
  const response = await fetch(uploadsUrl(engine, target), {
    method: "POST",
    headers: { ...authHeaders(engine), "Content-Type": "application/json" },
    body: JSON.stringify({ ...request, apply: false }),
    signal,
  });
  if (!response.ok) {
    throw await failure(response);
  }
  return (await response.json()) as UploadBatch;
}

export interface UploadHandlers {
  onStep: (event: StepEvent) => void;
  onProgress: (progress: UploadProgress) => void;
}

/**
 * Runs the upload (apply true) and resolves with its "done". Aborting the
 * signal cancels it: the engine stops when the client goes away.
 */
export async function runUploads(
  engine: EngineInfo,
  target: string,
  request: UploadRequest & { confirm?: string },
  handlers: UploadHandlers,
  signal?: AbortSignal,
): Promise<UploadDone> {
  const response = await fetch(uploadsUrl(engine, target), {
    method: "POST",
    headers: { ...authHeaders(engine), "Content-Type": "application/json" },
    body: JSON.stringify({ ...request, apply: true }),
    signal,
  });
  if (!response.ok || !response.body) {
    throw await failure(response);
  }
  let done: UploadDone = { ok: false, error: "the upload ended without an outcome", results: [] };
  await readEventStream(response.body, (message) => {
    if (message.event === "step") {
      handlers.onStep(JSON.parse(message.data) as StepEvent);
    } else if (message.event === "progress") {
      handlers.onProgress(JSON.parse(message.data) as UploadProgress);
    } else if (message.event === "done") {
      done = JSON.parse(message.data) as UploadDone;
    }
  });
  return done;
}
