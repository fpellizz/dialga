import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { StepEvent } from "./api";
import type { ExportItemRun } from "./exportRun";
import { ImportProgress, ImportResult, type RunState } from "./ImportRun";
import { DEV, renderI18n } from "./test/fixtures";
import { ENGINE } from "./test/http";

function ev(step: string, label: string): StepEvent {
  return { step, label, status: "ok" };
}

const SWAP: ExportItemRun = {
  database: "sales",
  key: "import/sales.bak",
  state: "running",
  strategy: "swap",
  steps: [
    ev("sales/restore", "Restore sales_tmp from s3://b/import/sales.bak (temporary name)"),
    ev("sales/wait", "Wait for the restore task"),
    ev("sales/rename-new", "Rename sales_tmp to sales (the restored database takes its place)"),
  ],
};

const PLAIN: ExportItemRun = {
  database: "hr",
  key: "import/hr.bak",
  state: "running",
  steps: [ev("hr/restore", "Restore hr from s3://b/import/hr.bak")],
};

function run(items: ExportItemRun[], phase: RunState["phase"] = "running"): RunState {
  return { phase, items };
}

function labels(name: string) {
  const list = within(screen.getByRole("heading", { name }).closest("li") as HTMLElement);
  return list.getAllByRole("listitem").map((li) => li.querySelector(".step-label")?.textContent);
}

describe("ImportProgress step labels", () => {
  it("translates the steps of a swap and of a plain restore per item", () => {
    renderI18n(<ImportProgress run={run([SWAP, PLAIN])} onCancel={() => {}} />, "it");
    expect(labels("Ripristino di sales")).toEqual([
      "Ripristina sales accanto a quello attuale (nome temporaneo)",
      "Attesa del task",
      "Rinomina il database ripristinato in sales",
    ]);
    expect(labels("Ripristino di hr")).toEqual(["Ripristino di hr da import/hr.bak"]);
  });

  it("labels a swap from the first step, before the rename is announced", () => {
    const early = { ...SWAP, steps: SWAP.steps.slice(0, 1) };
    renderI18n(<ImportProgress run={run([early])} onCancel={() => {}} />, "it");
    expect(labels("Ripristino di sales")).toEqual([
      "Ripristina sales accanto a quello attuale (nome temporaneo)",
    ]);
  });

  it("shows the plan labels in English", () => {
    renderI18n(<ImportProgress run={run([SWAP, PLAIN])} onCancel={() => {}} />);
    expect(labels("Restore sales")).toEqual([
      "Restore sales beside the current one (temporary name)",
      "Wait for the task",
      "Rename the restored database to sales",
    ]);
  });
});

describe("ImportResult step labels", () => {
  it("translates the steps of a failed drop with a backup first", () => {
    const failed: ExportItemRun = {
      database: "sales",
      key: "import/sales.bak",
      state: "failed",
      strategy: "drop",
      steps: [
        ev("sales/backup", "Back up sales to s3://b/export/x.bak (safety copy before the replace)"),
        ev("sales/wait-backup", "Wait for the backup task"),
        ev("sales/drop", "Drop database sales"),
      ],
    };
    renderI18n(
      <ImportResult
        engine={ENGINE}
        target={DEV}
        destinations={[
          { database: "sales", noRecovery: false, replace: "drop", backupFirst: true },
        ]}
        run={run([failed], "failed")}
        onAgain={() => {}}
      />,
      "it",
    );
    const section = screen.getByRole("region", { name: /sales/ });
    expect(
      within(section)
        .getAllByRole("listitem")
        .map((li) => li.querySelector(".step-label")?.textContent),
    ).toEqual([
      "Backup di sales su S3 (copia di sicurezza prima della sostituzione)",
      "Attendi il task di backup",
      "Eliminazione del database sales",
    ]);
  });
});
