/** @vitest-environment jsdom */
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
}));
vi.mock("../../ipc/events", () => ({ listenEvent: vi.fn() }));

import { moveItem, reorderIndex } from "../../app/tabReorder";
import { GLOBAL_AI_BOARD_KEY, useUi, type AppTab, type Pane, type Workspace } from "../../app/store";
import { sanitizeLayout, serializeLayout } from "../../app/layout";

function paneTab(id: string): AppTab {
  return { id, kind: "terminal", title: `Terminal ${id}`, sessionId: "s-1", tabId: `bt-${id}`, closable: true };
}

function pane(id: string, tabIds: string[], activeTabId: string | null = null): Pane {
  return { id, tabs: tabIds.map(paneTab), activeTabId };
}

function workspace(id: string, panes: Pane[]): Workspace {
  return {
    id,
    kind: "session",
    title: `Host ${id}`,
    sessionId: `s-${id}`,
    panes,
    activePaneId: panes[0].id,
    closable: true,
  };
}

function seedPanes() {
  const ws1 = workspace("ws-1", [pane("p1", ["t1", "t2", "t3"], "t2"), pane("p2", ["u1"], "u1")]);
  const ws2 = workspace("ws-2", [pane("p3", ["v1", "v2"], "v1")]);
  useUi.setState({ workspaces: [ws1, ws2], activeWorkspaceId: "ws-2" });
}

beforeEach(() => {
  document.body.replaceChildren();
  localStorage.clear();
  useUi.setState({
    workspaces: [],
    activeWorkspaceId: null,
    aiBoards: {},
    toasts: [],
  });
});

describe("reorderIndex", () => {
  it("returns the destination for valid moves and clamps out-of-range to values", () => {
    expect(reorderIndex(4, 0, 2)).toEqual({ from: 0, to: 2 });
    expect(reorderIndex(3, 0, 99)).toEqual({ from: 0, to: 2 });
    expect(reorderIndex(3, 2, -5)).toEqual({ from: 2, to: 0 });
  });

  it("returns null for out-of-range or non-integer from values, or no-op moves", () => {
    expect(reorderIndex(3, 3, 0)).toBeNull();
    expect(reorderIndex(3, -1, 0)).toBeNull();
    expect(reorderIndex(3, 0.5, 1)).toBeNull();
    expect(reorderIndex(3, 0, Number.NaN)).toBeNull();
    expect(reorderIndex(3, 1, 1)).toBeNull();
    expect(reorderIndex(0, 0, 0)).toBeNull();
    expect(reorderIndex(1, 0, 5)).toBeNull();
  });
});

describe("moveItem", () => {
  it("places moved elements at the target index for forward and backward moves", () => {
    expect(moveItem(["a", "b", "c", "d"], 0, 2)).toEqual(["b", "c", "a", "d"]);
    expect(moveItem(["a", "b", "c", "d"], 3, 1)).toEqual(["a", "d", "b", "c"]);
  });

  it("clamps out-of-range to values to the first or last index", () => {
    expect(moveItem(["a", "b", "c"], 0, 99)).toEqual(["b", "c", "a"]);
    expect(moveItem(["a", "b", "c"], 2, -1)).toEqual(["c", "a", "b"]);
  });

  it("returns the original array reference without mutation for invalid input", () => {
    const items = ["a", "b", "c"];
    expect(moveItem(items, 1, 1)).toBe(items);
    expect(moveItem(items, -1, 0)).toBe(items);
    expect(moveItem(items, 3, 0)).toBe(items);
    expect(moveItem(items, 0, 1.5)).toBe(items);
    expect(items).toEqual(["a", "b", "c"]);
  });

  it("does not mutate the original array for valid moves", () => {
    const items = ["a", "b", "c"];
    moveItem(items, 0, 2);
    expect(items).toEqual(["a", "b", "c"]);
  });
});

