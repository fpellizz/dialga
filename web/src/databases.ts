/** Pure helpers of the Databases screen and of the instance overview. */
import type { Database, Overview } from "./api";
import { type Lang, type MessageKey, messages } from "./i18n";

const MIB = 1024 * 1024;
const UNITS = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];

/** A byte count with binary units, as the RDS console and the CLI show it: 97.3 GiB. */
export function formatBytes(bytes: number, lang: Lang): string {
  let value = Math.max(0, bytes);
  let unit = 0;
  while (value >= 1024 && unit < UNITS.length - 1) {
    value /= 1024;
    unit++;
  }
  const text = new Intl.NumberFormat(lang, {
    maximumFractionDigits: unit === 0 ? 0 : 1,
  }).format(value);
  return `${text} ${UNITS[unit]}`;
}

/** Data plus log files, in bytes. */
export function databaseBytes(db: Database): number {
  return (db.dataMB + db.logMB) * MIB;
}

export type SortKey = "name" | "state" | "size" | "compatibility" | "recovery" | "owner";
/** The values of aria-sort, so the header can use them as they are. */
export type SortDir = "ascending" | "descending";

export interface Sort {
  key: SortKey;
  dir: SortDir;
}

export const DEFAULT_SORT: Sort = { key: "name", dir: "ascending" };

const collator = new Intl.Collator("en", { sensitivity: "base", numeric: true });

function compareBy(key: SortKey, a: Database, b: Database): number {
  switch (key) {
    case "size":
      return databaseBytes(a) - databaseBytes(b);
    case "compatibility":
      return a.compatibility - b.compatibility;
    case "name":
      return collator.compare(a.name, b.name);
    default:
      return collator.compare(a[key] ?? "", b[key] ?? "");
  }
}

/** A sorted copy; ties keep the name order. */
export function sortDatabases(list: Database[], sort: Sort): Database[] {
  const sign = sort.dir === "ascending" ? 1 : -1;
  return [...list].sort(
    (a, b) => sign * compareBy(sort.key, a, b) || collator.compare(a.name, b.name),
  );
}

/** Clicking a header: the same column flips, another one starts ascending (size: largest first). */
export function nextSort(current: Sort, key: SortKey): Sort {
  if (current.key === key) {
    return { key, dir: current.dir === "ascending" ? "descending" : "ascending" };
  }
  return { key, dir: key === "size" ? "descending" : "ascending" };
}

/** The databases whose name, owner, state or recovery model contain the query (any case). */
export function filterDatabases(list: Database[], query: string): Database[] {
  const q = query.trim().toLowerCase();
  if (!q) {
    return list;
  }
  return list.filter((db) =>
    [db.name, db.owner ?? "", db.state, db.recovery].some((field) =>
      field.toLowerCase().includes(q),
    ),
  );
}

/** Badge tone of a database state. */
export function stateBadge(state: string): string {
  switch (state) {
    case "ONLINE":
      return "badge-ok";
    case "RESTORING":
    case "RECOVERING":
      return "badge-running";
    case "RECOVERY_PENDING":
    case "SUSPECT":
    case "EMERGENCY":
      return "badge-failed";
    default:
      return "badge-skipped";
  }
}

/** Message key of a database state, when the UI knows it. */
export function dbStateKey(state: string): MessageKey | undefined {
  const key = `dbstate.${state}`;
  return key in messages.en ? (key as MessageKey) : undefined;
}

/** SQL Server release of a compatibility level: 140 is SQL Server 2017. */
const COMPAT_RELEASE: Record<number, string> = {
  90: "2005",
  100: "2008",
  110: "2012",
  120: "2014",
  130: "2016",
  140: "2017",
  150: "2019",
  160: "2022",
  170: "2025",
};

export function compatRelease(level: number): string | undefined {
  return COMPAT_RELEASE[level];
}

