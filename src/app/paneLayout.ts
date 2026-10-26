import {
  closePane as closeTreePane,
  findPanePath,
  nodeAt,
  paneIdsInOrder,
  reconcilePanes,
  type SplitNode,
} from "./splitTree";
import { legacyWorkspaceTree } from "./splitTreeSync";
import type { Workspace } from "./store";

// A workspace keeps its pane contents in `panes` and their placement in
// `layoutTree`. Code that only adds or removes panes may leave the stored tree
// behind; workspaceLayoutTree always answers with a tree holding exactly the
// workspace's panes, so rendering, focus and persistence agree.

type LayoutSource = Pick<Workspace, "panes" | "layoutTree">;

const treeCache = new WeakMap<object, SplitNode | null>();

function paneIds(workspace: LayoutSource): string[] {
  const seen = new Set<string>();
  for (const pane of workspace.panes) if (pane.id) seen.add(pane.id);
  return [...seen];
}

function computeTree(workspace: LayoutSource): SplitNode | null {
  const ids = paneIds(workspace);
  if (workspace.layoutTree) return reconcilePanes(workspace.layoutTree, ids).tree;
  // A workspace that never split stacks its panes top to bottom, as the pre-tree model rendered them.
  return legacyWorkspaceTree({ panes: ids.map((id) => ({ id })) }).tree;
}

/** The split tree over exactly the workspace's panes; null only when it has no panes. */
export function workspaceLayoutTree(workspace: LayoutSource): SplitNode | null {
  if (treeCache.has(workspace)) return treeCache.get(workspace) ?? null;
  const tree = computeTree(workspace);
  treeCache.set(workspace, tree);
  return tree;
}

/** Pane ids in visual reading order (left to right, top to bottom). */
export function workspacePaneOrder(workspace: LayoutSource): string[] {
  const tree = workspaceLayoutTree(workspace);
  return tree ? paneIdsInOrder(tree) : [];
}

/** Stores a changed tree together with the time of the change, which resolves merge conflicts. */
export function withLayoutTree<T extends Workspace>(workspace: T, tree: SplitNode, now = Date.now()): T {
  return { ...workspace, layoutTree: tree, layoutUpdatedAt: now };
}

/**
 * Brings a stored tree back in line with the pane list after panes were added
 * or removed elsewhere. Workspaces without a stored tree are left alone.
 */
export function syncWorkspaceTree<T extends Workspace>(workspace: T): T {
  if (!workspace.layoutTree) return workspace;
  const tree = workspaceLayoutTree(workspace);
  if (!tree || tree === workspace.layoutTree) return workspace;
  return withLayoutTree(workspace, tree);
}

/**
 * The pane that should take focus once `paneId` is gone: the pane of its sibling subtree that
 * bordered it (the one taking over its space), else any other pane.
 */
export function fallbackPaneAfterClose(workspace: LayoutSource, paneId: string): string | null {
  const tree = workspaceLayoutTree(workspace);
  if (!tree) return null;
  const path = findPanePath(tree, paneId);
  if (path && path.length > 0) {
    const parent = nodeAt(tree, path.slice(0, -1));
    if (parent?.type === "split") {
      const closedSide = path[path.length - 1];
      const sibling = paneIdsInOrder(closedSide === "a" ? parent.b : parent.a);
      return (closedSide === "a" ? sibling[0] : sibling[sibling.length - 1]) ?? null;
    }
  }
  return paneIdsInOrder(tree).find((id) => id !== paneId) ?? null;
}

/** The tree with `paneId` removed and its sibling collapsed into the parent's place. */
export function treeWithoutPane(workspace: LayoutSource, paneId: string): SplitNode | null {
  const tree = workspaceLayoutTree(workspace);
  if (!tree || !findPanePath(tree, paneId)) return tree;
  return closeTreePane(tree, paneId);
}
