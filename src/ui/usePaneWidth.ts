import { useCallback, useState } from "react";

export interface PaneWidthRange {
  min: number;
  max: number;
  default: number;
}

const PANE_WIDTH_PREFIX = "nexterm.pane-width.v1.";

function clampWidth(width: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, Math.round(width)));
}

function loadPaneWidth(key: string, range: PaneWidthRange): number {
  try {
    const raw = localStorage.getItem(PANE_WIDTH_PREFIX + key);
    if (raw === null) return range.default;
    const value = Number(raw);
    return Number.isFinite(value) ? clampWidth(value, range.min, range.max) : range.default;
  } catch {
    return range.default;
  }
}

// usePaneWidth is the width of a resizable list column, remembered on this device per key like the dock widths.
export function usePaneWidth(key: string, range: PaneWidthRange): [number, (width: number) => void] {
  const [width, setWidth] = useState(() => loadPaneWidth(key, range));
  const update = useCallback(
    (next: number) => {
      const clamped = clampWidth(next, range.min, range.max);
      setWidth(clamped);
      try {
        localStorage.setItem(PANE_WIDTH_PREFIX + key, String(clamped));
      } catch {
        // Storage can be unavailable (private mode); the width then lasts for this view only.
      }
    },
    [key, range.min, range.max],
  );
  return [width, update];
}
