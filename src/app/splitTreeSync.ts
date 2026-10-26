import {
  clampRatio,
  cleanTree,
  defaultIdFactory,
  mintUnique,
  nodeAt,
  findNodePath,
  reconcilePanes,
  repairTree,
  resolveActivePane,
  SPLIT_TREE_MAX_DEPTH,
  type IdFactory,
  type SplitBranch,
  type SplitLeaf,
  type SplitNode,
  type TreePath,
} from "./splitTree";

// Persistence, legacy conversion and multi-device merge for split trees.

export const SPLIT_TREE_FORMAT_VERSION = 1;

export interface SerializedSplitTree {
  v: typeof SPLIT_TREE_FORMAT_VERSION;
  root: SplitNode;
}

export type DecodeSplitTreeResult =
  | {
      ok: true;
      tree: SplitNode;
      /** Invariant violations that were repaired; empty for a clean document. */
      issues: string[];
      droppedPaneIds: string[];
    }
  | { ok: false; error: "malformed" }
  | { ok: false; error: "unsupported-version"; version: unknown };

export function encodeSplitTree(tree: SplitNode): SerializedSplitTree {
  return { v: SPLIT_TREE_FORMAT_VERSION, root: cleanTree(tree) };
}

/**
 * Decodes a serialized tree (object or JSON text). Any version other than
 * SPLIT_TREE_FORMAT_VERSION is rejected rather than guessed at; content of the
 * known version is rebuilt clean, repaired, and the repairs are reported.
 * Never throws: hostile input yields `{ ok: false }` or a repaired tree.
 */
export function decodeSplitTree(raw: unknown, newId: IdFactory = defaultIdFactory): DecodeSplitTreeResult {
  let doc = raw;
  if (typeof doc === "string") {
    try {
      doc = JSON.parse(doc) as unknown;
    } catch {
      return { ok: false, error: "malformed" };
    }
  }
  if (typeof doc !== "object" || doc === null || Array.isArray(doc)) return { ok: false, error: "malformed" };
  const record = doc as Record<string, unknown>;
  if (record.v !== SPLIT_TREE_FORMAT_VERSION) {
    return { ok: false, error: "unsupported-version", version: record.v };
  }
  let repaired: ReturnType<typeof repairTree>;
  try {
    repaired = repairTree(record.root, newId);
  } catch {
    return { ok: false, error: "malformed" };
  }
  if (!repaired.tree) return { ok: false, error: "malformed" };
  return { ok: true, tree: repaired.tree, issues: repaired.issues, droppedPaneIds: repaired.droppedPaneIds };
}

/** The in-session shape of a pre-tree workspace: panes stacked top to bottom. */
export interface LegacySplitWorkspace {
  panes: readonly { id: string }[];
  activePaneId?: string | null;
  splitRatio?: number;
}

export interface LegacyConversion {
  tree: SplitNode | null;
  activePaneId: string | null;
  droppedPaneIds: string[];
}

/**
 * Converts the legacy pane list into a tree. One pane becomes a leaf; two
 * panes become a top/bottom ("col") split using `splitRatio`. Additional
 * panes, which the legacy model never renders, nest under the bottom half at
 * an even ratio until the depth limit; the rest are reported as dropped.
 * Node ids derive from pane ids, so every device converting the same
 * workspace produces the same tree.
 */
export function legacyWorkspaceTree(
  workspace: LegacySplitWorkspace,
  newId: IdFactory = defaultIdFactory,
): LegacyConversion {
  const seen = new Set<string>();
  const ids: string[] = [];
  const dropped: string[] = [];
  for (const pane of workspace.panes) {
    if (!pane.id || seen.has(pane.id)) continue;
    seen.add(pane.id);
    if (ids.length > SPLIT_TREE_MAX_DEPTH) dropped.push(pane.id);
    else ids.push(pane.id);
  }
  if (ids.length === 0) return { tree: null, activePaneId: null, droppedPaneIds: dropped };
  const used = new Set<string>();
  const last = ids[ids.length - 1];
  let tree: SplitNode = { type: "leaf", id: mintUnique(used, `leaf:${last}`, newId), paneId: last };
  for (let index = ids.length - 2; index >= 0; index -= 1) {
    const top: SplitLeaf = { type: "leaf", id: mintUnique(used, `leaf:${ids[index]}`, newId), paneId: ids[index] };
    tree = {
      type: "split",
      id: mintUnique(used, `legacy:${ids[index]}`, newId),
      dir: "col",
      ratio: index === 0 ? clampRatio(workspace.splitRatio ?? Number.NaN) : clampRatio(0.5),
      a: top,
      b: tree,
    };
  }
  return { tree, activePaneId: resolveActivePane(tree, workspace.activePaneId), droppedPaneIds: dropped };
}

/** Shape signature: directions and pane placement, ignoring node ids and ratios. */
export function structureKey(node: SplitNode): string {
  if (node.type === "leaf") return JSON.stringify(node.paneId);
  return `${node.dir}(${structureKey(node.a)},${structureKey(node.b)})`;
}

export interface TimedSplitTree {
  tree: SplitNode;
  /** Wall-clock milliseconds of the last change on the device that produced this tree. */
  updatedAt: number;
}

