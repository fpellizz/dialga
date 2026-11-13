import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { CollationDialog } from "./CollationDialog";
import { clearCollationCache } from "./collation";
import {
  COLLATION_VIEW,
  COLLATIONS,
  CONVERT_PREVIEW,
  SET_COLLATION_PREVIEW,
} from "./test/collation";
import { renderWithPassword } from "./test/fixtures";
import { ENGINE, json, mockFetch, requestsTo, sseStream, text } from "./test/http";

const VIEW = "GET /api/v1/targets/dev-sql/databases/sales/collation";
const LIST = "GET /api/v1/targets/dev-sql/collations";
const SET = "POST /api/v1/targets/dev-sql/plans/set-collation";
const CONVERT = "POST /api/v1/targets/dev-sql/plans/convert-columns";

function open() {
  const onClose = vi.fn();
  renderWithPassword(
    <CollationDialog engine={ENGINE} target="dev-sql" database="sales" onClose={onClose} />,
  );
  return { onClose };
}

const body = (init: RequestInit) => JSON.parse(String(init.body));

beforeEach(() => clearCollationCache());

describe("CollationDialog", () => {
  it("shows the default, the server and tempdb with the temp table risk", async () => {
    mockFetch({ [VIEW]: () => json(COLLATION_VIEW) });
    open();
    const dialog = await screen.findByRole("dialog", { name: "Collation of sales" });
    await screen.findByText("Database default");
    const w = within(dialog);
    expect(w.getByText("Fixed on RDS")).toBeInTheDocument();
    expect(w.getByText("Differs from the server")).toBeInTheDocument();
    expect(w.getByText("Temporary table risk")).toBeInTheDocument();
    expect(w.getByText(/Msg 468/)).toBeInTheDocument();
    expect(w.getByText("COLLATE DATABASE_DEFAULT")).toBeInTheDocument();
    // The groups, the default marked.
    const groups = within(
      w.getByRole("table", { name: "Collations used by the columns of sales" }),
    );
    expect(groups.getByText("Default")).toBeInTheDocument();
    expect(groups.getByText("4")).toBeInTheDocument();
  });

  it("lists the columns that differ, filters them and flags the ones it cannot convert", async () => {
    mockFetch({ [VIEW]: () => json(COLLATION_VIEW) });
    open();
    const table = await screen.findByRole("table", {
      name: "Columns that do not use the default collation of sales",
    });
    expect(within(table).getAllByRole("row")).toHaveLength(5);
    expect(within(table).getByText("Not convertible")).toBeInTheDocument();
    await userEvent.type(screen.getByRole("searchbox", { name: "Filter" }), "refunds");
    expect(within(table).getAllByRole("row")).toHaveLength(2);
    expect(within(table).getByText("sales.refunds")).toBeInTheDocument();
  });

  it("says when every column uses the default", async () => {
    mockFetch({
      [VIEW]: () =>
        json({ ...COLLATION_VIEW, mismatchServer: false, tempTableRisk: false, columns: [] }),
    });
    open();
    expect(
      await screen.findByText("Every column uses the default collation of the database."),
    ).toBeInTheDocument();
    expect(screen.queryByText("Temporary table risk")).toBeNull();
  });

  it("shows the error of the read with a hint", async () => {
    mockFetch({ [VIEW]: () => text("database not found on the instance\n", 404) });
    open();
    expect(await screen.findByRole("alert")).toHaveTextContent("database not found");
  });

  it("opens the change plan with the collation typed, then reads the view again after a run", async () => {
    const run = sseStream();
    const fetch = mockFetch({
      [VIEW]: () => json(COLLATION_VIEW),
      [LIST]: () => json({ collations: COLLATIONS }),
      [SET]: () => json(SET_COLLATION_PREVIEW),
      [`${SET}/execute`]: () => run.response,
    });
    open();
    await screen.findByText("Database default");
    await userEvent.click(screen.getByRole("button", { name: "Change default collation…" }));
    const plan = await screen.findByRole("dialog", {
      name: "Change the default collation of sales",
    });
    const preview = within(plan).getByRole("button", { name: "Preview" });
    // Nothing to preview before a collation is chosen, and no request was made.
    expect(preview).toBeDisabled();
    expect(requestsTo(fetch, SET)).toHaveLength(0);
    // Free typing of a name the instance does not have does not go on.
    const box = within(plan).getByRole("combobox", { name: "New default collation" });
    await within(plan).findByRole("button", { name: "Show the collations" });
    await userEvent.type(box, "Nope_CI");
    expect(preview).toBeDisabled();
    await userEvent.clear(box);
    await userEvent.type(box, "Latin1_General_100_CS_AS");
    await waitFor(() => expect(preview).toBeEnabled());
    await userEvent.click(preview);

    // The preview: the summary above the actions and the check labels.
    expect(
      await within(plan).findByText(
        "The default collation changes from Latin1_General_100_CI_AS to Latin1_General_100_CS_AS.",
      ),
    ).toBeInTheDocument();
    expect(requestsTo(fetch, SET).map(body)).toEqual([
      { database: "sales", collation: "Latin1_General_100_CS_AS" },
    ]);
    const checks = within(within(plan).getByRole("region", { name: "Pre-flight checks" }));
    expect(checks.getByText("Names under the new collation")).toBeInTheDocument();
    expect(checks.getByText("Database access")).toBeInTheDocument();
    expect(checks.getByText("Existing columns")).toBeInTheDocument();

    await userEvent.type(
      within(plan).getByRole("textbox", { name: "Type sales to confirm" }),
      "sales",
    );
    await userEvent.click(within(plan).getByRole("button", { name: "Change collation" }));
    expect(body(requestsTo(fetch, `${SET}/execute`)[0])).toMatchObject({
      database: "sales",
      collation: "Latin1_General_100_CS_AS",
      confirm: "sales",
      digest: SET_COLLATION_PREVIEW.digest,
    });
    run.send("step", { step: "set-collation", label: "Change", status: "running" });
    run.send("step", { step: "set-collation", label: "Change", status: "ok" });
    run.send("done", { ok: true });
    run.close();
    expect(
      (await within(plan).findAllByText(/default collation of sales is now/)).length,
    ).toBeGreaterThan(0);

    // The fields of the plan read it too; the panel reads it again once the dialog closes.
    const before = requestsTo(fetch, VIEW).length;
    await userEvent.click(within(plan).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(requestsTo(fetch, VIEW).length).toBeGreaterThan(before));
  });

  it("offers the plan to convert the columns the change left behind, without chaining it", async () => {
    const run = sseStream();
    const fetch = mockFetch({
      [VIEW]: () => json(COLLATION_VIEW),
      [LIST]: () => json({ collations: COLLATIONS }),
      [SET]: () => json(SET_COLLATION_PREVIEW),
      [`${SET}/execute`]: () => run.response,
      [CONVERT]: () => json(CONVERT_PREVIEW),
    });
    open();
    await screen.findByText("Database default");
    await userEvent.click(screen.getByRole("button", { name: "Change default collation…" }));
    const plan = await screen.findByRole("dialog", {
      name: "Change the default collation of sales",
    });
    await within(plan).findByRole("button", { name: "Show the collations" });
    await userEvent.type(
      within(plan).getByRole("combobox", { name: "New default collation" }),
      "Latin1_General_100_CS_AS",
    );
    await userEvent.click(within(plan).getByRole("button", { name: "Preview" }));
    await userEvent.type(
      await within(plan).findByRole("textbox", { name: "Type sales to confirm" }),
      "sales",
    );
    await userEvent.click(within(plan).getByRole("button", { name: "Change collation" }));
    run.send("done", { ok: true });
    run.close();

    // Nothing converts by itself: the button opens the other plan, to be previewed first.
    const convert = await within(plan).findByRole("button", { name: "Convert the columns (4)…" });
    expect(requestsTo(fetch, CONVERT)).toHaveLength(0);
    await userEvent.click(convert);
    const next = await screen.findByRole("dialog", { name: "Convert the columns of sales" });
    expect(within(next).getByRole("radio", { name: /Database default/ })).toBeChecked();
    expect(requestsTo(fetch, CONVERT)).toHaveLength(0);
  });
});
