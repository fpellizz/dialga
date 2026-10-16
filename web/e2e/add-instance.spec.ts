import { readFile } from "node:fs/promises";
import type { Route } from "@playwright/test";
import { configPath, removeConfig } from "./config";
import { expect, test } from "./fixtures";

test.beforeEach(async () => {
  await removeConfig();
});

/** The dev origin the page runs on: the engine answers it with CORS headers. */
const CORS = { "Access-Control-Allow-Origin": "http://localhost:1420" };

/** Answers one discovery call in place of AWS (the engine has no credentials here). */
function answer(body: unknown) {
  return (route: Route) =>
    route.request().method() === "OPTIONS"
      ? route.fulfill({
          status: 204,
          headers: { ...CORS, "Access-Control-Allow-Headers": "Authorization, Content-Type" },
        })
      : route.fulfill({ json: body, headers: CORS });
}

test("adds a target that uses the integrated tunnel by default", async ({ page }) => {
  // Only the discovery is faked: the engine itself validates and writes the target.
  await page.route(
    (url) => url.pathname === "/api/v1/discovery/profiles",
    answer({ profiles: [{ name: "dialga-e2e", region: "eu-west-1" }] }),
  );
  await page.route(
    (url) => url.pathname === "/api/v1/discovery/instances",
    answer({
      profile: "dialga-e2e",
      region: "eu-west-1",
      accountId: "123456789012",
      instances: [
        {
          identifier: "e2e-sqlserver",
          engine: "sqlserver-se",
          engineVersion: "14.00.3540.1.v1",
          class: "db.m6i.large",
          status: "available",
          masterUser: "admin",
          optionGroup: "e2e-options",
          backupRestore: true,
        },
      ],
    }),
  );

  await page.goto("/");
  const empty = page.getByRole("status").filter({ hasText: "No instances yet" });
  await empty.getByRole("button", { name: "Add instance" }).click();
  const dialog = page.getByRole("dialog", { name: "Add instance" });
  await expect(dialog.getByRole("combobox", { name: "AWS profile" })).toHaveValue("dialga-e2e");
  await dialog.getByRole("button", { name: "Discover instances" }).click();
  // One instance: chosen for the user, the target fields follow.
  await expect(dialog.getByRole("radio", { name: /^e2e-sqlserver/ })).toBeChecked();
  await dialog.getByRole("textbox", { name: "S3 bucket" }).fill("e2e-backups");

  await expect(dialog.getByRole("radio", { name: "Integrated tunnel" })).toBeChecked();
  await expect(dialog.getByText(/^Recommended: Dialga opens the SSM tunnel/)).toBeVisible();
  await expect(dialog.getByRole("textbox", { name: "Local port" })).toHaveCount(0);

  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog).toHaveCount(0);
  const card = page.getByRole("region", { name: "e2e-sqlserver" });
  await expect(card.getByText("Integrated tunnel")).toBeVisible();

  const config = await readFile(configPath(), "utf8");
  expect(config).toMatch(/connect:\n\s+mode: tunnel\n/);
  expect(config).not.toContain("local_port");
});
