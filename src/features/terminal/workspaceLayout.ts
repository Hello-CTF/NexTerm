export const WORKSPACE_OVERLAY_MAX = 820;
export const WORKSPACE_COMPACT_MAX = 560;

export interface WorkspaceViewport {
  width: number;
  overlaySidebars: boolean;
  compact: boolean;
  coarsePointer: boolean;
  terminalKeys: boolean;
}

export function workspaceViewport(width: number, coarsePointer: boolean): WorkspaceViewport {
  const safeWidth = Number.isFinite(width) ? Math.max(0, width) : 0;
  return {
    width: safeWidth,
    overlaySidebars: safeWidth <= WORKSPACE_OVERLAY_MAX,
    compact: safeWidth <= WORKSPACE_COMPACT_MAX,
    coarsePointer,
    terminalKeys: coarsePointer || safeWidth <= WORKSPACE_COMPACT_MAX,
  };
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

export function splitRatioForKey(
  current: number,
  key: string,
  shiftKey = false,
): number | null {
  const ratio = clampSplitRatio(current);
  const step = shiftKey ? 0.1 : 0.02;
  switch (key) {
    case "ArrowUp":
      return clampSplitRatio(ratio - step);
    case "ArrowDown":
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
