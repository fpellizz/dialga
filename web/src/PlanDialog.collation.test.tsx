import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { clearCollationCache } from "./collation";
import { PlanDialog, type PlanResult } from "./PlanDialog";
import { COLLATION_VIEW, COLLATIONS, CONVERT_PREVIEW } from "./test/collation";
import { renderWithPassword } from "./test/fixtures";
import { ENGINE, json, mockFetch, requestsTo, sseStream } from "./test/http";

const VIEW = "GET /api/v1/targets/dev-sql/databases/sales/collation";
const LIST = "GET /api/v1/targets/dev-sql/collations";
const CONVERT = "POST /api/v1/targets/dev-sql/plans/convert-columns";

const body = (init: RequestInit) => JSON.parse(String(init.body));

function open() {
  const onClose = vi.fn<(r: PlanResult) => void>();
  renderWithPassword(
    <PlanDialog
      engine={ENGINE}
      target="dev-sql"
      operation="convert-columns"
      request={{ database: "sales" }}
      fields="convert-columns"
      onClose={onClose}
    />,
  );
  return { onClose, dialog: () => screen.getByRole("dialog") };
}

beforeEach(() => clearCollationCache());

describe("PlanDialog, convert-columns", () => {
  it("previews the database default with the tables ticked and runs the collation the preview resolved", async () => {
    const run = sseStream();
    const fetch = mockFetch({
      [VIEW]: () => json(COLLATION_VIEW),
      [LIST]: () => json({ collations: COLLATIONS }),
      [CONVERT]: () => json(CONVERT_PREVIEW),
      [`${CONVERT}/execute`]: () => run.response,
    });
    const { dialog } = open();
    const w = within(dialog());
    // The default of the database is the target until another one is chosen.
    const target = await w.findByRole("radio", {
      name: "Database default (Latin1_General_100_CI_AS)",
    });
    expect(target).toBeChecked();
    // The form waits: a preview reads the tables.
    expect(requestsTo(fetch, CONVERT)).toHaveLength(0);

    await userEvent.click(w.getByRole("radio", { name: "Only the tables I tick" }));
    // Some tables with none ticked is not a plan yet.
    expect(w.getByRole("button", { name: "Preview" })).toBeDisabled();
    // The ntext table cannot be converted: it is listed, marked, and cannot be ticked.
    const list = within(await w.findByRole("group", { name: "Tables to convert" }));
    expect(list.getByRole("checkbox", { name: "sales.orders" })).toBeDisabled();
    expect(list.getByText("Not convertible")).toBeInTheDocument();
    await userEvent.click(list.getByRole("checkbox", { name: "sales.refunds" }));
    await userEvent.click(list.getByRole("checkbox", { name: "dbo.customers" }));
    expect(w.getByText("Tables ticked: 2")).toBeInTheDocument();
    await userEvent.click(w.getByRole("checkbox", { name: "Skip the data checks" }));
    expect(w.getByText(/Without the checks a problem shows up/)).toBeInTheDocument();
    await userEvent.click(w.getByRole("checkbox", { name: "Skip the data checks" }));

    await userEvent.click(w.getByRole("button", { name: "Preview" }));
    expect(await w.findByText("Tables left as they are (1):")).toBeInTheDocument();
    expect(
      w.getByText(
        /Units: 1 · tables: 1 · columns: 2 · indexes: 3 · foreign keys: 1 · data: 12.5 MB/,
      ),
    ).toBeInTheDocument();
    expect(w.getByText(/text\/ntext: change it to varchar\(max\) first/)).toBeInTheDocument();
    expect(body(requestsTo(fetch, CONVERT)[0])).toEqual({
      database: "sales",
      tables: [
        { schema: "sales", name: "refunds" },
        { schema: "dbo", name: "customers" },
      ],
    });

    await userEvent.type(w.getByRole("textbox", { name: "Type sales to confirm" }), "sales");
    await userEvent.click(w.getByRole("button", { name: "Convert columns" }));
    // What runs is what was previewed: the collation the engine resolved, the same tables.
    expect(body(requestsTo(fetch, `${CONVERT}/execute`)[0])).toEqual({
      database: "sales",
      collation: "Latin1_General_100_CI_AS",
      confirm: "sales",
      digest: CONVERT_PREVIEW.digest,
    });
    run.send("done", { ok: true });
    run.close();
    await waitFor(() => expect(w.getByRole("button", { name: "Close" })).toBeInTheDocument());
  });

  it("asks for a new preview when the options change after one", async () => {
    mockFetch({
      [VIEW]: () => json(COLLATION_VIEW),
      [LIST]: () => json({ collations: COLLATIONS }),
      [CONVERT]: () => json(CONVERT_PREVIEW),
    });
    const { dialog } = open();
    const w = within(dialog());
    await w.findByRole("radio", { name: /Database default/ });
    await userEvent.click(w.getByRole("button", { name: "Preview" }));
    await w.findByText("What changes");
    await userEvent.click(w.getByRole("checkbox", { name: "Skip the data checks" }));
    expect(w.getByText("The values changed: preview again before running.")).toBeInTheDocument();
    expect(w.getByRole("button", { name: "Convert columns" })).toBeDisabled();
  });

  it("sends another collation, typed in the picker", async () => {
    const fetch = mockFetch({
      [VIEW]: () => json(COLLATION_VIEW),
      [LIST]: () => json({ collations: COLLATIONS }),
      [CONVERT]: () => json(CONVERT_PREVIEW),
    });
    const { dialog } = open();
    const w = within(dialog());
    await userEvent.click(await w.findByRole("radio", { name: "Another collation" }));
    await w.findByRole("button", { name: "Show the collations" });
    await userEvent.type(w.getByRole("combobox", { name: "Collation" }), "Cyrillic_General_CI_AS");
    await userEvent.click(w.getByRole("button", { name: "Preview" }));
    await w.findByText("What changes");
    expect(body(requestsTo(fetch, CONVERT)[0])).toEqual({
      database: "sales",
      collation: "Cyrillic_General_CI_AS",
    });
  });
});

