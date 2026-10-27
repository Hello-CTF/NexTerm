import type { Terminal } from "@xterm/xterm";
import { measureTerminalGeometry } from "./terminalGeometry";
import {
  FOLLOWER_FONT_STEP_PX,
  FOLLOWER_MIN_FONT_PX,
  followerFontSize,
  followerGridFits,
  type FollowerScale,
  type FollowerZoom,
} from "./followerScale";

// The follower display state of one terminal view, kept in a ref and read when the view measures.
export interface FollowerView {
  // The user's terminal font size.
  base: number;
  // Another device owns the PTY size: the view renders the PTY grid and does not size it.
  following: boolean;
  zoom: FollowerZoom;
  // The last reported scale; null while the view sizes the PTY.
  scale: FollowerScale | null;
  report: (scale: FollowerScale | null) => void;
}

function report(view: FollowerView, scale: FollowerScale | null): void {
  const last = view.scale;
  if (last === scale || (last && scale && last.ratio === scale.ratio && last.zoom === scale.zoom && last.panX === scale.panX)) {
    return;
  }
  view.scale = scale;
  view.report(scale);
}

// applyFollowerFont shows the PTY grid another device sized without resizing it: the font shrinks until the grid
// fits the pane (its rows only when zoomed), never past the user's size. While the view sizes the PTY, the font is
// the user's and the pane does not pan.
export function applyFollowerFont(term: Terminal | null, host: HTMLElement | null, view: FollowerView): void {
  if (!host || !term) return;
  const base = view.base;
  if (!view.following) {
    if (view.scale === null) return;
    if (term.options.fontSize !== base) term.options.fontSize = base;
    host.style.overflowX = "";
    host.scrollLeft = 0;
    report(view, null);
    return;
  }
  const zoom = view.zoom;
  const geometry = measureTerminalGeometry(term, host);
  if (!geometry) return;
  const current = term.options.fontSize || base;
  const grid = { cols: term.cols, rows: term.rows };
  const cellAtBase = {
    widthPx: (geometry.metrics.widthPx * base) / current,
    heightPx: (geometry.metrics.heightPx * base) / current,
  };
  let size = followerFontSize(base, grid, geometry.viewport, cellAtBase, zoom);
  if (size !== current) term.options.fontSize = size;
  // Cell sizes snap to device pixels, so the estimate can miss by a step.
  let measured = measureTerminalGeometry(term, host);
  for (let step = 0; step < 4 && measured && size > FOLLOWER_MIN_FONT_PX; step++) {
    if (followerGridFits(grid, measured.viewport, measured.metrics, zoom).fits) break;
    size = Math.max(FOLLOWER_MIN_FONT_PX, size - FOLLOWER_FONT_STEP_PX);
    term.options.fontSize = size;
    measured = measureTerminalGeometry(term, host);
  }
  const panX = measured ? !followerGridFits(grid, measured.viewport, measured.metrics, zoom).widthFits : false;
  // overflow-x hidden (not clip) lets the gestures pan the columns; the host clips again once they fit.
  host.style.overflowX = panX ? "hidden" : "";
  if (!panX) host.scrollLeft = 0;
  report(view, { ratio: Math.round((size / base) * 100) / 100, zoom, panX });
}
