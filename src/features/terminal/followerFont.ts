import type { Terminal } from "@xterm/xterm";
import { measureTerminalGeometry } from "./terminalGeometry";
import { followerFontSize, followerGridFits, type FollowerScale, type FollowerZoom } from "./followerScale";

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
  if (
    last === scale ||
    (last && scale && last.ratio === scale.ratio && last.zoom === scale.zoom && last.panX === scale.panX && last.panY === scale.panY)
  ) {
    return;
  }
  view.scale = scale;
  view.report(scale);
}

// applyFollowerFont shows the PTY grid another device sized without resizing it: the font shrinks until the grid
// fits the pane (its rows only when zoomed), never past the user's size. The target size is computed first and the
// font set once. Columns wider than the pane pan sideways; rows taller than the pane at the smallest font pan up and
// down. While the view sizes the PTY, the font is the user's and the pane does not pan.
export function applyFollowerFont(term: Terminal | null, host: HTMLElement | null, view: FollowerView): void {
  if (!host || !term) return;
  const base = view.base;
  if (!view.following) {
    if (view.scale === null) return;
    if (term.options.fontSize !== base) term.options.fontSize = base;
    host.style.overflowX = "";
    host.style.overflowY = "";
    host.scrollLeft = 0;
    host.scrollTop = 0;
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
  const size = followerFontSize(base, grid, geometry.viewport, cellAtBase, zoom, window.devicePixelRatio || 1);
  if (size !== current) term.options.fontSize = size;
  const measured = size === current ? geometry : measureTerminalGeometry(term, host);
  const fit = measured ? followerGridFits(grid, measured.viewport, measured.metrics, zoom) : null;
  const panX = fit ? !fit.widthFits : false;
  const panY = fit ? !fit.heightFits : false;
  // overflow hidden (not clip) lets the gestures pan; the host clips again once the grid fits.
  host.style.overflowX = panX ? "hidden" : "";
  host.style.overflowY = panY ? "hidden" : "";
  if (!panX) host.scrollLeft = 0;
  if (!panY) host.scrollTop = 0;
  report(view, { ratio: Math.round((size / base) * 100) / 100, zoom, panX, panY });
}
