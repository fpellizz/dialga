/**
 * Collations: the read-only views of the engine (server, tempdb, the
 * default of each database, the columns that differ, the collations the
 * instance knows) and the pure helpers of the collation screens. The
 * operations (set-collation, convert-columns) are plans: plans.ts.
 */
import { authHeaders, failure } from "./api";
import type { EngineInfo } from "./engine";
import type { MessageKey } from "./i18n";

/** A collation of the instance (sys.fn_helpcollations). */
export interface CollationInfo {
  name: string;
  description: string;
  /** The code page of char and varchar; 0 = Unicode only. */
  codePage: number;
  version: number;
  caseSensitive: boolean;
  accentSensitive: boolean;
  kanaSensitive: boolean;
  widthSensitive: boolean;
  binary: boolean;
  utf8: boolean;
  legacy: boolean;
}

export interface TableRef {
  schema: string;
  name: string;
}

/** A user database in the collation overview. */
export interface DatabaseCollationSummary {
  name: string;
  state: string;
  collation?: string;
  containment: string;
  mismatchServer: boolean;
  tempTableRisk: boolean;
}

/** GET .../collation. */
export interface CollationOverview {
  target: string;
  instance: string;
  server: string;
  tempdb: string;
  /** Always true on RDS: the server collation is chosen when the instance is created. */
  serverFixed: boolean;
  databases: DatabaseCollationSummary[];
}

export interface CollationGroup {
  collation: string;
  default: boolean;
  columns: number;
  tables: number;
  codePage: number;
}

/** A column whose collation is not the default of its database. */
export interface CollationColumn {
  schema: string;
  table: string;
  column: string;
  type: string;
  baseType: string;
  maxLength: number;
  nullable: boolean;
  collation: string;
  computed?: boolean;
  sparse?: boolean;
  aliasType?: boolean;
  encrypted?: boolean;
  /** Why convert-columns never touches it; absent when it can be converted. */
  unsupported?: string;
}

/** GET .../databases/{db}/collation. */
export interface DatabaseCollationView {
  target: string;
  instance: string;
  database: string;
  collation: string;
  server: string;
  tempdb: string;
  containment: string;
  mismatchServer: boolean;
  tempTableRisk: boolean;
  charColumns: number;
  groups: CollationGroup[];
  columns: CollationColumn[];
  truncated: boolean;
}

export interface RefusedTable extends TableRef {
  reason: string;
}

/** The summary of a collation plan (PlanPreview.collation). */
export interface CollationPlan {
  database: string;
  from?: string;
  to: string;
  units: number;
  tables: number;
  columns: number;
  indexes: number;
  foreignKeys: number;
  dataMB: number;
  refused?: RefusedTable[];
}

/** The most tables one convert-columns plan takes (the engine refuses more). */
export const MAX_TABLES = 200;

const NAME_PATTERN = /^[A-Za-z][A-Za-z0-9_]{0,127}$/;

/** The name looks like a collation name (what the engine accepts before asking SQL Server). */
export function validCollationName(name: string): boolean {
  return NAME_PATTERN.test(name);
}

function targetURL(engine: EngineInfo, target: string): string {
  return `${engine.url}/api/v1/targets/${encodeURIComponent(target)}`;
}

async function get<T>(url: string, engine: EngineInfo, signal?: AbortSignal): Promise<T> {
  const response = await fetch(url, { headers: authHeaders(engine), signal });
  if (!response.ok) {
    throw await failure(response);
  }
  return (await response.json()) as T;
}

/** The server, tempdb and the default collation of every user database. */
export function getCollationOverview(
  engine: EngineInfo,
  target: string,
  signal?: AbortSignal,
): Promise<CollationOverview> {
  return get(`${targetURL(engine, target)}/collation`, engine, signal);
}

/** The collations of one database: the groups of its columns and the columns that differ. */
export function getDatabaseCollation(
  engine: EngineInfo,
  target: string,
  database: string,
  signal?: AbortSignal,
): Promise<DatabaseCollationView> {
  return get(
    `${targetURL(engine, target)}/databases/${encodeURIComponent(database)}/collation`,
    engine,
    signal,
  );
}

interface CacheEntry {
  epoch: number;
  promise: Promise<CollationInfo[]>;
  list?: CollationInfo[];
}

// The list is long (about 5,500) and the same for the whole instance: one read per target.
const cache = new Map<string, CacheEntry>();

function cacheKey(engine: EngineInfo, target: string): string {
  return `${engine.url}\n${target}`;
}

/**
 * The collations the instance knows, by name. Kept per target in memory;
 * a new epoch of the SQL password dialog (a password was saved) reads it
 * again. A failed read is not kept.
 */
