import type { SplitDir, SplitSide } from "./splitTree";

/** Where a tab dragged over a pane lands: one of its edges (split) or its centre (move). */
export type PaneEdge = "left" | "right" | "top" | "bottom";
export type DropRegion = PaneEdge | "center";

export interface DropRect {
  left: number;
  top: number;
  width: number;
  height: number;
}

// Each edge band covers a quarter of the pane, but never less than EDGE_MIN_PX so narrow panes keep a
// reachable band, and never more than a third so the centre stays reachable.
export const DROP_EDGE_FRACTION = 0.25;
export const DROP_EDGE_MIN_PX = 24;
const DROP_EDGE_MAX_FRACTION = 1 / 3;

function band(size: number): number {
  return Math.min(Math.max(size * DROP_EDGE_FRACTION, DROP_EDGE_MIN_PX), size * DROP_EDGE_MAX_FRACTION);
}

/**
 * The drop region under `point` in a pane occupying `rect`. Returns null outside the pane. When
 * `edges` is false (single-pane layouts, or a pane that cannot give up its only tab) the whole pane
 * is the centre. In a corner the edge the point is relatively closest to wins.
 */
export function dropRegionAt(
  point: { x: number; y: number },
  rect: DropRect,
  options: { edges: boolean },
): DropRegion | null {
  if (!(rect.width > 0) || !(rect.height > 0)) return null;
  const dx = point.x - rect.left;
  const dy = point.y - rect.top;
  if (dx < 0 || dy < 0 || dx > rect.width || dy > rect.height) return null;
  if (!options.edges) return "center";
  const horizontal = band(rect.width);
  const vertical = band(rect.height);
  const candidates: { edge: PaneEdge; depth: number }[] = [
    { edge: "left", depth: dx / horizontal },
    { edge: "right", depth: (rect.width - dx) / horizontal },
    { edge: "top", depth: dy / vertical },
    { edge: "bottom", depth: (rect.height - dy) / vertical },
  ];
  let best: { edge: PaneEdge; depth: number } | null = null;
  for (const candidate of candidates) {
    if (candidate.depth < 1 && (!best || candidate.depth < best.depth)) best = candidate;
  }
  return best ? best.edge : "center";
}

/** The split that places a new pane on `edge` of the target pane. */
export function edgeSplit(edge: PaneEdge): { dir: SplitDir; side: SplitSide; direction: "right" | "down" } {
  switch (edge) {
    case "left":
      return { dir: "row", side: "before", direction: "right" };
    case "right":
      return { dir: "row", side: "after", direction: "right" };
    case "top":
      return { dir: "col", side: "before", direction: "down" };
    case "bottom":
      return { dir: "col", side: "after", direction: "down" };
  }
}

/** The share of the pane a drop preview highlights, in fractions of the pane: the half the new pane takes. */
export function dropPreviewRect(region: DropRegion): { x: number; y: number; width: number; height: number } {
  switch (region) {
    case "left":
      return { x: 0, y: 0, width: 0.5, height: 1 };
    case "right":
      return { x: 0.5, y: 0, width: 0.5, height: 1 };
    case "top":
      return { x: 0, y: 0, width: 1, height: 0.5 };
    case "bottom":
      return { x: 0, y: 0.5, width: 1, height: 0.5 };
    case "center":
      return { x: 0, y: 0, width: 1, height: 1 };
  }
}

export const EDGE_LABEL: Record<PaneEdge, string> = { left: "向左", right: "向右", top: "向上", bottom: "向下" };
