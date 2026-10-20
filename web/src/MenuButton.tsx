import { MoreHorizontal } from "lucide-react";
import {
  type KeyboardEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";

export type MenuItem = {
  key: string;
  /** Visible text. */
  label: ReactNode;
  /** Accessible name when it must say more than the visible text. */
  ariaLabel?: string;
  onSelect: () => void;
  /** Destructive: red text, and a separator above it. */
  danger?: boolean;
};

const GAP = 4;
const MARGIN = 8;

/**
 * A menu button (WAI-ARIA menu button pattern): a button that opens a list of actions.
 * Arrow keys, Home and End move, Enter or Space activates, Escape closes and gives the focus
 * back to the button, a click outside closes. The menu is rendered in a portal with fixed
 * positioning, so a scrolling container (a table) never clips it.
 */
export function MenuButton({
  label,
  items,
  icon,
  className = "btn btn-ghost btn-sm btn-icon",
}: {
  /** Accessible name of the button ("Actions for sales"). */
  label: string;
  items: MenuItem[];
  icon?: ReactNode;
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null);
  const button = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const id = useId();

  const close = useCallback((restoreFocus: boolean) => {
    setOpen(false);
    setPos(null);
    if (restoreFocus) {
      button.current?.focus();
    }
  }, []);

  // Place the menu under the button, right-aligned, kept inside the viewport.
  useLayoutEffect(() => {
    if (!open || !button.current || !menu.current) {
      return;
    }
    const b = button.current.getBoundingClientRect();
    const m = menu.current.getBoundingClientRect();
    let left = b.right - m.width;
    left = Math.max(MARGIN, Math.min(left, window.innerWidth - m.width - MARGIN));
    let top = b.bottom + GAP;
    if (top + m.height > window.innerHeight - MARGIN) {
      top = Math.max(MARGIN, b.top - GAP - m.height);
    }
    setPos({ top, left });
  }, [open]);

  // Focus the first item once the menu is placed.
  useEffect(() => {
    if (pos) {
      menu.current?.querySelector<HTMLElement>('[role="menuitem"]')?.focus();
    }
  }, [pos]);

  useEffect(() => {
    if (!open) {
      return;
    }
    const outside = (e: PointerEvent) => {
      const target = e.target as Node;
      if (!menu.current?.contains(target) && !button.current?.contains(target)) {
        close(false);
      }
    };
    const away = () => close(false);
    document.addEventListener("pointerdown", outside);
    window.addEventListener("resize", away);
    window.addEventListener("scroll", away, true);
    return () => {
      document.removeEventListener("pointerdown", outside);
      window.removeEventListener("resize", away);
      window.removeEventListener("scroll", away, true);
    };
  }, [open, close]);

  const entries = () =>
    Array.from(menu.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? []);

  const onMenuKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const all = entries();
    const at = all.indexOf(document.activeElement as HTMLElement);
    let next = -1;
    switch (e.key) {
      case "ArrowDown":
        next = (at + 1) % all.length;
        break;
      case "ArrowUp":
        next = (at - 1 + all.length) % all.length;
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = all.length - 1;
        break;
      case "Escape":
        e.preventDefault();
        e.stopPropagation();
        close(true);
        return;
      case "Tab":
        // Leaving the menu closes it; the focus follows the page order from the button.
        close(true);
        return;
      default:
        return;
    }
    e.preventDefault();
    all[next]?.focus();
  };

  const onButtonKey = (e: KeyboardEvent<HTMLButtonElement>) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      setOpen(true);
    }
  };

  const menuId = `${id}-menu`;
  return (
    <>
      <button
        ref={button}
        type="button"
        className={className}
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={() => (open ? close(false) : setOpen(true))}
        onKeyDown={onButtonKey}
      >
        {icon ?? <MoreHorizontal size={16} aria-hidden="true" />}
      </button>
      {open &&
        createPortal(
          <div
            ref={menu}
            id={menuId}
            role="menu"
            aria-label={label}
            className="menu"
            style={{
              top: pos?.top ?? 0,
              left: pos?.left ?? 0,
              visibility: pos ? "visible" : "hidden",
            }}
            onKeyDown={onMenuKey}
          >
            {items.map((item) => (
              <div
                key={item.key}
                role="none"
                className={item.danger ? "menu-danger-row" : undefined}
              >
                <button
                  type="button"
                  role="menuitem"
                  tabIndex={-1}
                  aria-label={item.ariaLabel}
                  className={item.danger ? "menu-item menu-item-danger" : "menu-item"}
                  onClick={() => {
                    // The button gets the focus back first: a dialog opened by the item returns to it.
                    close(true);
                    item.onSelect();
                  }}
                >
                  {item.label}
                </button>
              </div>
            ))}
          </div>,
          document.body,
        )}
    </>
  );
}
