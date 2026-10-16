import { describe, expect, it } from "vitest";
import type { Database, Overview } from "./api";
import {
  compatRelease,
  DEFAULT_SORT,
  databaseBytes,
  databasesHref,
  dbStateKey,
  engineLabel,
  filterDatabases,
  formatBytes,
  instanceBadge,
  instanceStatusKey,
  loadLastTarget,
  nextSort,
  pickTarget,
  saveLastTarget,
  sortDatabases,
  stateBadge,
  storageShare,
  targetFromHash,
} from "./databases";

const GIB = 1024 ** 3;

function db(name: string, more: Partial<Database> = {}): Database {
  return {
    name,
    state: "ONLINE",
    recovery: "FULL",
    compatibility: 140,
    owner: "rdsa",
    userAccess: "MULTI_USER",
    readOnly: false,
    createdAt: "2026-10-15T13:05:40",
    dataMB: 16,
    logMB: 8,
    ...more,
  };
}

const LIST: Database[] = [
  db("sales", { dataMB: 2048, logMB: 512 }),
  db("app_db10", { compatibility: 130, recovery: "SIMPLE" }),
  db("App_db2", { state: "RESTORING", owner: undefined }),
  db("archive", { state: "OFFLINE", dataMB: 8, logMB: 8, owner: "appowner" }),
];

const names = (list: Database[]) => list.map((d) => d.name);

describe("formatBytes", () => {
  it("uses binary units and one decimal at most", () => {
    expect(formatBytes(0, "en")).toBe("0 B");
    expect(formatBytes(512, "en")).toBe("512 B");
    expect(formatBytes(1536, "en")).toBe("1.5 KiB");
    expect(formatBytes(24 * 1024 * 1024, "en")).toBe("24 MiB");
    expect(formatBytes(97.34 * GIB, "en")).toBe("97.3 GiB");
    expect(formatBytes(3 * 1024 * GIB, "en")).toBe("3 TiB");
  });

  it("follows the language for the decimal separator", () => {
    expect(formatBytes(97.34 * GIB, "it")).toBe("97,3 GiB");
  });

  it("never shows a negative size", () => {
    expect(formatBytes(-5, "en")).toBe("0 B");
  });

  it("adds data and log files", () => {
    expect(databaseBytes(db("x", { dataMB: 16, logMB: 8.5 }))).toBe(24.5 * 1024 * 1024);
  });
});

describe("sortDatabases", () => {
  it("sorts names naturally and without case by default", () => {
    expect(names(sortDatabases(LIST, DEFAULT_SORT))).toEqual([
      "App_db2",
      "app_db10",
      "archive",
      "sales",
    ]);
  });

  it("sorts sizes as numbers, both ways", () => {
    expect(names(sortDatabases(LIST, { key: "size", dir: "descending" }))).toEqual([
      "sales",
      "App_db2",
      "app_db10",
      "archive",
    ]);
    expect(names(sortDatabases(LIST, { key: "size", dir: "ascending" }))[0]).toBe("archive");
  });

  it("keeps the name order on ties and puts a missing owner first", () => {
    expect(names(sortDatabases(LIST, { key: "owner", dir: "ascending" }))).toEqual([
      "App_db2",
      "archive",
      "app_db10",
      "sales",
    ]);
    expect(names(sortDatabases(LIST, { key: "compatibility", dir: "ascending" }))).toEqual([
      "app_db10",
      "App_db2",
      "archive",
      "sales",
    ]);
  });

  it("does not change the list it receives", () => {
    const copy = [...LIST];
    sortDatabases(LIST, { key: "state", dir: "descending" });
    expect(LIST).toEqual(copy);
  });

  it("flips the same column and starts another one ascending, size largest first", () => {
    expect(nextSort(DEFAULT_SORT, "name")).toEqual({ key: "name", dir: "descending" });
    expect(nextSort({ key: "name", dir: "descending" }, "name")).toEqual(DEFAULT_SORT);
    expect(nextSort(DEFAULT_SORT, "owner")).toEqual({ key: "owner", dir: "ascending" });
    expect(nextSort(DEFAULT_SORT, "size")).toEqual({ key: "size", dir: "descending" });
  });
});

describe("filterDatabases", () => {
  it("matches name, owner, state and recovery without case", () => {
    expect(names(filterDatabases(LIST, "APP_DB"))).toEqual(["app_db10", "App_db2"]);
    expect(names(filterDatabases(LIST, "appowner"))).toEqual(["archive"]);
    expect(names(filterDatabases(LIST, "restoring"))).toEqual(["App_db2"]);
    expect(names(filterDatabases(LIST, "simple"))).toEqual(["app_db10"]);
  });

  it("returns everything for a blank query and nothing for no match", () => {
    expect(filterDatabases(LIST, "  ")).toBe(LIST);
    expect(filterDatabases(LIST, "nope")).toEqual([]);
  });
});

