import { describe, expect, it } from "vitest";
import {
  closePane,
  isValidTree,
  paneIdsInOrder,
  setSplitRatio,
  splitPane,
  SPLIT_TREE_MAX_DEPTH,
  type SplitDir,
  type SplitNode,
} from "../app/splitTree";
import {
  decodeSplitTree,
  encodeSplitTree,
  legacyWorkspaceTree,
  mergeSplitTrees,
  SPLIT_TREE_FORMAT_VERSION,
  structureKey,
} from "../app/splitTreeSync";

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

const sample = split("root", "row", leaf("A"), split("right", "col", leaf("B"), leaf("C")), 0.4);

describe("split tree serialization", () => {
  it("round-trips through objects and JSON text", () => {
    const encoded = encodeSplitTree(sample);
    expect(encoded.v).toBe(SPLIT_TREE_FORMAT_VERSION);
    expect(encoded.root).not.toBe(sample);
    expect(decodeSplitTree(encoded)).toEqual({ ok: true, tree: sample, issues: [], droppedPaneIds: [] });
    expect(decodeSplitTree(JSON.stringify(encoded))).toEqual({
      ok: true,
      tree: sample,
      issues: [],
      droppedPaneIds: [],
    });
  });

  it.each([
    ["a newer version", { v: SPLIT_TREE_FORMAT_VERSION + 1, root: sample }, SPLIT_TREE_FORMAT_VERSION + 1],
    ["an older version", { v: 0, root: sample }, 0],
    ["a string version", { v: "1", root: sample }, "1"],
    ["a missing version", { root: sample }, undefined],
  ])("rejects %s", (_label, doc, version) => {
    expect(decodeSplitTree(doc)).toEqual({ ok: false, error: "unsupported-version", version });
  });

  it.each([
    ["invalid JSON", "{"],
    ["null", null],
    ["an array", [sample]],
    ["an empty root", { v: SPLIT_TREE_FORMAT_VERSION, root: null }],
  ])("rejects %s as malformed", (_label, doc) => {
    expect(decodeSplitTree(doc)).toEqual({ ok: false, error: "malformed" });
  });

  it("repairs content of the known version and reports what changed", () => {
    const doc = { v: SPLIT_TREE_FORMAT_VERSION, root: split("s", "row", leaf("A", "x"), leaf("A", "y"), 0.99) };
    const result = decodeSplitTree(doc);
    expect(result).toMatchObject({ ok: true, tree: leaf("A", "x"), droppedPaneIds: [] });
    expect(result.ok && result.issues.length).toBeGreaterThan(0);
  });
});

describe("legacy workspace conversion", () => {
  it("converts a single pane to a leaf", () => {
    const result = legacyWorkspaceTree({ panes: [{ id: "p1" }], activePaneId: "p1", splitRatio: 0.5 }, ids());
    expect(result).toEqual({ tree: { type: "leaf", id: "n1", paneId: "p1" }, activePaneId: "p1", droppedPaneIds: [] });
  });

  it("converts a two-pane stack to a top/bottom split with the clamped ratio", () => {
    const result = legacyWorkspaceTree(
      { panes: [{ id: "top" }, { id: "bottom" }], activePaneId: "bottom", splitRatio: 0.95 },
      ids(),
    );
    expect(result.tree).toMatchObject({
      type: "split",
      dir: "col",
      ratio: 0.85,
      a: { paneId: "top" },
      b: { paneId: "bottom" },
    });
    expect(result.activePaneId).toBe("bottom");
    expect(isValidTree(result.tree)).toBe(true);
  });

  it("falls back to the first pane and the default ratio", () => {
    const result = legacyWorkspaceTree({ panes: [{ id: "top" }, { id: "bottom" }], activePaneId: "gone" }, ids());
    expect(result.activePaneId).toBe("top");
    expect(result.tree).toMatchObject({ ratio: 0.5 });
  });

  it("returns no tree for a workspace without panes", () => {
    expect(legacyWorkspaceTree({ panes: [], activePaneId: "x", splitRatio: 0.5 })).toEqual({
      tree: null,
      activePaneId: null,
      droppedPaneIds: [],
    });
  });

  it("nests extra panes, skips duplicates and reports panes beyond the depth limit", () => {
    const panes = [{ id: "p0" }, { id: "p0" }, { id: "" }];
    for (let index = 1; index <= SPLIT_TREE_MAX_DEPTH + 1; index += 1) panes.push({ id: `p${index}` });
    const result = legacyWorkspaceTree({ panes, splitRatio: 0.3 }, ids());
    expect(isValidTree(result.tree)).toBe(true);
    expect(result.tree && paneIdsInOrder(result.tree)).toHaveLength(SPLIT_TREE_MAX_DEPTH + 1);
    expect(result.droppedPaneIds).toEqual([`p${SPLIT_TREE_MAX_DEPTH + 1}`]);
    expect(result.tree).toMatchObject({ ratio: 0.3, b: { ratio: 0.5 } });
  });
});

