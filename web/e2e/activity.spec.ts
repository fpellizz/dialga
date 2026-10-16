import { removeConfig } from "./config";
import { expect, test } from "./fixtures";

test("shows the empty Activity page of an engine without jobs", async ({ page }) => {
  await removeConfig();
  await page.goto("/#activity");
  await expect(page.getByRole("heading", { level: 1, name: "Activity" })).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: "No jobs yet" })).toBeVisible();
  const activity = page.getByRole("link", { name: "Activity", exact: true });
  await expect(activity).toHaveAttribute("aria-current", "page");
  await expect(page.getByRole("alert")).toHaveCount(0);

  // Away from the page and back from the sidebar.
  await page.getByRole("link", { name: "Instances" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Instances" })).toBeVisible();
  await activity.click();
  await expect(page).toHaveURL(/#activity$/);
  await expect(page.getByRole("status").filter({ hasText: "No jobs yet" })).toBeVisible();

  // A job the engine does not know: its 404, said plainly.
  await page.goto("/#activity/20200101T000000Z-00000000");
  await expect(page.getByRole("alert")).toHaveText(
    "No job with this id: the history keeps the last 200 jobs, for 30 days at most.",
  );
  await page.getByRole("link", { name: "All jobs" }).click();
  await expect(page.getByRole("heading", { level: 1, name: "Activity" })).toBeVisible();
});
