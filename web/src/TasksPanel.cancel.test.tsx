import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { TasksPanel } from "./TasksPanel";
import { BACKUP_FAILED, RESTORING, renderI18n } from "./test/fixtures";
import { ENGINE, json, mockFetch, requestsTo, sseStream } from "./test/http";
import { CANCEL_PREVIEW } from "./test/plans";

const TASKS = "GET /api/v1/targets/dev-sql/tasks";
const PLAN = "POST /api/v1/targets/dev-sql/plans/cancel";

describe("TasksPanel cancel", () => {
  it("cancels a running task through its plan, then reloads the list", async () => {
    const stream = sseStream();
    const fetch = mockFetch({
      [TASKS]: () => json({ tasks: [RESTORING, BACKUP_FAILED] }),
      [PLAN]: () => json(CANCEL_PREVIEW),
      [`${PLAN}/execute`]: () => stream.response,
    });
    renderI18n(<TasksPanel engine={ENGINE} target="dev-sql" />);
    const [running, failed] = await screen.findAllByRole("listitem");
    expect(within(failed).queryByRole("button", { name: /^Cancel/ })).toBeNull();
    await userEvent.click(within(running).getByRole("button", { name: "Cancel task #7" }));

    const dialog = within(screen.getByRole("dialog", { name: "Cancel task #7" }));
    expect(await dialog.findByText("Native task")).toBeInTheDocument();
    expect(JSON.parse(String(requestsTo(fetch, PLAN)[0].body))).toEqual({ taskId: 7 });
    await userEvent.click(dialog.getByRole("button", { name: "Cancel task" }));
    stream.send("step", { step: "cancel", label: "Cancel task 7", status: "ok" });
    stream.send("done", { ok: true });
    stream.close();
    expect(
      await dialog.findByText(/^Cancel requested: RDS stops the task at its next step/, {
        selector: ".banner-text",
      }),
    ).toBeInTheDocument();
    await userEvent.click(dialog.getByRole("button", { name: "Close" }));
    await waitFor(() => expect(requestsTo(fetch, TASKS)).toHaveLength(2));
  });

  it("offers no cancel while a cancel is already requested", async () => {
    mockFetch({
      [TASKS]: () => json({ tasks: [{ ...RESTORING, lifecycle: "CANCEL_REQUESTED" }] }),
    });
    renderI18n(<TasksPanel engine={ENGINE} target="dev-sql" />);
    await screen.findByText("Cancelling");
    expect(screen.queryByRole("button", { name: "Cancel task #7" })).toBeNull();
  });
});
