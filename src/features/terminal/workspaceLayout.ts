export const WORKSPACE_OVERLAY_MAX = 820;
export const WORKSPACE_COMPACT_MAX = 560;
export const WORKSPACE_SPLIT_MIN_HEIGHT = 480;

export interface WorkspaceViewport {
  width: number;
  height: number;
  overlaySidebars: boolean;
  compact: boolean;
  coarsePointer: boolean;
  terminalKeys: boolean;
  splitAllowed: boolean;
}

export function splitAllowedForHeight(height: number): boolean {
  if (!Number.isFinite(height)) return true;
  return height >= WORKSPACE_SPLIT_MIN_HEIGHT;
}

export function workspaceViewport(
  width: number,
  coarsePointer: boolean,
  height: number = Number.POSITIVE_INFINITY,
): WorkspaceViewport {
  const safeWidth = Number.isFinite(width) ? Math.max(0, width) : 0;
  return {
    width: safeWidth,
    height,
    overlaySidebars: safeWidth <= WORKSPACE_OVERLAY_MAX,
    compact: safeWidth <= WORKSPACE_COMPACT_MAX,
    coarsePointer,
    terminalKeys: coarsePointer || safeWidth <= WORKSPACE_COMPACT_MAX,
    splitAllowed: splitAllowedForHeight(height),
  };
}

// A split must leave both halves a usable terminal: at least SPLIT_MIN_ROWS x SPLIT_MIN_COLS cells at
// a typical cell size, below a tab strip and beside a divider.
export const SPLIT_MIN_ROWS = 8;
export const SPLIT_MIN_COLS = 20;
const SPLIT_CELL_HEIGHT_PX = 17;
const SPLIT_CELL_WIDTH_PX = 8;
const SPLIT_TAB_STRIP_PX = 35;
const SPLIT_DIVIDER_PX = 6;

/** Whether a pane of `size` pixels can be split in `direction` and keep both halves usable. */
export function splitFitsPane(direction: "right" | "down", size: { width: number; height: number }): boolean {
  if (direction === "down") {
    const half = (size.height - SPLIT_DIVIDER_PX) / 2 - SPLIT_TAB_STRIP_PX;
    return half >= SPLIT_MIN_ROWS * SPLIT_CELL_HEIGHT_PX;
  }
  return (size.width - SPLIT_DIVIDER_PX) / 2 >= SPLIT_MIN_COLS * SPLIT_CELL_WIDTH_PX;
}

export const SPLIT_RATIO_RANGE = { min: 0.15, max: 0.85, default: 0.5 } as const;

export function clampSplitRatio(value: number, fallback: number = SPLIT_RATIO_RANGE.default): number {
  if (!Number.isFinite(value)) return fallback;
  return Math.min(SPLIT_RATIO_RANGE.max, Math.max(SPLIT_RATIO_RANGE.min, value));
}

export function splitRatioAt(
  clientY: number,
  rect: { top: number; height: number },
  fallback: number,
): number {
  if (!Number.isFinite(clientY) || !Number.isFinite(rect.top) || rect.height <= 0) {
    return clampSplitRatio(fallback);
  }
  return clampSplitRatio((clientY - rect.top) / rect.height, fallback);
}

// splitRatioForKey maps a key on a split divider to the next ratio. A "col" split (top/bottom) moves
// with ArrowUp/ArrowDown, a "row" split (left/right) with ArrowLeft/ArrowRight.
export function splitRatioForKey(
  current: number,
  key: string,
  shiftKey = false,
  dir: "row" | "col" = "col",
): number | null {
  const ratio = clampSplitRatio(current);
  const step = shiftKey ? 0.1 : 0.02;
  const decrease = dir === "row" ? "ArrowLeft" : "ArrowUp";
  const increase = dir === "row" ? "ArrowRight" : "ArrowDown";
  switch (key) {
    case decrease:
      return clampSplitRatio(ratio - step);
    case increase:
      return clampSplitRatio(ratio + step);
    case "Home":
      return SPLIT_RATIO_RANGE.min;
    case "End":
      return SPLIT_RATIO_RANGE.max;
    case "Enter":
      return SPLIT_RATIO_RANGE.default;
    default:
      return null;
  }
}