describe("split tree merge", () => {
  const base = { tree: sample, updatedAt: 100 };
  const at = (tree: SplitNode, updatedAt: number) => ({ tree, updatedAt });

  it("keeps identical trees", () => {
    const result = mergeSplitTrees(base, at(sample, 200), at(sample, 150));
    expect(result).toEqual({ tree: sample, updatedAt: 200, structure: "same", unplacedPaneIds: [] });
  });

  it("takes the only side that changed structure and keeps the other side's ratio edits", () => {
    const local = at(setSplitRatio(sample, "right", 0.7), 300);
    const remote = at(splitPane(sample, "A", { dir: "col", side: "after", newPaneId: "D" }, ids("r")), 200);
    const result = mergeSplitTrees(base, local, remote);
    expect(result.structure).toBe("remote");
    expect(result.tree && paneIdsInOrder(result.tree)).toEqual(["A", "D", "B", "C"]);
    expect(result.tree).toMatchObject({ b: { id: "right", ratio: 0.7 } });

    const flipped = mergeSplitTrees(base, remote, local);
    expect(flipped.structure).toBe("local");
    expect(flipped.tree).toEqual(result.tree);
  });

  it.each([
    ["the newer local tree", 300, 200, "conflict-local", ["A", "D", "B", "C"]],
    ["the newer remote tree", 200, 300, "conflict-remote", ["A", "B"]],
  ] as const)("resolves a structural conflict with %s", (_label, localTime, remoteTime, structure, panes) => {
    const local = at(splitPane(sample, "A", { dir: "col", side: "after", newPaneId: "D" }, ids("l")), localTime);
    const remote = at(closePane(sample, "C") as SplitNode, remoteTime);
    const result = mergeSplitTrees(base, local, remote);
    expect(result.structure).toBe(structure);
    expect(result.tree && paneIdsInOrder(result.tree)).toEqual(panes);
  });

  it("breaks structural ties deterministically on every device", () => {
    const local = at(closePane(sample, "B") as SplitNode, 500);
    const remote = at(closePane(sample, "C") as SplitNode, 500);
    const one = mergeSplitTrees(base, local, remote);
    const other = mergeSplitTrees(base, remote, local);
    expect(one.tree && structureKey(one.tree)).toBe(other.tree && structureKey(other.tree));
    expect([one.structure, other.structure].sort()).toEqual(["conflict-local", "conflict-remote"]);
  });

  it("treats a missing base as a conflict and ignores invalid timestamps", () => {
    const local = at(closePane(sample, "B") as SplitNode, Number.NaN);
    const remote = at(closePane(sample, "C") as SplitNode, 10);
    const result = mergeSplitTrees(null, local, remote);
    expect(result.structure).toBe("conflict-remote");
    expect(result.updatedAt).toBe(10);
    expect(mergeSplitTrees(null, at(sample, Number.NaN), at(sample, Number.NaN)).updatedAt).toBe(0);
  });

  it.each([
    ["only local changed", 0.7, 0.4, 100, 900, 0.7],
    ["only remote changed", 0.4, 0.6, 900, 100, 0.6],
    ["both changed, local newer", 0.7, 0.6, 900, 100, 0.7],
    ["both changed, remote newer", 0.7, 0.6, 100, 900, 0.6],
    ["both changed at the same time", 0.7, 0.6, 500, 500, 0.6],
  ])("merges split ratios when %s", (_label, localRatio, remoteRatio, localTime, remoteTime, expected) => {
    const local = at(setSplitRatio(sample, "root", localRatio), localTime);
    const remote = at(setSplitRatio(sample, "root", remoteRatio), remoteTime);
    const result = mergeSplitTrees(base, local, remote);
    expect(result.structure).toMatch(/^same/);
    expect(result.tree).toMatchObject({ id: "root", ratio: expected });
  });

  it("matches splits by position when equal shapes carry different ids", () => {
    const local = at(split("l1", "row", leaf("A", "la"), leaf("B", "lb"), 0.3), 900);
    const remote = at(split("r1", "row", leaf("A", "ra"), leaf("B", "rb"), 0.6), 100);
    const result = mergeSplitTrees(null, local, remote);
    expect(result.tree).toMatchObject({ id: "l1", ratio: 0.3 });
  });

  it("does not mix ratios across splits whose direction changed", () => {
    const local = at(split("root", "col", leaf("A"), leaf("B"), 0.3), 900);
    const remote = at(split("root", "row", leaf("A"), leaf("B"), 0.7), 100);
    const result = mergeSplitTrees(null, local, remote);
    expect(result.tree).toEqual(local.tree);
  });

  it("reconciles the merged tree with the merged pane list", () => {
    const local = at(splitPane(sample, "A", { dir: "col", side: "after", newPaneId: "D" }, ids("l")), 300);
    const remote = at(splitPane(sample, "C", { dir: "row", side: "after", newPaneId: "E" }, ids("r")), 400);
    const result = mergeSplitTrees(base, local, remote, { paneIds: ["A", "B", "C", "D", "E"], newId: ids("m") });
    expect(result.structure).toBe("conflict-remote");
    expect(result.tree && [...paneIdsInOrder(result.tree)].sort()).toEqual(["A", "B", "C", "D", "E"]);
    expect(result.unplacedPaneIds).toEqual([]);
    expect(isValidTree(result.tree)).toBe(true);
    expect(mergeSplitTrees(base, local, remote, { paneIds: [] }).tree).toBeNull();
  });
});
