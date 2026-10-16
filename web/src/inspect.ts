import { authHeaders, failure, type StepEvent, type StepStatus } from "./api";
import type { EngineInfo } from "./engine";
import { readEventStream } from "./sse";

/** Verdict of a backup, or of the whole inspection (engine inspect.Outcome). */
export type Outcome = "restorable" | "not-restorable" | "read" | "error";

/** RESTORE HEADERONLY, first backup set. */
export interface BackupHeader {
  databaseName: string;
  serverName: string;
  /** full, differential, log, file, file differential, partial or partial differential. */
  backupType: string;
  version: string;
  major: number;
  product: string;
  compatibilityLevel: number;
  collation: string;
  recoveryModel: string;
  compressed: boolean;
  checksums: boolean;
  copyOnly: boolean;
  encryption?: string;
  backupSize: number;
  compressedSize: number;
  startedAt: string;
  finishedAt: string;
}

export interface Finding {
  /** version, type, encryption, tde, filestream, sets or space. */
  check: string;
  status: StepStatus;
  message: string;
}

/** One backup: a .bak file, or the stripes of one backup. */
export interface InspectedBackup {
  files: string[];
  outcome: Outcome;
  error?: string;
  header?: BackupHeader;
  /** What the restored database takes: the sum of its files. */
  requiredBytes: number;
  backupSets: number;
  findings?: Finding[];
  /** What to do when it is not restorable or unreadable. */
  remedy?: string;
}

export interface InspectTarget {
  name: string;
  instance: string;
  engine: string;
  engineVersion: string;
  major: number;
  freeBytes?: number;
}

export interface InspectResult {
  outcome: Outcome;
  runtime: string;
  image: string;
  server: string;
  target?: InspectTarget;
  backups: InspectedBackup[];
  requiredBytes: number;
  space?: Finding;
}

export interface InspectHandlers {
  onStep: (event: StepEvent) => void;
  /** Why the inspection stopped (the stream then ends without a result). */
  onNotice: (message: string) => void;
}

/**
 * Reads local .bak files in a throwaway SQL Server (POST /api/v1/inspect)
 * and judges them against the target. Resolves with the result, or null
 * when the inspection stopped (onNotice says why).
 */
export async function inspectBackups(
  engine: EngineInfo,
  paths: string[],
  target: string,
  handlers: InspectHandlers,
  signal?: AbortSignal,
): Promise<InspectResult | null> {
  const response = await fetch(`${engine.url}/api/v1/inspect`, {
    method: "POST",
    headers: { ...authHeaders(engine), "Content-Type": "application/json" },
    body: JSON.stringify({ paths, target }),
    signal,
  });
  if (!response.ok || !response.body) {
    throw await failure(response);
  }
  let result: InspectResult | null = null;
  await readEventStream(response.body, (message) => {
    if (message.event === "step") {
      handlers.onStep(JSON.parse(message.data) as StepEvent);
    } else if (message.event === "notice") {
      handlers.onNotice((JSON.parse(message.data) as { message: string }).message);
    } else if (message.event === "result") {
      result = JSON.parse(message.data) as InspectResult;
    }
  });
  return result;
}