export function listCollations(
  engine: EngineInfo,
  target: string,
  signal?: AbortSignal,
  epoch = 0,
): Promise<CollationInfo[]> {
  const key = cacheKey(engine, target);
  const hit = cache.get(key);
  if (hit && hit.epoch === epoch) {
    return hit.promise;
  }
  // The read is shared and survives a caller that leaves: no signal on the request itself.
  const entry: CacheEntry = {
    epoch,
    promise: get<{ collations: CollationInfo[] }>(
      `${targetURL(engine, target)}/collations`,
      engine,
    ).then(
      (r) => {
        entry.list = r.collations;
        return r.collations;
      },
      (e: unknown) => {
        if (cache.get(key) === entry) {
          cache.delete(key);
        }
        throw e;
      },
    ),
  };
  cache.set(key, entry);
  if (!signal) {
    return entry.promise;
  }
  // The caller's signal only stops waiting for it.
  return new Promise<CollationInfo[]>((resolve, reject) => {
    const abort = () => reject(new DOMException("The operation was aborted.", "AbortError"));
    if (signal.aborted) {
      abort();
      return;
    }
    signal.addEventListener("abort", abort, { once: true });
    entry.promise.then(resolve, reject);
  });
}

/** Forgets the lists read so far (tests, a changed target). */
export function clearCollationCache(): void {
  cache.clear();
}

/**
 * Whether a name is one of the collations of the target: undefined while the
 * list has not been read (nothing is refused before it is known).
 */
export function knownCollation(
  engine: EngineInfo,
  target: string,
  name: string,
): boolean | undefined {
  const list = cache.get(cacheKey(engine, target))?.list;
  if (!list) {
    return undefined;
  }
  return list.some((c) => c.name.toLowerCase() === name.trim().toLowerCase());
}

/** CI·AS·UTF8: the traits of a collation in the short form SQL Server people read. */
export function traitsLabel(info: CollationInfo): string {
  if (info.binary) {
    return info.utf8 ? "BIN·UTF8" : "BIN";
  }
  const parts = [info.caseSensitive ? "CS" : "CI", info.accentSensitive ? "AS" : "AI"];
  if (info.kanaSensitive) {
    parts.push("KS");
  }
  if (info.widthSensitive) {
    parts.push("WS");
  }
  if (info.utf8) {
    parts.push("UTF8");
  }
  return parts.join("·");
}

/** The message keys of the traits, for the long form ("Case-insensitive"). */
export function traitKeys(info: CollationInfo): MessageKey[] {
  const keys: MessageKey[] = [];
  if (info.binary) {
    keys.push("collation.trait.binary");
  } else {
    keys.push(info.caseSensitive ? "collation.trait.cs" : "collation.trait.ci");
    keys.push(info.accentSensitive ? "collation.trait.as" : "collation.trait.ai");
  }
  if (info.utf8) {
    keys.push("collation.trait.utf8");
  }
  if (info.legacy) {
    keys.push("collation.trait.legacy");
  }
  return keys;
}

/** The distinct tables of some columns, by schema and name. */
export function tablesOf(columns: readonly { schema: string; table: string }[]): TableRef[] {
  const seen = new Map<string, TableRef>();
  for (const c of columns) {
    const key = `${c.schema}\n${c.table}`;
    if (!seen.has(key)) {
      seen.set(key, { schema: c.schema, name: c.table });
    }
  }
  return [...seen.values()].sort(
    (a, b) => a.schema.localeCompare(b.schema) || a.name.localeCompare(b.name),
  );
}

/** [schema].[name], with ] doubled: how the engine and T-SQL write it. */
export function formatTableRef(ref: TableRef): string {
  const quote = (s: string) => `[${s.replaceAll("]", "]]")}]`;
  return `${quote(ref.schema)}.${quote(ref.name)}`;
}

/** schema.name for people, without quotes. */
export function plainTableRef(ref: TableRef): string {
  return `${ref.schema}.${ref.name}`;
}

/**
 * schema.table, or [schema].[table] for names that hold a dot (] is written ]]).
 * null when it is not two parts.
 */
export function parseTableRef(text: string): TableRef | null {
  const input = text.trim();
  if (input.startsWith("[")) {
    const parts: string[] = [];
    let i = 0;
    while (i < input.length) {
      if (input[i] !== "[") {
        return null;
      }
      let name = "";
      i++;
      for (;;) {
        if (i >= input.length) {
          return null;
        }
        if (input[i] === "]") {
          if (input[i + 1] === "]") {
            name += "]";
            i += 2;
            continue;
          }
          i++;
          break;
        }
        name += input[i];
        i++;
      }
      parts.push(name);
      if (i < input.length) {
        if (input[i] !== ".") {
          return null;
        }
        i++;
      }
    }
    const [schema, name] = parts;
    return parts.length === 2 && schema && name ? { schema, name } : null;
  }
  const dot = input.indexOf(".");
  if (dot <= 0 || dot === input.length - 1 || input.indexOf(".", dot + 1) !== -1) {
    return null;
  }
  return { schema: input.slice(0, dot), name: input.slice(dot + 1) };
}

/** The first number of a message (the count in the engine's "N column(s) keep X" warning), 0 if none. */
export function firstNumber(text: string | undefined): number {
  const m = text?.match(/\d+/);
  return m ? Number(m[0]) : 0;
}
