import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import type { PasswordInfo } from "./api";
import { PasswordDialog } from "./PasswordDialog";
import { CONFIG_PATH, DEV, renderI18n } from "./test/fixtures";
import { deferred, ENGINE, json, mockFetch, type Route, requestsTo, text } from "./test/http";

const TARGETS = "GET /api/v1/targets";
const PUT = "PUT /api/v1/targets/dev-sql/password";

const PASSWORD: PasswordInfo = { source: "prompt", available: false, canRemember: true };

function targets(password: PasswordInfo): Route {
  return () => json({ configPath: CONFIG_PATH, targets: [{ ...DEV, password }] });
}

function open(put: Route, password: PasswordInfo = PASSWORD) {
  const fetch = mockFetch({ [TARGETS]: targets(password), [PUT]: put });
  const onClose = vi.fn();
  const onSaved = vi.fn();
  renderI18n(
    <PasswordDialog engine={ENGINE} target="dev-sql" onClose={onClose} onSaved={onSaved} />,
  );
  return { fetch, onClose, onSaved };
}

const field = () => screen.getByLabelText("Password") as HTMLInputElement;
const connect = () => screen.getByRole("button", { name: /^Connect/ });

describe("PasswordDialog", () => {
  it("is a named dialog on a password field that takes the focus", async () => {
    open(() => new Response(null, { status: 204 }));
    const dialog = screen.getByRole("dialog", { name: "SQL password for dev-sql" });
    expect(dialog).toBeVisible();
    expect(field()).toHaveAttribute("type", "password");
    expect(field()).toHaveAttribute("autocomplete", "current-password");
    expect(field()).toHaveAccessibleDescription(/never written to disk/);
    await waitFor(() => expect(field()).toHaveFocus());
    expect(await screen.findByText("SQL user: admin")).toBeVisible();
  });

  it("shows and hides the password", async () => {
    open(() => new Response(null, { status: 204 }));
    const toggle = screen.getByRole("button", { name: "Show password" });
    expect(toggle).toHaveAttribute("aria-pressed", "false");
    await userEvent.click(toggle);
    expect(field()).toHaveAttribute("type", "text");
    expect(toggle).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(toggle);
    expect(field()).toHaveAttribute("type", "password");
  });

  it("verifies and saves: the password only travels in the body", async () => {
    const { fetch, onSaved, onClose } = open(() => new Response(null, { status: 204 }));
    await userEvent.type(field(), "s3cret");
    await userEvent.click(connect());
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1));
    const [request] = requestsTo(fetch, PUT);
    expect(JSON.parse(String(request?.body))).toEqual({ password: "s3cret", remember: false });
    const urls = fetch.mock.calls.map(([input]) => String(input));
    expect(urls.some((u) => u.includes("s3cret"))).toBe(false);
    expect(urls.some((u) => u.includes("/password?verify=1"))).toBe(true);
    expect(onClose).not.toHaveBeenCalled();
  });

  it("is busy while it connects, without losing the focus", async () => {
    const answer = deferred<Response>();
    const { fetch } = open(() => answer.promise);
    await userEvent.type(field(), "x");
    await userEvent.click(connect());
    const busy = await screen.findByRole("button", { name: /^Connecting/ });
    expect(busy).toHaveAttribute("aria-disabled", "true");
    await userEvent.click(busy);
    expect(requestsTo(fetch, PUT)).toHaveLength(1);
    answer.resolve(new Response(null, { status: 204 }));
  });

  it("asks for a password before it calls the engine", async () => {
    const { fetch } = open(() => new Response(null, { status: 204 }));
    await userEvent.click(connect());
    expect(screen.getByText("Enter the password.")).toBeVisible();
    expect(field()).toHaveAttribute("aria-invalid", "true");
    expect(field()).toHaveFocus();
    expect(requestsTo(fetch, PUT)).toHaveLength(0);
  });

  it("remembers in the keyring only when there is one", async () => {
    const { fetch } = open(() => new Response(null, { status: 204 }));
    const box = await screen.findByRole("checkbox", { name: "Remember in the system keyring" });
    await waitFor(() => expect(box).toBeEnabled());
    await userEvent.type(field(), "pw");
    await userEvent.click(box);
    await userEvent.click(connect());
    await waitFor(() => expect(requestsTo(fetch, PUT)).toHaveLength(1));
    expect(JSON.parse(String(requestsTo(fetch, PUT)[0]?.body)).remember).toBe(true);
  });

  it("disables Remember, with the reason, without a keyring", async () => {
    open(() => new Response(null, { status: 204 }), { ...PASSWORD, canRemember: false });
    const box = screen.getByRole("checkbox", { name: "Remember in the system keyring" });
    expect(box).toBeDisabled();
    expect(box).not.toBeChecked();
    expect(screen.getByText(/No system keyring is available/)).toBeVisible();
  });

  it("shows SQL Server's refusal with what to do and puts the focus back on the field", async () => {
    const { onSaved, onClose } = open(() => text("Login failed for user 'admin'.\n", 422));
    await userEvent.type(field(), "bad");
    await userEvent.click(connect());
    const banner = await screen.findByRole("alert");
    expect(banner).toHaveTextContent("Login failed for user 'admin'.");
    expect(banner).toHaveTextContent("check the password and the SQL user");
    expect(field()).toHaveAttribute("aria-invalid", "true");
    await waitFor(() => expect(field()).toHaveFocus());
    expect(onSaved).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
  });

  it.each([
    [409, "The SQL password of dev-sql comes from the environment.", "not entered here"],
    [501, "no system keyring", "Untick"],
  ])("explains a %i", async (status, message, hint) => {
    open(() => text(`${message}\n`, status));
    await userEvent.type(field(), "pw");
    await userEvent.click(connect());
    const banner = await screen.findByRole("alert");
    expect(banner).toHaveTextContent(message);
    expect(banner).toHaveTextContent(hint);
  });

  it("closes on Cancel and on Escape", async () => {
    const first = open(() => new Response(null, { status: 204 }));
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(first.onClose).toHaveBeenCalledTimes(1);
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(first.onClose).toHaveBeenCalledTimes(2));
  });

  it("starts empty every time it opens: the field is cleared on close", async () => {
    mockFetch({ [TARGETS]: targets(PASSWORD) });
    function Harness() {
      const [on, setOn] = useState(true);
      return (
        <>
          <button type="button" onClick={() => setOn(true)}>
            Again
          </button>
          {on && (
            <PasswordDialog
              engine={ENGINE}
              target="dev-sql"
              onClose={() => setOn(false)}
              onSaved={() => setOn(false)}
            />
          )}
        </>
      );
    }
    renderI18n(<Harness />);
    await userEvent.type(field(), "typed");
    expect(field()).toHaveValue("typed");
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    // The typed value is nowhere in the page any more.
    expect(document.body.innerHTML).not.toContain("typed");
    await userEvent.click(screen.getByRole("button", { name: "Again" }));
    expect(field()).toHaveValue("");
  });
});
