import { FAKE_TARGET, removeConfig, writeConfig } from "./config";
import { expect, test } from "./fixtures";

test.beforeEach(async () => {
  await removeConfig();
});

test("walks the import wizard up to the first error: a file that is not there", async ({
  page,
}) => {
  await writeConfig(FAKE_TARGET);
  await page.goto("/");
  // From the card of the instance, like a user would.
  const card = page.getByRole("region", { name: "e2e-fake" });
  await card.getByRole("link", { name: "Import into e2e-fake" }).click();
  await expect(page).toHaveURL(/#import\/e2e-fake$/);
  await expect(page.getByRole("heading", { level: 1, name: "Import" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Import" })).toHaveAttribute("aria-current", "page");
  await expect(page.getByLabel("Instance")).toHaveValue("e2e-fake");

  // 1. Files: a relative path is refused in the page, before the engine.
  await expect(page.getByRole("heading", { level: 2, name: "Files" })).toBeVisible();
  const next = page.getByRole("button", { name: "Next" });
  await expect(next).toBeDisabled();
  const paths = page.getByLabel("File paths");
  await paths.fill("backups/app_db.bak");
  await page.getByRole("button", { name: "Add files" }).click();
  await expect(paths).toHaveAttribute("aria-invalid", "true");
  await expect(page.getByText("backups/app_db.bak is not an absolute path")).toBeVisible();

  await paths.fill("/nonexistent/dialga-e2e/app_db.bak");
  await page.getByRole("button", { name: "Add files" }).click();
  await expect(page.getByText("app_db.bak", { exact: true })).toBeVisible();

  // 2. Verify: the engine checks the path before starting any container.
  await next.click();
  const verify = page.getByRole("heading", { level: 2, name: "Verify" });
  await expect(verify).toBeVisible();
  await expect(verify).toBeFocused();
  await expect(page.getByRole("alert")).toContainText(
    "/nonexistent/dialga-e2e/app_db.bak: no such file or directory",
  );
  await expect(page.getByRole("button", { name: "Verify again" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Next" })).toBeDisabled();

  // Back to the files, the list is still there.
  await page.getByRole("button", { name: "Back" }).click();
  await expect(page.getByRole("heading", { level: 2, name: "Files" })).toBeFocused();
  await expect(page.getByText("/nonexistent/dialga-e2e/app_db.bak")).toBeVisible();
});
