import { describe, expect, it } from "vitest";
import { messages } from "./i18n";
import {
  addPaths,
  allRestorable,
  blockingChecks,
  confirmations,
  databaseNameError,
  defaultDatabaseName,
  destinationErrors,
  folderError,
  formatDuration,
  isAbsolutePath,
  parsePaths,
  percentOf,
  previewReady,
  restoreKey,
  restoreRequests,
  spaceUse,
  uploadRequest,
} from "./importWizard";
import type { InspectedBackup, InspectResult } from "./inspect";
import type { PlanPreview } from "./plans";
import type { UploadBatch } from "./uploads";

function backup(files: string[], change: Partial<InspectedBackup> = {}): InspectedBackup {
  return {
    files,
    outcome: "restorable",
    requiredBytes: 1024,
    backupSets: 1,
    header: {
      databaseName: "app_db",
      serverName: "SRC",
      backupType: "full",
      version: "13.0.5026",
      major: 13,
      product: "SQL Server 2016",
      compatibilityLevel: 130,
      collation: "SQL_Latin1_General_CP1_CI_AS",
      recoveryModel: "FULL",
      compressed: true,
      checksums: true,
      copyOnly: false,
      backupSize: 2048,
      compressedSize: 512,
      startedAt: "2026-10-01T10:00:00",
      finishedAt: "2026-10-01T10:05:00",
    },
    ...change,
  };
}

function result(backups: InspectedBackup[]): InspectResult {
  return {
    outcome: "restorable",
    runtime: "podman 5.8.4",
    image: "mcr.microsoft.com/mssql/server:2022-latest",
    server: "16.0.4135.4",
    backups,
    requiredBytes: 1024,
  };
}

function preview(checks: PlanPreview["checks"], confirmation?: string): PlanPreview {
  return {
    plan: { operation: "restore", target: "dev-sql", instance: "x", protected: false, actions: [] },
    checks,
    ready: checks.every((c) => c.status !== "failed"),
    confirmation,
    digest: "d1",
    preview: "",
    request: {},
  };
}

const BATCH: UploadBatch = {
  plan: { operation: "upload", target: "dev-sql", instance: "x", protected: false, actions: [] },
  confirmation: "",
  items: [],
  rejected: [],
};

