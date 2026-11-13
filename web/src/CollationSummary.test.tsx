import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { CollationSummary } from "./CollationSummary";
import { COLLATION_OVERVIEW } from "./test/collation";
import { renderWithPassword } from "./test/fixtures";
import { ENGINE, json, mockFetch, requestsTo, text } from "./test/http";

const OVERVIEW = "GET /api/v1/targets/dev-sql/collation";

describe("CollationSummary", () => {
  it("reads nothing until it is asked", async () => {
    const fetch = mockFetch({ [OVERVIEW]: () => json(COLLATION_OVERVIEW) });
    renderWithPassword(<CollationSummary engine={ENGINE} target="dev-sql" />);
    expect(requestsTo(fetch, OVERVIEW)).toHaveLength(0);
    const button = screen.getByRole("button", { name: "Show collation of dev-sql" });
    expect(button).toHaveAttribute("aria-expanded", "false");

    await userEvent.click(button);
    expect(await screen.findByText("Fixed on RDS")).toBeInTheDocument();
    expect(screen.getAllByText("SQL_Latin1_General_CP1_CI_AS")).toHaveLength(2);
    const link = screen.getByRole("link", { name: "Databases that differ from the server: 1" });
    expect(link).toHaveAttribute("href", "#databases/dev-sql");
    expect(screen.getByText(/at risk with temporary tables: 1/)).toBeInTheDocument();
    expect(requestsTo(fetch, OVERVIEW)).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Hide collation of dev-sql" })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
  });

  it("says when every database matches the server", async () => {
    mockFetch({
      [OVERVIEW]: () =>
        json({
          ...COLLATION_OVERVIEW,
          databases: COLLATION_OVERVIEW.databases.map((d) => ({
            ...d,
            mismatchServer: false,
            tempTableRisk: false,
          })),
        }),
    });
    renderWithPassword(<CollationSummary engine={ENGINE} target="dev-sql" />);
    await userEvent.click(screen.getByRole("button", { name: "Show collation of dev-sql" }));
    expect(await screen.findByText("All match the server")).toBeInTheDocument();
  });

  it("shows the error of the read", async () => {
    mockFetch({ [OVERVIEW]: () => text("SQL Server on x: login failed\n", 502) });
    renderWithPassword(<CollationSummary engine={ENGINE} target="dev-sql" />);
    await userEvent.click(screen.getByRole("button", { name: "Show collation of dev-sql" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("login failed");
  });
});
