/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoice: vi.fn(),
  closeTab: vi.fn(),
  prepareDurable: vi.fn(async () => ({ state: "ready" })),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, askChoice: mocks.askChoice }));
vi.mock("../../ipc/commands", () => ({
  dbApi: {},
  sessionApi: { prepareDurable: mocks.prepareDurable },
  vaultApi: {},
  terminalApi: { closeTab: mocks.closeTab },
}));

import type { AppTab, Pane, Workspace } from "../../app/store";
import { useUi } from "../../app/store";
import { syncWorkspaceTree, withLayoutTree, workspaceLayoutTree, workspacePaneOrder } from "../../app/paneLayout";
import { findPanePath, paneRects, SPLIT_TREE_MAX_DEPTH, type SplitNode } from "../../app/splitTree";

const innerHeight = Object.getOwnPropertyDescriptor(window, "innerHeight");
const innerWidth = Object.getOwnPropertyDescriptor(window, "innerWidth");

function setHeight(value: number): void {
  Object.defineProperty(window, "innerHeight", { configurable: true, writable: true, value });
}

function setWidth(value: number): void {
  Object.defineProperty(window, "innerWidth", { configurable: true, writable: true, value });
}

function file(id: string): AppTab {
  return { id, kind: "files", title: id, sessionId: "s", path: `/${id}`, closable: true };
}

function pane(id: string, tabs: AppTab[] = [file(`${id}-tab`)]): Pane {
  return { id, tabs, activeTabId: tabs[0]?.id ?? null };
}

function ws(panes: Pane[], patch: Partial<Workspace> = {}): Workspace {
  return { id: "ws", kind: "session", title: "ws", sessionId: "s", panes, activePaneId: panes[0].id, closable: true, ...patch };
}

function current(): Workspace {
  return useUi.getState().workspaces[0];
}

function tree(): SplitNode {
  return workspaceLayoutTree(current())!;
}

function rectOf(paneId: string) {
  return paneRects(tree()).find((entry) => entry.paneId === paneId)?.rect;
}

beforeEach(() => {
  vi.clearAllMocks();
  setHeight(900);
  useUi.setState({ workspaces: [ws([pane("p1")])], activeWorkspaceId: "ws", toasts: [], sessions: [] });
});

afterEach(() => {
  if (innerHeight) Object.defineProperty(window, "innerHeight", innerHeight);
  if (innerWidth) Object.defineProperty(window, "innerWidth", innerWidth);
  document.body.replaceChildren();
});

