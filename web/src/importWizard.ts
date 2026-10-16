import type { JobItemRequest, JobPlan } from "./exportJob";
import type { MessageKey } from "./i18n";
import type { InspectedBackup, InspectResult } from "./inspect";
import type { ReplaceMode } from "./plans";

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
  /** What to do when the database already exists: "" refuses. */
  replace: ReplaceMode;
  /** With "drop": back the current database up to S3 first. */
  backupFirst: boolean;
}

export function defaultDestinations(result: InspectResult): Destination[] {
  return result.backups.map((b) => ({
    database: defaultDatabaseName(b),
    noRecovery: false,
    replace: "",
    backupFirst: true,
  }));
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

/**
 * The items of the import job: one per inspected backup, its file or the
 * stripes of it, with the database it restores into. The files go to
 * <folder>/<file name> (the prefix of the job).
 */
export function importItems(
  result: InspectResult,
  destinations: readonly Destination[],
  overwrite: boolean,
): JobItemRequest[] {
  return result.backups.map((b, i) => {
    const item: JobItemRequest = { database: destinations[i].database };
    if (b.files.length === 1) {
      item.file = b.files[0];
    } else {
      item.files = [...b.files];
    }
    if (isDifferential(b)) {
      item.differential = true;
    }
    if (destinations[i].noRecovery) {
      item.noRecovery = true;
    }
    if (overwrite) {
      item.overwrite = true;
    }
    // The engine refuses a replace with a differential or a restore left restoring.
    if (destinations[i].replace && !isDifferential(b) && !destinations[i].noRecovery) {
      item.replace = destinations[i].replace as "auto" | "swap" | "drop";
      if (item.replace === "drop") {
        item.backupFirst = destinations[i].backupFirst;
      }
    }
    return item;
  });
}

/**
 * The items to start what the dry run planned: the same ones, with the key
 * of the plan. A "*" pattern (the stripes) is not an object key: the engine
 * builds it again from the files.
 */
export function startItems(items: readonly JobItemRequest[], plan: JobPlan): JobItemRequest[] {
  return items.map((item) => {
    const key = plan.items.find((p) => p.database === item.database)?.key;
    return key && !key.includes("*") ? { ...item, key } : item;
  });
}

/** The import can start: the plan is ready and no file is refused. */
export function importReady(plan: JobPlan | null): boolean {
  return (
    plan?.ready === true &&
    plan.items.length > 0 &&
    plan.items.every((it) => (it.rejected ?? []).length === 0)
  );
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

/** [name] with "]" doubled: a T-SQL identifier. */
function quoteName(name: string): string {
  return `[${name.replaceAll("]", "]]")}]`;
}

/**
 * The statements that give each orphaned user a login, to adapt and run by
 * hand: the password is a placeholder, never a real one.
 */
export function orphanFixSql(database: string, users: readonly string[]): string {
  const logins = users.map((u) => `CREATE LOGIN ${quoteName(u)} WITH PASSWORD = '...';`);
  const maps = users.map((u) => `ALTER USER ${quoteName(u)} WITH LOGIN = ${quoteName(u)};`);
  return [...logins, `USE ${quoteName(database)};`, ...maps].join("\n");
}