/** RDS engine major version → SQL Server release: 14.00.3465.1.v1 is 2017. */
const ENGINE_RELEASE: Record<string, string> = {
  "12": "2014",
  "13": "2016",
  "14": "2017",
  "15": "2019",
  "16": "2022",
  "17": "2025",
};

const EDITIONS: Record<string, string> = {
  ee: "Enterprise",
  se: "Standard",
  ex: "Express",
  web: "Web",
  dev: "Developer",
};

/** "SQL Server 2017 Standard" from sqlserver-se and 14.00.3465.1.v1; the raw values otherwise. */
export function engineLabel(engine: string, version: string): string {
  const edition = EDITIONS[engine.replace(/^sqlserver-/, "")];
  const release = ENGINE_RELEASE[version.split(".")[0] ?? ""];
  if (!engine.startsWith("sqlserver-") || !edition || !release) {
    return `${engine} ${version}`.trim();
  }
  return `SQL Server ${release} ${edition}`;
}

/** Badge tone of an RDS instance status. */
export function instanceBadge(status: string): string {
  if (status === "available") {
    return "badge-ok";
  }
  if (status === "stopped" || status === "stopping") {
    return "badge-skipped";
  }
  if (
    status === "failed" ||
    status === "storage-full" ||
    status.startsWith("incompatible-") ||
    status.startsWith("inaccessible-")
  ) {
    return "badge-failed";
  }
  return "badge-running";
}

/** Message key of an RDS instance status, when the UI knows it. */
export function instanceStatusKey(status: string): MessageKey | undefined {
  const key = `rdsstatus.${status}`;
  return key in messages.en ? (key as MessageKey) : undefined;
}

export interface StorageShare {
  freeBytes: number;
  totalBytes: number;
  /** Rounded percentages of the allocated storage. */
  usedPercent: number;
  freePercent: number;
  tone: "ok" | "warning" | "critical";
}

/** Free and used share of the allocated storage; null without a CloudWatch value. */
export function storageShare(o: Overview): StorageShare | null {
  if (o.freeBytes === undefined || o.allocatedGiB <= 0) {
    return null;
  }
  const totalBytes = o.allocatedGiB * 1024 * MIB;
  const free = Math.min(Math.max(o.freeBytes, 0), totalBytes);
  const freePercent = Math.round((free / totalBytes) * 100);
  let tone: StorageShare["tone"] = "ok";
  if (freePercent < 10) {
    tone = "critical";
  } else if (freePercent < 20) {
    tone = "warning";
  }
  return { freeBytes: free, totalBytes, usedPercent: 100 - freePercent, freePercent, tone };
}

const LAST_TARGET_KEY = "dialga.databases.target";
// Without storage (private mode, blocked) the choice lasts for this session.
let lastTargetInMemory: string | null = null;

/** The target last shown on the Databases screen. */
export function loadLastTarget(): string | null {
  try {
    return localStorage.getItem(LAST_TARGET_KEY) ?? lastTargetInMemory;
  } catch {
    return lastTargetInMemory;
  }
}

export function saveLastTarget(name: string): void {
  lastTargetInMemory = name;
  try {
    localStorage.setItem(LAST_TARGET_KEY, name);
  } catch {
    // Kept in memory only.
  }
}

/** The wanted target when it exists, then the remembered one, otherwise the first one. */
export function pickTarget(
  names: string[],
  remembered: string | null,
  wanted: string | null = null,
): string {
  for (const name of [wanted, remembered]) {
    if (name && names.includes(name)) {
      return name;
    }
  }
  return names[0] ?? "";
}

const DATABASES_HASH = "#databases/";

/** Link to the Databases screen on a target. */
export function databasesHref(target: string): string {
  return DATABASES_HASH + encodeURIComponent(target);
}

/** The target of a #databases/<target> link, if any. */
export function targetFromHash(hash: string): string | null {
  if (!hash.startsWith(DATABASES_HASH)) {
    return null;
  }
  try {
    return decodeURIComponent(hash.slice(DATABASES_HASH.length)) || null;
  } catch {
    return null;
  }
}
