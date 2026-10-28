/** @vitest-environment jsdom */

import { describe, expect, it, vi } from "vitest";

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {});
});

import { newerLayoutVersion, sanitizeLayout, serializeLayout } from "../../app/layout";
import { paneIdsInOrder, type SplitNode } from "../../app/splitTree";
import type { AppTab, Pane, Workspace } from "../../app/store";

function terminal(id: string): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "s", tabId: `kernel-${id}`, closable: true };
}

function pane(id: string, tabs: AppTab[] = [terminal(`${id}-tab`)]): Pane {
  return { id, tabs, activeTabId: tabs[0]?.id ?? null };
}

const leaf = (paneId: string): SplitNode => ({ type: "leaf", id: `leaf-${paneId}`, paneId });

// [ a | [ b / [ c | d ] ] ]: three levels of nesting.
const nestedTree: SplitNode = {
  type: "split",
  id: "root",
  dir: "row",
  ratio: 0.3,
  a: leaf("a"),
  b: {
    type: "split",
    id: "right",
    dir: "col",
    ratio: 0.6,
    a: leaf("b"),
    b: { type: "split", id: "corner", dir: "row", ratio: 0.45, a: leaf("c"), b: leaf("d") },
  },
};

function workspace(patch: Partial<Workspace> = {}): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "prod",
    sessionId: "s",
    assetId: "asset",
    panes: [pane("a"), pane("b"), pane("c"), pane("d")],
    activePaneId: "c",
    layoutTree: nestedTree,
    layoutUpdatedAt: 1700,
    closable: true,
    ...patch,
  };
}

function source(workspaces: Workspace[]) {
  return {
    leftOpen: true,
    leftMode: "assets" as const,
    rightOpen: true,
    leftWidth: 248,
    rightWidth: 352,
    workspaces,
    activeWorkspaceId: workspaces[0]?.id ?? null,
    aiBoards: {},
  };
}

function roundTrip(workspaces: Workspace[]) {
  const serialized = serializeLayout(source(workspaces));
  return sanitizeLayout(JSON.parse(JSON.stringify(serialized)));
}

describe("layout document v4", () => {
  it("round-trips a nested split tree with its ratios, panes and layout time", () => {
    const restored = roundTrip([workspace()]);
    expect(restored?.v).toBe(4);
    const ws = restored!.workspaces[0];
    expect(ws.layoutTree).toEqual(nestedTree);
    expect(ws.layoutUpdatedAt).toBe(1700);
    expect(ws.activePaneId).toBe("c");
    expect(ws.panes.map((item) => item.id)).toEqual(["a", "b", "c", "d"]);
  });

  it("stores a tree for workspaces that never split", () => {
    const restored = roundTrip([workspace({ layoutTree: undefined, layoutUpdatedAt: undefined, panes: [pane("a")], activePaneId: "a" })]);
    expect(restored!.workspaces[0].layoutTree).toEqual({ type: "leaf", id: expect.any(String), paneId: "a" });
    expect(restored!.workspaces[0].layoutUpdatedAt).toBe(0);
  });

  it("keeps an empty pane the tree places and drops empty panes it does not", () => {
    const tree: SplitNode = { type: "split", id: "s", dir: "row", ratio: 0.5, a: leaf("a"), b: leaf("waiting") };
    const raw = JSON.parse(JSON.stringify(serializeLayout(source([
      workspace({ panes: [pane("a"), pane("waiting", [])], layoutTree: tree, activePaneId: "a" }),
    ]))));
    // Another writer listed an empty pane its tree does not place.
    raw.workspaces[0].panes.push({ id: "stray", tabs: [], activeTabId: null });
    const ws = sanitizeLayout(raw)!.workspaces[0];
    expect(ws.panes.map((item) => item.id)).toEqual(["a", "waiting"]);
    expect(paneIdsInOrder(ws.layoutTree!)).toEqual(["a", "waiting"]);
  });

  it("repairs a damaged tree and places panes it is missing", () => {
    const raw = JSON.parse(JSON.stringify(serializeLayout(source([workspace()]))));
    raw.workspaces[0].layoutTree = { type: "split", id: "root", dir: "diagonal", ratio: 7, a: leaf("a"), b: { type: "bogus" }, extra: 1 };
    const ws = sanitizeLayout(raw)!.workspaces[0];
    expect(paneIdsInOrder(ws.layoutTree!).sort()).toEqual(["a", "b", "c", "d"]);
    expect(ws.panes).toHaveLength(4);

    raw.workspaces[0].layoutTree = "not a tree";
    expect(paneIdsInOrder(sanitizeLayout(raw)!.workspaces[0].layoutTree!)).toEqual(["a", "b", "c", "d"]);
  });

  it("rejects versions it does not know instead of guessing", () => {
    const raw = JSON.parse(JSON.stringify(serializeLayout(source([workspace()]))));
    for (const v of [1, 5, "4", undefined, null]) {
      expect(sanitizeLayout({ ...raw, v })).toBeNull();
    }
    expect(newerLayoutVersion({ ...raw, v: 5 })).toBe(5);
    expect(newerLayoutVersion(raw)).toBeNull();
    expect(newerLayoutVersion({ ...raw, v: 3 })).toBeNull();
    expect(newerLayoutVersion({ ...raw, v: "9" })).toBeNull();
    expect(newerLayoutVersion(null)).toBeNull();
  });

  it("rebuilds a pre-tree document as one pane per workspace that keeps every tab", () => {
    const legacy = {
      ...source([
        { ...workspace(), layoutTree: undefined, panes: [pane("upper", [terminal("t1"), terminal("t2")]), pane("lower", [terminal("t3"), terminal("t1")])], activePaneId: "lower" },
      ]),
      v: 3,
    };
    const ws = sanitizeLayout(JSON.parse(JSON.stringify({ ...legacy, workspaces: legacy.workspaces.map((item) => ({ ...item, splitRatio: 0.3 })) })))!.workspaces[0];
    expect(ws.panes).toHaveLength(1);
    expect(ws.panes[0].id).toBe("upper");
    expect(ws.panes[0].tabs.map((tab) => tab.id)).toEqual(["t1", "t2", "t3"]);
    expect(ws.panes[0].activeTabId).toBe("t3");
    expect(ws.layoutTree).toMatchObject({ type: "leaf", paneId: "upper" });
    expect(ws.layoutUpdatedAt).toBe(0);
    expect("splitRatio" in ws).toBe(false);
  });
});
