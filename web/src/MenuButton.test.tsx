import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { MenuButton } from "./MenuButton";

function setup() {
  const onExport = vi.fn();
  const onDrop = vi.fn();
  render(
    <div>
      <button type="button">Before</button>
      <MenuButton
        label="Actions for sales"
        items={[
          { key: "export", label: "Export", onSelect: onExport },
          { key: "access", label: "Access…", onSelect: vi.fn() },
          { key: "drop", label: "Drop", onSelect: onDrop, danger: true },
        ]}
      />
      <p>Elsewhere</p>
    </div>,
  );
  return { onExport, onDrop, button: screen.getByRole("button", { name: "Actions for sales" }) };
}

describe("MenuButton", () => {
  it("is a closed menu button at first", () => {
    const { button } = setup();
    expect(button).toHaveAttribute("aria-haspopup", "menu");
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("opens on click and focuses the first item", async () => {
    const { button } = setup();
    await userEvent.click(button);
    expect(button).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("menu", { name: "Actions for sales" })).toBeInTheDocument();
    expect(button).toHaveAttribute("aria-controls", screen.getByRole("menu").id);
    await waitFor(() => expect(screen.getByRole("menuitem", { name: "Export" })).toHaveFocus());
  });

  it("opens with the arrow down key", async () => {
    const { button } = setup();
    button.focus();
    await userEvent.keyboard("{ArrowDown}");
    expect(await screen.findByRole("menu")).toBeInTheDocument();
  });

  it("moves with the arrows, Home and End, and wraps", async () => {
    const { button } = setup();
    await userEvent.click(button);
    const [exp, acc, drop] = screen.getAllByRole("menuitem");
    await waitFor(() => expect(exp).toHaveFocus());
    await userEvent.keyboard("{ArrowDown}");
    expect(acc).toHaveFocus();
    await userEvent.keyboard("{End}");
    expect(drop).toHaveFocus();
    await userEvent.keyboard("{ArrowDown}");
    expect(exp).toHaveFocus();
    await userEvent.keyboard("{ArrowUp}");
    expect(drop).toHaveFocus();
    await userEvent.keyboard("{Home}");
    expect(exp).toHaveFocus();
  });

  it.each(["{Enter}", " "])("activates the focused item with %j", async (key) => {
    const { button, onExport } = setup();
    await userEvent.click(button);
    await waitFor(() => expect(screen.getByRole("menuitem", { name: "Export" })).toHaveFocus());
    await userEvent.keyboard(key);
    expect(onExport).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("menu")).toBeNull();
    expect(button).toHaveFocus();
  });

  it("closes with Escape and gives the focus back to the button", async () => {
    const { button } = setup();
    await userEvent.click(button);
    await waitFor(() => expect(screen.getByRole("menuitem", { name: "Export" })).toHaveFocus());
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("menu")).toBeNull();
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(button).toHaveFocus();
  });

  it("closes with a click outside, without running anything", async () => {
    const { button, onExport, onDrop } = setup();
    await userEvent.click(button);
    await userEvent.click(screen.getByText("Elsewhere"));
    expect(screen.queryByRole("menu")).toBeNull();
    expect(onExport).not.toHaveBeenCalled();
    expect(onDrop).not.toHaveBeenCalled();
  });

  it("closes with a second click on the button", async () => {
    const { button } = setup();
    await userEvent.click(button);
    await userEvent.click(button);
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("closes on Tab and lets the focus go on from the button", async () => {
    const { button } = setup();
    await userEvent.click(button);
    await userEvent.keyboard("{Tab}");
    expect(screen.queryByRole("menu")).toBeNull();
    expect(button).toHaveAttribute("aria-expanded", "false");
  });

  it("marks the destructive item and renders the menu outside of a clipping container", async () => {
    const { button } = setup();
    await userEvent.click(button);
    const drop = screen.getByRole("menuitem", { name: "Drop" });
    expect(drop).toHaveClass("menu-item-danger");
    expect(screen.getByRole("menu").parentElement).toBe(document.body);
  });
});
