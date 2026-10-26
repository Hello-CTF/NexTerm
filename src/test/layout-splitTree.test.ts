import { describe, expect, it } from "vitest";
import {
  closePane,
  createLeaf,
  findLeaf,
  findNodePath,
  findPanePath,
  focusNeighbor,
  isValidTree,
  movePane,
  nodeAt,
  paneIdsInOrder,
  paneRects,
  reconcilePanes,
  repairTree,
  resolveActivePane,
  setSplitRatio,
  splitPane,
  SPLIT_TREE_MAX_DEPTH,
  swapPanes,
  treeDepth,
  validateTree,
  type FocusDirection,
  type SplitDir,
  type SplitNode,
} from "../app/splitTree";
import { decodeSplitTree, encodeSplitTree } from "../app/splitTreeSync";

function ids(prefix = "n") {
  let next = 0;
  return () => `${prefix}${(next += 1)}`;
}

const leaf = (paneId: string, id = `l-${paneId}`): SplitNode => ({ type: "leaf", id, paneId });
const split = (id: string, dir: SplitDir, a: SplitNode, b: SplitNode, ratio = 0.5): SplitNode => ({
  type: "split",
  id,
  dir,
  ratio,
  a,
  b,
});

// Layout used by several geometry tests:
// +-----+-----+
// |     |  B  |
// |  A  +--+--+
// |     |C | D|
// +-----+--+--+
const grid = split(
  "root",
  "row",
  leaf("A"),
  split("right", "col", leaf("B"), split("bottom", "row", leaf("C"), leaf("D"))),
);

