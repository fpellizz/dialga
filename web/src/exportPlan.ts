/**
 * Pure helpers of the Export wizard: the address of the screen, the options
 * of each database, the backup and maintenance windows of the instance and
 * a rough duration, the local paths of the downloads.
 */
import type { Database } from "./api";
import type { MessageKey } from "./i18n";

const EXPORT_HASH = "#export";

/** #export, #export/<target>, #export/<target>?db=sales&db=crm. */
export function exportHref(target?: string, databases: string[] = []): string {
  if (!target) {
    return EXPORT_HASH;
  }
  const query = new URLSearchParams(databases.map((db) => ["db", db])).toString();
  return `${EXPORT_HASH}/${encodeURIComponent(target)}${query ? `?${query}` : ""}`;
}

/** The target and databases of an #export address. */
export function exportFromHash(hash: string): { target: string | null; databases: string[] } {
  if (!hash.startsWith(`${EXPORT_HASH}/`)) {
    return { target: null, databases: [] };
  }
  const [path = "", query = ""] = hash.slice(EXPORT_HASH.length + 1).split("?");
  let target: string | null = null;
  try {
    target = decodeURIComponent(path) || null;
  } catch {
    target = null;
  }
  return { target, databases: new URLSearchParams(query).getAll("db").filter(Boolean) };
}

/** Only an online database can be backed up (the engine refuses the others). */
export function exportable(db: Database): boolean {
  return db.state === "ONLINE";
}

/** The S3 key rules of the engine (validate.S3Key); "*" because an export writes one file. */
const KEY_PATTERN = /^[A-Za-z0-9._/+=@-]+$/;

/** What is wrong with a key the user typed; empty means the engine default. */
export function keyError(key: string): MessageKey | undefined {
  const k = key.trim();
  if (!k) {
    return undefined;
  }
  if (k.includes("*")) {
    return "export.error.keyStar";
  }
  if (!KEY_PATTERN.test(k) || k.startsWith("/") || k.includes("..")) {
    return "export.error.key";
  }
  return undefined;
}

/** An absolute path on the computer of the engine (Linux, macOS or Windows). */
export function isAbsolutePath(path: string): boolean {
  return path.startsWith("/") || /^[A-Za-z]:[\\/]/.test(path) || path.startsWith("\\\\");
}

export interface DownloadOptions {
  /** Download each backup to folder after it is in S3. */
  enabled: boolean;
  folder: string;
  /** Replace a local file with the same name. */
  overwrite: boolean;
}

export interface OptionErrors {
  keys: Record<string, MessageKey>;
  folder?: MessageKey;
}

/** The errors of the options step: keys and the download folder. */
export function validateOptions(
  databases: string[],
  keys: Record<string, string>,
  download: DownloadOptions,
): OptionErrors {
  const errors: OptionErrors = { keys: {} };
  const seen = new Set<string>();
  for (const db of databases) {
    const key = (keys[db] ?? "").trim();
    const error = keyError(key);
    if (error) {
      errors.keys[db] = error;
    } else if (key && seen.has(key)) {
      errors.keys[db] = "export.error.keyTwice";
    }
    if (key) {
      seen.add(key);
    }
  }
  if (download.enabled) {
    const folder = download.folder.trim();
    if (!folder) {
      errors.folder = "export.error.folder";
    } else if (!isAbsolutePath(folder)) {
      errors.folder = "export.error.folderAbsolute";
    }
  }
  return errors;
}

export function hasErrors(errors: OptionErrors): boolean {
  return errors.folder !== undefined || Object.keys(errors.keys).length > 0;
}

/** The local file a download writes: the folder plus the name of the key. */
export function localPath(folder: string, key: string): string {
  const name = key.slice(key.lastIndexOf("/") + 1);
  const sep = folder.includes("\\") && !folder.includes("/") ? "\\" : "/";
  return folder.endsWith(sep) ? `${folder}${name}` : `${folder}${sep}${name}`;
}

