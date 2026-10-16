import type { StepEvent } from "./api";
import type { MessageKey } from "./i18n";
import type { InspectedBackup, InspectResult } from "./inspect";
import type { PlanPreview, PlanRequest } from "./plans";
import type { UploadBatch, UploadRequest } from "./uploads";

/** The steps of the wizard, in order (plan §3.5 "Importa"). */
export const STEPS = ["files", "verify", "destination", "preview", "progress", "result"] as const;
export type WizardStep = (typeof STEPS)[number];

/** Engine validate.DBName and validate.S3Key. */
const DB_NAME = /^[A-Za-z0-9_][A-Za-z0-9_-]{0,99}$/;
const SYSTEM_DATABASES = new Set(["master", "model", "msdb", "tempdb", "rdsadmin"]);
const S3_KEY = /^[A-Za-z0-9._/+=@-]+$/;

/** An absolute path on the engine's machine (Linux; Windows drive paths for v2). */
export function isAbsolutePath(path: string): boolean {
  return path.startsWith("/") || /^[A-Za-z]:[\\/]/.test(path);
}

/**
 * The paths in a pasted text: one per line, quotes and file:// URIs (what a
 * file manager copies) accepted, blank lines skipped.
 */
export function parsePaths(text: string): string[] {
  const paths: string[] = [];
  for (const raw of text.split(/\r?\n/)) {
    let line = raw.trim().replace(/^(["'])(.*)\1$/, "$2");
    if (line.startsWith("file://")) {
      try {
        line = decodeURIComponent(new URL(line).pathname);
      } catch {
        // Not a URI after all: kept as typed, the check below says why.
      }
    }
    if (line !== "") {
      paths.push(line);
    }
  }
  return paths;
}

/** list with the new paths at the end, without duplicates. */
export function addPaths(list: readonly string[], more: readonly string[]): string[] {
  const out = [...list];
  for (const path of more) {
    if (!out.includes(path)) {
      out.push(path);
    }
  }
  return out;
}

export function baseName(path: string): string {
  return path.split(/[\\/]/).pop() ?? path;
}

/** Why a database name is refused, like the engine (validate.DBName). */
export function databaseNameError(name: string): MessageKey | undefined {
  if (!DB_NAME.test(name)) {
    return "import.error.dbName";
  }
  if (SYSTEM_DATABASES.has(name.toLowerCase())) {
    return "import.error.dbSystem";
  }
  return undefined;
}

/** The database name of a backup, made valid when it is not. */
export function defaultDatabaseName(backup: InspectedBackup): string {
  const fromHeader = backup.header?.databaseName ?? "";
  const fromFile = baseName(backup.files[0] ?? "").replace(/\.bak$/i, "");
  for (const name of [fromHeader, fromFile]) {
    const cleaned = name
      .replace(/[^A-Za-z0-9_-]+/g, "_")
      .replace(/^-+/, "")
      .slice(0, 100);
    if (cleaned !== "" && !databaseNameError(cleaned)) {
      return cleaned;
    }
  }
  return "restored_db";
}

/** Why an S3 folder is refused: the engine's key rules, no "*", no trailing "/". */
export function folderError(folder: string): MessageKey | undefined {
  if (folder === "") {
    return undefined; // the bucket root
  }
  if (
    !S3_KEY.test(folder) ||
    folder.startsWith("/") ||
    folder.endsWith("/") ||
    folder.includes("..")
  ) {
    return "import.error.folder";
  }
  return undefined;
}

export function joinKey(folder: string, name: string): string {
  return folder === "" ? name : `${folder}/${name}`;
}

/**
 * The key RDS restores from: the object of a single file, or a "*" pattern
 * that matches the stripes of a backup (common start + "*" + common end).
 */
export function restoreKey(folder: string, files: readonly string[]): string {
  const names = files.map(baseName);
  if (names.length <= 1) {
    return joinKey(folder, names[0] ?? "");
  }
  let start = names[0];
  let end = names[0];
  for (const name of names.slice(1)) {
    while (!name.startsWith(start)) {
      start = start.slice(0, -1);
    }
    while (!name.endsWith(end)) {
      end = end.slice(1);
    }
  }
  // The two must not overlap inside the shortest name.
  const shortest = Math.min(...names.map((n) => n.length));
  if (start.length + end.length > shortest) {
    end = end.slice(start.length + end.length - shortest);
  }
  return joinKey(folder, `${start}*${end}`);
}

/** A differential backup restores onto a database left RESTORING. */
export function isDifferential(backup: InspectedBackup): boolean {
  return backup.header?.backupType.includes("differential") ?? false;
}

/** Where a backup goes. */
export interface Destination {
  database: string;
  /** Leave the database RESTORING (WITH NORECOVERY), to apply a differential next. */
  noRecovery: boolean;
}

export function defaultDestinations(result: InspectResult): Destination[] {
  return result.backups.map((b) => ({ database: defaultDatabaseName(b), noRecovery: false }));
}

/** The problem of each destination; duplicated names are refused too. */
export function destinationErrors(
  destinations: readonly Destination[],
): (MessageKey | undefined)[] {
  return destinations.map((d, i) => {
    const error = databaseNameError(d.database);
    if (error) {
      return error;
    }
    const lower = d.database.toLowerCase();
    return destinations.some((o, j) => j < i && o.database.toLowerCase() === lower)
      ? "import.error.dbDuplicate"
      : undefined;
  });
}

/** Every backup can be restored: the wizard goes on only then. */
export function allRestorable(result: InspectResult | null): boolean {
  return (
    result !== null &&
    result.backups.length > 0 &&
    result.backups.every((b) => b.outcome === "restorable")
  );
}

/** The upload of all the files of the inspected backups into folder. */
export function uploadRequest(
  result: InspectResult,
  folder: string,
  overwrite: boolean,
): UploadRequest {
  const files = result.backups.flatMap((b) => b.files);
  return overwrite ? { files, prefix: folder, overwrite } : { files, prefix: folder };
}

/** The restore of each backup, with the key its files get. */
export function restoreRequests(
  result: InspectResult,
  destinations: readonly Destination[],
  folder: string,
): PlanRequest[] {
  return result.backups.map((b, i) => {
    const request: PlanRequest = {
      database: destinations[i].database,
      key: restoreKey(folder, b.files),
    };
    if (isDifferential(b)) {
      request.differential = true;
    }
    if (destinations[i].noRecovery) {
      request.noRecovery = true;
    }
    return request;
  });
}

/** The check of a restore preview that fails until the files are uploaded. */
export const SOURCE_CHECK = "source";

/** The failed checks of a restore preview, but the source one (uploaded next). */
export function blockingChecks(preview: PlanPreview): StepEvent[] {
  return preview.checks.filter((c) => c.status === "failed" && c.step !== SOURCE_CHECK);
}

/** The import can start: nothing rejected, no restore blocked but by the upload. */
export function previewReady(
  upload: UploadBatch | null,
  restores: readonly PlanPreview[],
): boolean {
  return (
    upload !== null &&
    upload.rejected.length === 0 &&
    restores.length > 0 &&
    restores.every((p) => blockingChecks(p).length === 0)
  );
}

/** The texts to retype, once each (the target name of a protected target). */
export function confirmations(
  upload: UploadBatch | null,
  restores: readonly PlanPreview[],
): string[] {
  const all = [upload?.confirmation, ...restores.map((p) => p.confirmation)];
  return [...new Set(all.filter((c): c is string => Boolean(c)))];
}

/** How full the free storage gets: percent of free taken, and its tone. */
export function spaceUse(
  requiredBytes: number,
  freeBytes: number | undefined,
): { percent: number; tone: "ok" | "warning" | "failed" } | null {
  if (freeBytes === undefined || freeBytes <= 0) {
    return null;
  }
  const percent = Math.round((requiredBytes / freeBytes) * 100);
  let tone: "ok" | "warning" | "failed" = "ok";
  if (percent > 100) {
    tone = "failed";
  } else if (percent > 80) {
    tone = "warning";
  }
  return { percent, tone };
}

/** 95 → "1:35", 3725 → "1:02:05". */
export function formatDuration(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const pad = (n: number) => String(n).padStart(2, "0");
  return h > 0 ? `${h}:${pad(m)}:${pad(s % 60)}` : `${m}:${pad(s % 60)}`;
}

/** Percent done of a transfer, 0-100. */
export function percentOf(done: number, total: number): number {
  return total > 0 ? Math.min(100, Math.floor((done / total) * 100)) : 0;
}
