import type { TerminalCellMetrics, TerminalGrid, TerminalViewport } from "./terminalGrid";

// A terminal whose PTY size another device owns keeps the PTY's columns and rows (a local resize would reflow the
// screen differently from the device that sized it) and shrinks its font until the grid fits the pane. A smaller
// font keeps the text sharp and the mouse and selection mapping exact, which a CSS transform would not: xterm.js maps
// pointer positions with untransformed cell sizes.

// The font never grows past the user's size; this is the smallest it shrinks to.
export const FOLLOWER_MIN_FONT_PX = 4;
export const FOLLOWER_FONT_STEP_PX = 0.5;
// Below this share of the user's font size the text is hard to read; the pane says how to enlarge it.
export const FOLLOWER_READABLE_RATIO = 0.6;
// A two-finger gesture whose finger distance changes by this factor is a pinch.
export const FOLLOWER_PINCH_FACTOR = 1.2;

// fit shows the whole grid; height fits the rows only, so the text is larger and the columns pan sideways.
export type FollowerZoom = "fit" | "height";

export interface FollowerScale {
  // The shown font size relative to the user's font size, at most 1.
  ratio: number;
  zoom: FollowerZoom;
  // The columns are wider than the pane and pan sideways.
  panX: boolean;
}

function axisRatio(pixels: number, cells: number, cell: number): number {
  const ratio = pixels / (cells * cell);
  return Number.isFinite(ratio) && ratio > 0 ? ratio : 1;
}

// followerFontSize is the largest font, in steps of FOLLOWER_FONT_STEP_PX and no larger than baseFont, at which the
// grid fits the viewport (only its height for zoom "height"). cellAtBase is the cell size at baseFont.
export function followerFontSize(
  baseFont: number,
  grid: TerminalGrid,
  viewport: TerminalViewport,
  cellAtBase: TerminalCellMetrics,
  zoom: FollowerZoom,
): number {
  if (!(baseFont > 0) || grid.cols <= 0 || grid.rows <= 0 || cellAtBase.widthPx <= 0 || cellAtBase.heightPx <= 0) {
    return baseFont;
  }
  const byHeight = axisRatio(viewport.heightPx, grid.rows, cellAtBase.heightPx);
  const byWidth = axisRatio(viewport.widthPx, grid.cols, cellAtBase.widthPx);
  const ratio = Math.min(1, zoom === "fit" ? Math.min(byWidth, byHeight) : byHeight);
  if (ratio >= 1) return baseFont;
  const stepped = Math.floor((baseFont * ratio) / FOLLOWER_FONT_STEP_PX) * FOLLOWER_FONT_STEP_PX;
  return Math.min(baseFont, Math.max(Math.min(FOLLOWER_MIN_FONT_PX, baseFont), stepped));
}

// followerGridFits reports whether the grid at the measured cell size fits the viewport for the zoom.
export function followerGridFits(
  grid: TerminalGrid,
  viewport: TerminalViewport,
  cell: TerminalCellMetrics,
  zoom: FollowerZoom,
): { fits: boolean; widthFits: boolean } {
  const widthFits = grid.cols * cell.widthPx <= viewport.widthPx + 0.5;
  const heightFits = grid.rows * cell.heightPx <= viewport.heightPx + 0.5;
  return { fits: heightFits && (zoom === "height" || widthFits), widthFits };
}

export function followerReadable(scale: FollowerScale): boolean {
  return scale.ratio >= FOLLOWER_READABLE_RATIO;
}

// pinchDirection classifies a finished two-finger gesture by how far apart the fingers moved: out enlarges.
export function pinchDirection(startDistance: number, endDistance: number): "in" | "out" | null {
  if (!(startDistance > 0) || !(endDistance > 0)) return null;
  const factor = endDistance / startDistance;
  if (factor >= FOLLOWER_PINCH_FACTOR) return "out";
  if (factor <= 1 / FOLLOWER_PINCH_FACTOR) return "in";
  return null;
}
