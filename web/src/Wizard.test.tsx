import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { renderI18n } from "./test/fixtures";
import { Wizard } from "./Wizard";

const STEPS = [
  { id: "one", label: "Databases" },
  { id: "two", label: "Options" },
  { id: "three", label: "Preview" },
];

function Demo({ locked = false, start = 0 }: { locked?: boolean; start?: number }) {
  const [current, setCurrent] = useState(start);
  return (
    <Wizard
      title="Export"
      lead="Back up databases to S3."
      steps={STEPS}
      current={current}
      onStep={setCurrent}
      locked={locked}
      footer={
        <button type="button" onClick={() => setCurrent((c) => c + 1)}>
          Next
        </button>
      }
    >
      <p>Panel {current}</p>
    </Wizard>
  );
}

describe("Wizard", () => {
  it("marks the current step and names the panel after it", () => {
    renderI18n(<Demo start={1} />);
    expect(screen.getByRole("heading", { level: 1, name: "Export" })).toBeInTheDocument();
    const steps = within(screen.getByRole("navigation", { name: "Steps" }));
    expect(steps.getAllByRole("listitem")).toHaveLength(3);
    expect(steps.getByText("Options").closest("[aria-current]")).toHaveAttribute(
      "aria-current",
      "step",
    );
    expect(screen.getByRole("region", { name: "Step 2 of 3: Options" })).toBeInTheDocument();
    // The done step is a button; the next one is plain text.
    expect(steps.getByRole("button", { name: "Go back to Databases" })).toBeInTheDocument();
    expect(steps.queryByRole("button", { name: /Preview/ })).toBeNull();
  });

  it("moves the focus to the step heading on change, not on the first render", async () => {
    renderI18n(<Demo />);
    const heading = screen.getByRole("heading", { level: 2 });
    expect(heading).not.toHaveFocus();
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(screen.getByRole("heading", { level: 2, name: "Step 2 of 3: Options" })).toHaveFocus();
    await userEvent.click(screen.getByRole("button", { name: "Go back to Databases" }));
    expect(screen.getByRole("heading", { level: 2, name: "Step 1 of 3: Databases" })).toHaveFocus();
  });

  it("does not offer the earlier steps while locked", () => {
    renderI18n(<Demo start={2} locked />);
    const steps = within(screen.getByRole("navigation", { name: "Steps" }));
    expect(steps.queryAllByRole("button")).toHaveLength(0);
    expect(steps.getByText("Databases").parentElement).toHaveTextContent("(done)");
  });

  it("speaks Italian", () => {
    const onStep = vi.fn();
    renderI18n(
      <Wizard title="Esporta" steps={STEPS} current={1} onStep={onStep}>
        <p>x</p>
      </Wizard>,
      "it",
    );
    expect(screen.getByRole("navigation", { name: "Passi" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Passo 2 di 3: Options" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Torna a Databases" })).toBeInTheDocument();
  });
});