describe("split pane actions", () => {
  it("splits right and down, focusing the new pane and stamping the layout time", () => {
    vi.spyOn(Date, "now").mockReturnValue(1234);
    const right = useUi.getState().splitPane("right")!;
    expect(current().activePaneId).toBe(right);
    expect(current().layoutUpdatedAt).toBe(1234);
    expect(rectOf("p1")).toEqual({ x: 0, y: 0, width: 0.5, height: 1 });
    expect(rectOf(right)).toEqual({ x: 0.5, y: 0, width: 0.5, height: 1 });

    const down = useUi.getState().splitPane("down", right, "ws")!;
    expect(rectOf(right)).toEqual({ x: 0.5, y: 0, width: 0.5, height: 0.5 });
    expect(rectOf(down)).toEqual({ x: 0.5, y: 0.5, width: 0.5, height: 0.5 });
    expect(workspacePaneOrder(current())).toEqual(["p1", right, down]);
    vi.restoreAllMocks();
  });

  it("refuses a downward split in a short window and past the depth limit, with a notice", () => {
    setHeight(320);
    expect(useUi.getState().splitPane("down")).toBeNull();
    expect(current().panes).toHaveLength(1);
    expect(useUi.getState().toasts.at(-1)?.text).toContain("窗口高度不足");

    setWidth(1_000_000);
    for (let level = 0; level < SPLIT_TREE_MAX_DEPTH; level += 1) {
      expect(useUi.getState().splitPane("right")).not.toBeNull();
    }
    const before = current();
    expect(useUi.getState().splitPane("right")).toBeNull();
    expect(current()).toBe(before);
    expect(useUi.getState().toasts.at(-1)?.text).toContain(`${SPLIT_TREE_MAX_DEPTH} 层`);
  });

  it("refuses a split that would leave either half too small, measuring the pane on screen", () => {
    setWidth(600);
    const right = useUi.getState().splitPane("right")!;
    expect(right).not.toBeNull();
    // Each half of the 300px-wide new pane would be narrower than 20 columns.
    expect(useUi.getState().splitPane("right", right)).toBeNull();
    expect(useUi.getState().toasts.at(-1)?.text).toContain("窗格太小，无法向右分屏");

    // A laid-out pane is measured directly: 300px tall halves to less than 8 rows below a tab strip.
    const element = document.createElement("div");
    element.dataset.paneId = "p1";
    element.getBoundingClientRect = () => ({ width: 600, height: 300 }) as DOMRect;
    document.body.append(element);
    expect(useUi.getState().splitPane("down", "p1")).toBeNull();
    expect(useUi.getState().toasts.at(-1)?.text).toContain("窗格太小，无法向下分屏");
    element.getBoundingClientRect = () => ({ width: 600, height: 500 }) as DOMRect;
    expect(useUi.getState().splitPane("down", "p1")).not.toBeNull();
  });

  it("ignores unknown workspaces and panes", () => {
    expect(useUi.getState().splitPane("right", undefined, "missing")).toBeNull();
    expect(useUi.getState().splitPane("right", "missing")).toBeNull();
    expect(current().panes).toHaveLength(1);
  });

  it("opens a terminal in the new pane of a connected host workspace", async () => {
    useUi.setState({
      workspaces: [ws([pane("p1")])],
      sessions: [{ id: "s", name: "host", kind: "ssh", assetId: null } as never],
    });
    const right = useUi.getState().splitPane("right")!;
    await vi.waitFor(() => {
      const added = current().panes.find((item) => item.id === right)!;
      expect(added.tabs.map((tab) => tab.kind)).toEqual(["terminal"]);
    });
    expect(mocks.prepareDurable).toHaveBeenCalledWith("s");
  });

  it("closes a pane into the pane taking its space, keeping every tab in order", () => {
    const right = useUi.getState().splitPane("right")!;
    useUi.getState().addTab(file("r1"), right);
    const down = useUi.getState().splitPane("down", right)!;
    useUi.getState().addTab(file("d1"), down);
    useUi.getState().addTab(file("d2"), down);
    useUi.getState().setActiveTab("d1");

    useUi.getState().closePane(down, "ws");
    expect(current().panes.map((item) => item.id)).toEqual(["p1", right]);
    const heir = current().panes.find((item) => item.id === right)!;
    expect(heir.tabs.map((tab) => tab.id)).toEqual(["r1", "d1", "d2"]);
    expect(heir.activeTabId).toBe("d1");
    expect(current().activePaneId).toBe(right);
    expect(rectOf(right)).toEqual({ x: 0.5, y: 0, width: 0.5, height: 1 });

    // Closing the left pane hands its tabs to the right one.
    useUi.getState().closePane("p1", "ws");
    expect(current().panes.map((item) => item.id)).toEqual([right]);
    expect(current().panes[0].tabs.map((tab) => tab.id)).toEqual(["r1", "d1", "d2", "p1-tab"]);
    expect(current().panes[0].activeTabId).toBe("p1-tab");
    expect(tree()).toMatchObject({ type: "leaf", paneId: right });
    // The last pane stays.
    const before = useUi.getState().workspaces;
    useUi.getState().closePane(right, "ws");
    expect(useUi.getState().workspaces).toBe(before);
  });

  it("keeps focus on another active pane, merges an empty pane away and ignores unknown panes", () => {
    const right = useUi.getState().splitPane("right")!;
    useUi.getState().setActivePane("p1", "ws");
    const before = useUi.getState().workspaces;
    useUi.getState().closePane("missing", "ws");
    expect(useUi.getState().workspaces).toBe(before);
    useUi.getState().closePane(right, "ws");
    expect(current().activePaneId).toBe("p1");
    expect(current().panes.map((item) => item.id)).toEqual(["p1"]);
    expect(current().panes[0].tabs.map((tab) => tab.id)).toEqual(["p1-tab"]);
    expect(current().panes[0].activeTabId).toBe("p1-tab");
  });

  it("sets one split's ratio, clamped, and leaves the layout alone for unknown splits", () => {
    const right = useUi.getState().splitPane("right")!;
    useUi.getState().splitPane("down", right);
    const outer = tree();
    if (outer.type !== "split" || outer.b.type !== "split") throw new Error("expected a nested split");
    useUi.getState().setSplitRatio(outer.b.id, 0.99);
    const next = tree();
    expect(next.type === "split" && next.ratio).toBe(0.5);
    expect(next.type === "split" && next.b.type === "split" && next.b.ratio).toBe(0.85);

    const before = useUi.getState().workspaces;
    useUi.getState().setSplitRatio("missing", 0.3);
    useUi.getState().setSplitRatio(outer.b.id, 0.85);
    expect(useUi.getState().workspaces).toBe(before);
  });

  it("moves focus by geometry", () => {
    const right = useUi.getState().splitPane("right")!;
    const down = useUi.getState().splitPane("down", right)!;
    useUi.getState().setActivePane("p1", "ws");
    expect(useUi.getState().focusPane("left")).toBeNull();
    expect(useUi.getState().focusPane("right")).toBe(right);
    expect(useUi.getState().focusPane("down")).toBe(down);
    expect(useUi.getState().focusPane("left")).toBe("p1");
    expect(useUi.getState().focusPane("up", "missing")).toBeNull();
  });

  it("moves a tab to another pane and closes the pane it leaves empty", () => {
    const right = useUi.getState().splitPane("right")!;
    useUi.getState().addTab(file("moving"), right);
    useUi.getState().moveTabToPane("p1-tab", right);
    expect(current().panes.map((item) => item.id)).toEqual([right]);
    expect(current().panes[0].tabs.map((tab) => tab.id)).toEqual(["moving", "p1-tab"]);
    expect(current().panes[0].activeTabId).toBe("p1-tab");
    expect(current().activePaneId).toBe(right);
    expect(findPanePath(tree(), "p1")).toBeNull();
  });

  it("keeps the source pane when it still has tabs and ignores no-op moves", () => {
    useUi.setState({ workspaces: [ws([pane("p1", [file("a"), file("b")])])] });
    const right = useUi.getState().splitPane("right")!;
    useUi.getState().moveTabToPane("a", right);
    expect(current().panes.find((item) => item.id === "p1")?.tabs.map((tab) => tab.id)).toEqual(["b"]);
    const before = useUi.getState().workspaces;
    useUi.getState().moveTabToPane("b", "p1");
    useUi.getState().moveTabToPane("b", "missing");
    useUi.getState().moveTabToPane("missing", right);
    expect(useUi.getState().workspaces).toBe(before);
  });

  it("collapses the split when a pane's last tab closes", async () => {
    const right = useUi.getState().splitPane("right")!;
    useUi.getState().addTab(file("only"), right);
    await useUi.getState().closeTab("only");
    expect(current().panes.map((item) => item.id)).toEqual(["p1"]);
    expect(current().activePaneId).toBe("p1");
    expect(current().layoutTree).toMatchObject({ type: "leaf", paneId: "p1" });
  });

  it("opens a file in the pane below, else beside, else in a new lower pane", () => {
    useUi.getState().openInLowerPane(file("first"), "ws");
    const lower = current().activePaneId;
    expect(rectOf(lower)).toEqual({ x: 0, y: 0.5, width: 1, height: 0.5 });
    useUi.getState().setActivePane("p1", "ws");
    useUi.getState().openInLowerPane(file("second"), "ws");
    expect(current().panes.find((item) => item.id === lower)?.tabs.map((tab) => tab.id)).toEqual(["first", "second"]);
  });
});

