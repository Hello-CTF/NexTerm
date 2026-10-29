import { useEffect, useRef, type ReactNode } from "react";

// Anchored panel for the log toolbar: it floats over the log area instead of taking a row, closes on an outside
// click or Escape, and renders nothing while closed. On phone widths it becomes a full-width sheet pinned above the
// soft keyboard and the home indicator, so it can never overflow the screen or sit under the keyboard.
export const SHEET_CLASS = [
  "absolute top-full z-30 mt-1 max-h-[60vh] overflow-y-auto overscroll-contain rounded-md border border-neutral-700/80 bg-neutral-900 p-3 shadow-xl",
  "max-[560px]:fixed max-[560px]:inset-x-0 max-[560px]:top-auto max-[560px]:bottom-[var(--nx-kb-inset,0px)] max-[560px]:mt-0",
  "max-[560px]:max-h-[min(70dvh,calc(100dvh-var(--nx-kb-inset,0px)-var(--nx-safe-top,0px)-3rem))] max-[560px]:w-auto",
  "max-[560px]:rounded-b-none max-[560px]:pb-[calc(0.75rem+var(--nx-safe-bottom,0px))] max-[560px]:shadow-[0_-8px_24px_rgb(0_0_0/0.45)]",
].join(" ");

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
          className={`${SHEET_CLASS} ${widthClass} ${align === "right" ? "right-0" : "left-0"}`}
        >
          {children}
        </div>
      )}
    </div>
  );
}
