import { expect, test } from "@playwright/test";
import { BROKEN, configPath, FAKE_TARGET, removeConfig, writeConfig } from "./config";

// Every test starts like a first run: no configuration file.
test.beforeEach(async () => {
  await removeConfig();
});

test("navigates with the sidebar, the hash and history", async ({ page }) => {
  await page.goto("/");
  const instances = page.getByRole("link", { name: "Instances" });
  const settings = page.getByRole("link", { name: "Settings" });
  await expect(page.getByRole("heading", { level: 1, name: "Instances" })).toBeVisible();
  await expect(instances).toHaveAttribute("aria-current", "page");
  await expect(page.getByText("engine dev")).toBeVisible();
  // The coming pages are not links yet.
  await expect(page.getByRole("navigation").getByRole("link")).toHaveCount(1);

  await settings.click();
  await expect(page).toHaveURL(/#settings$/);
  await expect(page.getByRole("heading", { level: 1, name: "Settings" })).toBeVisible();
  await expect(settings).toHaveAttribute("aria-current", "page");
  await expect(instances).not.toHaveAttribute("aria-current");

  await page.goBack();
  await expect(page.getByRole("heading", { level: 1, name: "Instances" })).toBeVisible();

  await page.goto("/#settings");
  await expect(page.getByRole("heading", { level: 1, name: "Settings" })).toBeVisible();
});

test("keeps language and theme across a reload", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/#settings");
  const html = page.locator("html");
  await expect(html).toHaveAttribute("lang", "en");
  await expect(page.getByRole("radio", { name: "System" })).toBeChecked();
  await expect(html).toHaveAttribute("data-theme", "light");
  // "System" follows the OS while the app is open.
  await page.emulateMedia({ colorScheme: "dark" });
  await expect(html).toHaveAttribute("data-theme", "dark");

  await page.getByRole("radio", { name: "Italiano" }).check();
  await page.getByRole("radio", { name: "Chiaro" }).check();
  await expect(page.getByRole("heading", { level: 1, name: "Impostazioni" })).toBeVisible();
  await expect(html).toHaveAttribute("lang", "it");
  await expect(html).toHaveAttribute("data-theme", "light");

  await page.reload();
  await expect(page.getByRole("heading", { level: 1, name: "Impostazioni" })).toBeVisible();
  await expect(page.getByRole("radio", { name: "Italiano" })).toBeChecked();
  await expect(page.getByRole("radio", { name: "Chiaro" })).toBeChecked();
  await expect(html).toHaveAttribute("lang", "it");
  // A fixed theme wins over the dark OS.
  await expect(html).toHaveAttribute("data-theme", "light");
  await expect(page.getByRole("link", { name: "Istanze" })).toBeVisible();
});

test("shows where to create the configuration on the first run", async ({ page }) => {
  await page.goto("/");
  const empty = page.getByRole("status").filter({ hasText: "No instances yet" });
  await expect(empty).toBeVisible();
  await expect(empty.locator("code")).toHaveText(configPath());
  await expect(page.getByRole("alert")).toHaveCount(0);

  await page.getByRole("link", { name: "Settings" }).click();
  const config = page.getByRole("region", { name: "Configuration file" });
  await expect(config.locator("code")).toHaveText(configPath());
});

test("shows the errors of the configuration until it is fixed", async ({ page }) => {
  await writeConfig(BROKEN);
  await page.goto("/");
  const banner = page.getByRole("alert");
  await expect(banner).toContainText("The configuration file has errors");
  await expect(banner.locator("pre")).toContainText("bogus_key");
  await expect(page.getByText("No instances yet")).toHaveCount(0);

  // The engine reads the file again on every request: Refresh is enough.
  await writeConfig(FAKE_TARGET);
  await page.getByRole("button", { name: "Refresh" }).click();
  await expect(page.getByRole("region", { name: "e2e-fake" })).toBeVisible();
  await expect(banner).toHaveCount(0);
});

test("runs a check that stops at the AWS step", async ({ page }) => {
  await writeConfig(FAKE_TARGET);
  await page.goto("/");
  const card = page.getByRole("region", { name: "e2e-fake" });
  await expect(card.getByText("e2e-fake-instance · eu-west-1")).toBeVisible();
  await expect(card.getByText("Open tunnel on port 11433")).toBeVisible();

  await card.getByRole("button", { name: "Run check" }).click();
  await expect(card.getByText("Some checks failed")).toBeVisible({ timeout: 15_000 });
  // No DIALGA_SQL_PASSWORD in the engine environment: said up front.
  await expect(card.getByText(/^SQL login will be skipped: /)).toBeVisible();

  const steps = card.getByRole("listitem");
  await expect(steps).toHaveCount(8);
  await expect(steps.first()).toContainText("AWS credentials");
  await expect(steps.first()).toContainText("dialga-e2e-missing");
  await expect(steps.first().locator(".badge")).toHaveText("Failed");
  await expect(card.locator(".steps .badge-skipped")).toHaveCount(7);
  await expect(card.getByRole("button", { name: "Run check" })).toBeEnabled();
});

test("explains why the tasks cannot be listed", async ({ page }) => {
  await writeConfig(FAKE_TARGET);
  await page.goto("/");
  const card = page.getByRole("region", { name: "e2e-fake" });
  await card.getByRole("button", { name: "Show tasks" }).click();
  await expect(card.getByRole("button", { name: "Hide tasks" })).toHaveAttribute(
    "aria-expanded",
    "true",
  );
  // 422 from the engine, shown as is: no "Error:" prefix.
  await expect(card.getByRole("alert")).toHaveText(/^SQL password: /);
});
