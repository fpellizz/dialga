import { act, fireEvent, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { Modal } from "./Modal";
import { renderI18n } from "./test/fixtures";

function Opener({ onClose }: { onClose: () => void }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Open
      </button>
      {open && (
        <Modal
          title="Drop sales"
          onClose={() => {
            onClose();
            setOpen(false);
          }}
          footer={<button type="button">Cancel</button>}
        >
          <p>Body</p>
        </Modal>
      )}
    </>
  );
}

describe("Modal", () => {
  it("is a labelled dialog that gives the focus back on close", async () => {
    const onClose = vi.fn();
    renderI18n(<Opener onClose={onClose} />);
    const opener = screen.getByRole("button", { name: "Open" });
    await userEvent.click(opener);
    const dialog = screen.getByRole("dialog", { name: "Drop sales" });
    expect(dialog).toHaveFocus();

    await userEvent.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(opener).toHaveFocus();
  });

  it("asks to close once when the browser sends both keydown and cancel", () => {
    vi.useFakeTimers();
    const onClose = vi.fn();
    renderI18n(
      <Modal title="Report" onClose={onClose}>
        <p>Body</p>
      </Modal>,
    );
    const dialog = screen.getByRole("dialog", { name: "Report" });
    fireEvent.keyDown(dialog, { key: "Escape" });
    const cancel = new Event("cancel", { cancelable: true });
    fireEvent(dialog, cancel);
    expect(onClose).toHaveBeenCalledTimes(1);
    // The browser would close the dialog itself: the page decides instead.
    expect(cancel.defaultPrevented).toBe(true);
    act(() => vi.advanceTimersByTime(1));
    fireEvent(dialog, new Event("cancel", { cancelable: true }));
    expect(onClose).toHaveBeenCalledTimes(2);
  });
});
