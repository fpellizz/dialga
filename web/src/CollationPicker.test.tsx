import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { beforeEach, describe, expect, it } from "vitest";
import { CollationPicker } from "./CollationPicker";
import { clearCollationCache } from "./collation";
import { COLLATIONS } from "./test/collation";
import { renderWithPassword } from "./test/fixtures";
import { ENGINE, json, mockFetch } from "./test/http";

const LIST = "GET /api/v1/targets/dev-sql/collations";

function Harness({ initial = "", onValue }: { initial?: string; onValue?: (v: string) => void }) {
  const [value, setValue] = useState(initial);
  return (
    <CollationPicker
      id="coll"
      engine={ENGINE}
      target="dev-sql"
      value={value}
      onChange={(v) => {
        setValue(v);
        onValue?.(v);
      }}
      label="Collation"
      suggestions={[{ name: "SQL_Latin1_General_CP1_CI_AS", kind: "server" }]}
    />
  );
}

beforeEach(() => {
  clearCollationCache();
  mockFetch({ [LIST]: () => json({ collations: COLLATIONS }) });
});

describe("CollationPicker", () => {
  it("opens with the server collation first and shows the traits", async () => {
    renderWithPassword(<Harness />);
    const box = await screen.findByRole("combobox", { name: "Collation" });
    await screen.findByRole("button", { name: "Show the collations" });
    await userEvent.click(box);
    const options = await screen.findAllByRole("option");
    expect(options[0]).toHaveTextContent("SQL_Latin1_General_CP1_CI_AS");
    expect(options[0]).toHaveTextContent("Server");
    expect(options[0]).toHaveTextContent("CI·AS");
    expect(options).toHaveLength(COLLATIONS.length);
  });

  it("filters while typing and picks with the keyboard", async () => {
    const seen: string[] = [];
    renderWithPassword(<Harness onValue={(v) => seen.push(v)} />);
    const box = await screen.findByRole("combobox", { name: "Collation" });
    await screen.findByRole("button", { name: "Show the collations" });
    await userEvent.type(box, "utf8");
    const options = await screen.findAllByRole("option");
    expect(options.map((o) => o.textContent)).toEqual([
      expect.stringContaining("Latin1_General_100_CI_AS_SC_UTF8"),
    ]);
    await userEvent.keyboard("{ArrowDown}{Enter}");
    expect(box).toHaveValue("Latin1_General_100_CI_AS_SC_UTF8");
    expect(box).toHaveAttribute("aria-expanded", "false");
    expect(seen.at(-1)).toBe("Latin1_General_100_CI_AS_SC_UTF8");
  });

  it("Escape closes the list and keeps the dialog behind it", async () => {
    renderWithPassword(<Harness />);
    const box = await screen.findByRole("combobox", { name: "Collation" });
    await screen.findByRole("button", { name: "Show the collations" });
    await userEvent.click(box);
    expect(box).toHaveAttribute("aria-expanded", "true");
    await userEvent.keyboard("{Escape}");
    expect(box).toHaveAttribute("aria-expanded", "false");
  });

  it("marks a name that the instance does not have", async () => {
    renderWithPassword(<Harness />);
    const box = await screen.findByRole("combobox", { name: "Collation" });
    await screen.findByRole("button", { name: "Show the collations" });
    await userEvent.type(box, "Nope_CI_AS");
    expect(box).toHaveAttribute("aria-invalid", "true");
    expect(box).toHaveAccessibleDescription(/No collation of the instance has this name/);
    expect(screen.queryByRole("option")).toBeNull();
    await userEvent.clear(box);
    await userEvent.type(box, "latin1_general_bin2");
    expect(box).not.toHaveAttribute("aria-invalid");
  });
});
