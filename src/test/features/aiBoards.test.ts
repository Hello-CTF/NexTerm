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
    title: `Host ${id}`,
    sessionId: `s-${id}`,
    panes: [{ id: `pane-${id}`, tabs: [], activeTabId: null }],
    activePaneId: `pane-${id}`,
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

describe("AI board store actions", () => {
  it("ensureAiBoard idempotently creates a board with at least one tab", () => {
    const first = useUi.getState().ensureAiBoard("ws-1");
    expect(first.tabs).toHaveLength(1);
    expect(first.activeTabId).toBe(first.tabs[0].id);
    const second = useUi.getState().ensureAiBoard("ws-1");
    expect(second).toBe(first);
    expect(useUi.getState().ensureAiBoard("ws-2").tabs).toHaveLength(1);
  });

  it("addAiTab appends and activates a new tab, setActiveAiTab switches tabs, and closeAiTab closes and activates an adjacent tab", () => {
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

  it("closing the only tab resets it to a fresh tab without leaving an empty board", () => {
    const st = () => useUi.getState();
    const tabA = st().ensureAiBoard("ws-1").tabs[0].id;
    st().updateAiTab("ws-1", tabA, { conversationId: "conv-1", title: "Old conversation" });
    st().closeAiTab("ws-1", tabA);
    const board = st().aiBoards["ws-1"];
    expect(board.tabs).toHaveLength(1);
    expect(board.tabs[0].id).not.toBe(tabA);
    expect(board.tabs[0].conversationId).toBeUndefined();
    expect(board.activeTabId).toBe(board.tabs[0].id);
  });

  it("updateAiTab changes only the target tab", () => {
    const st = () => useUi.getState();
    const tabA = st().ensureAiBoard("ws-1").tabs[0].id;
    const tabB = st().addAiTab("ws-1");
    st().updateAiTab("ws-1", tabA, { conversationId: "conv-1", title: "Investigate 502" });
    const board = st().aiBoards["ws-1"];
    expect(board.tabs[0]).toMatchObject({ id: tabA, conversationId: "conv-1", title: "Investigate 502" });
    expect(board.tabs[1]).toMatchObject({ id: tabB, title: "新会话" });
  });

  it("closeWorkspace cleans up the corresponding board without touching other boards", async () => {
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

describe("AI board layout persistence", () => {
  it("serializeLayout writes aiBoards and sanitizeLayout reads them back unchanged", () => {
    const st = () => useUi.getState();
    useUi.setState({ workspaces: [workspace("ws-1")], activeWorkspaceId: "ws-1" });
    const tabA = st().ensureAiBoard("ws-1").tabs[0].id;
    st().updateAiTab("ws-1", tabA, { conversationId: "conv-1", title: "Investigate 502" });
    st().ensureAiBoard(GLOBAL_AI_BOARD_KEY);

    const dto = serializeLayout(useUi.getState());
    expect(dto).not.toBeNull();
    const parsed = sanitizeLayout(JSON.parse(JSON.stringify(dto)));
    expect(parsed?.aiBoards?.["ws-1"]).toEqual({
      tabs: [{ id: tabA, title: "Investigate 502", conversationId: "conv-1" }],
      activeTabId: tabA,
    });
    expect(parsed?.aiBoards?.[GLOBAL_AI_BOARD_KEY]?.tabs).toHaveLength(1);
  });

  it("sanitizeLayout discards board groups that do not belong to any workspace and preserves the global group", () => {
    const parsed = sanitizeLayout({
      v: 4,
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

  it("layouts from older versions are invalid, as are layouts missing the aiBoards field", () => {
    expect(
      sanitizeLayout({
        v: 1,
        workspaces: [workspace("ws-1")],
        activeWorkspaceId: "ws-1",
        aiBoards: {},
      }),
    ).toBeNull();
    expect(
      sanitizeLayout({
        v: 4,
        workspaces: [workspace("ws-1")],
        activeWorkspaceId: "ws-1",
      }),
    ).toBeNull();
  });

  it("board tabs with missing fields are sanitized: entries without an id are dropped, a missing title falls back to the default conversation title, and the active tab points to an existing tab", () => {
    const parsed = sanitizeLayout({
      v: 4,
      workspaces: [workspace("ws-1")],
      activeWorkspaceId: "ws-1",
      aiBoards: {
        "ws-1": {
          tabs: [
            { title: "missing id" },
            { id: "ai-1", title: "  " },
            { id: "ai-2", title: "Normal", conversationId: "conv-1" },
          ],
          activeTabId: "ai-gone",
        },
      },
    });
    expect(parsed?.aiBoards?.["ws-1"]).toEqual({
      tabs: [
        { id: "ai-1", title: "新会话", conversationId: undefined },
        { id: "ai-2", title: "Normal", conversationId: "conv-1" },
      ],
      activeTabId: "ai-2",
    });
  });
});
