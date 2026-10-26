import { clampSplitRatio, SPLIT_RATIO_RANGE } from "../features/terminal/workspaceLayout";

// Nested split layout for one workspace. A "row" split places `a` left of
// `b`; a "col" split places `a` above `b`. `ratio` is the share of `a`.
// All operations are pure: they never mutate their input and return the same
// reference when the requested change does not apply.

export type SplitDir = "row" | "col";
export type SplitSide = "before" | "after";
export type FocusDirection = "left" | "right" | "up" | "down";
export type TreePath = readonly ("a" | "b")[];

export interface SplitLeaf {
  type: "leaf";
  id: string;
  paneId: string;
}

export interface SplitBranch {
  type: "split";
  id: string;
  dir: SplitDir;
  ratio: number;
  a: SplitNode;
  b: SplitNode;
}

export type SplitNode = SplitLeaf | SplitBranch;

export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface PaneRect {
  paneId: string;
  rect: Rect;
}

export type IdFactory = () => string;

export const SPLIT_TREE_MAX_DEPTH = 8;
export const SPLIT_RATIO_MIN = SPLIT_RATIO_RANGE.min;
export const SPLIT_RATIO_MAX = SPLIT_RATIO_RANGE.max;
export const SPLIT_RATIO_DEFAULT = SPLIT_RATIO_RANGE.default;

const EPSILON = 1e-9;
const UNIT_RECT: Rect = { x: 0, y: 0, width: 1, height: 1 };

export const defaultIdFactory: IdFactory = () => `node-${globalThis.crypto.randomUUID()}`;

export function createLeaf(paneId: string, newId: IdFactory = defaultIdFactory): SplitLeaf {
  return { type: "leaf", id: newId(), paneId };
}

export function clampRatio(ratio: number): number {
  return clampSplitRatio(ratio, SPLIT_RATIO_DEFAULT);
}

/** Number of split nodes on the longest root-to-leaf path; a single leaf is 0. */
export function treeDepth(node: SplitNode): number {
  return node.type === "leaf" ? 0 : 1 + Math.max(treeDepth(node.a), treeDepth(node.b));
}

/** Pane ids in visual reading order: left-to-right, top-to-bottom. */
export function paneIdsInOrder(node: SplitNode): string[] {
  const out: string[] = [];
  const walk = (current: SplitNode) => {
    if (current.type === "leaf") out.push(current.paneId);
    else {
      walk(current.a);
      walk(current.b);
    }
  };
  walk(node);
  return out;
}

export function nodeAt(node: SplitNode, path: TreePath): SplitNode | null {
  let current: SplitNode = node;
  for (const step of path) {
    if (current.type !== "split") return null;
    current = current[step];
  }
  return current;
}

export function findPanePath(node: SplitNode, paneId: string): TreePath | null {
  if (node.type === "leaf") return node.paneId === paneId ? [] : null;
  const inA = findPanePath(node.a, paneId);
  if (inA) return ["a", ...inA];
  const inB = findPanePath(node.b, paneId);
  return inB ? ["b", ...inB] : null;
}

export function findLeaf(node: SplitNode, paneId: string): { leaf: SplitLeaf; path: TreePath } | null {
  const path = findPanePath(node, paneId);
  if (!path) return null;
  return { leaf: nodeAt(node, path) as SplitLeaf, path };
}

export function findNodePath(node: SplitNode, id: string): TreePath | null {
  if (node.id === id) return [];
  if (node.type === "leaf") return null;
  const inA = findNodePath(node.a, id);
  if (inA) return ["a", ...inA];
  const inB = findNodePath(node.b, id);
  return inB ? ["b", ...inB] : null;
}

function replaceAt(node: SplitNode, path: TreePath, replacement: SplitNode): SplitNode {
  if (path.length === 0) return replacement;
  if (node.type !== "split") return node;
  const [step, ...rest] = path;
  return { ...node, [step]: replaceAt(node[step], rest, replacement) };
}

export interface SplitOptions {
  dir: SplitDir;
  /** Where the new pane goes relative to the split pane: before = left/top, after = right/bottom. */
  side: SplitSide;
  newPaneId: string;
  ratio?: number;
}

/**
 * Splits the leaf holding `paneId`. No-op when the pane is missing, the new
 * pane id already exists, or the result would exceed SPLIT_TREE_MAX_DEPTH.
 */
export function splitPane(
  tree: SplitNode,
  paneId: string,
  options: SplitOptions,
  newId: IdFactory = defaultIdFactory,
): SplitNode {
  return insertLeaf(tree, paneId, options, null, newId);
}