describe("split tree operations", () => {
  it("creates a single leaf", () => {
    const tree = createLeaf("p1", ids());
    expect(tree).toEqual({ type: "leaf", id: "n1", paneId: "p1" });
    expect(treeDepth(tree)).toBe(0);
    expect(validateTree(tree)).toEqual([]);
  });

  it.each([
    ["row", "after", ["p1", "p2"]],
    ["row", "before", ["p2", "p1"]],
    ["col", "after", ["p1", "p2"]],
    ["col", "before", ["p2", "p1"]],
  ] as const)("splits %s on the %s side", (dir, side, order) => {
    const tree = splitPane(leaf("p1"), "p1", { dir, side, newPaneId: "p2" }, ids());
    expect(tree.type === "split" && tree.dir).toBe(dir);
    expect(paneIdsInOrder(tree)).toEqual(order);
    expect(isValidTree(tree)).toBe(true);
  });

  it("ignores splits that do not apply", () => {
    const tree = split("s", "row", leaf("p1"), leaf("p2"));
    expect(splitPane(tree, "missing", { dir: "row", side: "after", newPaneId: "p3" })).toBe(tree);
    expect(splitPane(tree, "p1", { dir: "row", side: "after", newPaneId: "p2" })).toBe(tree);
    expect(splitPane(tree, "p1", { dir: "row", side: "after", newPaneId: "" })).toBe(tree);
  });

  it("refuses to split beyond the depth limit", () => {
    const newId = ids();
    let tree: SplitNode = leaf("p0");
    for (let index = 1; index <= SPLIT_TREE_MAX_DEPTH; index += 1) {
      tree = splitPane(tree, `p${index - 1}`, { dir: "col", side: "after", newPaneId: `p${index}` }, newId);
    }
    expect(treeDepth(tree)).toBe(SPLIT_TREE_MAX_DEPTH);
    const deepest = `p${SPLIT_TREE_MAX_DEPTH}`;
    expect(splitPane(tree, deepest, { dir: "row", side: "after", newPaneId: "extra" }, newId)).toBe(tree);
    expect(splitPane(tree, "p0", { dir: "row", side: "after", newPaneId: "extra" }, newId)).not.toBe(tree);
  });

  it.each([
    ["the a side", "A", split("right", "col", leaf("B"), split("bottom", "row", leaf("C"), leaf("D")))],
    ["a nested b side", "D", split("root", "row", leaf("A"), split("right", "col", leaf("B"), leaf("C")))],
    ["a nested a side", "B", split("root", "row", leaf("A"), split("bottom", "row", leaf("C"), leaf("D")))],
  ])("collapses the parent when closing %s", (_label, paneId, expected) => {
    expect(closePane(grid, paneId)).toEqual(expected);
  });

  it("closes the last pane to null and ignores unknown panes", () => {
    expect(closePane(leaf("p1"), "p1")).toBeNull();
    expect(closePane(grid, "missing")).toBe(grid);
  });

  it.each([
    [0.3, 0.3],
    [0, 0.15],
    [-4, 0.15],
    [0.9, 0.85],
    [12, 0.85],
    [0.15, 0.15],
    [0.85, 0.85],
  ])("clamps ratio %s to %s", (input, expected) => {
    const tree = setSplitRatio(grid, "right", input);
    expect(nodeAt(tree, ["b"])).toMatchObject({ id: "right", ratio: expected });
  });

  it("ignores ratio updates that do not apply", () => {
    expect(setSplitRatio(grid, "right", Number.NaN)).toBe(grid);
    expect(setSplitRatio(grid, "missing", 0.3)).toBe(grid);
    expect(setSplitRatio(grid, "l-A", 0.3)).toBe(grid);
    expect(setSplitRatio(grid, "right", 0.5)).toBe(grid);
  });

  it("finds leaves, nodes and visual order", () => {
    expect(paneIdsInOrder(grid)).toEqual(["A", "B", "C", "D"]);
    expect(findLeaf(grid, "C")).toEqual({ leaf: leaf("C"), path: ["b", "b", "a"] });
    expect(findLeaf(grid, "missing")).toBeNull();
    expect(findNodePath(grid, "bottom")).toEqual(["b", "b"]);
    expect(findNodePath(grid, "missing")).toBeNull();
    expect(nodeAt(grid, ["a", "a"])).toBeNull();
    expect(treeDepth(grid)).toBe(3);
  });

  it("resolves the active pane", () => {
    expect(resolveActivePane(grid, "C")).toBe("C");
    expect(resolveActivePane(grid, "gone")).toBe("A");
    expect(resolveActivePane(grid, null)).toBe("A");
  });

  it("swaps panes in place", () => {
    const swapped = swapPanes(grid, "A", "D");
    expect(paneIdsInOrder(swapped)).toEqual(["D", "B", "C", "A"]);
    expect(findLeaf(swapped, "D")?.leaf.id).toBe("l-A");
    expect(swapPanes(grid, "A", "A")).toBe(grid);
    expect(swapPanes(grid, "A", "missing")).toBe(grid);
  });

  it("moves a pane next to a target and collapses its old slot", () => {
    const moved = movePane(grid, "A", "D", { dir: "col", side: "after" }, ids());
    expect(moved).toMatchObject({
      id: "right",
      a: { paneId: "B" },
      b: { id: "bottom", a: { paneId: "C" }, b: { type: "split", dir: "col", a: { paneId: "D" }, b: { id: "l-A" } } },
    });
    expect(isValidTree(moved)).toBe(true);
    expect(movePane(grid, "A", "A", { dir: "row", side: "after" })).toBe(grid);
    expect(movePane(grid, "missing", "A", { dir: "row", side: "after" })).toBe(grid);
    expect(movePane(grid, "A", "missing", { dir: "row", side: "after" })).toBe(grid);
  });

  it("does not move a pane past the depth limit", () => {
    const newId = ids();
    let tree: SplitNode = split("s0", "row", split("pair", "col", leaf("L1"), leaf("L2")), leaf("p0"));
    for (let index = 1; index < SPLIT_TREE_MAX_DEPTH; index += 1) {
      tree = splitPane(tree, `p${index - 1}`, { dir: "col", side: "after", newPaneId: `p${index}` }, newId);
    }
    expect(treeDepth(tree)).toBe(SPLIT_TREE_MAX_DEPTH);
    expect(movePane(tree, "L1", `p${SPLIT_TREE_MAX_DEPTH - 1}`, { dir: "row", side: "after" }, newId)).toBe(tree);
  });
});

