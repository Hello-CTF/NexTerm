import { describe, expect, it } from "vitest";
import {
  closePane,
  contentIdFactory,
  isValidTree,
  movePane,
  paneIdsInOrder,
  reconcilePanes,
  repairTree,
  setSplitRatio,
  splitPane,
  SPLIT_TREE_MAX_DEPTH,
  swapPanes,
  treeDepth,
  validateTree,
  type SplitDir,
  type SplitNode,
} from "../app/splitTree";
import {
  decodeSplitTree,
  encodeSplitTree,
  legacyWorkspaceTree,
  mergeSplitTrees,
  SPLIT_TREE_FORMAT_VERSION,
  type TimedSplitTree,
} from "../app/splitTreeSync";

const leaf = (paneId: string, id = `l-${paneId}`): SplitNode => ({ type: "leaf", id, paneId });
const split = (id: string, dir: SplitDir, a: SplitNode, b: SplitNode, ratio = 0.5): SplitNode => ({
  type: "split",
  id,
  dir,
  ratio,
  a,
  b,
});

function allIds(node: SplitNode): string[] {
  return node.type === "leaf" ? [node.id] : [node.id, ...allIds(node.a), ...allIds(node.b)];
}

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

// Applies `steps` random edits with the default (content-derived) id factory,
// as a real device would.
function evolve(tree: SplitNode, random: () => number, steps: number, prefix: string): SplitNode {
  const pick = <T>(values: readonly T[]): T => values[Math.floor(random() * values.length)];
  let next = tree;
  for (let step = 0; step < steps; step += 1) {
    const panes = paneIdsInOrder(next);
    const op = random();
    if (op < 0.4) {
      next = splitPane(next, pick(panes), {
        dir: pick(["row", "col"] as const),
        side: pick(["before", "after"] as const),
        newPaneId: `${prefix}${step}`,
      });
    } else if (op < 0.6 && panes.length > 1) {
      next = closePane(next, pick(panes)) as SplitNode;
    } else if (op < 0.8 && next.type === "split") {
      next = setSplitRatio(next, next.id, 0.15 + random() * 0.7);
    } else if (op < 0.9) {
      next = swapPanes(next, pick(panes), pick(panes));
    } else {
      next = movePane(next, pick(panes), pick(panes), { dir: "row", side: "after" });
    }
  }
  return next;
}

describe("split tree merge convergence", () => {
  const base = split("root", "row", leaf("A"), split("right", "col", leaf("B"), leaf("C")), 0.4);

  it.each([
    ["equal shapes with different node ids, local newer", 900, 100],
    ["equal shapes with different node ids, same time", 500, 500],
  ])("converges on %s", (_label, localTime, remoteTime) => {
    const local = { tree: split("l1", "row", leaf("A", "la"), leaf("B", "lb"), 0.3), updatedAt: localTime };
    const remote = { tree: split("r1", "row", leaf("A", "ra"), leaf("B", "rb"), 0.6), updatedAt: remoteTime };
    const one = mergeSplitTrees(null, local, remote);
    const other = mergeSplitTrees(null, remote, local);
    expect(one.tree).toEqual(other.tree);
    expect(encodeSplitTree(one.tree as SplitNode)).toEqual(encodeSplitTree(other.tree as SplitNode));
  });

  it.each([1, 2, 3, 5, 8, 13, 21, 34, 55, 89, 144, 233])(
    "merge(local, remote) equals merge(remote, local) including ids for seed %i",
    (seed) => {
      const random = rng(seed);
      const common = evolve(base, random, 6, `c${seed}-`);
      const baseTree: TimedSplitTree = { tree: common, updatedAt: 100 };
      const local: TimedSplitTree = { tree: evolve(common, random, 4, "L"), updatedAt: 100 + Math.floor(random() * 3) };
      const remote: TimedSplitTree = {
        tree: evolve(common, random, 4, "R"),
        updatedAt: 100 + Math.floor(random() * 3),
      };
      const paneIds = [...new Set([...paneIdsInOrder(local.tree), ...paneIdsInOrder(remote.tree)])].sort();
      for (const options of [{}, { paneIds }]) {
        const one = mergeSplitTrees(baseTree, local, remote, options);
        const other = mergeSplitTrees(baseTree, remote, local, options);
        expect(one.tree).toEqual(other.tree);
        expect(one.updatedAt).toBe(other.updatedAt);
        expect(isValidTree(one.tree)).toBe(true);
        // Running the same merge again yields the same tree, and merging the
        // result with itself is a fixed point.
        expect(mergeSplitTrees(baseTree, local, remote, options).tree).toEqual(one.tree);
        const settled = { tree: one.tree as SplitNode, updatedAt: one.updatedAt };
        expect(mergeSplitTrees(settled, settled, settled, options).tree).toEqual(one.tree);
      }
    },
  );

  it("mints the same ids on every device for reconciled panes", () => {
    const local = { tree: split("s", "row", leaf("A"), leaf("B")), updatedAt: 1 };
    const remote = { tree: split("s", "row", leaf("A"), leaf("B")), updatedAt: 2 };
    const one = mergeSplitTrees(null, local, remote, { paneIds: ["A", "B", "C"] });
    const other = mergeSplitTrees(null, remote, local, { paneIds: ["A", "B", "C"] });
    expect(one.tree).toEqual(other.tree);
    expect(one.tree && paneIdsInOrder(one.tree)).toEqual(["A", "B", "C"]);
  });

  it("converts the same legacy workspace identically on two devices", () => {
    const workspace = { panes: [{ id: "top" }, { id: "bottom" }], activePaneId: "bottom", splitRatio: 0.6 };
    expect(legacyWorkspaceTree(workspace)).toEqual(legacyWorkspaceTree(structuredClone(workspace)));
  });

  it("repairs the same document identically on two devices", () => {
    const raw = split("s", "row", leaf("A", ""), split("s", "col", leaf("B", "x"), leaf("C", "x")));
    expect(repairTree(raw).tree).toEqual(repairTree(structuredClone(raw)).tree);
    expect(isValidTree(repairTree(raw).tree)).toBe(true);
  });

  it("salts minted ids that collide with existing ones", () => {
    const identity = (seed: string) => seed;
    const tree = split("s", "row", leaf("A", "leaf:C"), leaf("B"));
    const grown = splitPane(tree, "B", { dir: "col", side: "after", newPaneId: "C" }, identity);
    expect(isValidTree(grown)).toBe(true);
    expect(new Set(allIds(grown)).size).toBe(allIds(grown).length);
    expect(allIds(grown)).toContain("leaf:C#1");
    expect(contentIdFactory("x")).toBe(contentIdFactory("x"));
    expect(contentIdFactory("x")).not.toBe(contentIdFactory("y"));
  });
});