describe("moveTabInPane", () => {
  it("reorders only the target pane's tabs and preserves activeTabId and the rest of state", () => {
    seedPanes();
    const before = useUi.getState();
    before.moveTabInPane("p1", 0, 2);
    const after = useUi.getState();
    const ws1 = after.workspaces.find((w) => w.id === "ws-1");
    expect(ws1?.panes[0].tabs.map((t) => t.id)).toEqual(["t2", "t3", "t1"]);
    expect(ws1?.panes[0].activeTabId).toBe("t2");
    expect(ws1?.panes[1]).toBe(before.workspaces[0].panes[1]);
    expect(ws1?.activePaneId).toBe("p1");
    expect(after.activeWorkspaceId).toBe("ws-2");
    expect(after.workspaces[1]).toBe(before.workspaces[1]);
    expect(after.aiBoards).toBe(before.aiBoards);
    expect(after.sessions).toBe(before.sessions);
  });

  it("clamps out-of-range toIndex values to the end", () => {
    seedPanes();
    useUi.getState().moveTabInPane("p1", 0, 99);
    const ws1 = useUi.getState().workspaces.find((w) => w.id === "ws-1");
    expect(ws1?.panes[0].tabs.map((t) => t.id)).toEqual(["t2", "t3", "t1"]);
  });

  it("no-ops for invalid fromIndex values or no-op moves", () => {
    seedPanes();
    const st = () => useUi.getState();
    const before = st().workspaces;
    st().moveTabInPane("p1", -1, 0);
    st().moveTabInPane("p1", 7, 0);
    st().moveTabInPane("p1", 0.5, 1);
    st().moveTabInPane("p1", 1, 1);
    expect(st().workspaces).toBe(before);
  });

  it("no-ops when paneId does not exist", () => {
    seedPanes();
    const before = useUi.getState().workspaces;
    useUi.getState().moveTabInPane("ghost", 0, 1);
    expect(useUi.getState().workspaces).toBe(before);
  });
});

describe("moveAiTab", () => {
  it("reorders only the target board and preserves activeTabId and other boards", () => {
    const st = () => useUi.getState();
    const a = st().ensureAiBoard("ws-1").tabs[0].id;
    const b = st().addAiTab("ws-1");
    const c = st().addAiTab("ws-1");
    st().setActiveAiTab("ws-1", b);
    st().ensureAiBoard(GLOBAL_AI_BOARD_KEY);
    const otherBefore = st().aiBoards[GLOBAL_AI_BOARD_KEY];

    st().moveAiTab("ws-1", 0, 2);
    const board = st().aiBoards["ws-1"];
    expect(board.tabs.map((t) => t.id)).toEqual([b, c, a]);
    expect(board.activeTabId).toBe(b);
    expect(st().aiBoards[GLOBAL_AI_BOARD_KEY]).toBe(otherBefore);
  });

  it("no-ops when boardKey does not exist or index is invalid", () => {
    const st = () => useUi.getState();
    st().ensureAiBoard("ws-1");
    st().addAiTab("ws-1");
    const before = st().aiBoards;
    st().moveAiTab("ghost", 0, 1);
    st().moveAiTab("ws-1", -1, 0);
    st().moveAiTab("ws-1", 5, 0);
    st().moveAiTab("ws-1", 0, 0);
    expect(st().aiBoards).toBe(before);
  });
});

describe("layout serialization preserves ordering", () => {
  it("preserves pane and board tab order and active state across serialize/sanitize round trips", () => {
    const st = () => useUi.getState();
    useUi.setState({
      workspaces: [workspace("ws-1", [pane("p1", ["t1", "t2", "t3"], "t3")])],
      activeWorkspaceId: "ws-1",
    });
    const a = st().ensureAiBoard("ws-1").tabs[0].id;
    const b = st().addAiTab("ws-1");
    const c = st().addAiTab("ws-1");

    st().moveTabInPane("p1", 2, 0);
    st().moveAiTab("ws-1", 0, 2);

    const dto = serializeLayout(useUi.getState());
    const parsed = sanitizeLayout(JSON.parse(JSON.stringify(dto)));
    expect(parsed?.workspaces[0]?.panes[0]?.tabs.map((t) => t.id)).toEqual(["t3", "t1", "t2"]);
    expect(parsed?.workspaces[0]?.panes[0]?.activeTabId).toBe("t3");
    expect(parsed?.aiBoards["ws-1"]?.tabs.map((t) => t.id)).toEqual([b, c, a]);
    expect(parsed?.aiBoards["ws-1"]?.activeTabId).toBe(c);
  });
});