describe("split tree geometry", () => {
  it("computes normalized rectangles", () => {
    const rects = Object.fromEntries(paneRects(grid).map((entry) => [entry.paneId, entry.rect]));
    expect(rects.A).toEqual({ x: 0, y: 0, width: 0.5, height: 1 });
    expect(rects.B).toEqual({ x: 0.5, y: 0, width: 0.5, height: 0.5 });
    expect(rects.C).toEqual({ x: 0.5, y: 0.5, width: 0.25, height: 0.5 });
    expect(rects.D).toEqual({ x: 0.75, y: 0.5, width: 0.25, height: 0.5 });
  });

  it.each([
    ["A", "right", "B"],
    ["A", "left", null],
    ["A", "up", null],
    ["B", "left", "A"],
    ["B", "down", "C"],
    ["B", "right", null],
    ["C", "left", "A"],
    ["C", "right", "D"],
    ["C", "up", "B"],
    ["D", "up", "B"],
    ["D", "left", "C"],
    ["D", "down", null],
    ["missing", "right", null],
  ] as const)("moves focus from %s %s to %s", (from, direction, expected) => {
    expect(focusNeighbor(grid, from, direction as FocusDirection)).toBe(expected);
  });

  it("prefers the larger overlap, then the nearer centre", () => {
    // Left column split 0.7/0.3, right pane spans the height: A is the
    // larger overlap for R moving left.
    const tree = split("root", "row", split("left", "col", leaf("A"), leaf("B"), 0.7), leaf("R"));
    expect(focusNeighbor(tree, "R", "left")).toBe("A");
    // Equal overlaps: a centred target beats an offset one.
    const even = split(
      "root",
      "row",
      split("left", "col", leaf("A"), split("mid", "col", leaf("B"), leaf("C"))),
      split("right", "col", leaf("X"), leaf("Y")),
    );
    // X spans y 0-0.5 and overlaps A fully (0-0.5).
    expect(focusNeighbor(even, "X", "left")).toBe("A");
    expect(focusNeighbor(even, "Y", "left")).toBe("B");
  });
});