function insertLeaf(
  tree: SplitNode,
  targetPaneId: string,
  options: SplitOptions,
  existingLeaf: SplitLeaf | null,
  newId: IdFactory,
): SplitNode {
  const path = findPanePath(tree, targetPaneId);
  if (!path || !options.newPaneId || findPanePath(tree, options.newPaneId)) return tree;
  if (path.length + 1 > SPLIT_TREE_MAX_DEPTH) return tree;
  const target = nodeAt(tree, path) as SplitLeaf;
  const added: SplitLeaf = existingLeaf
    ? { ...existingLeaf, paneId: options.newPaneId }
    : { type: "leaf", id: newId(), paneId: options.newPaneId };
  const branch: SplitBranch = {
    type: "split",
    id: newId(),
    dir: options.dir,
    ratio: clampRatio(options.ratio ?? SPLIT_RATIO_DEFAULT),
    a: options.side === "before" ? added : target,
    b: options.side === "before" ? target : added,
  };
  return replaceAt(tree, path, branch);
}

/**
 * Removes the leaf holding `paneId`; its sibling takes the parent's place.
 * Returns null when the last pane is closed and the tree unchanged when the
 * pane is missing.
 */
export function closePane(tree: SplitNode, paneId: string): SplitNode | null {
  const path = findPanePath(tree, paneId);
  if (!path) return tree;
  if (path.length === 0) return null;
  const parentPath = path.slice(0, -1);
  const parent = nodeAt(tree, parentPath) as SplitBranch;
  const sibling = path[path.length - 1] === "a" ? parent.b : parent.a;
  return replaceAt(tree, parentPath, sibling);
}

/** Sets the ratio of split `splitId`, clamped to [SPLIT_RATIO_MIN, SPLIT_RATIO_MAX]. */
export function setSplitRatio(tree: SplitNode, splitId: string, ratio: number): SplitNode {
  if (!Number.isFinite(ratio)) return tree;
  const path = findNodePath(tree, splitId);
  if (!path) return tree;
  const node = nodeAt(tree, path) as SplitNode;
  if (node.type !== "split") return tree;
  const next = clampRatio(ratio);
  if (next === node.ratio) return tree;
  return replaceAt(tree, path, { ...node, ratio: next });
}

/** Exchanges the panes shown in two leaves; leaf ids and the shape stay put. */
export function swapPanes(tree: SplitNode, first: string, second: string): SplitNode {
  if (first === second) return tree;
  const firstHit = findLeaf(tree, first);
  const secondHit = findLeaf(tree, second);
  if (!firstHit || !secondHit) return tree;
  const swapped = replaceAt(tree, firstHit.path, { ...firstHit.leaf, paneId: second });
  return replaceAt(swapped, secondHit.path, { ...secondHit.leaf, paneId: first });
}

/**
 * Moves `paneId` next to `targetPaneId`, collapsing its old position. The
 * moved leaf keeps its id. No-op for missing or identical panes or when the
 * move would exceed the depth limit.
 */
export function movePane(
  tree: SplitNode,
  paneId: string,
  targetPaneId: string,
  placement: { dir: SplitDir; side: SplitSide; ratio?: number },
  newId: IdFactory = defaultIdFactory,
): SplitNode {
  if (paneId === targetPaneId) return tree;
  const moving = findLeaf(tree, paneId);
  if (!moving || !findPanePath(tree, targetPaneId)) return tree;
  const without = closePane(tree, paneId) as SplitNode;
  const moved = insertLeaf(without, targetPaneId, { ...placement, newPaneId: paneId }, moving.leaf, newId);
  return moved === without ? tree : moved;
}

/** Normalized rectangles for every leaf, in visual order. */
export function paneRects(tree: SplitNode, bounds: Rect = UNIT_RECT): PaneRect[] {
  const out: PaneRect[] = [];
  const walk = (node: SplitNode, rect: Rect) => {
    if (node.type === "leaf") {
      out.push({ paneId: node.paneId, rect });
      return;
    }
    if (node.dir === "row") {
      const width = rect.width * node.ratio;
      walk(node.a, { ...rect, width });
      walk(node.b, { ...rect, x: rect.x + width, width: rect.width - width });
    } else {
      const height = rect.height * node.ratio;
      walk(node.a, { ...rect, height });
      walk(node.b, { ...rect, y: rect.y + height, height: rect.height - height });
    }
  };
  walk(tree, bounds);
  return out;
}

