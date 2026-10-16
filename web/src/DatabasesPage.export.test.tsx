import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Database } from "./api";
import { DatabasesPage } from "./DatabasesPage";
import { CONFIG_PATH, DEV, renderI18n } from "./test/fixtures";
import { ENGINE, json, mockFetch } from "./test/http";

const db = (name: string, state = "ONLINE"): Database => ({
  name,
  state,
  recovery: "FULL",
  compatibility: 140,
  owner: "rdsa",
  userAccess: "MULTI_USER",
  readOnly: false,
  createdAt: "2026-10-15T08:00:00",
  dataMB: 8,
  logMB: 2,
});

function serve() {
  mockFetch({
    "GET /api/v1/targets": () => json({ configPath: CONFIG_PATH, targets: [DEV] }),
    "GET /api/v1/targets/dev-sql/databases": () =>
      json({ databases: [db("sales"), db("crm"), db("staged", "RESTORING")] }),
  });
}

describe("DatabasesPage export selection", () => {
  it("exports the ticked online databases in the order of the list", async () => {
    serve();
    renderI18n(<DatabasesPage engine={ENGINE} engineError="" />);
    const exportButton = await screen.findByRole("button", { name: "Export selected (0)" });
    expect(exportButton).toBeDisabled();
    // A restoring database cannot be backed up: no checkbox.
    const staged = screen.getByRole("rowheader", { name: /^staged/ }).closest("tr") as HTMLElement;
    expect(within(staged).queryByRole("checkbox")).toBeNull();

    await userEvent.click(screen.getByRole("checkbox", { name: "Select sales" }));
    await userEvent.click(screen.getByRole("checkbox", { name: "Select crm" }));
    const all = screen.getByRole("checkbox", { name: "Select all shown databases" });
    expect(all).toBeChecked();
    await userEvent.click(screen.getByRole("button", { name: "Export selected (2)" }));
    // Sorted by name: crm before sales.
    expect(window.location.hash).toBe("#export/dev-sql?db=crm&db=sales");
  });

  it("selects and clears all the shown databases at once", async () => {
    serve();
    renderI18n(<DatabasesPage engine={ENGINE} engineError="" />);
    const all = await screen.findByRole("checkbox", { name: "Select all shown databases" });
    await userEvent.click(all);
    expect(screen.getByRole("button", { name: "Export selected (2)" })).toBeEnabled();
    await userEvent.click(screen.getByRole("checkbox", { name: "Select crm" }));
    expect(all).not.toBeChecked();
    expect((all as HTMLInputElement).indeterminate).toBe(true);
    await userEvent.click(all);
    await userEvent.click(all);
    expect(screen.getByRole("button", { name: "Export selected (0)" })).toBeDisabled();
  });
});