describe("pane layout helpers", () => {
  it("reads a workspace without a stored tree as panes stacked top to bottom", () => {
    const stacked = ws([pane("a"), pane("b"), pane("c")]);
    expect(workspacePaneOrder(stacked)).toEqual(["a", "b", "c"]);
    expect(paneRects(workspaceLayoutTree(stacked)!).map((entry) => entry.rect.x)).toEqual([0, 0, 0]);
    expect(workspaceLayoutTree(ws([pane("a")], { panes: [] }))).toBeNull();
  });

  it("re-syncs a stored tree after panes change outside the split actions", () => {
    const split = withLayoutTree(ws([pane("a"), pane("b")]), {
      type: "split",
      id: "s",
      dir: "row",
      ratio: 0.4,
      a: { type: "leaf", id: "la", paneId: "a" },
      b: { type: "leaf", id: "lb", paneId: "b" },
    }, 10);
    expect(syncWorkspaceTree(split)).toBe(split);
    const withoutB = syncWorkspaceTree({ ...split, panes: [pane("a")] });
    expect(withoutB.layoutTree).toMatchObject({ type: "leaf", paneId: "a" });
    expect(withoutB.layoutUpdatedAt).toBeGreaterThan(10);
    const plain = ws([pane("a")]);
    expect(syncWorkspaceTree(plain)).toBe(plain);
  });
});