/** Canonical serialization including node ids, used for deterministic tie-breaks. */
function canonicalTree(tree: SplitNode): string {
  return JSON.stringify(cleanTree(tree));
}

/**
 * same / same-remote: both sides share a shape; the newer side's node ids are
 * kept (ties: the smaller canonical serialization). local/remote: only that side changed the
 * shape since base. conflict-local/conflict-remote: both changed it (or there
 * is no base); the newer tree won.
 */
export type SplitTreeStructureResolution =
  "same" | "same-remote" | "local" | "remote" | "conflict-local" | "conflict-remote";

export interface SplitTreeMergeResult {
  tree: SplitNode | null;
  updatedAt: number;
  structure: SplitTreeStructureResolution;
  unplacedPaneIds: string[];
}

export interface SplitTreeMergeOptions {
  /** The merged pane list for the workspace; the tree is reconciled to hold exactly these panes. */
  paneIds?: readonly string[];
  newId?: IdFactory;
}

function timeOf(value: number): number {
  return Number.isFinite(value) ? value : Number.NEGATIVE_INFINITY;
}

function counterpart(
  side: SplitNode | undefined,
  sameShape: boolean,
  id: string,
  path: TreePath,
  dir: SplitBranch["dir"],
): number | undefined {
  if (!side) return undefined;
  const byId = findNodePath(side, id);
  const found = byId ? nodeAt(side, byId) : sameShape ? nodeAt(side, path) : null;
  return found?.type === "split" && found.dir === dir ? found.ratio : undefined;
}

/**
 * Three-way merge of one workspace's tree, following layoutMerge semantics: a
 * side that did not change since base yields to the other. Structure is
 * resolved as a whole; when both sides reshaped the tree, the newer one wins
 * (ties go to the smaller structure key); with equal shapes the newer side's
 * node ids are kept (ties: smaller canonical form), so every device converges. Split
 * ratios then merge per node, matched by node id (or position when shapes
 * match): an unchanged side yields, and a true conflict is last-write-wins
 * by `updatedAt`, ties taking the smaller ratio.
 */
export function mergeSplitTrees(
  base: TimedSplitTree | null,
  local: TimedSplitTree,
  remote: TimedSplitTree,
  options: SplitTreeMergeOptions = {},
): SplitTreeMergeResult {
  const localKey = structureKey(local.tree);
  const remoteKey = structureKey(remote.tree);
  const baseKey = base ? structureKey(base.tree) : null;
  let structure: SplitTreeStructureResolution;
  const localTime = timeOf(local.updatedAt);
  const remoteTime = timeOf(remote.updatedAt);
  if (localKey === remoteKey) {
    // Same shape: still pick one side's node ids deterministically so both
    // devices converge on the same tree.
    const localWins =
      localTime === remoteTime ? canonicalTree(local.tree) <= canonicalTree(remote.tree) : localTime > remoteTime;
    structure = localWins ? "same" : "same-remote";
  } else if (baseKey === localKey) structure = "remote";
  else if (baseKey === remoteKey) structure = "local";
  else {
    const localWins = localTime === remoteTime ? localKey < remoteKey : localTime > remoteTime;
    structure = localWins ? "conflict-local" : "conflict-remote";
  }
  const winnerIsRemote = structure === "same-remote" || structure === "remote" || structure === "conflict-remote";
  const winner = winnerIsRemote ? remote.tree : local.tree;
  const winnerKey = winnerIsRemote ? remoteKey : localKey;
  const localNewer = localTime > remoteTime;
  const remoteNewer = remoteTime > localTime;

  const mergeRatios = (node: SplitNode, path: TreePath): SplitNode => {
    if (node.type === "leaf") return node;
    const localRatio = counterpart(local.tree, localKey === winnerKey, node.id, path, node.dir);
    const remoteRatio = counterpart(remote.tree, remoteKey === winnerKey, node.id, path, node.dir);
    const baseRatio = counterpart(base?.tree, baseKey === winnerKey, node.id, path, node.dir);
    let ratio = node.ratio;
    if (localRatio !== undefined && remoteRatio !== undefined && localRatio !== remoteRatio) {
      if (baseRatio !== undefined && localRatio === baseRatio) ratio = remoteRatio;
      else if (baseRatio !== undefined && remoteRatio === baseRatio) ratio = localRatio;
      else if (localNewer) ratio = localRatio;
      else if (remoteNewer) ratio = remoteRatio;
      else ratio = Math.min(localRatio, remoteRatio);
    }
    const a = mergeRatios(node.a, [...path, "a"]);
    const b = mergeRatios(node.b, [...path, "b"]);
    ratio = clampRatio(ratio);
    if (ratio === node.ratio && a === node.a && b === node.b) return node;
    return { ...node, ratio, a, b };
  };

  const merged = cleanTree(mergeRatios(winner, []));
  const latest = Math.max(localTime, remoteTime);
  const updatedAt = Number.isFinite(latest) ? latest : 0;
  if (!options.paneIds) return { tree: merged, updatedAt, structure, unplacedPaneIds: [] };
  const reconciled = reconcilePanes(merged, options.paneIds, options.newId ?? defaultIdFactory);
  return { tree: reconciled.tree, updatedAt, structure, unplacedPaneIds: reconciled.unplacedPaneIds };
}
