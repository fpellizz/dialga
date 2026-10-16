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
      "Bucket S3bucket not foundFallito",
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

  it("renders an empty list without steps", () => {
    renderI18n(<StepList steps={[]} />);
    expect(screen.getByRole("list")).toBeEmptyDOMElement();
  });
});
