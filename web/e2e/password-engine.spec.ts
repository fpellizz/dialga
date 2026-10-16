import { FAKE_TARGET, removeConfig, writeConfig } from "./config";
import { expect, test } from "./fixtures";

// Against the real engine of this build: e2e-fake has sql.password: prompt
// and the engine has no DIALGA_SQL_PASSWORD, so the databases answer 428. No
// SQL Server and no AWS: the verification of the password cannot reach the
// instance (no such AWS profile) and answers 502. The engine runs without a
// session bus, so its keyring is unusable.
test.beforeEach(async () => {
  await removeConfig();
  await writeConfig(FAKE_TARGET);
});

test("the engine asks for the SQL password; the verification cannot reach the instance", async ({
  page,
}) => {
  await page.goto("/#databases/e2e-fake");
  const banner = page.getByRole("alert");
  await expect(banner).toContainText("The SQL password for e2e-fake is needed");
  await banner.getByRole("button", { name: "Enter password for e2e-fake" }).click();

  const dialog = page.getByRole("dialog", { name: "SQL password for e2e-fake" });
  await expect(dialog.getByText("SQL user: e2e")).toBeVisible();
  await expect(dialog.getByLabel("Password", { exact: true })).toBeFocused();
  // No keyring: Remember is off, with the reason.
  const remember = dialog.getByRole("checkbox", { name: "Remember in the system keyring" });
  await expect(remember).toBeDisabled();
  await expect(dialog.getByText(/No system keyring is available/)).toBeVisible();

  await dialog.getByLabel("Password", { exact: true }).fill("not-a-real-password");
  await dialog.getByRole("button", { name: "Connect" }).click();
  // 502: the AWS profile does not exist. The dialog stays open, the field keeps the focus.
  await expect(dialog.getByRole("alert")).toContainText("dialga-e2e-missing");
  await expect(dialog.getByLabel("Password", { exact: true })).toBeFocused();
  await expect(dialog).toBeVisible();
  expect(page.url()).not.toContain("not-a-real-password");

  // Cancel: the databases still need it, and the page says so.
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toBeHidden();
  await expect(page.getByRole("alert")).toContainText("The SQL password for e2e-fake is needed");
});

test("the Instances card says the password is asked once per session", async ({ page }) => {
  await page.goto("/#instances");
  const card = page.getByRole("region", { name: "e2e-fake" });
  await expect(card.getByText("Asked once per session · not entered yet")).toBeVisible();
  await expect(
    card.getByRole("button", { name: "Enter password for e2e-fake" }).first(),
  ).toBeVisible();
});