/**
 * A rough duration of the backups, in minutes: about 10 MB/s per database
 * plus 2 minutes for RDS to start the task. RDS native backups vary a lot
 * (compression, instance class, storage): this only says whether a window
 * might be crossed.
 */
export const BACKUP_MB_PER_MINUTE = 600;
export const TASK_START_MINUTES = 2;

export function estimateMinutes(databases: Pick<Database, "dataMB" | "logMB">[]): number {
  let minutes = 0;
  for (const db of databases) {
    minutes += TASK_START_MINUTES + db.dataMB / BACKUP_MB_PER_MINUTE;
  }
  return Math.ceil(minutes);
}

const DAY = 24 * 60;
const WEEK = 7 * DAY;
const WEEKDAYS = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"];

/** A window of the instance: minutes from 00:00 UTC (daily) or Monday 00:00 UTC (weekly). */
export interface MaintenanceWindow {
  start: number;
  /** After start: a window across midnight (or Sunday night) ends in the next period. */
  end: number;
  period: number;
}

function minutesOf(time: string): number | null {
  const m = /^([01]\d|2[0-3]):([0-5]\d)$/.exec(time);
  return m ? Number(m[1]) * 60 + Number(m[2]) : null;
}

/** "04:29-04:59" (daily) or "sun:05:00-sun:05:30" (weekly), as RDS writes them. */
export function parseWindow(text: string | undefined): MaintenanceWindow | null {
  if (!text) {
    return null;
  }
  const weekly = /^([a-z]{3}):(\d\d:\d\d)-([a-z]{3}):(\d\d:\d\d)$/.exec(text.trim().toLowerCase());
  if (weekly) {
    const [, d1 = "", t1 = "", d2 = "", t2 = ""] = weekly;
    const day1 = WEEKDAYS.indexOf(d1);
    const day2 = WEEKDAYS.indexOf(d2);
    const m1 = minutesOf(t1);
    const m2 = minutesOf(t2);
    if (day1 < 0 || day2 < 0 || m1 === null || m2 === null) {
      return null;
    }
    const start = day1 * DAY + m1;
    let end = day2 * DAY + m2;
    if (end <= start) {
      end += WEEK;
    }
    return { start, end, period: WEEK };
  }
  const daily = /^(\d\d:\d\d)-(\d\d:\d\d)$/.exec(text.trim());
  if (daily) {
    const start = minutesOf(daily[1] ?? "");
    let end = minutesOf(daily[2] ?? "");
    if (start === null || end === null) {
      return null;
    }
    if (end <= start) {
      end += DAY;
    }
    return { start, end, period: DAY };
  }
  return null;
}

/** The start of the period (UTC midnight, or Monday 00:00 UTC) that contains date. */
function periodStart(date: Date, period: number): number {
  const midnight = Date.UTC(date.getUTCFullYear(), date.getUTCMonth(), date.getUTCDate());
  if (period === DAY) {
    return midnight;
  }
  const mondayBased = (date.getUTCDay() + 6) % 7;
  return midnight - mondayBased * DAY * 60_000;
}

export interface Occurrence {
  start: Date;
  end: Date;
}

/** The first occurrence of the window that overlaps [from, to], if any. */
export function overlap(window: MaintenanceWindow, from: Date, to: Date): Occurrence | null {
  const base = periodStart(from, window.period);
  const span = window.period * 60_000;
  for (let k = -1; base + k * span <= to.getTime(); k++) {
    const start = base + k * span + window.start * 60_000;
    const end = base + k * span + window.end * 60_000;
    if (start < to.getTime() && end > from.getTime()) {
      return { start: new Date(start), end: new Date(end) };
    }
  }
  return null;
}

/** The next occurrence of the window that ends after date. */
export function nextOccurrence(window: MaintenanceWindow, after: Date): Occurrence {
  const base = periodStart(after, window.period);
  const span = window.period * 60_000;
  for (let k = -1; ; k++) {
    const start = base + k * span + window.start * 60_000;
    const end = base + k * span + window.end * 60_000;
    if (end > after.getTime()) {
      return { start: new Date(start), end: new Date(end) };
    }
  }
}
