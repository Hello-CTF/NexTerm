/** @vitest-environment jsdom */
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  captureDeviceLayout,
  deviceLayoutStorageKey,
  hasDeviceLayoutChange,
  loadDeviceLayout,
  restoreDeviceLayout,
  saveDeviceLayout,
} from "../../app/layoutFocus";
import {
  hasPersistentLayoutChange,
  mergeLayout,
  sharedLayoutKey,
  type PersistedLayout,
} from "../../app/layoutMerge";
import type { AiBoard, AppTab, Pane, Workspace } from "../../app/store";
import { paneIdsInOrder, type SplitNode } from "../../app/splitTree";

function terminal(id: string, tabId = `kernel-${id}`): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "session", tabId, closable: true };
}

function pane(id: string, tabs: AppTab[], activeTabId = tabs[0]?.id ?? null): Pane {
  return { id, tabs, activeTabId };
}

function workspace(id: string, assetId: string, panes: Pane[]): Workspace {
  return {
    id,
    kind: "session",
    title: id,
    sessionId: `session-${id}`,
    assetId,
    panes,
    activePaneId: panes[0]?.id ?? id,
    closable: true,
  };
}

function layout(workspaces: Workspace[], aiBoards: Record<string, AiBoard> = {}): PersistedLayout {
  return {
    v: 4,
    leftOpen: true,
    leftMode: "assets",
    rightOpen: true,
    leftWidth: 248,
    rightWidth: 352,
    workspaces,
    activeWorkspaceId: workspaces[0]?.id ?? null,
    aiBoards,
  };
}

function board(...tabs: { id: string; conversationId?: string }[]): AiBoard {
  return {
    tabs: tabs.map((tab) => ({ ...tab, title: tab.id })),
    activeTabId: tabs[0]?.id ?? null,
  };
}