describe("split tree validation and repair", () => {
  it.each([
    ["a non-object", 3, /not a node/],
    ["an unknown type", { type: "box", id: "x" }, /unknown node type/],
    ["a missing id", { type: "leaf", paneId: "p" }, /missing node id/],
    ["a missing pane id", { type: "leaf", id: "x" }, /missing pane id/],
    ["an invalid direction", { ...split("s", "row", leaf("a"), leaf("b")), dir: "diag" }, /invalid direction/],
    ["a ratio out of range", split("s", "row", leaf("a"), leaf("b"), 0.95), /ratio out of range/],
    ["a duplicate pane", split("s", "row", leaf("a", "x"), leaf("a", "y")), /duplicate pane id a/],
    ["a duplicate node id", split("s", "row", leaf("a", "x"), leaf("b", "x")), /duplicate node id x/],
    ["an empty split", { type: "split", id: "s", dir: "row", ratio: 0.5, a: leaf("a") }, /root\.b: not a node/],
  ])("reports %s", (_label, input, pattern) => {
    expect(validateTree(input).join("\n")).toMatch(pattern);
    expect(isValidTree(input)).toBe(false);
  });

  it("reports excess depth", () => {
    let raw: unknown = leaf("deep");
    for (let index = 0; index <= SPLIT_TREE_MAX_DEPTH; index += 1)
      raw = split(`s${index}`, "row", leaf(`p${index}`), raw as SplitNode);
    expect(validateTree(raw).join("\n")).toMatch(/depth exceeds/);
  });

  it("returns valid input unchanged", () => {
    expect(repairTree(grid)).toEqual({ tree: grid, issues: [], droppedPaneIds: [] });
  });

  it("collapses empty splits and malformed children", () => {
    const result = repairTree(
      { type: "split", id: "s", dir: "row", ratio: 0.5, a: leaf("a"), b: { type: "?" } },
      ids(),
    );
    expect(result.tree).toEqual(leaf("a"));
    expect(result.issues.length).toBeGreaterThan(0);
    expect(repairTree({ type: "split", id: "s", a: null, b: 1 }).tree).toBeNull();
    expect(repairTree({ type: "leaf", id: "x", paneId: "" }).tree).toBeNull();
    expect(repairTree("nope").tree).toBeNull();
  });

  it("drops duplicate panes, regenerates ids and clamps ratios", () => {
    const raw = split("s", "diag" as SplitDir, leaf("a", "x"), split("x", "row", leaf("a", "y"), leaf("b", "")), 2);
    const result = repairTree(raw, ids("new"));
    expect(result.droppedPaneIds).toEqual([]);
    expect(result.tree).toEqual({
      type: "split",
      id: "s",
      dir: "col",
      ratio: 0.85,
      a: leaf("a", "x"),
      b: { type: "leaf", id: expect.stringMatching(/^new/), paneId: "b" },
    });
    expect(isValidTree(result.tree)).toBe(true);
    const missingRatio = repairTree({ type: "split", id: "s", dir: "row", a: leaf("a"), b: leaf("b") });
    expect(missingRatio.tree).toMatchObject({ ratio: 0.5 });
  });

  it("keeps the first pane of splits beyond the depth limit", () => {
    let raw: SplitNode = split("deep", "row", leaf("x"), split("deeper", "row", leaf("y"), leaf("p0")));
    for (let index = 1; index < SPLIT_TREE_MAX_DEPTH; index += 1)
      raw = split(`s${index}`, "col", leaf(`p${index}`), raw);
    const result = repairTree(raw, ids("new"));
    expect(result.tree && treeDepth(result.tree)).toBe(SPLIT_TREE_MAX_DEPTH);
    expect(result.droppedPaneIds).toEqual(["p0"]);
    expect(result.tree && paneIdsInOrder(result.tree)).toEqual(expect.arrayContaining(["x", "y"]));
    expect(isValidTree(result.tree)).toBe(true);
  });

  it("does not report pruned duplicates whose first copy survives", () => {
    let raw: SplitNode = split("deep", "row", leaf("p1", "dup"), leaf("z"));
    for (let index = 1; index <= SPLIT_TREE_MAX_DEPTH; index += 1)
      raw = split(`s${index}`, "col", leaf(`p${index}`), raw);
    const result = repairTree(raw, ids("new"));
    expect(result.droppedPaneIds).toEqual([]);
    expect(result.tree && paneIdsInOrder(result.tree)).toContain("z");
    expect(result.tree && treeDepth(result.tree)).toBe(SPLIT_TREE_MAX_DEPTH);
  });
});

describe("reconcilePanes", () => {
  it("removes extra panes and adds missing ones next to the shallowest leaf", () => {
    const result = reconcilePanes(grid, ["B", "C", "D", "E"], ids());
    expect(result.unplacedPaneIds).toEqual([]);
    expect(result.tree && paneIdsInOrder(result.tree)).toEqual(["B", "E", "C", "D"]);
    expect(isValidTree(result.tree)).toBe(true);
  });

  it("builds from nothing and empties to null", () => {
    const built = reconcilePanes(null, ["a", "b"], ids());
    expect(built.tree && paneIdsInOrder(built.tree)).toEqual(["a", "b"]);
    expect(reconcilePanes(grid, []).tree).toBeNull();
  });

  it("reports panes that cannot be placed within the depth limit", () => {
    const panes = Array.from({ length: 2 ** SPLIT_TREE_MAX_DEPTH + 1 }, (_, index) => `p${index}`);
    const result = reconcilePanes(null, panes, ids());
    expect(result.unplacedPaneIds).toEqual([`p${2 ** SPLIT_TREE_MAX_DEPTH}`]);
    expect(isValidTree(result.tree)).toBe(true);
  });
});