describe("PlanDialog, convert-columns tables", () => {
  it("words the checklist honestly and warns when the engine truncated the columns", async () => {
    mockFetch({
      [VIEW]: () => json({ ...COLLATION_VIEW, truncated: true }),
      [LIST]: () => json({ collations: COLLATIONS }),
    });
    const { dialog } = open();
    const w = within(dialog());
    expect(
      await w.findByText("Tables with columns outside the database default"),
    ).toBeInTheDocument();
    expect(w.getByText(/lists at most 2,000 columns/)).toBeInTheDocument();
  });

  it("converts every table when the target is another collation", async () => {
    const fetch = mockFetch({
      [VIEW]: () => json(COLLATION_VIEW),
      [LIST]: () => json({ collations: COLLATIONS }),
      [CONVERT]: () => json(CONVERT_PREVIEW),
    });
    const { dialog } = open();
    const w = within(dialog());
    await userEvent.click(await w.findByRole("radio", { name: "Only the tables I tick" }));
    await userEvent.click(w.getByRole("radio", { name: "Another collation" }));
    // The list only has the columns that differ from the default: no checklist.
    expect(w.queryByRole("group", { name: "Tables to convert" })).toBeNull();
    expect(w.getByRole("radio", { name: "Only the tables I tick" })).toBeDisabled();
    expect(w.getByRole("radio", { name: "All tables" })).toBeChecked();
    expect(w.getByText(/every table is converted/)).toBeInTheDocument();
    // An empty picker is not the database default.
    expect(w.getByRole("button", { name: "Preview" })).toBeDisabled();
    await userEvent.type(w.getByRole("combobox", { name: "Collation" }), "Cyrillic_General_CI_AS");
    await userEvent.click(w.getByRole("button", { name: "Preview" }));
    await w.findByText("What changes");
    expect(body(requestsTo(fetch, CONVERT)[0]).tables).toBeUndefined();
  });

  it("does not run a plan without actions and says there is nothing to do", async () => {
    mockFetch({
      [VIEW]: () => json(COLLATION_VIEW),
      [LIST]: () => json({ collations: COLLATIONS }),
      [CONVERT]: () =>
        json({
          ...CONVERT_PREVIEW,
          plan: { ...CONVERT_PREVIEW.plan, actions: [] },
          confirmation: undefined,
        }),
    });
    const { dialog } = open();
    const w = within(dialog());
    await w.findByRole("radio", { name: /Database default/ });
    await userEvent.click(w.getByRole("button", { name: "Preview" }));
    expect(await w.findByText(/Nothing to do/)).toBeInTheDocument();
    expect(w.getByRole("button", { name: "Convert columns" })).toBeDisabled();
  });
});
