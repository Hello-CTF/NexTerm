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
  return { id, kind: "terminal", title: `终端 ${id}`, sessionId: "s-1", tabId: `bt-${id}`, closable: true };
}

function pane(id: string, tabIds: string[], activeTabId: string | null = null): Pane {
  return { id, tabs: tabIds.map(paneTab), activeTabId };
}

function workspace(id: string, panes: Pane[]): Workspace {
  return {
    id,
    kind: "session",
    title: `主机 ${id}`,
    sessionId: `s-${id}`,
    panes,
    activePaneId: panes[0].id,
    splitRatio: 0.5,
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
  it("合法移动返回落点, to 越界时 clamp 到边界", () => {
    expect(reorderIndex(4, 0, 2)).toEqual({ from: 0, to: 2 });
    expect(reorderIndex(3, 0, 99)).toEqual({ from: 0, to: 2 });
    expect(reorderIndex(3, 2, -5)).toEqual({ from: 2, to: 0 });
  });

  it("from 越界/非整数/原地不动时返回 null", () => {
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
  it("前移与后移后元素落在目标下标", () => {
    expect(moveItem(["a", "b", "c", "d"], 0, 2)).toEqual(["b", "c", "a", "d"]);
    expect(moveItem(["a", "b", "c", "d"], 3, 1)).toEqual(["a", "d", "b", "c"]);
  });

  it("to 越界 clamp 到首尾", () => {
    expect(moveItem(["a", "b", "c"], 0, 99)).toEqual(["b", "c", "a"]);
    expect(moveItem(["a", "b", "c"], 2, -1)).toEqual(["c", "a", "b"]);
  });

  it("无效输入返回原数组引用且不改动原数组", () => {
    const items = ["a", "b", "c"];
    expect(moveItem(items, 1, 1)).toBe(items);
    expect(moveItem(items, -1, 0)).toBe(items);
    expect(moveItem(items, 3, 0)).toBe(items);
    expect(moveItem(items, 0, 1.5)).toBe(items);
    expect(items).toEqual(["a", "b", "c"]);
  });

  it("有效移动不改动原数组", () => {
    const items = ["a", "b", "c"];
    moveItem(items, 0, 2);
    expect(items).toEqual(["a", "b", "c"]);
  });
});

describe("moveTabInPane", () => {
  it("只重排目标 Pane 的 tabs, activeTabId 与其余 state 不变", () => {
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

  it("toIndex 越界时 clamp 到末尾", () => {
    seedPanes();
    useUi.getState().moveTabInPane("p1", 0, 99);
    const ws1 = useUi.getState().workspaces.find((w) => w.id === "ws-1");
    expect(ws1?.panes[0].tabs.map((t) => t.id)).toEqual(["t2", "t3", "t1"]);
  });

  it("fromIndex 无效或原地不动时 no-op", () => {
    seedPanes();
    const st = () => useUi.getState();
    const before = st().workspaces;
    st().moveTabInPane("p1", -1, 0);
    st().moveTabInPane("p1", 7, 0);
    st().moveTabInPane("p1", 0.5, 1);
    st().moveTabInPane("p1", 1, 1);
    expect(st().workspaces).toBe(before);
  });

  it("paneId 不存在时 no-op", () => {
    seedPanes();
    const before = useUi.getState().workspaces;
    useUi.getState().moveTabInPane("ghost", 0, 1);
    expect(useUi.getState().workspaces).toBe(before);
  });
});

describe("moveAiTab", () => {
  it("只重排目标看板, activeTabId 与其他看板不变", () => {
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

  it("boardKey 不存在或 index 无效时 no-op", () => {
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

describe("布局序列化保持排序结果", () => {
  it("serialize/sanitize 往返后 pane 与看板标签顺序与激活态不变", () => {
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