describe("split a pane with a dragged tab", () => {
  it("splits the target on each edge and moves the tab into the new pane in one step", () => {
    useUi.setState({ workspaces: [ws([pane("p1", [file("a"), file("b"), file("c")])])] });
    const states: Workspace[] = [];
    const stop = useUi.subscribe((state) => states.push(state.workspaces[0]));
    const left = useUi.getState().splitPaneWithTab("a", "p1", "left", "ws")!;
    stop();
    // One store update: no intermediate state with an empty new pane.
    expect(states).toHaveLength(1);
    expect(rectOf(left)).toEqual({ x: 0, y: 0, width: 0.5, height: 1 });
    expect(rectOf("p1")).toEqual({ x: 0.5, y: 0, width: 0.5, height: 1 });
    const created = current().panes.find((item) => item.id === left)!;
    expect(created.tabs.map((tab) => tab.id)).toEqual(["a"]);
    expect(created.activeTabId).toBe("a");
    expect(current().activePaneId).toBe(left);

    const top = useUi.getState().splitPaneWithTab("b", "p1", "top")!;
    expect(rectOf(top)).toEqual({ x: 0.5, y: 0, width: 0.5, height: 0.5 });
    expect(rectOf("p1")).toEqual({ x: 0.5, y: 0.5, width: 0.5, height: 0.5 });
    expect(current().panes.find((item) => item.id === "p1")!.tabs.map((tab) => tab.id)).toEqual(["c"]);
    expect(current().panes.find((item) => item.id === "p1")!.activeTabId).toBe("c");
  });

  it("closes the source pane when its only tab moves to another pane's edge", () => {
    const right = useUi.getState().splitPane("right")!;
    useUi.getState().addTab(file("r"), right);
    const below = useUi.getState().splitPaneWithTab("p1-tab", right, "bottom", "ws")!;
    expect(current().panes.map((item) => item.id).sort()).toEqual([right, below].sort());
    expect(findPanePath(tree(), "p1")).toBeNull();
    expect(rectOf(right)).toEqual({ x: 0, y: 0, width: 1, height: 0.5 });
    expect(rectOf(below)).toEqual({ x: 0, y: 0.5, width: 1, height: 0.5 });
  });

  it("refuses to split a pane off its only tab, a too-small pane, a short window and past the depth limit", () => {
    const before = useUi.getState().workspaces;
    expect(useUi.getState().splitPaneWithTab("p1-tab", "p1", "right")).toBeNull();
    expect(useUi.getState().toasts.at(-1)?.text).toContain("只有一个标签页");
    expect(useUi.getState().workspaces).toBe(before);

    useUi.setState({ workspaces: [ws([pane("p1", [file("a"), file("b")])])] });
    const element = document.createElement("div");
    element.dataset.paneId = "p1";
    element.getBoundingClientRect = () => ({ width: 300, height: 300 }) as DOMRect;
    document.body.append(element);
    const small = useUi.getState().workspaces;
    expect(useUi.getState().splitPaneWithTab("a", "p1", "left")).toBeNull();
    expect(useUi.getState().toasts.at(-1)?.text).toContain("窗格太小，无法向左分屏");
    expect(useUi.getState().splitPaneWithTab("a", "p1", "top")).toBeNull();
    expect(useUi.getState().toasts.at(-1)?.text).toContain("窗格太小，无法向上分屏");
    expect(useUi.getState().workspaces).toBe(small);
    element.remove();

    setHeight(320);
    expect(useUi.getState().splitPaneWithTab("a", "p1", "bottom")).toBeNull();
    expect(useUi.getState().toasts.at(-1)?.text).toContain("窗口高度不足，无法向下分屏");
    setHeight(900);

    setWidth(1_000_000);
    let target = "p1";
    for (let level = 0; level < SPLIT_TREE_MAX_DEPTH; level += 1) target = useUi.getState().splitPane("right", target)!;
    useUi.getState().addTab(file("deep-a"), target);
    useUi.getState().addTab(file("deep-b"), target);
    const deep = useUi.getState().workspaces;
    expect(useUi.getState().splitPaneWithTab("deep-a", target, "right")).toBeNull();
    expect(useUi.getState().toasts.at(-1)?.text).toContain(`${SPLIT_TREE_MAX_DEPTH} 层`);
    expect(useUi.getState().workspaces).toBe(deep);
  });

  it("ignores unknown tabs, panes and workspaces", () => {
    useUi.setState({ workspaces: [ws([pane("p1", [file("a"), file("b")])])] });
    const before = useUi.getState().workspaces;
    expect(useUi.getState().splitPaneWithTab("missing", "p1", "right")).toBeNull();
    expect(useUi.getState().splitPaneWithTab("a", "missing", "right")).toBeNull();
    expect(useUi.getState().splitPaneWithTab("a", "p1", "right", "other")).toBeNull();
    expect(useUi.getState().workspaces).toBe(before);
  });

  it("inserts a moved tab at the requested index, clamped to the pane", () => {
    useUi.setState({ workspaces: [ws([pane("p1", [file("a"), file("b"), file("c")])])] });
    const right = useUi.getState().splitPaneWithTab("a", "p1", "right")!;
    useUi.getState().moveTabToPane("b", right, 0);
    expect(current().panes.find((item) => item.id === right)!.tabs.map((tab) => tab.id)).toEqual(["b", "a"]);
    useUi.getState().moveTabToPane("c", right, 99);
    expect(current().panes.find((item) => item.id === right)!.tabs.map((tab) => tab.id)).toEqual(["b", "a", "c"]);
  });
});