describe("labels and tones", () => {
  it("gives every database state a badge", () => {
    expect(stateBadge("ONLINE")).toBe("badge-ok");
    expect(stateBadge("RESTORING")).toBe("badge-running");
    expect(stateBadge("SUSPECT")).toBe("badge-failed");
    expect(stateBadge("OFFLINE")).toBe("badge-skipped");
    expect(stateBadge("SOMETHING_NEW")).toBe("badge-skipped");
  });

  it("translates the states and statuses it knows", () => {
    for (const state of ["ONLINE", "RESTORING", "RECOVERING", "RECOVERY_PENDING", "SUSPECT"]) {
      expect(dbStateKey(state)).toBe(`dbstate.${state}`);
    }
    expect(dbStateKey("SOMETHING_NEW")).toBeUndefined();
    expect(instanceStatusKey("available")).toBe("rdsstatus.available");
    expect(instanceStatusKey("upgrading")).toBeUndefined();
  });

  it("names SQL Server releases", () => {
    expect(compatRelease(140)).toBe("2017");
    expect(compatRelease(160)).toBe("2022");
    expect(compatRelease(1)).toBeUndefined();
    expect(engineLabel("sqlserver-se", "14.00.3465.1.v1")).toBe("SQL Server 2017 Standard");
    expect(engineLabel("sqlserver-ee", "16.00.4125.3.v1")).toBe("SQL Server 2022 Enterprise");
    expect(engineLabel("sqlserver-xx", "14.00")).toBe("sqlserver-xx 14.00");
    expect(engineLabel("postgres", "17.2")).toBe("postgres 17.2");
  });

  it("colors the instance status", () => {
    expect(instanceBadge("available")).toBe("badge-ok");
    expect(instanceBadge("backing-up")).toBe("badge-running");
    expect(instanceBadge("stopped")).toBe("badge-skipped");
    expect(instanceBadge("storage-full")).toBe("badge-failed");
    expect(instanceBadge("incompatible-parameters")).toBe("badge-failed");
  });
});

describe("storageShare", () => {
  const base: Overview = {
    target: "dev-sql",
    instance: "dev-sqlserver",
    status: "available",
    engine: "sqlserver-se",
    engineVersion: "14.00.3465.1.v1",
    class: "db.m6i.large",
    allocatedGiB: 200,
    sql: "skipped",
  };

  it("computes the free and used share of the allocated storage", () => {
    expect(storageShare({ ...base, freeBytes: 97.3 * GIB })).toEqual({
      freeBytes: 97.3 * GIB,
      totalBytes: 200 * GIB,
      usedPercent: 51,
      freePercent: 49,
      tone: "ok",
    });
  });

  it("warns under 20% free and alarms under 10%", () => {
    expect(storageShare({ ...base, freeBytes: 30 * GIB })?.tone).toBe("warning");
    expect(storageShare({ ...base, freeBytes: 10 * GIB })?.tone).toBe("critical");
  });

  it("clamps odd values and needs a CloudWatch value", () => {
    expect(storageShare({ ...base, freeBytes: 500 * GIB })?.usedPercent).toBe(0);
    expect(storageShare({ ...base, freeBytes: -1 })?.freePercent).toBe(0);
    expect(storageShare(base)).toBeNull();
    expect(storageShare({ ...base, allocatedGiB: 0, freeBytes: 1 })).toBeNull();
  });
});

describe("last target", () => {
  it("keeps the remembered target only while it exists", () => {
    expect(pickTarget(["dev-sql", "prod-sql"], "prod-sql")).toBe("prod-sql");
    expect(pickTarget(["dev-sql", "prod-sql"], "gone")).toBe("dev-sql");
    expect(pickTarget(["dev-sql"], null)).toBe("dev-sql");
    expect(pickTarget([], "dev-sql")).toBe("");
  });

  it("lets a #databases/<target> link win while the target exists", () => {
    expect(pickTarget(["dev-sql", "prod-sql"], "dev-sql", "prod-sql")).toBe("prod-sql");
    expect(pickTarget(["dev-sql", "prod-sql"], "dev-sql", "gone")).toBe("dev-sql");
    expect(targetFromHash(databasesHref("prod-sql"))).toBe("prod-sql");
    expect(databasesHref("prod-sql")).toBe("#databases/prod-sql");
    expect(targetFromHash("#databases")).toBeNull();
    expect(targetFromHash("#databases/")).toBeNull();
    expect(targetFromHash("#databases/%E0%A4%A")).toBeNull();
    expect(targetFromHash("#settings")).toBeNull();
  });

  it("remembers it for the session without storage", () => {
    // The test environment has no localStorage, like a blocked one.
    expect(loadLastTarget()).toBeNull();
    expect(() => saveLastTarget("prod-sql")).not.toThrow();
    expect(loadLastTarget()).toBe("prod-sql");
  });
});
