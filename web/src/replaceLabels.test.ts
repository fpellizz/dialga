import { describe, expect, it } from "vitest";
import { jobStepTitle } from "./ActivityPage";
import { translate } from "./i18n";
import type { JobItem, JobStep } from "./jobs";
import { replaceStrategy, stepLabel } from "./PlanPreview";

const it_ = translate("it");
const en = translate("en");

function step(id: string, title: string): JobStep {
  return { id, title, status: "succeeded" };
}

const SWAP = [
  step("restore", "Restore db from s3://b/in/db.bak (temporary name)"),
  step("wait", "Wait for the restore task"),
  step("single-user", "Close db to other sessions (disconnects them)"),
  step("rename-old", "Rename db to db_old (the current database is kept)"),
  step("rename-new", "Rename db_tmp to db (the restored database takes its place)"),
  step("multi-user", "Open db to all sessions again"),
  step("multi-user-old", "Open db_old to all sessions again"),
  step("post", "Post-restore report of db (read-only)"),
];

const DROP = [
  step("backup", "Back up db to s3://b/out/db.bak (safety copy before the replace)"),
  step("wait-backup", "Wait for the backup task"),
  step("drop", "Drop database db"),
  step("restore", "Restore db from s3://b/in/db.bak"),
  step("wait", "Wait for the restore task"),
];

function item(steps: JobStep[]): JobItem {
  return { name: "db", state: "running", percent: 0, steps };
}

describe("replace step labels", () => {
  it("tells a swap from a drop by the step ids, with prefixes and repeats", () => {
    expect(replaceStrategy(SWAP.map((s) => s.id))).toBe("swap");
    expect(replaceStrategy(DROP.map((s) => `db/${s.id}`))).toBe("drop");
    expect(replaceStrategy(["restore", "wait", "post"])).toBe("");
    expect(replaceStrategy(["drop"])).toBe("");
  });

  it("translates the steps of a swap in the activity detail", () => {
    const swap = item(SWAP);
    const titles = SWAP.map((s) => jobStepTitle(it_, s, swap));
    expect(titles).toEqual([
      "Ripristina db accanto a quello attuale (nome temporaneo)",
      "Attesa del task",
      "Chiudi db alle altre sessioni (le disconnette)",
      "Rinomina db col nome vecchio (il database attuale resta)",
      "Rinomina il database ripristinato in db",
      "Riapri db a tutte le sessioni",
      "Riapri il vecchio database a tutte le sessioni",
      "Report post-ripristino di db (sola lettura)",
    ]);
  });

  it("translates drop and backup-first steps, matching repeated ids by id", () => {
    const drop = item(DROP);
    expect(DROP.map((s) => jobStepTitle(it_, s, drop))).toEqual([
      "Backup di db su S3 (copia di sicurezza prima della sostituzione)",
      "Attendi il task di backup",
      "Eliminazione del database db",
      "Ripristino di db da s3://b/in/db.bak",
      "Attesa del task",
    ]);
    expect(jobStepTitle(en, step("drop#2", "x"), drop)).toBe("Drop database db");
  });

  it("keeps the engine title outside a replace", () => {
    const plain = item([step("restore", "Restore db from s3://b/k"), step("wait", "Wait")]);
    expect(jobStepTitle(it_, plain.steps[0], plain)).toBe("Restore db from s3://b/k");
  });

  it("translates the import progress events with the same labels", () => {
    const ev = { step: "db/rename-new", label: "Rename db_tmp to db", status: "ok" as const };
    expect(stepLabel(it_, ev, { database: "db", key: "k" }, "swap")).toBe(
      "Rinomina il database ripristinato in db",
    );
  });
});