function overlap(start1: number, end1: number, start2: number, end2: number): number {
  return Math.min(end1, end2) - Math.max(start1, start2);
}

/**
 * The pane geometrically adjacent to `paneId` in `direction`, or null at the
 * edge. Candidates must touch the current pane's edge and overlap it on the
 * perpendicular axis; the largest overlap wins, then the nearest centre, then
 * visual order.
 */
export function focusNeighbor(tree: SplitNode, paneId: string, direction: FocusDirection): string | null {
  const rects = paneRects(tree);
  const current = rects.find((entry) => entry.paneId === paneId)?.rect;
  if (!current) return null;
  const horizontal = direction === "left" || direction === "right";
  let best: { paneId: string; overlap: number; offset: number } | null = null;
  for (const entry of rects) {
    if (entry.paneId === paneId) continue;
    const rect = entry.rect;
    const touches =
      direction === "right"
        ? Math.abs(rect.x - (current.x + current.width)) < EPSILON
        : direction === "left"
          ? Math.abs(rect.x + rect.width - current.x) < EPSILON
          : direction === "down"
            ? Math.abs(rect.y - (current.y + current.height)) < EPSILON
            : Math.abs(rect.y + rect.height - current.y) < EPSILON;
    if (!touches) continue;
    const shared = horizontal
      ? overlap(rect.y, rect.y + rect.height, current.y, current.y + current.height)
      : overlap(rect.x, rect.x + rect.width, current.x, current.x + current.width);
    if (shared <= EPSILON) continue;
    const offset = horizontal
      ? Math.abs(rect.y + rect.height / 2 - (current.y + current.height / 2))
      : Math.abs(rect.x + rect.width / 2 - (current.x + current.width / 2));
    if (
      !best ||
      shared > best.overlap + EPSILON ||
      (Math.abs(shared - best.overlap) <= EPSILON && offset < best.offset - EPSILON)
    ) {
      best = { paneId: entry.paneId, overlap: shared, offset };
    }
  }
  return best?.paneId ?? null;
}

/** Returns `activePaneId` when the tree holds it, else the first pane in visual order. */
export function resolveActivePane(tree: SplitNode, activePaneId: string | null | undefined): string {
  if (activePaneId && findPanePath(tree, activePaneId)) return activePaneId;
  return paneIdsInOrder(tree)[0];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.length > 0;
}

/** Lists every invariant violation; an empty list means `input` is a valid SplitNode. */
export function validateTree(input: unknown): string[] {
  const issues: string[] = [];
  const panes = new Set<string>();
  const ids = new Set<string>();
  const walk = (raw: unknown, path: string, level: number) => {
    const at = path;
    if (!isRecord(raw)) {
      issues.push(`${at}: not a node`);
      return;
    }
    if (!isNonEmptyString(raw.id)) issues.push(`${at}: missing node id`);
    else if (ids.has(raw.id)) issues.push(`${at}: duplicate node id ${raw.id}`);
    else ids.add(raw.id);
    if (raw.type === "leaf") {
      if (!isNonEmptyString(raw.paneId)) issues.push(`${at}: missing pane id`);
      else if (panes.has(raw.paneId)) issues.push(`${at}: duplicate pane id ${raw.paneId}`);
      else panes.add(raw.paneId);
      return;
    }
    if (raw.type !== "split") {
      issues.push(`${at}: unknown node type`);
      return;
    }
    if (level >= SPLIT_TREE_MAX_DEPTH) issues.push(`${at}: depth exceeds ${SPLIT_TREE_MAX_DEPTH}`);
    if (raw.dir !== "row" && raw.dir !== "col") issues.push(`${at}: invalid direction`);
    if (
      typeof raw.ratio !== "number" ||
      !Number.isFinite(raw.ratio) ||
      raw.ratio < SPLIT_RATIO_MIN ||
      raw.ratio > SPLIT_RATIO_MAX
    ) {
      issues.push(`${at}: ratio out of range`);
    }
    walk(raw.a, `${path}.a`, level + 1);
    walk(raw.b, `${path}.b`, level + 1);
  };
  walk(input, "root", 0);
  return issues;
}

export function isValidTree(input: unknown): input is SplitNode {
  return validateTree(input).length === 0;
}

export interface RepairResult {
  tree: SplitNode | null;
  /** Invariant violations found in the input; empty when it was already valid. */
  issues: string[];
  /** Panes whose leaves had to be removed (duplicates or beyond the depth limit). */
  droppedPaneIds: string[];
}

