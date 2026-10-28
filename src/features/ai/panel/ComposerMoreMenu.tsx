import { useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { IconMore } from "../../../ui/icons";

export interface ComposerMoreItem {
  key: string;
  label: string;
  /** Short badge shown after the label, e.g. 实验性. */
  badge?: string;
  title: string;
  icon: ReactNode;
  disabled?: boolean;
  /** Shown instead of the badge while disabled so the reason is visible. */
  disabledReason?: string;
  run: () => void;
}

/**
 * Overflow menu for rarely used composer actions. Renders nothing when no
 * action applies, so an unusable control is never shown.
 */
export function ComposerMoreMenu({ items }: { items: ComposerMoreItem[] }) {
  const [open, setOpen] = useState(false);
  const boxRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (boxRef.current && !boxRef.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const first = boxRef.current?.querySelector<HTMLButtonElement>('[role="menuitem"]:not(:disabled)');
    (first ?? boxRef.current?.querySelector<HTMLElement>('[role="menu"]'))?.focus();
  }, [open]);

  if (items.length === 0) return null;

  const close = (restoreFocus: boolean) => {
    setOpen(false);
    if (restoreFocus) triggerRef.current?.focus();
  };

  const onMenuKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      close(true);
      return;
    }
    if (e.key === "Tab") {
      setOpen(false);
      return;
    }
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp" && e.key !== "Home" && e.key !== "End") return;
    e.preventDefault();
    const enabled = [...e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)')];
    if (enabled.length === 0) return;
    const at = enabled.indexOf(document.activeElement as HTMLButtonElement);
    let next: number;
    if (e.key === "Home") next = 0;
    else if (e.key === "End") next = enabled.length - 1;
    else next = (at + (e.key === "ArrowDown" ? 1 : -1) + enabled.length) % enabled.length;
    enabled[next].focus();
  };

  return (
    <div className="relative shrink-0" ref={boxRef}>
      <button
        ref={triggerRef}
        type="button"
        className={`nx-cbar-icon ${open ? "is-open" : ""}`}
        title="更多"
        aria-label="更多"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <IconMore size={14} />
      </button>
      {open && (
        <div
          role="menu"
          aria-label="更多操作"
          tabIndex={-1}
          className="absolute bottom-full right-0 z-[80] mb-1 w-60 max-w-[calc(100vw-1.5rem)] rounded-md border border-neutral-700 bg-neutral-900 p-1 shadow-lg outline-none"
          onKeyDown={onMenuKeyDown}
        >
          {items.map((item) => (
            <button
              key={item.key}
              type="button"
              role="menuitem"
              className="nx-menu-item w-full"
              title={item.disabled && item.disabledReason ? item.disabledReason : item.title}
              disabled={item.disabled}
              onClick={() => {
                close(true);
                item.run();
              }}
            >
              <span className="nx-menu-icon">{item.icon}</span>
              <span className="nx-menu-label">{item.label}</span>
              {item.disabled && item.disabledReason ? (
                <span className="nx-menu-hint truncate">{item.disabledReason}</span>
              ) : item.badge ? (
                <span className="nx-menu-hint">{item.badge}</span>
              ) : null}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
