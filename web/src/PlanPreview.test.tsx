import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { PlanPreviewView, readyWith } from "./PlanPreview";
import type { PlanPreview } from "./plans";
import { renderI18n } from "./test/fixtures";
import { DROP_PREVIEW, RESTORE_PREVIEW } from "./test/plans";

/** A restore whose backup is not in the bucket yet (the wizard uploads it next). */
const BEFORE_UPLOAD: PlanPreview = {
  ...RESTORE_PREVIEW,
  ready: false,
  checks: [
    {
      step: "source",
      label: "Backup in S3",
      status: "failed",
      detail: "object not found: s3://my-sqlserver-backups/import/sales.bak (upload it first)",
    },
    RESTORE_PREVIEW.checks[1],
  ],
};

describe("PlanPreviewView", () => {
  it("shows an expected failure as pending, and the plan as ready for it", () => {
    renderI18n(
      <PlanPreviewView
        preview={BEFORE_UPLOAD}
        confirm=""
        onConfirmChange={() => {}}
        pending={{ source: "Uploaded by the next step" }}
      />,
    );
    const source = screen.getByText("Backup in S3").closest("li") as HTMLElement;
    expect(within(source).getByText("Uploaded by the next step")).toBeInTheDocument();
    expect(within(source).getByText("Skipped")).toHaveClass("badge-skipped");
    expect(screen.queryByText(/the plan cannot run/)).toBeNull();
    expect(readyWith(BEFORE_UPLOAD, { source: "Uploaded by the next step" })).toBe(true);
    expect(readyWith(BEFORE_UPLOAD)).toBe(false);
    expect(readyWith(RESTORE_PREVIEW)).toBe(true);
  });

  it("says the plan cannot run when a failure is not expected", () => {
    renderI18n(<PlanPreviewView preview={BEFORE_UPLOAD} confirm="" onConfirmChange={() => {}} />);
    expect(screen.getByText("Failed")).toHaveClass("badge-failed");
    expect(screen.getByText(/the plan cannot run/)).toBeInTheDocument();
  });

  it("hands the typed confirmation and Enter to the caller", async () => {
    const onConfirmChange = vi.fn();
    const onSubmit = vi.fn();
    renderI18n(
      <PlanPreviewView
        preview={DROP_PREVIEW}
        confirm="sale"
        onConfirmChange={onConfirmChange}
        onSubmit={onSubmit}
        autoFocus={false}
      />,
    );
    const input = screen.getByRole("textbox", { name: "Type sales to confirm" });
    expect(input).not.toHaveFocus();
    expect(input).toHaveValue("sale");
    await userEvent.type(input, "s{Enter}");
    expect(onConfirmChange).toHaveBeenLastCalledWith("sales");
    expect(onSubmit).toHaveBeenCalledTimes(1);
  });
});
