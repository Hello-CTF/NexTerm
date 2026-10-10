/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ listLive: vi.fn() }));
vi.mock("../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: {},
  terminalApi: { listLive: mocks.listLive },
  vaultApi: {},
}));

import { pollUnreadTabsOnce, resetUnreadTrackingForTest } from "../app/unread";
import { useUi, type AppTab, type Pane, type Workspace } from "../app/store";
import type { LiveTabInfo } from "../ipc/commands";

function terminal(id: string, tabId: string): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "s", tabId, closable: true };
}

function ws(panes: Pane[]): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "workspace",
    sessionId: "s",
    panes,
    activePaneId: panes[0].id,
    splitRatio: 0.5,
    closable: true,
  };
}

function live(tabId: string, lastOutputMsAgo: number): LiveTabInfo {
  return {
    tabId,
    sessionId: "s",
    sessionName: "session",
    sessionKind: "ssh",
    cols: 80,
    rows: 24,
    controller: null,
    subscribers: 1,
    viewers: 1,
    exited: false,
    lastOutputMsAgo,
  };
}

function tabById(id: string): AppTab | undefined {
  return useUi
    .getState()
    .workspaces.flatMap((w) => w.panes)
    .flatMap((p) => p.tabs)
    .find((t) => t.id === id);
}

beforeEach(() => {
  vi.clearAllMocks();
  resetUnreadTrackingForTest();
  useUi.setState({
    workspaces: [ws([{ id: "p", tabs: [terminal("t1", "T1"), terminal("t2", "T2")], activeTabId: "t1" }])],
    activeWorkspaceId: "ws",
    toasts: [],
  });
});

describe("后台标签未读标记", () => {
  it("首轮轮询只建基线,不打标", async () => {
    mocks.listLive.mockResolvedValue([live("T1", 100), live("T2", 100)]);
    await pollUnreadTabsOnce();
    expect(tabById("t1")?.unread).toBeUndefined();
    expect(tabById("t2")?.unread).toBeUndefined();
  });

  it("后台标签有新输出时打标,活动标签不打", async () => {
    mocks.listLive.mockResolvedValue([live("T1", 1000), live("T2", 1000)]);
    await pollUnreadTabsOnce();
    mocks.listLive.mockResolvedValue([live("T1", 50), live("T2", 80)]);
    await pollUnreadTabsOnce();
    expect(tabById("t1")?.unread).toBeUndefined();
    expect(tabById("t2")?.unread).toBe(true);
  });

  it("没有新输出时既不打标也不触碰状态", async () => {
    mocks.listLive.mockResolvedValue([live("T1", 1000), live("T2", 1000)]);
    await pollUnreadTabsOnce();
    mocks.listLive.mockResolvedValue([live("T1", 3000), live("T2", 3000)]);
    const before = useUi.getState().workspaces;
    await pollUnreadTabsOnce();
    expect(tabById("t2")?.unread).toBeUndefined();
    expect(useUi.getState().workspaces).toBe(before);
  });

  it("激活标签时清除未读标记", async () => {
    mocks.listLive.mockResolvedValue([live("T2", 1000)]);
    await pollUnreadTabsOnce();
    mocks.listLive.mockResolvedValue([live("T2", 80)]);
    await pollUnreadTabsOnce();
    expect(tabById("t2")?.unread).toBe(true);

    useUi.getState().setActiveTab("t2");
    expect(tabById("t2")?.unread).toBeUndefined();
    expect(useUi.getState().workspaces[0].panes[0].activeTabId).toBe("t2");
  });

  it("列表不可用时静默跳过", async () => {
    mocks.listLive.mockRejectedValue(new Error("down"));
    await pollUnreadTabsOnce();
    expect(tabById("t2")?.unread).toBeUndefined();
  });

  it("不经 setActiveTab 的激活路径,下一轮轮询清除陈旧标记", async () => {
    mocks.listLive.mockResolvedValue([live("T2", 1000)]);
    await pollUnreadTabsOnce();
    mocks.listLive.mockResolvedValue([live("T2", 80)]);
    await pollUnreadTabsOnce();
    expect(tabById("t2")?.unread).toBe(true);

    useUi.setState((s) => ({
      workspaces: s.workspaces.map((w) => ({
        ...w,
        panes: w.panes.map((p) => ({ ...p, activeTabId: "t2" })),
      })),
    }));
    await pollUnreadTabsOnce();
    expect(tabById("t2")?.unread).toBeUndefined();
  });

  it("非活动工作区的标签有新输出时同样打标,切回后清除", async () => {
    useUi.setState((s) => ({
      workspaces: [
        ...s.workspaces,
        {
          id: "ws2",
          kind: "session" as const,
          title: "other",
          sessionId: "s2",
          panes: [{ id: "p2", tabs: [terminal("t9", "T9")], activeTabId: "t9" }],
          activePaneId: "p2",
          splitRatio: 0.5,
          closable: true,
        },
      ],
    }));
    mocks.listLive.mockResolvedValue([live("T9", 1000)]);
    await pollUnreadTabsOnce();
    mocks.listLive.mockResolvedValue([live("T9", 80)]);
    await pollUnreadTabsOnce();
    expect(tabById("t9")?.unread).toBe(true);

    useUi.setState({ activeWorkspaceId: "ws2" });
    await pollUnreadTabsOnce();
    expect(tabById("t9")?.unread).toBeUndefined();
  });
});