describe("three-way shared layout merge", () => {
  it("keeps concurrent additions and converges across both client perspectives", () => {
    const local = layout([workspace("ws-a", "asset", [pane("pane-a", [terminal("tab-a")])])]);
    const remote = layout([workspace("ws-b", "asset", [pane("pane-b", [terminal("tab-b")])])]);
    local.aiBoards = { "ws-a": board({ id: "ai-a", conversationId: "conversation-a" }) };
    remote.aiBoards = { "ws-b": board({ id: "ai-b", conversationId: "conversation-b" }) };

    const fromA = mergeLayout(null, local, remote);
    const fromB = mergeLayout(null, remote, local);

    expect(fromA.workspaces).toHaveLength(1);
    expect(fromA.workspaces[0].panes).toHaveLength(1);
    expect(fromA.workspaces[0].panes[0].tabs.map((tab) => tab.id)).toEqual(["tab-a", "tab-b"]);
    expect(Object.keys(fromA.aiBoards)).toEqual(["ws-a"]);
    expect(fromA.aiBoards["ws-a"].tabs.map((tab) => tab.id)).toEqual(["ai-a", "ai-b"]);
    expect(sharedLayoutKey(fromA)).toBe(sharedLayoutKey(fromB));
  });

  it("keeps both panes of concurrent splits and converges from both perspectives", () => {
    const base = layout([workspace("ws", "asset", [pane("pane-0", [terminal("base")])])]);
    const local = layout([
      workspace("ws", "asset", [pane("pane-0", [terminal("base")]), pane("pane-a", [terminal("tab-a")])]),
    ]);
    const remote = layout([
      workspace("ws", "asset", [pane("pane-0", [terminal("base")]), pane("pane-b", [terminal("tab-b")])]),
    ]);

    const merged = mergeLayout(base, local, remote);
    const mirrored = mergeLayout(base, remote, local);
    expect(merged.workspaces[0].panes.map((item) => item.id).sort()).toEqual(["pane-0", "pane-a", "pane-b"]);
    expect(merged.workspaces[0].panes.flatMap((item) => item.tabs.map((tab) => tab.id)).sort()).toEqual(["base", "tab-a", "tab-b"]);
    expect(paneIdsInOrder(merged.workspaces[0].layoutTree!).sort()).toEqual(["pane-0", "pane-a", "pane-b"]);
    expect(sharedLayoutKey(merged)).toBe(sharedLayoutKey(mirrored));

    // Workspaces without a stored tree keep every pane; nothing is capped any more.
    const legacy = layout([
      workspace("ws", "asset", [
        pane("p1", [terminal("t1")]),
        pane("p2", [terminal("t2")]),
        pane("p3", [terminal("t3")]),
      ]),
    ]);
    const kept = mergeLayout(null, legacy, legacy);
    expect(kept.workspaces[0].panes.map((item) => item.id)).toEqual(["p1", "p2", "p3"]);
    expect(paneIdsInOrder(kept.workspaces[0].layoutTree!)).toEqual(["p1", "p2", "p3"]);
  });

  it("lets close win over concurrent edits in either direction", () => {
    const base = layout([workspace("ws", "asset", [pane("pane", [terminal("keep"), terminal("gone")])])]);
    const closed = layout([workspace("ws", "asset", [pane("pane", [terminal("keep")])])]);
    const edited = layout([
      workspace("ws", "asset", [
        pane("pane", [terminal("keep"), { ...terminal("gone"), title: "edited" }]),
      ]),
    ]);

    expect(mergeLayout(base, closed, edited).workspaces[0].panes[0].tabs.map((tab) => tab.id)).toEqual(["keep"]);
    expect(mergeLayout(base, edited, closed).workspaces[0].panes[0].tabs.map((tab) => tab.id)).toEqual(["keep"]);
  });

  it("preserves only protected local tabs on remote deletion and keeps SQL draft workspaces", () => {
    const base = layout([
      workspace("ws", "asset", [pane("pane", [terminal("clean"), terminal("dirty")])]),
    ]);
    const local = layout([
      workspace("ws", "asset", [pane("pane", [terminal("clean"), terminal("dirty")])]),
    ]);
    const remote = layout([]);

    const dirtyOnly = mergeLayout(base, local, remote, {
      preserveLocalTab: (tab) => tab.id === "dirty",
    });
    expect(dirtyOnly.workspaces).toHaveLength(1);
    expect(dirtyOnly.workspaces[0].panes[0].tabs.map((tab) => tab.id)).toEqual(["dirty"]);

    const sqlDraft = mergeLayout(base, local, remote, { preserveLocalWorkspace: () => true });
    expect(sqlDraft.workspaces[0].panes[0].tabs.map((tab) => tab.id)).toEqual(["clean", "dirty"]);
  });

  it("uses a surviving local reorder and deterministic order for concurrent additions", () => {
    const base = layout([workspace("ws", "asset", [pane("pane", [terminal("a"), terminal("b")])])]);
    const reordered = layout([workspace("ws", "asset", [pane("pane", [terminal("b"), terminal("a")])])]);
    expect(mergeLayout(base, reordered, base).workspaces[0].panes[0].tabs.map((tab) => tab.id)).toEqual(["b", "a"]);

    const local = layout([workspace("ws", "asset", [pane("pane", [terminal("a"), terminal("b"), terminal("z")])])]);
    const remote = layout([workspace("ws", "asset", [pane("pane", [terminal("c"), terminal("a"), terminal("b")])])]);
    const fromA = mergeLayout(base, local, remote);
    const fromB = mergeLayout(base, remote, local);
    expect(fromA.workspaces[0].panes[0].tabs.map((tab) => tab.id)).toEqual(["a", "b", "c", "z"]);
    expect(sharedLayoutKey(fromA)).toBe(sharedLayoutKey(fromB));
  });

  it("ignores every device-local field in the shared signature", () => {
    const local = layout([workspace("ws", "asset", [pane("pane", [terminal("a"), terminal("b")], "a")])]);
    local.aiBoards = { ws: board({ id: "ai-a" }, { id: "ai-b" }) };
    const changed = structuredClone(local);
    changed.leftOpen = false;
    changed.leftMode = "files";
    changed.rightOpen = false;
    changed.leftWidth = 500;
    changed.rightWidth = 700;
    changed.activeWorkspaceId = null;
    changed.workspaces[0].activePaneId = "missing";
    changed.workspaces[0].panes[0].activeTabId = "b";
    changed.aiBoards.ws.activeTabId = "ai-b";
    expect(sharedLayoutKey(changed)).toBe(sharedLayoutKey(local));
  });
});

describe("persistent reference filtering", () => {
  it("ignores focus, AI drafts, and unrelated runtime but detects shared edits", () => {
    const previous = layout([workspace("ws", "asset", [pane("pane", [terminal("a")])])]);
    previous.aiBoards = { ws: board({ id: "ai-a" }) };
    const runtime = structuredClone(previous);
    runtime.workspaces[0].panes[0].activeTabId = null;
    runtime.aiBoards.ws.activeTabId = null;
    runtime.aiBoards.ws.tabs[0].draft = "local draft";
    expect(hasPersistentLayoutChange(previous, runtime)).toBe(false);
    expect(hasDeviceLayoutChange(previous, runtime)).toBe(true);

    const edited = structuredClone(runtime);
    edited.workspaces[0].panes[0].tabs[0].title = "renamed";
    expect(hasPersistentLayoutChange(runtime, edited)).toBe(true);
    expect(hasPersistentLayoutChange(previous, { ...previous, workspaces: previous.workspaces })).toBe(false);
  });
});