describe("import wizard", () => {
  it("reads pasted paths: quotes, file URIs, blank lines", () => {
    expect(
      parsePaths(
        ' /data/a.bak \n\n"/data/with space.bak"\nfile:///data/c%20d.bak\r\n relative.bak',
      ),
    ).toEqual(["/data/a.bak", "/data/with space.bak", "/data/c d.bak", "relative.bak"]);
    expect(isAbsolutePath("/data/a.bak")).toBe(true);
    expect(isAbsolutePath("C:\\data\\a.bak")).toBe(true);
    expect(isAbsolutePath("relative.bak")).toBe(false);
    expect(addPaths(["/a"], ["/b", "/a", "/c"])).toEqual(["/a", "/b", "/c"]);
  });

  it("checks database names like the engine", () => {
    expect(databaseNameError("app_db-2")).toBeUndefined();
    expect(databaseNameError("-x")).toBe("import.error.dbName");
    expect(databaseNameError("a b")).toBe("import.error.dbName");
    expect(databaseNameError("a".repeat(101))).toBe("import.error.dbName");
    expect(databaseNameError("MSDB")).toBe("import.error.dbSystem");
    expect(
      destinationErrors([
        { database: "app", noRecovery: false },
        { database: "APP", noRecovery: false },
        { database: "", noRecovery: false },
      ]),
    ).toEqual([undefined, "import.error.dbDuplicate", "import.error.dbName"]);
  });

  it("names the database after the backup, made valid", () => {
    expect(defaultDatabaseName(backup(["/d/x.bak"]))).toBe("app_db");
    const odd = backup(["/d/Sales Data.bak"]);
    odd.header = {
      ...(odd.header as NonNullable<InspectedBackup["header"]>),
      databaseName: "Sales Data",
    };
    expect(defaultDatabaseName(odd)).toBe("Sales_Data");
    expect(defaultDatabaseName(backup(["/d/crm.bak"], { header: undefined }))).toBe("crm");
    expect(defaultDatabaseName(backup(["/d/master.bak"], { header: undefined }))).toBe(
      "restored_db",
    );
  });

  it("checks the S3 folder like the engine's keys", () => {
    expect(folderError("import")).toBeUndefined();
    expect(folderError("import/2026-10")).toBeUndefined();
    expect(folderError("")).toBeUndefined();
    for (const bad of ["/import", "import/", "a b", "x/../y", "a*"]) {
      expect(folderError(bad), bad).toBe("import.error.folder");
    }
  });

  it("restores a single file from its key and stripes from a pattern", () => {
    expect(restoreKey("import", ["/d/app_db.bak"])).toBe("import/app_db.bak");
    expect(restoreKey("", ["/d/app_db.bak"])).toBe("app_db.bak");
    expect(restoreKey("import", ["/d/crm_1.bak", "/d/crm_2.bak", "/d/crm_3.bak"])).toBe(
      "import/crm_*.bak",
    );
    // Overlapping start and end stay inside the shortest name.
    expect(restoreKey("i", ["/d/ab.bak", "/d/ab1.bak"])).toBe("i/ab*.bak");
  });

  it("builds the upload and the restores of the inspected backups", () => {
    const stripes = backup(["/d/crm_1.bak", "/d/crm_2.bak"], {
      header: {
        ...(backup([]).header as NonNullable<InspectedBackup["header"]>),
        backupType: "differential",
      },
    });
    const r = result([backup(["/d/app_db.bak"]), stripes]);
    expect(uploadRequest(r, "import", false)).toEqual({
      files: ["/d/app_db.bak", "/d/crm_1.bak", "/d/crm_2.bak"],
      prefix: "import",
    });
    expect(uploadRequest(r, "import", true).overwrite).toBe(true);
    expect(
      restoreRequests(
        r,
        [
          { database: "app_db", noRecovery: true },
          { database: "crm", noRecovery: false },
        ],
        "import",
      ),
    ).toEqual([
      { database: "app_db", key: "import/app_db.bak", noRecovery: true },
      { database: "crm", key: "import/crm_*.bak", differential: true },
    ]);
  });

  it("goes on only when every backup is restorable", () => {
    expect(allRestorable(null)).toBe(false);
    expect(allRestorable(result([]))).toBe(false);
    expect(allRestorable(result([backup(["/a.bak"])]))).toBe(true);
    expect(
      allRestorable(
        result([backup(["/a.bak"]), backup(["/b.bak"], { outcome: "not-restorable" })]),
      ),
    ).toBe(false);
  });

  it("starts when only the source check waits for the upload", () => {
    const source = {
      step: "source",
      label: "Backup in S3",
      status: "failed" as const,
      detail: "not found",
    };
    const exists = {
      step: "database",
      label: "Target database",
      status: "failed" as const,
      detail: "exists",
    };
    const ok = { step: "tasks", label: "Native tasks", status: "ok" as const };
    expect(blockingChecks(preview([source, ok]))).toEqual([]);
    expect(blockingChecks(preview([source, exists]))).toEqual([exists]);
    expect(previewReady(BATCH, [preview([source, ok])])).toBe(true);
    expect(previewReady(BATCH, [preview([exists])])).toBe(false);
    expect(
      previewReady({ ...BATCH, rejected: [{ path: "/a.bak", reason: "exists" }] }, [preview([ok])]),
    ).toBe(false);
    expect(previewReady(null, [preview([ok])])).toBe(false);
    expect(previewReady(BATCH, [])).toBe(false);
  });

  it("asks each confirmation once", () => {
    expect(confirmations(BATCH, [preview([])])).toEqual([]);
    expect(
      confirmations({ ...BATCH, confirmation: "prod-sql" }, [
        preview([], "prod-sql"),
        preview([], "prod-sql"),
      ]),
    ).toEqual(["prod-sql"]);
  });

  it("measures the space and the transfers", () => {
    expect(spaceUse(50, 100)).toEqual({ percent: 50, tone: "ok" });
    expect(spaceUse(85, 100)).toEqual({ percent: 85, tone: "warning" });
    expect(spaceUse(150, 100)).toEqual({ percent: 150, tone: "failed" });
    expect(spaceUse(1, undefined)).toBeNull();
    expect(formatDuration(95)).toBe("1:35");
    expect(formatDuration(3725)).toBe("1:02:05");
    expect(formatDuration(-3)).toBe("0:00");
    expect(percentOf(512, 1024)).toBe(50);
    expect(percentOf(5, 0)).toBe(0);
    expect(percentOf(2000, 1024)).toBe(100);
  });

  it("has a message for every error it returns", () => {
    for (const key of [
      "import.error.dbName",
      "import.error.dbSystem",
      "import.error.dbDuplicate",
      "import.error.folder",
    ]) {
      expect(messages.en).toHaveProperty(key);
    }
  });
});
