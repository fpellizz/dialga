import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ReportDialog } from "./ReportDialog";
import { renderI18n } from "./test/fixtures";
import { deferred, ENGINE, json, mockFetch, text } from "./test/http";
import { REPORT } from "./test/plans";

const ROUTE = "GET /api/v1/targets/dev-sql/databases/sales/report";

function open(lang: "en" | "it" = "en") {
  const onClose = vi.fn();
  renderI18n(
    <ReportDialog engine={ENGINE} target="dev-sql" database="sales" onClose={onClose} />,
    lang,
  );
  return onClose;
}

describe("ReportDialog", () => {
  it("is busy until the report arrives", async () => {
    const answer = deferred<Response>();
    mockFetch({ [ROUTE]: () => answer.promise });
    open();
    const dialog = screen.getByRole("dialog", { name: "Report of sales" });
    expect(dialog).toHaveAttribute("aria-busy", "true");
    expect(screen.getByText("Reading the report…")).toBeInTheDocument();
    answer.resolve(json(REPORT));
    expect(await screen.findByRole("region", { name: "Objects" })).toBeInTheDocument();
    expect(dialog).toHaveAttribute("aria-busy", "false");
  });

  it("says when the login cannot enter the database", async () => {
    mockFetch({
      [ROUTE]: () =>
        json({
          ...REPORT,
          ok: false,
          access: false,
          orphanUsers: [],
          objects: [],
          findings: [
            REPORT.findings[0],
            {
              step: "access",
              label: "Login access",
              status: "failed",
              detail: "admin cannot enter sales: restored databases are owned by rdsa",
            },
          ],
        }),
    });
    open();
    expect(
      await screen.findByText(
        "The login cannot enter the database: users and objects are not read.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText(/owned by rdsa/)).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Objects" })).toBeNull();
    expect(screen.queryByRole("region", { name: "SQL users without a login" })).toBeNull();
  });

  it("explains an engine error and closes on Escape", async () => {
    mockFetch({ [ROUTE]: () => text("sales: database not found on the instance\n", 404) });
    const onClose = open("it");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("sales: database not found on the instance");
    expect(screen.getByRole("dialog", { name: "Report di sales" })).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
