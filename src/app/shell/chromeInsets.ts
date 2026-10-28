import { useEffect } from "react";

export function aiToastPlacement(
  width: number,
  overlaySidebars: boolean,
  rightDockOpen: boolean,
  rightWidth: number,
): "dock" | "left" | "overlay" | null {
  if (!rightDockOpen) return null;
  if (overlaySidebars) return "overlay";
  return width - rightWidth - 32 >= 200 ? "dock" : "left";
}

// The key toolbar can wrap into several rows on phones; toasts and overlays need its real height.
export function useKeysToolbarHeight(active: boolean, remountKey: string | undefined): void {
  useEffect(() => {
    const root = document.documentElement;
    if (!active || typeof ResizeObserver === "undefined") {
      root.style.removeProperty("--nx-keys-h");
      return;
    }
    const keys = document.querySelector<HTMLElement>(".nx-terminal-keys");
    if (!keys) return;
    const measure = () => {
      const height = Math.round(keys.getBoundingClientRect().height);
      if (height > 0) root.style.setProperty("--nx-keys-h", `${height}px`);
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(keys);
    return () => {
      observer.disconnect();
      root.style.removeProperty("--nx-keys-h");
    };
  }, [active, remountKey]);
}

export function useAiToastInset(active: boolean): void {
  useEffect(() => {
    const root = document.documentElement;
    if (!active) {
      root.style.removeProperty("--nx-ai-toast-max");
      return;
    }
    const measure = () => {
      const input = document.querySelector(".nx-right-dock textarea");
      if (!input) return;
      const top = input.getBoundingClientRect().top;
      if (top <= 0) return;
      const stack = parseFloat(getComputedStyle(root).getPropertyValue("--nx-header-stack")) || 36;
      root.style.setProperty("--nx-ai-toast-max", `${Math.max(140, Math.round(top - 12 - stack - 8))}px`);
    };
    measure();
    const observer = typeof ResizeObserver !== "undefined" ? new ResizeObserver(measure) : null;
    const dock = document.querySelector(".nx-right-dock");
    const input = document.querySelector(".nx-right-dock textarea");
    if (dock) observer?.observe(dock);
    if (input) observer?.observe(input);
    window.addEventListener("resize", measure);
    window.visualViewport?.addEventListener("resize", measure);
    return () => {
      observer?.disconnect();
      window.removeEventListener("resize", measure);
      window.visualViewport?.removeEventListener("resize", measure);
      root.style.removeProperty("--nx-ai-toast-max");
    };
  }, [active]);
}
