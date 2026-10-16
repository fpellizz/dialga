import { describe, expect, it } from "vitest";
import {
  estimateMinutes,
  exportFromHash,
  exportHref,
  hasErrors,
  isAbsolutePath,
  keyError,
  localPath,
  nextOccurrence,
  overlap,
  parseWindow,
  validateOptions,
} from "./exportPlan";

const at = (iso: string) => new Date(iso);

describe("export address", () => {
  it("carries the target and the databases", () => {
    expect(exportHref()).toBe("#export");
    expect(exportHref("dev-sql")).toBe("#export/dev-sql");
    const href = exportHref("dev-sql", ["sales", "crm"]);
    expect(href).toBe("#export/dev-sql?db=sales&db=crm");
    expect(exportFromHash(href)).toEqual({ target: "dev-sql", databases: ["sales", "crm"] });
  });

  it("ignores other pages and broken addresses", () => {
    expect(exportFromHash("#export")).toEqual({ target: null, databases: [] });
    expect(exportFromHash("#databases/dev-sql")).toEqual({ target: null, databases: [] });
    expect(exportFromHash("#export/%E0%A4%A")).toEqual({ target: null, databases: [] });
  });
});

describe("options", () => {
  it("checks the keys like the engine, empty meaning the default", () => {
    expect(keyError("")).toBeUndefined();
    expect(keyError("export/sales.bak")).toBeUndefined();
    expect(keyError("export/*.bak")).toBe("export.error.keyStar");
    for (const bad of ["/export/a.bak", "a b.bak", "export/../a.bak", "a'b.bak"]) {
      expect(keyError(bad), bad).toBe("export.error.key");
    }
  });

  it("wants a free key per database and an absolute folder for the downloads", () => {
    const off = { enabled: false, folder: "", overwrite: false };
    expect(hasErrors(validateOptions(["sales"], {}, off))).toBe(false);
    const errors = validateOptions(
      ["sales", "crm", "hr"],
      { sales: "export/x.bak", crm: " export/x.bak ", hr: "bad key" },
      { enabled: true, folder: "backups", overwrite: false },
    );
    expect(errors.keys).toEqual({ crm: "export.error.keyTwice", hr: "export.error.key" });
    expect(errors.folder).toBe("export.error.folderAbsolute");
    expect(validateOptions([], {}, { ...off, enabled: true }).folder).toBe("export.error.folder");
  });

  it("knows absolute paths on every system", () => {
    for (const p of ["/home/u/backups", "C:\\Backups", "D:/x", "\\\\server\\share"]) {
      expect(isAbsolutePath(p), p).toBe(true);
    }
    expect(isAbsolutePath("backups")).toBe(false);
  });

  it("names the local file after the key", () => {
    expect(localPath("/home/u/backups", "export/sales_20261016.bak")).toBe(
      "/home/u/backups/sales_20261016.bak",
    );
    expect(localPath("/home/u/backups/", "a.bak")).toBe("/home/u/backups/a.bak");
    expect(localPath("C:\\Backups", "export/a.bak")).toBe("C:\\Backups\\a.bak");
  });
});

describe("windows", () => {
  it("reads the daily and weekly windows of RDS", () => {
    expect(parseWindow("04:29-04:59")).toEqual({ start: 269, end: 299, period: 1440 });
    // Across midnight: the end belongs to the next day.
    expect(parseWindow("23:30-00:15")).toEqual({ start: 1410, end: 1455, period: 1440 });
    expect(parseWindow("sun:05:00-sun:05:30")).toEqual({
      start: 6 * 1440 + 300,
      end: 6 * 1440 + 330,
      period: 7 * 1440,
    });
    // Sunday night to Monday morning wraps the week.
    expect(parseWindow("sun:23:00-mon:01:00")?.end).toBe(7 * 1440 + 60);
    for (const bad of [undefined, "", "4:29-4:59", "xyz:05:00-sun:05:30", "25:00-26:00"]) {
      expect(parseWindow(bad), String(bad)).toBeNull();
    }
  });

  it("finds the window that the backups would cross", () => {
    const daily = parseWindow("04:29-04:59");
    expect(daily).not.toBeNull();
    if (!daily) {
      return;
    }
    // 2026-10-16 is a Friday.
    expect(overlap(daily, at("2026-10-16T04:00:00Z"), at("2026-10-16T04:20:00Z"))).toBeNull();
    expect(overlap(daily, at("2026-10-16T04:00:00Z"), at("2026-10-16T04:40:00Z"))).toEqual({
      start: at("2026-10-16T04:29:00Z"),
      end: at("2026-10-16T04:59:00Z"),
    });
    // Started inside the window.
    expect(overlap(daily, at("2026-10-16T04:45:00Z"), at("2026-10-16T05:30:00Z"))?.start).toEqual(
      at("2026-10-16T04:29:00Z"),
    );
    // Long enough to reach tomorrow's window.
    expect(overlap(daily, at("2026-10-16T22:00:00Z"), at("2026-10-17T05:00:00Z"))?.start).toEqual(
      at("2026-10-17T04:29:00Z"),
    );
    const midnight = parseWindow("23:30-00:15");
    expect(
      midnight && overlap(midnight, at("2026-10-17T00:00:00Z"), at("2026-10-17T00:10:00Z")),
    ).toEqual({ start: at("2026-10-16T23:30:00Z"), end: at("2026-10-17T00:15:00Z") });
  });

  it("finds the weekly window on its day only", () => {
    const weekly = parseWindow("sun:05:00-sun:05:30");
    expect(weekly).not.toBeNull();
    if (!weekly) {
      return;
    }
    expect(overlap(weekly, at("2026-10-16T05:00:00Z"), at("2026-10-16T06:00:00Z"))).toBeNull();
    expect(overlap(weekly, at("2026-10-18T04:50:00Z"), at("2026-10-18T05:10:00Z"))).toEqual({
      start: at("2026-10-18T05:00:00Z"),
      end: at("2026-10-18T05:30:00Z"),
    });
    expect(nextOccurrence(weekly, at("2026-10-16T12:00:00Z"))).toEqual({
      start: at("2026-10-18T05:00:00Z"),
      end: at("2026-10-18T05:30:00Z"),
    });
    // During the window, the next one is the current one.
    expect(nextOccurrence(weekly, at("2026-10-18T05:10:00Z")).start).toEqual(
      at("2026-10-18T05:00:00Z"),
    );
  });

  it("estimates the backups roughly", () => {
    expect(estimateMinutes([])).toBe(0);
    expect(estimateMinutes([{ dataMB: 8, logMB: 8 }])).toBe(3);
    expect(
      estimateMinutes([
        { dataMB: 6000, logMB: 1000 },
        { dataMB: 600, logMB: 0 },
      ]),
    ).toBe(15);
  });
});
