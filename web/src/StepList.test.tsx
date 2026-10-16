import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { StepEvent } from "./api";
import { StepList } from "./StepList";
import { renderI18n } from "./test/fixtures";

const STEPS: StepEvent[] = [
  { step: "aws", label: "AWS credentials", status: "ok", detail: "profile default" },
  { step: "instance", label: "RDS instance", status: "running" },
  { step: "storage", label: "Storage", status: "warning", detail: "no CloudWatch data" },
  { step: "bucket", label: "S3 bucket", status: "failed", detail: "bucket not found" },
  { step: "sql", label: "SQL Server login", status: "skipped", detail: "needs a connection" },
];

function rows() {
  return screen.getAllByRole("listitem");
}

describe("StepList", () => {
  it("shows every step in order with its status badge and icon", () => {
    renderI18n(<StepList steps={STEPS} />);
    const expected = [
      ["AWS credentials", "Passed", "badge-ok", ".tone-success"],
      ["RDS instance", "Running", "badge-running", ".spin.tone-info"],
      ["Storage", "Warning", "badge-warning", ".tone-warning"],
      ["S3 bucket", "Failed", "badge-failed", ".tone-destructive"],
      ["SQL Server login (TLS verified)", "Skipped", "badge-skipped", ".tone-muted"],
    ];
    expect(rows()).toHaveLength(expected.length);
    rows().forEach((row, i) => {
      const [label, status, badge, icon] = expected[i];
      expect(within(row).getByText(label)).toBeInTheDocument();
      expect(within(row).getByText(status)).toHaveClass("badge", badge);
      expect(row.querySelector(`svg${icon}`)).not.toBeNull();
    });
  });

  it("translates step labels and statuses", () => {
    renderI18n(<StepList steps={STEPS} />, "it");
    expect(rows().map((row) => row.textContent)).toEqual([
      "Credenziali AWSprofile defaultSuperato",
      "Istanza RDSIn corso",
      "Spazio di archiviazioneno CloudWatch dataAttenzione",
      "Bucket S3bucket not foundControlla il bucket del target nella configurazione.Fallito",
      "Login SQL Server (TLS verificato)needs a connectionSaltato",
    ]);
  });

  it("falls back to the engine label for a step the UI does not know", () => {
    renderI18n(<StepList steps={[{ step: "future", label: "Future step", status: "ok" }]} />, "it");
    expect(screen.getByText("Future step")).toBeInTheDocument();
  });

  it("shows the detail only when the engine sent one", () => {
    const { container } = renderI18n(<StepList steps={STEPS} />);
    expect(container.querySelectorAll(".step-detail")).toHaveLength(4);
    expect(within(rows()[1]).queryByText(/./, { selector: ".step-detail" })).toBeNull();
  });

  it("adds what to do to a failed step only", () => {
    const { container } = renderI18n(<StepList steps={STEPS} />);
    expect(container.querySelectorAll(".step-hint")).toHaveLength(1);
    expect(
      within(rows()[3]).getByText("Check the bucket of the target in the configuration."),
    ).toHaveClass("step-hint");
  });

  it.each([
    [
      { step: "aws", status: "failed", detail: "failed to get shared config profile, dev" },
      "Create the profile dev in ~/.aws/config (aws configure sso), or fix aws_profile in the configuration.",
    ],
    [
      { step: "instance", status: "failed", detail: "modifying · sqlserver-se 14.00.3465.1.v1" },
      "Wait until the instance is available and its option group in-sync, then run the check again.",
    ],
    [
      { step: "option", status: "failed", detail: "SQLSERVER_BACKUP_RESTORE missing" },
      "Add SQLSERVER_BACKUP_RESTORE to the option group of the instance, with the IAM role for the bucket, and wait until it is in-sync.",
    ],
  ] as [Omit<StepEvent, "label">, string][])("hints at the fix of a failed %o", (step, hint) => {
    renderI18n(<StepList steps={[{ ...step, label: step.step }]} />);
    expect(screen.getByText(hint)).toHaveClass("step-hint");
  });

  it("names the list for screen readers", () => {
    renderI18n(<StepList steps={STEPS} />);
    expect(screen.getByRole("list", { name: "Check steps" })).toBeInTheDocument();
  });

  it("renders an empty list without steps", () => {
    renderI18n(<StepList steps={[]} />);
    expect(screen.getByRole("list")).toBeEmptyDOMElement();
  });
});
