import { type ReactNode, useEffect, useId, useRef } from "react";

/**
 * A modal dialog on the native <dialog>: showModal() makes the page behind
 * inert (focus stays inside, screen readers see only the dialog) and puts it
 * in the top layer. Escape asks onClose, which decides; focus goes back to
 * the element that opened the dialog. Where showModal is missing (jsdom) the
 * dialog is only opened.
 */
export function Modal({
  title,
  onClose,
  children,
  footer,
  busy = false,
  className,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  /** Something is loading inside: aria-busy on the dialog. */
  busy?: boolean;
  className?: string;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  // The latest onClose, without reopening the dialog when it changes.
  const latest = useRef(onClose);
  latest.current = onClose;
  // Escape can reach both handlers below: one close request per keystroke.
  const asked = useRef(false);
  const requestClose = () => {
    if (asked.current) {
      return;
    }
    asked.current = true;
    // Not a microtask: those run between the keydown and the cancel event.
    setTimeout(() => {
      asked.current = false;
    }, 0);
    latest.current();
  };

  useEffect(() => {
    const dialog = ref.current;
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    if (!dialog) {
      return;
    }
    if (typeof dialog.showModal === "function") {
      if (!dialog.open) {
        dialog.showModal();
      }
    } else {
      dialog.setAttribute("open", "");
    }
    // showModal focuses the first control; without one (or without showModal), the dialog.
    if (!dialog.contains(document.activeElement)) {
      dialog.focus();
    }
    return () => {
      if (typeof dialog.close === "function" && dialog.open) {
        dialog.close();
      }
      opener?.focus();
    };
  }, []);

  return (
    <dialog
      ref={ref}
      className={className ? `modal ${className}` : "modal"}
      aria-labelledby={titleId}
      aria-busy={busy}
      tabIndex={-1}
      // Escape: the browser fires cancel (and closes unless prevented); jsdom only keydown.
      onCancel={(e) => {
        e.preventDefault();
        requestClose();
      }}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.preventDefault();
          e.stopPropagation();
          requestClose();
        } else if (e.key === "Tab" && ref.current) {
          // The page behind is inert, but Tab would still leave for the browser: wrap.
          const all = focusables(ref.current);
          const first = all[0];
          const last = all[all.length - 1];
          if (!first || !last) {
            e.preventDefault();
          } else if (
            e.shiftKey &&
            (document.activeElement === first || document.activeElement === ref.current)
          ) {
            e.preventDefault();
            last.focus();
          } else if (!e.shiftKey && document.activeElement === last) {
            e.preventDefault();
            first.focus();
          }
        }
      }}
    >
      <h2 id={titleId} className="modal-title">
        {title}
      </h2>
      <div className="modal-body">{children}</div>
      {footer && <div className="modal-foot">{footer}</div>}
    </dialog>
  );
}

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), summary, [tabindex]:not([tabindex="-1"])';

/** The controls of the dialog that Tab reaches, in order. */
function focusables(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>(FOCUSABLE)].filter(
    (el) => el.getClientRects().length > 0 || el === document.activeElement,
  );
}
