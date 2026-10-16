import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import type { ReplaceMode } from "./plans";
import { ReplaceChoice } from "./ReplaceChoice";
import { renderI18n } from "./test/fixtures";

function Harness({
  onChange,
}: {
  onChange: (v: { replace: ReplaceMode; backupFirst: boolean }) => void;
}) {
  const [value, setValue] = useState<{ replace: ReplaceMode; backupFirst: boolean }>({
    replace: "",
    backupFirst: true,
  });
  return (
    <ReplaceChoice
      database="sales"
      mode={value.replace}
      backupFirst={value.backupFirst}
      onChange={(v) => {
        setValue(v);
        onChange(v);
      }}
    />
  );
}

describe("ReplaceChoice", () => {
  it("is a named radiogroup with Stop chosen and a hint on every option", () => {
    renderI18n(<Harness onChange={vi.fn()} />);
    const group = screen.getByRole("radiogroup", { name: "The database sales already exists" });
    expect(group).toBeInTheDocument();
    const radios = screen.getAllByRole("radio");
    expect(radios.map((r) => (r as HTMLInputElement).labels?.[0].textContent)).toEqual([
      "Stop",
      "Replace: choose automatically (recommended)",
      "Keep the old one (swap)",
      "Drop it and restore",
    ]);
    expect(screen.getByRole("radio", { name: "Stop" })).toBeChecked();
    for (const radio of radios) {
      expect(radio).toHaveAccessibleDescription(/\S/);
    }
    expect(screen.queryByRole("checkbox")).toBeNull();
  });

  it("reports the choice with the keyboard", async () => {
    const onChange = vi.fn();
    renderI18n(<Harness onChange={onChange} />);
    await userEvent.click(screen.getByRole("radio", { name: "Stop" }));
    await userEvent.keyboard("{ArrowDown}");
    expect(onChange).toHaveBeenLastCalledWith({ replace: "auto", backupFirst: true });
    await userEvent.click(screen.getByRole("radio", { name: "Keep the old one (swap)" }));
    expect(onChange).toHaveBeenLastCalledWith({ replace: "swap", backupFirst: true });
    expect(screen.queryByRole("checkbox")).toBeNull();
  });

  it("backs up before a drop, and warns clearly when that is switched off", async () => {
    const onChange = vi.fn();
    renderI18n(<Harness onChange={onChange} />);
    await userEvent.click(screen.getByRole("radio", { name: "Drop it and restore" }));
    const backup = screen.getByRole("checkbox", {
      name: "Back up the current database to S3 first",
    });
    expect(backup).toBeChecked();
    expect(screen.queryByRole("alert")).toBeNull();

    await userEvent.click(backup);
    expect(onChange).toHaveBeenLastCalledWith({ replace: "drop", backupFirst: false });
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Without the backup, sales is lost for good",
    );
    await userEvent.click(backup);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("speaks Italian", () => {
    renderI18n(<Harness onChange={vi.fn()} />, "it");
    expect(
      screen.getByRole("radiogroup", { name: "Il database sales esiste già" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Tieni il vecchio (swap)" })).toBeInTheDocument();
  });
});
