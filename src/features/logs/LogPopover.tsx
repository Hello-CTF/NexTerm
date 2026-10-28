import { useEffect, useRef, type ReactNode } from "react";

// Anchored panel for the log toolbar: it floats over the log area instead of taking a row, closes on an outside
// click or Escape, and renders nothing while closed.
export function LogPopover({
  open,
  onOpenChange,
  trigger,
  children,
  label,
  widthClass = "w-[min(34rem,calc(100vw-1.5rem))]",
  align = "left",
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  trigger: ReactNode;
  children: ReactNode;
  label: string;
  widthClass?: string;
  align?: "left" | "right";
}) {
  const rootRef = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) onOpenChange(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") onOpenChange(false);
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open, onOpenChange]);
  return (
    <div ref={rootRef} className="relative">
      {trigger}
      {open && (
        <div
          role="dialog"
          aria-label={label}
          className={`absolute top-full z-30 mt-1 max-h-[60vh] overflow-y-auto rounded-md border border-neutral-700/80 bg-neutral-900 p-3 shadow-xl ${widthClass} ${align === "right" ? "right-0" : "left-0"}`}
        >
          {children}
        </div>
      )}
    </div>
  );
}