function rawPaneIds(raw: unknown, out: string[] = []): string[] {
  if (!isRecord(raw)) return out;
  if (raw.type === "leaf") {
    if (isNonEmptyString(raw.paneId)) out.push(raw.paneId);
  } else if (raw.type === "split") {
    rawPaneIds(raw.a, out);
    rawPaneIds(raw.b, out);
  }
  return out;
}

/**
 * Returns a valid tree built from untrusted input. Malformed nodes and empty
 * splits collapse into their surviving child, duplicate panes keep their first
 * occurrence, ratios are clamped, duplicate or missing node ids are replaced,
 * and splits beyond the depth limit keep only their first pane. Removed panes
 * are reported so callers can surface them.
 */
export function repairTree(input: unknown, newId: IdFactory = defaultIdFactory): RepairResult {
  const issues = validateTree(input);
  if (issues.length === 0) return { tree: input as SplitNode, issues, droppedPaneIds: [] };
  const panes = new Set<string>();
  const ids = new Set<string>();
  const dropped: string[] = [];
  const nodeId = (raw: Record<string, unknown>) => {
    const id = isNonEmptyString(raw.id) && !ids.has(raw.id) ? raw.id : newId();
    ids.add(id);
    return id;
  };
  const leaf = (raw: Record<string, unknown>, paneId: string): SplitLeaf | null => {
    if (panes.has(paneId)) {
      dropped.push(paneId);
      return null;
    }
    panes.add(paneId);
    return { type: "leaf", id: nodeId(raw), paneId };
  };
  const walk = (raw: unknown, level: number): SplitNode | null => {
    if (!isRecord(raw)) return null;
    if (raw.type === "leaf") return isNonEmptyString(raw.paneId) ? leaf(raw, raw.paneId) : null;
    if (raw.type !== "split") return null;
    if (level >= SPLIT_TREE_MAX_DEPTH) {
      let kept: SplitLeaf | null = null;
      for (const paneId of rawPaneIds(raw)) {
        if (!kept && !panes.has(paneId)) kept = leaf({}, paneId);
        else dropped.push(paneId);
      }
      return kept;
    }
    const id = nodeId(raw);
    const a = walk(raw.a, level + 1);
    const b = walk(raw.b, level + 1);
    if (!a || !b) return a ?? b;
    return {
      type: "split",
      id,
      dir: raw.dir === "row" ? "row" : "col",
      ratio: clampRatio(typeof raw.ratio === "number" ? raw.ratio : Number.NaN),
      a,
      b,
    };
  };
  return { tree: walk(input, 0), issues, droppedPaneIds: dropped };
}

export interface ReconcileResult {
  tree: SplitNode | null;
  /** Panes that could not be placed because every leaf sits at the depth limit. */
  unplacedPaneIds: string[];
}

/**
 * Makes the tree hold exactly `paneIds`: leaves for other panes collapse, and
 * missing panes are added by splitting the shallowest leaf (the last one in
 * visual order among equals) to the right. The tree is null when `paneIds` is
 * empty.
 */
export function reconcilePanes(
  tree: SplitNode | null,
  paneIds: readonly string[],
  newId: IdFactory = defaultIdFactory,
): ReconcileResult {
  const wanted = new Set(paneIds);
  let next = tree;
  for (const paneId of tree ? paneIdsInOrder(tree) : []) {
    if (!wanted.has(paneId) && next) next = closePane(next, paneId);
  }
  const unplaced: string[] = [];
  for (const paneId of wanted) {
    if (!next) {
      next = createLeaf(paneId, newId);
      continue;
    }
    if (findPanePath(next, paneId)) continue;
    const grown = splitPane(next, shallowestLeaf(next), { dir: "row", side: "after", newPaneId: paneId }, newId);
    if (grown === next) unplaced.push(paneId);
    next = grown;
  }
  return { tree: next, unplacedPaneIds: unplaced };
}

function shallowestLeaf(tree: SplitNode): string {
  let bestPane = "";
  let bestDepth = Number.POSITIVE_INFINITY;
  const walk = (node: SplitNode, depth: number) => {
    if (node.type === "leaf") {
      if (depth <= bestDepth) {
        bestPane = node.paneId;
        bestDepth = depth;
      }
      return;
    }
    walk(node.a, depth + 1);
    walk(node.b, depth + 1);
  };
  walk(tree, 0);
  return bestPane;
}
