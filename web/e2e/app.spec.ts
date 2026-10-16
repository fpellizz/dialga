import { readFile } from "node:fs/promises";
import { BROKEN, configPath, FAKE_TARGET, removeConfig, TWO_TARGETS, writeConfig } from "./config";
import { expect, test } from "./fixtures";

// Every test starts like a first run: no configuration file.
test.beforeEach(async () => {
  await removeConfig();
});

test("navigates with the sidebar, the hash and history", async ({ page }) => {
  await page.goto("/");
  const nav = page.getByRole("navigation");
  const instances = nav.getByRole("link", { name: "Instances" });
  const databases = nav.getByRole("link", { name: "Databases" });
  const settings = page.getByRole("link", { name: "Settings" });
  await expect(page.getByRole("heading", { level: 1, name: "Instances" })).toBeVisible();
  await expect(instances).toHaveAttribute("aria-current", "page");
  await expect(page.getByText("engine dev")).toBeVisible();
  // Every item of the sidebar is a page.
  await expect(nav.getByRole("link")).toHaveText([
    "Instances",
    "Databases",
    "Import",
    "Export",
    "Activity",
    "Settings",
  ]);

  await databases.click();
  await expect(page).toHaveURL(/#databases$/);
  await expect(page.getByRole("heading", { level: 1, name: "Databases" })).toBeVisible();
  await expect(databases).toHaveAttribute("aria-current", "page");
  await expect(instances).not.toHaveAttribute("aria-current");

  await settings.click();
  await expect(page).toHaveURL(/#settings$/);
  await expect(page.getByRole("heading", { level: 1, name: "Settings" })).toBeVisible();
  // As a page load would: the window title, and the focus on the page title.
  await expect(page).toHaveTitle("Settings · Dialga");
  await expect(page.getByRole("heading", { level: 1, name: "Settings" })).toBeFocused();
  await expect(settings).toHaveAttribute("aria-current", "page");
  await expect(instances).not.toHaveAttribute("aria-current");

  await page.goBack();
  await expect(page.getByRole("heading", { level: 1, name: "Databases" })).toBeVisible();
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
  await page.getByRole("button", { name: "Refresh the instances" }).click();
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
  const verdict = card.locator(".banner-fail");
  await expect(verdict).toHaveText("Some checks failed · Failed: 1 · warnings: 0", {
    timeout: 15_000,
  });
  // The card's polite live region says the same, once.
  await expect(card.getByRole("status")).toHaveText("Some checks failed · Failed: 1 · warnings: 0");
  // No DIALGA_SQL_PASSWORD in the engine environment: said up front.
  await expect(card.getByText(/^SQL login will be skipped: /)).toBeVisible();

  const steps = card.getByRole("listitem");
  await expect(steps).toHaveCount(8);
  await expect(steps.first()).toContainText("AWS credentials");
  await expect(steps.first()).toContainText("dialga-e2e-missing");
  await expect(steps.first().locator(".badge")).toHaveText("Failed");
  // What to do, from the engine's message.
  await expect(steps.first().locator(".step-hint")).toHaveText(
    "Create the profile dialga-e2e-missing in ~/.aws/config (aws configure sso), or fix aws_profile in the configuration.",
  );
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
  // 428 from the engine: the app asks for the password instead of showing the error.
  await expect(card.getByRole("alert")).toContainText("The SQL password for e2e-fake is needed");
});

test("shows why the overview of an instance is missing, and retries", async ({ page }) => {
  await writeConfig(FAKE_TARGET);
  await page.goto("/");
  const card = page.getByRole("region", { name: "e2e-fake" });
  // 502: the AWS profile does not exist, so not even the RDS part is read.
  const error = card.locator(".overview-error");
  await expect(error).toHaveText(/dialga-e2e-missing/);

  const again = page.waitForRequest(/\/api\/v1\/targets\/e2e-fake\/overview$/);
  await card.getByRole("button", { name: "Retry" }).click();
  await again;
  await expect(error).toHaveText(/dialga-e2e-missing/);
  await expect(card.getByRole("button", { name: "Retry" })).toBeEnabled();
});

test("sends to Instances from Databases without a configuration", async ({ page }) => {
  await page.goto("/#databases");
  const empty = page.getByRole("status").filter({ hasText: "No instances configured" });
  await expect(empty).toBeVisible();
  await expect(page.getByRole("combobox", { name: "Instance" })).toHaveCount(0);

  await empty.getByRole("link", { name: "Go to Instances" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Instances" })).toBeVisible();
  await expect(page.getByText("No instances yet")).toBeVisible();
});

test("shows why the databases cannot be listed, per target", async ({ page }) => {
  await writeConfig(TWO_TARGETS);
  await page.goto("/#databases/e2e-fake");
  const target = page.getByRole("combobox", { name: "Instance" });
  const banner = page.getByRole("alert");
  await expect(target).toHaveValue("e2e-fake");
  // 428: no SQL password for e2e-fake in the engine yet.
  await expect(banner).toContainText("The SQL password for e2e-fake is needed");
  await expect(page.getByRole("table")).toHaveCount(0);

  // 502: e2e-env has its password, its session fails at the AWS profile.
  await target.selectOption("e2e-env");
  await expect(page).toHaveURL(/#databases\/e2e-env$/);
  await expect(banner.locator(".banner-text")).toHaveText(/dialga-e2e-missing/);

  // The address and the remembered target both say e2e-env now.
  await page.reload();
  await expect(target).toHaveValue("e2e-env");
  await page.goto("/#databases");
  await page.reload();
  await expect(target).toHaveValue("e2e-env");
  await expect(banner.locator(".banner-text")).toHaveText(/dialga-e2e-missing/);
});

test("starts the first target from the empty state, as far as AWS lets it", async ({ page }) => {
  await page.goto("/");
  const empty = page.getByRole("status").filter({ hasText: "No instances yet" });
  await empty.getByRole("button", { name: "Add instance" }).click();
  const dialog = page.getByRole("dialog", { name: "Add instance" });
  await expect(dialog).toBeVisible();

  // The engine's AWS configuration is empty: no profiles to choose from.
  const profile = dialog.getByRole("textbox", { name: "AWS profile" });
  await expect(profile).toBeEnabled();
  await expect(profile).toBeFocused();
  await expect(dialog.getByText(/No AWS profiles on this computer/)).toBeVisible();

  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog.getByText("Choose an AWS profile.")).toBeVisible();
  await expect(dialog.getByText("Check the highlighted fields.")).toBeVisible();

  await profile.fill("dialga-e2e-missing");
  await dialog.getByRole("button", { name: "Discover instances" }).click();
  await expect(dialog.getByRole("alert").first()).toContainText("dialga-e2e-missing");

  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(empty.getByRole("button", { name: "Add instance" })).toBeFocused();
});

test("checks the prerequisites and removes a target after its name is retyped", async ({
  page,
}) => {
  await writeConfig(TWO_TARGETS);
  await page.goto("/#settings");
  // What is installed depends on the machine: two programs, each found or not.
  const prereqs = page.getByRole("region", { name: "Prerequisites" });
  await expect(prereqs.getByRole("listitem")).toHaveCount(2);
  await expect(prereqs.locator(".badge")).toHaveCount(2);

  const targets = page.getByRole("region", { name: "Targets" });
  await expect(targets.getByRole("listitem")).toHaveCount(2);

  // Escape gives the focus back to the button that asked.
  const removeFake = targets.getByRole("button", { name: "Remove e2e-fake" });
  await removeFake.click();
  await expect(targets.getByRole("textbox", { name: "Target name" })).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(targets.getByRole("form")).toHaveCount(0);
  await expect(removeFake).toBeFocused();

  await targets.getByRole("button", { name: "Remove e2e-env" }).click();
  const confirm = targets.getByRole("button", { name: "Remove target" });
  await targets.getByRole("textbox", { name: "Target name" }).fill("e2e-en");
  await expect(confirm).toBeDisabled();
  await targets.getByRole("textbox", { name: "Target name" }).fill("e2e-env");
  await confirm.click();

  await expect(targets.getByRole("status")).toHaveText("Target e2e-env removed.");
  await expect(targets.getByRole("listitem")).toHaveCount(1);
  await expect(targets.getByRole("heading", { name: "Targets" })).toBeFocused();
  // The engine rewrote the file: e2e-fake stays, e2e-env is gone.
  const text = await readFile(configPath(), "utf8");
  expect(text).toContain("e2e-fake:");
  expect(text).not.toContain("e2e-env:");
});
