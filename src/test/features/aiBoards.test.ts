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

import { GLOBAL_AI_BOARD_KEY, useUi, type Workspace } from "../../app/store";
import { sanitizeLayout, serializeLayout } from "../../app/layout";

function workspace(id: string): Workspace {
  return {
    id,
    kind: "session",
    title: `主机 ${id}`,
    sessionId: `s-${id}`,
    panes: [{ id: `pane-${id}`, tabs: [], activeTabId: null }],
    activePaneId: `pane-${id}`,
    splitRatio: 0.5,
    closable: true,
  };
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

describe("AI 看板 store 动作", () => {
  it("ensureAiBoard 幂等地创建至少一个标签的看板", () => {
    const first = useUi.getState().ensureAiBoard("ws-1");
    expect(first.tabs).toHaveLength(1);
    expect(first.activeTabId).toBe(first.tabs[0].id);
    const second = useUi.getState().ensureAiBoard("ws-1");
    expect(second).toBe(first);
    expect(useUi.getState().ensureAiBoard("ws-2").tabs).toHaveLength(1);
  });

  it("addAiTab 追加并激活新标签, setActiveAiTab 切换, closeAiTab 关闭并激活相邻标签", () => {
    const st = () => useUi.getState();
    const board = st().ensureAiBoard("ws-1");
    const tabA = board.tabs[0].id;
    const tabB = st().addAiTab("ws-1");
    const tabC = st().addAiTab("ws-1");
    expect(st().aiBoards["ws-1"].tabs.map((t) => t.id)).toEqual([tabA, tabB, tabC]);
    expect(st().aiBoards["ws-1"].activeTabId).toBe(tabC);

    st().setActiveAiTab("ws-1", tabA);
    expect(st().aiBoards["ws-1"].activeTabId).toBe(tabA);

    st().closeAiTab("ws-1", tabA);
    expect(st().aiBoards["ws-1"].tabs.map((t) => t.id)).toEqual([tabB, tabC]);
    expect(st().aiBoards["ws-1"].activeTabId).toBe(tabB);

    st().closeAiTab("ws-1", tabC);
    expect(st().aiBoards["ws-1"].tabs.map((t) => t.id)).toEqual([tabB]);
    expect(st().aiBoards["ws-1"].activeTabId).toBe(tabB);
  });

  it("关闭唯一标签时重置为全新标签, 不留空看板", () => {
    const st = () => useUi.getState();
    const tabA = st().ensureAiBoard("ws-1").tabs[0].id;
    st().updateAiTab("ws-1", tabA, { conversationId: "conv-1", title: "旧会话" });
    st().closeAiTab("ws-1", tabA);
    const board = st().aiBoards["ws-1"];
    expect(board.tabs).toHaveLength(1);
    expect(board.tabs[0].id).not.toBe(tabA);
    expect(board.tabs[0].conversationId).toBeUndefined();
    expect(board.activeTabId).toBe(board.tabs[0].id);
  });

  it("updateAiTab 只改目标标签", () => {
    const st = () => useUi.getState();
    const tabA = st().ensureAiBoard("ws-1").tabs[0].id;
    const tabB = st().addAiTab("ws-1");
    st().updateAiTab("ws-1", tabA, { conversationId: "conv-1", title: "排查 502" });
    const board = st().aiBoards["ws-1"];
    expect(board.tabs[0]).toMatchObject({ id: tabA, conversationId: "conv-1", title: "排查 502" });
    expect(board.tabs[1]).toMatchObject({ id: tabB, title: "新会话" });
  });

  it("closeWorkspace 清理对应看板, 不动其他看板", async () => {
    const st = () => useUi.getState();
    useUi.setState({ workspaces: [workspace("ws-1"), workspace("ws-2")], activeWorkspaceId: "ws-1" });
    st().ensureAiBoard("ws-1");
    st().ensureAiBoard("ws-2");
    st().ensureAiBoard(GLOBAL_AI_BOARD_KEY);
    await st().closeWorkspace("ws-1");
    expect(st().aiBoards["ws-1"]).toBeUndefined();
    expect(st().aiBoards["ws-2"]).toBeDefined();
    expect(st().aiBoards[GLOBAL_AI_BOARD_KEY]).toBeDefined();
  });
});

describe("AI 看板布局持久化", () => {
  it("serializeLayout 写出 aiBoards, sanitizeLayout 原样读回", () => {
    const st = () => useUi.getState();
    useUi.setState({ workspaces: [workspace("ws-1")], activeWorkspaceId: "ws-1" });
    const tabA = st().ensureAiBoard("ws-1").tabs[0].id;
    st().updateAiTab("ws-1", tabA, { conversationId: "conv-1", title: "排查 502" });
    st().ensureAiBoard(GLOBAL_AI_BOARD_KEY);

    const dto = serializeLayout(useUi.getState());
    expect(dto).not.toBeNull();
    const parsed = sanitizeLayout(JSON.parse(JSON.stringify(dto)));
    expect(parsed?.aiBoards?.["ws-1"]).toEqual({
      tabs: [{ id: tabA, title: "排查 502", conversationId: "conv-1" }],
      activeTabId: tabA,
    });
    expect(parsed?.aiBoards?.[GLOBAL_AI_BOARD_KEY]?.tabs).toHaveLength(1);
  });

  it("sanitizeLayout 丢弃不属于任何工作区的看板分组, 保留全局分组", () => {
    const parsed = sanitizeLayout({
      v: 1,
      workspaces: [workspace("ws-1")],
      activeWorkspaceId: "ws-1",
      aiBoards: {
        [GLOBAL_AI_BOARD_KEY]: { tabs: [{ id: "ai-g", title: "新会话" }], activeTabId: "ai-g" },
        "ws-1": { tabs: [{ id: "ai-1", title: "新会话" }], activeTabId: "ai-1" },
        "ws-ghost": { tabs: [{ id: "ai-x", title: "新会话" }], activeTabId: "ai-x" },
      },
    });
    expect(Object.keys(parsed?.aiBoards ?? {}).sort()).toEqual([GLOBAL_AI_BOARD_KEY, "ws-1"].sort());
  });

  it("旧布局没有 aiBoards 字段时读为空看板表", () => {
    const parsed = sanitizeLayout({
      v: 1,
      workspaces: [workspace("ws-1")],
      activeWorkspaceId: "ws-1",
    });
    expect(parsed?.aiBoards).toEqual({});
  });

  it("看板标签缺字段时被清洗: 无 id 丢弃, 无标题回落「新会话」, 激活标签指向存在的标签", () => {
    const parsed = sanitizeLayout({
      v: 1,
      workspaces: [workspace("ws-1")],
      activeWorkspaceId: "ws-1",
      aiBoards: {
        "ws-1": {
          tabs: [
            { title: "没有 id" },
            { id: "ai-1", title: "  " },
            { id: "ai-2", title: "正常", conversationId: "conv-1" },
          ],
          activeTabId: "ai-gone",
        },
      },
    });
    expect(parsed?.aiBoards?.["ws-1"]).toEqual({
      tabs: [
        { id: "ai-1", title: "新会话", conversationId: undefined },
        { id: "ai-2", title: "正常", conversationId: "conv-1" },
      ],
      activeTabId: "ai-2",
    });
  });
});