function deepChain(levels: number, side: "a" | "b"): string {
  let json = '{"type":"leaf","id":"bottom","paneId":"bottom"}';
  for (let index = 0; index < levels; index += 1) {
    const sibling = `{"type":"leaf","id":"l${index}","paneId":"p${index}"}`;
    const children = side === "a" ? `"a":${json},"b":${sibling}` : `"a":${sibling},"b":${json}`;
    json = `{"type":"split","id":"s${index}","dir":"row","ratio":0.5,${children}}`;
  }
  return `{"v":${SPLIT_TREE_FORMAT_VERSION},"root":${json}}`;
}

describe("split tree hostile input", () => {
  it.each(["a", "b"] as const)("decodes a 20000-level chain on the %s side without throwing", (side) => {
    const text = deepChain(20_000, side);
    const result = decodeSplitTree(text);
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(isValidTree(result.tree)).toBe(true);
    expect(treeDepth(result.tree)).toBeLessThanOrEqual(SPLIT_TREE_MAX_DEPTH);
    expect(result.issues.join("\n")).toMatch(/depth exceeds/);
    expect(decodeSplitTree(JSON.parse(text)).ok).toBe(true);
  });

  it("handles cyclic objects", () => {
    const cyclic: Record<string, unknown> = { type: "split", id: "s", dir: "row", ratio: 0.5, a: leaf("A") };
    cyclic.b = cyclic;
    expect(validateTree(cyclic).join("\n")).toMatch(/repeated node object/);
    expect(repairTree(cyclic).tree).toEqual(leaf("A"));
    expect(decodeSplitTree({ v: SPLIT_TREE_FORMAT_VERSION, root: cyclic })).toMatchObject({
      ok: true,
      tree: leaf("A"),
    });

    const selfOnly: Record<string, unknown> = { type: "split", id: "s", dir: "row", ratio: 0.5 };
    selfOnly.a = selfOnly;
    selfOnly.b = selfOnly;
    expect(decodeSplitTree({ v: SPLIT_TREE_FORMAT_VERSION, root: selfOnly })).toEqual({
      ok: false,
      error: "malformed",
    });
  });

  it("handles cycles below the depth limit", () => {
    const cyclic: Record<string, unknown> = { type: "split", id: "deep", dir: "row", ratio: 0.5, a: leaf("X") };
    cyclic.b = cyclic;
    let raw: unknown = cyclic;
    for (let index = 0; index < SPLIT_TREE_MAX_DEPTH; index += 1)
      raw = split(`s${index}`, "col", leaf(`p${index}`), raw as SplitNode);
    const result = repairTree(raw);
    expect(isValidTree(result.tree)).toBe(true);
    expect(result.tree && paneIdsInOrder(result.tree)).toContain("X");
  });

  it("reports a decode that throws while reading as malformed", () => {
    const root = {
      type: "leaf",
      id: "x",
      get paneId(): string {
        throw new Error("hostile getter");
      },
    };
    expect(decodeSplitTree({ v: SPLIT_TREE_FORMAT_VERSION, root })).toEqual({ ok: false, error: "malformed" });
  });

  it("drops fields outside the model", () => {
    const text = `{"v":${SPLIT_TREE_FORMAT_VERSION},"root":{"type":"split","id":"s","dir":"row","ratio":0.5,"extra":1,"a":{"type":"leaf","id":"a","paneId":"A","__proto__":{"polluted":true}},"b":{"type":"leaf","id":"b","paneId":"B"}}}`;
    const result = decodeSplitTree(text);
    expect(result.ok && result.issues.join("\n")).toMatch(/unexpected fields/);
    const expected = split("s", "row", leaf("A", "a"), leaf("B", "b"));
    expect(result).toMatchObject({ ok: true, tree: expected, droppedPaneIds: [] });
    if (!result.ok) return;
    expect(JSON.stringify(encodeSplitTree(result.tree))).toBe(
      JSON.stringify({ v: SPLIT_TREE_FORMAT_VERSION, root: expected }),
    );
    expect(Object.keys(result.tree)).toEqual(["type", "id", "dir", "ratio", "a", "b"]);
    const junk = { ...leaf("A"), extra: true } as unknown as SplitNode;
    expect(Object.keys(encodeSplitTree(junk).root)).toEqual(["type", "id", "paneId"]);
  });

  it("reports empty pane ids as unplaced during reconciliation", () => {
    const result = reconcilePanes(split("s", "row", leaf("A"), leaf("B")), ["A", "", "B"]);
    expect(result.unplacedPaneIds).toEqual([""]);
    expect(result.tree && paneIdsInOrder(result.tree)).toEqual(["A", "B"]);
  });
});
