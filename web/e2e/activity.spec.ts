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

const ENGINE_ORIGIN = "http://localhost:1420";
const REFUSED_ID = "20261016T100000Z-00000003";

const job = (id: string, extra: Record<string, unknown>) => ({
  id,
  kind: "operation",
  origin: "app",
  target: "e2e-fake",
  instance: "e2e-fake-instance",
  state: "succeeded",
  createdAt: "2026-10-16T09:00:00Z",
  updatedAt: "2026-10-16T09:05:00Z",
  endedAt: "2026-10-16T09:05:00Z",
  databases: ["sales"],
  succeeded: 1,
  failed: 0,
  ...extra,
});

test("lists operations, the CLI and refused attempts, with filters", async ({ page }) => {
  await removeConfig();
  const jobs = [
    job("20261016T110000Z-00000004", { operation: "finish", origin: "cli", databases: ["hr"] }),
    job(REFUSED_ID, {
      operation: "drop",
      state: "failed",
      succeeded: 0,
      refusal: { status: 412, reason: "pre-flight checks failed" },
    }),
    job("20261016T080000Z-00000005", { kind: "export", state: "failed", succeeded: 0, failed: 1 }),
  ];
  await page.route("**/api/v1/jobs", (route) =>
    route.request().method() === "OPTIONS"
      ? route.continue()
      : route.fulfill({
          status: 200,
          contentType: "application/json",
          headers: { "Access-Control-Allow-Origin": ENGINE_ORIGIN },
          body: JSON.stringify({ jobs }),
        }),
  );
  await page.route(`**/api/v1/jobs/${REFUSED_ID}`, (route) =>
    route.request().method() === "OPTIONS"
      ? route.continue()
      : route.fulfill({
          status: 200,
          contentType: "application/json",
          headers: { "Access-Control-Allow-Origin": ENGINE_ORIGIN },
          body: JSON.stringify({
            ...jobs[1],
            maxTasks: 1,
            items: [],
            events: [],
            refusal: {
              status: 412,
              reason: "pre-flight checks failed",
              checks: [{ step: "database", label: "Database", status: "failed", detail: "gone" }],
            },
          }),
        }),
  );

  await page.goto("/#activity");
  const finish = page.getByRole("link", { name: /^Finish restore on e2e-fake/ });
  await expect(finish.getByText("CLI")).toBeVisible();
  const refused = page.getByRole("link", { name: /^Drop on e2e-fake/ });
  await expect(refused.getByText("Refused")).toBeVisible();
  await expect(refused.getByText("HTTP 412 · Precondition failed")).toBeVisible();
  await expect(page.getByText("Previews and dry runs are not recorded here.")).toBeVisible();

  // Chips: keyboard only.
  await page.getByRole("radio", { name: "All (3)" }).focus();
  await page.keyboard.press("ArrowRight");
  await page.keyboard.press("ArrowRight");
  await page.keyboard.press("ArrowRight");
  await expect(page.getByRole("radio", { name: "Refused (1)" })).toBeChecked();
  await expect(finish).toHaveCount(0);
  await expect(refused).toBeVisible();

  await refused.click();
  const panel = page.getByRole("region", { name: "Refused before running" });
  await expect(panel).toContainText("pre-flight checks failed");
  await expect(panel).toContainText("Database");
  await expect(panel).toContainText("gone");
});