describe("split tree merge", () => {
  const leaf = (paneId: string): SplitNode => ({ type: "leaf", id: `leaf-${paneId}`, paneId });
  const row = (id: string, a: SplitNode, b: SplitNode, ratio = 0.5): SplitNode => ({ type: "split", id, dir: "row", ratio, a, b });

  function timed(panes: Pane[], layoutTree: SplitNode, layoutUpdatedAt: number): Workspace {
    return { ...workspace("ws", "asset", panes), layoutTree, layoutUpdatedAt };
  }

  it("lets the newer reshape win, keeps the other side's ratio edit and stays symmetric", () => {
    const panes = [pane("p1", [terminal("t1")]), pane("p2", [terminal("t2")])];
    const base = layout([timed(panes, row("s", leaf("p1"), leaf("p2")), 10)]);
    // Local only drags the divider; remote swaps the panes later.
    const local = layout([timed(panes, row("s", leaf("p1"), leaf("p2"), 0.3), 20)]);
    const remote = layout([timed(panes, row("s", leaf("p2"), leaf("p1")), 30)]);

    const merged = mergeLayout(base, local, remote);
    const mirrored = mergeLayout(base, remote, local);
    const tree = merged.workspaces[0].layoutTree!;
    expect(paneIdsInOrder(tree)).toEqual(["p2", "p1"]);
    expect(merged.workspaces[0].layoutUpdatedAt).toBe(30);
    expect(mirrored.workspaces[0].layoutTree).toEqual(tree);
    expect(sharedLayoutKey(merged)).toBe(sharedLayoutKey(mirrored));
  });

  it("collapses a pane closed on one side and places a pane added on the other", () => {
    const base = layout([timed([pane("p1", [terminal("t1")]), pane("p2", [terminal("t2")])], row("s", leaf("p1"), leaf("p2")), 10)]);
    const local = layout([timed([pane("p1", [terminal("t1")])], leaf("p1"), 20)]);
    const remote = layout([
      timed(
        [pane("p1", [terminal("t1")]), pane("p2", [terminal("t2")]), pane("p3", [terminal("t3")])],
        row("s", leaf("p1"), row("s2", leaf("p2"), leaf("p3"))),
        15,
      ),
    ]);
    const merged = mergeLayout(base, local, remote).workspaces[0];
    expect(merged.panes.map((item) => item.id).sort()).toEqual(["p1", "p3"]);
    expect(paneIdsInOrder(merged.layoutTree!).sort()).toEqual(["p1", "p3"]);
  });

  it("hands the tabs of a pane the tree cannot place to the active pane", () => {
    // A full tree at the depth limit has no leaf left to split.
    let next = 0;
    const build = (depth: number): SplitNode =>
      depth === 0 ? leaf(`p${next++}`) : row(`s${depth}-${next}`, build(depth - 1), build(depth - 1));
    const full = build(8);
    const placed = paneIdsInOrder(full).map((id) => pane(id, []));
    placed[0] = pane("p0", [terminal("first")]);
    const extra = pane("extra", [terminal("orphan")]);
    const ws = { ...timed([...placed, extra], full, 1), activePaneId: "p0" };
    const merged = mergeLayout(null, layout([ws]), layout([ws])).workspaces[0];
    expect(merged.panes.some((item) => item.id === "extra")).toBe(false);
    expect(merged.panes.find((item) => item.id === "p0")?.tabs.map((tab) => tab.id)).toEqual(["first", "orphan"]);
  });
});

describe("device layout focus", () => {
  afterEach(() => vi.restoreAllMocks());

  it("restores focus by logical identity and chooses a local survivor when it disappears", () => {
    const local = layout([
      workspace("ws-a", "asset-a", [pane("pane-a", [terminal("a"), terminal("b")], "b")]),
      workspace("ws-c", "asset-c", [pane("pane-c", [terminal("c")])]),
    ]);
    local.activeWorkspaceId = "ws-c";
    const device = captureDeviceLayout(local);
    device.activeWorkspace = { id: "deleted", key: "session:asset:deleted" };
    const remote = layout([
      workspace("ws-b", "asset-a", [pane("pane-b", [terminal("a"), terminal("b")], "a")]),
      workspace("ws-d", "asset-d", [pane("pane-d", [terminal("d")])]),
    ]);
    remote.activeWorkspaceId = "ws-d";
    remote.leftOpen = false;
    remote.leftWidth = 500;

    const restored = restoreDeviceLayout(remote, device);
    expect(restored.activeWorkspaceId).toBe("ws-b");
    expect(restored.workspaces[0].panes[0].activeTabId).toBe("b");
    expect(restored.leftOpen).toBe(true);
    expect(restored.leftWidth).toBe(248);
  });

  it("uses remote focus only when no device focus exists", () => {
    const remote = layout([
      workspace("ws-a", "asset-a", [pane("pane-a", [terminal("a")])]),
      workspace("ws-b", "asset-b", [pane("pane-b", [terminal("b")])]),
    ]);
    remote.activeWorkspaceId = "ws-b";
    expect(restoreDeviceLayout(remote, null).activeWorkspaceId).toBe("ws-b");
  });

  it("fails closed when localStorage is unavailable and namespaces accounts", () => {
    const state = captureDeviceLayout(layout([]));
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    expect(loadDeviceLayout("key")).toBeNull();
    expect(() => saveDeviceLayout("key", state)).not.toThrow();
    expect(deviceLayoutStorageKey("user/a")).not.toBe(deviceLayoutStorageKey("user/b"));
    expect(deviceLayoutStorageKey("user/a")).toContain("user%2Fa");
  });
});