// Deterministic PRNG (mulberry32) so failures reproduce from the seed.
function rng(seed: number) {
  let state = seed >>> 0;
  return () => {
    state = (state + 0x6d2b79f5) >>> 0;
    let t = state;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function assertInvariants(tree: SplitNode, expectedPanes: Set<string>) {
  expect(validateTree(tree)).toEqual([]);
  expect(new Set(paneIdsInOrder(tree))).toEqual(expectedPanes);
  expect(paneIdsInOrder(tree)).toHaveLength(expectedPanes.size);
  const rects = paneRects(tree);
  const area = rects.reduce((sum, entry) => sum + entry.rect.width * entry.rect.height, 0);
  expect(area).toBeCloseTo(1, 9);
  for (let i = 0; i < rects.length; i += 1) {
    for (let j = i + 1; j < rects.length; j += 1) {
      const first = rects[i].rect;
      const second = rects[j].rect;
      const width = Math.min(first.x + first.width, second.x + second.width) - Math.max(first.x, second.x);
      const height = Math.min(first.y + first.height, second.y + second.height) - Math.max(first.y, second.y);
      expect(width <= 1e-9 || height <= 1e-9).toBe(true);
    }
  }
  const decoded = decodeSplitTree(JSON.stringify(encodeSplitTree(tree)));
  expect(decoded).toEqual({ ok: true, tree, issues: [], droppedPaneIds: [] });
}

function splitIds(node: SplitNode): string[] {
  return node.type === "leaf" ? [] : [node.id, ...splitIds(node.a), ...splitIds(node.b)];
}

const DIRECTIONS: FocusDirection[] = ["left", "right", "up", "down"];

describe("split tree random operation sequences", () => {
  it.each([1, 2, 3, 7, 42, 1337, 2026, 90210])("preserves invariants for seed %i", (seed) => {
    const random = rng(seed);
    const newId = ids(`s${seed}-`);
    const pick = <T>(values: readonly T[]): T => values[Math.floor(random() * values.length)];
    let tree: SplitNode = createLeaf("p0", newId);
    const panes = new Set(["p0"]);
    let nextPane = 1;
    for (let step = 0; step < 200; step += 1) {
      const current = paneIdsInOrder(tree);
      const op = random();
      if (op < 0.35) {
        const paneId = `p${nextPane++}`;
        const target = pick(current);
        const options = {
          dir: pick(["row", "col"] as const),
          side: pick(["before", "after"] as const),
          newPaneId: paneId,
          ratio: random(),
        };
        const next = splitPane(tree, target, options, newId);
        if (next !== tree) panes.add(paneId);
        else expect(findPanePath(tree, target)).toHaveLength(SPLIT_TREE_MAX_DEPTH);
        tree = next;
      } else if (op < 0.55 && current.length > 1) {
        const paneId = pick(current);
        tree = closePane(tree, paneId) as SplitNode;
        panes.delete(paneId);
      } else if (op < 0.7 && tree.type === "split") {
        tree = setSplitRatio(tree, pick(splitIds(tree)), random() * 1.4 - 0.2);
      } else if (op < 0.8) {
        tree = swapPanes(tree, pick(current), pick(current));
      } else if (op < 0.9) {
        tree = movePane(
          tree,
          pick(current),
          pick(current),
          { dir: pick(["row", "col"] as const), side: pick(["before", "after"] as const) },
          newId,
        );
      } else {
        const from = pick(current);
        const direction = pick(DIRECTIONS);
        const target = focusNeighbor(tree, from, direction);
        if (target) {
          const rects = Object.fromEntries(paneRects(tree).map((entry) => [entry.paneId, entry.rect]));
          const a = rects[from];
          const b = rects[target];
          if (direction === "right") expect(b.x).toBeCloseTo(a.x + a.width, 9);
          if (direction === "left") expect(b.x + b.width).toBeCloseTo(a.x, 9);
          if (direction === "down") expect(b.y).toBeCloseTo(a.y + a.height, 9);
          if (direction === "up") expect(b.y + b.height).toBeCloseTo(a.y, 9);
        }
      }
      assertInvariants(tree, panes);
    }
  });
});
