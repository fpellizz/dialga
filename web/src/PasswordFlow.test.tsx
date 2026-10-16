import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import { ApiError, failure, PasswordNeededError, passwordNeeded } from "./api";
import { DatabasesPage } from "./DatabasesPage";
import { ExportPage } from "./ExportPage";
import { resetBackends } from "./exportBackend";
import { sqlSessionHint } from "./hints";
import { InstancesPage } from "./InstancesPage";
import { CONFIG_PATH, DEV, database, overview, renderWithPassword } from "./test/fixtures";
import { ENGINE, json, mockFetch, requestsTo, text } from "./test/http";

const NEEDED = "SQL password needed for target dev-sql";

/** The engine's 428: text and the header that names the target. */
function needed(target = "dev-sql"): Response {
  return new Response(`SQL password needed for target ${target}\n`, {
    status: 428,
    headers: { "Content-Type": "text/plain; charset=utf-8", "Dialga-Password-Target": target },
  });
}

const PASSWORD = { source: "prompt", available: false, canRemember: false } as const;
const WITH_PASSWORD = { ...DEV, password: PASSWORD };
const ok = () => new Response(null, { status: 204 });

async function enterPassword() {
  await userEvent.click(await screen.findByRole("button", { name: "Enter password for dev-sql" }));
  const dialog = await screen.findByRole("dialog", { name: "SQL password for dev-sql" });
  await userEvent.type(screen.getByLabelText("Password"), "s3cret");
  await userEvent.click(screen.getByRole("button", { name: "Connect" }));
  await waitFor(() => expect(dialog).not.toBeInTheDocument());
}

beforeEach(() => {
  window.history.replaceState(null, "", "/");
  localStorage.setItem("dialga.databases.target", "dev-sql");
  resetBackends();
});

describe("a 428 from the engine", () => {
  it("becomes a typed error with the target, from the header or the text", async () => {
    const error = await failure(needed("lab-sql"));
    expect(error).toBeInstanceOf(PasswordNeededError);
    expect(error).toBeInstanceOf(ApiError);
    expect(passwordNeeded(error)).toBe("lab-sql");
    // Kept as text by a page: the message says the same.
    expect(passwordNeeded(NEEDED)).toBe("dev-sql");
    expect(passwordNeeded(new Error("something else"))).toBeNull();
    expect(passwordNeeded(await failure(text("nope\n", 428)))).toBeNull();
  });

  it("names the way the engine connects in the hint of a SQL session", () => {
    expect(sqlSessionHint("tunnel").key).toBe("databases.errorHint.tunnel");
    expect(sqlSessionHint("external").key).toBe("databases.errorHint.external");
    expect(sqlSessionHint("direct").key).toBe("databases.errorHint.direct");
    expect(sqlSessionHint().key).toBe("databases.errorHint");
  });
});

describe("the password dialog from the pages", () => {
  it("Databases: asks, saves and loads the list again", async () => {
    const fetch = mockFetch({
      "GET /api/v1/targets": () => json({ configPath: CONFIG_PATH, targets: [WITH_PASSWORD] }),
      "GET /api/v1/targets/dev-sql/databases": (_req, call) =>
        call === 0 ? needed() : json({ databases: [database("sales")] }),
      "PUT /api/v1/targets/dev-sql/password": ok,
    });
    renderWithPassword(<DatabasesPage engine={ENGINE} engineError="" />);
    const banner = await screen.findByRole("alert");
    expect(banner).toHaveTextContent("The SQL password for dev-sql is needed");
    expect(banner).not.toHaveTextContent("SQL password needed for target");
    await enterPassword();
    expect(await screen.findByRole("table")).toBeVisible();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(requestsTo(fetch, "GET /api/v1/targets/dev-sql/databases")).toHaveLength(2);
  });

  it("Export, step 1: asks, saves and shows the databases", async () => {
    window.location.hash = "#export/dev-sql";
    const fetch = mockFetch({
      "GET /api/v1/targets": () => json({ configPath: CONFIG_PATH, targets: [WITH_PASSWORD] }),
      "GET /api/v1/targets/dev-sql/databases": (_req, call) =>
        call === 0 ? needed() : json({ databases: [database("sales")] }),
      "PUT /api/v1/targets/dev-sql/password": ok,
    });
    renderWithPassword(<ExportPage engine={ENGINE} engineError="" />);
    expect(await screen.findByText("The SQL password for dev-sql is needed")).toBeVisible();
    await enterPassword();
    expect(await screen.findByRole("checkbox", { name: /sales/ })).toBeVisible();
    expect(screen.queryByText("The SQL password for dev-sql is needed")).toBeNull();
    expect(requestsTo(fetch, "GET /api/v1/targets/dev-sql/databases")).toHaveLength(2);
  });

  it("Instances: the card says where the password comes from and forgets it", async () => {
    let state = { source: "prompt", available: true, canRemember: false };
    const fetch = mockFetch({
      "GET /api/v1/targets": () =>
        json({ configPath: CONFIG_PATH, targets: [{ ...DEV, password: state }] }),
      "GET /api/v1/targets/dev-sql/overview": () => json(overview("dev-sql")),
      "DELETE /api/v1/targets/dev-sql/password": () => {
        state = { ...state, available: false };
        return ok();
      },
    });
    renderWithPassword(<InstancesPage engine={ENGINE} engineError="" />);
    expect(await screen.findByText("Asked once per session · entered")).toBeVisible();
    await userEvent.click(screen.getByRole("button", { name: "Forget password of dev-sql" }));
    expect(await screen.findByText("Asked once per session · not entered yet")).toBeVisible();
    expect(requestsTo(fetch, "DELETE /api/v1/targets/dev-sql/password")).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Enter password for dev-sql" })).toBeVisible();
  });

  it.each([
    [
      { source: "env", available: true, canRemember: false, envVar: "SQL_PW" },
      "From environment variable SQL_PW",
      false,
    ],
    [{ source: "env", available: true, canRemember: false }, "From environment", false],
    [
      { source: "keyring", available: true, canRemember: true },
      "Saved in the system keyring",
      true,
    ],
    [
      { source: "secretsmanager", available: true, canRemember: false },
      "From AWS Secrets Manager",
      false,
    ],
  ])("Instances: %j", async (info, label, forget) => {
    mockFetch({
      "GET /api/v1/targets": () =>
        json({ configPath: CONFIG_PATH, targets: [{ ...DEV, password: info }] }),
      "GET /api/v1/targets/dev-sql/overview": () => json(overview("dev-sql")),
    });
    renderWithPassword(<InstancesPage engine={ENGINE} engineError="" />);
    expect(await screen.findByText(label)).toBeVisible();
    expect(!!screen.queryByRole("button", { name: /^Forget password/ })).toBe(forget);
  });
});
