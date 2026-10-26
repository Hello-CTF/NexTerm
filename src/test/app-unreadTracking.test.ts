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
import { isTabUnread } from "../app/unreadStore";
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
    closable: true,
  };
}

function live(tabId: string, lastOutputAt: number, lastOutputMsAgo = 0): LiveTabInfo {
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
    lastOutputAt,
    lastOutputMsAgo,
  };
}

function liveWithoutTimestamp(tabId: string, lastOutputMsAgo: number): LiveTabInfo {
  const { lastOutputAt: _lastOutputAt, ...ageOnly } = live(tabId, 0, lastOutputMsAgo);
  return ageOnly;
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

describe("background tab unread markers", () => {
  it("establishes a baseline on the first poll without marking tabs", async () => {
    mocks.listLive.mockResolvedValue([live("T1", 100), live("T2", 100)]);
    await pollUnreadTabsOnce();
    expect(isTabUnread("t1")).toBe(false);
    expect(isTabUnread("t2")).toBe(false);
  });

  it("marks background tabs without changing workspace or tab references", async () => {
    mocks.listLive.mockResolvedValue([live("T1", 1000, 1000), live("T2", 1000, 1000)]);
    await pollUnreadTabsOnce();
    const before = useUi.getState().workspaces;
    const target = tabById("t2");
    mocks.listLive.mockResolvedValue([live("T1", 1000, 50), live("T2", 1080, 80)]);
    await pollUnreadTabsOnce();
    expect(isTabUnread("t1")).toBe(false);
    expect(isTabUnread("t2")).toBe(true);
    expect(useUi.getState().workspaces).toBe(before);
    expect(tabById("t2")).toBe(target);
  });

  it("uses the absolute output timestamp even when the relative age increases", async () => {
    mocks.listLive.mockResolvedValue([live("T1", 1000, 1000), live("T2", 1000, 1000)]);
    await pollUnreadTabsOnce();
    mocks.listLive.mockResolvedValue([live("T1", 2000, 3000), live("T2", 2000, 3000)]);
    await pollUnreadTabsOnce();
    expect(isTabUnread("t1")).toBe(false);
    expect(isTabUnread("t2")).toBe(true);
  });

  it("detects the first output after an explicit zero timestamp", async () => {
    mocks.listLive.mockResolvedValue([live("T1", 0), live("T2", 0)]);
    await pollUnreadTabsOnce();
    mocks.listLive.mockResolvedValue([live("T1", 1000), live("T2", 2000)]);
    await pollUnreadTabsOnce();
    expect(isTabUnread("t1")).toBe(false);
    expect(isTabUnread("t2")).toBe(true);
  });

  it("does not mark tabs or touch state without new output", async () => {
    mocks.listLive.mockResolvedValue([live("T1", 1000, 1000), live("T2", 1000, 1000)]);
    await pollUnreadTabsOnce();
    mocks.listLive.mockResolvedValue([live("T1", 1000, 3000), live("T2", 1000, 3000)]);
    const before = useUi.getState().workspaces;
    await pollUnreadTabsOnce();
    expect(isTabUnread("t2")).toBe(false);
    expect(useUi.getState().workspaces).toBe(before);
  });

  it("does not treat relative-age-only responses as the current output contract", async () => {
    mocks.listLive.mockResolvedValue([liveWithoutTimestamp("T1", 1000), liveWithoutTimestamp("T2", 1000)]);
    await pollUnreadTabsOnce();
    mocks.listLive.mockResolvedValue([liveWithoutTimestamp("T1", 50), liveWithoutTimestamp("T2", 80)]);
    await pollUnreadTabsOnce();
    expect(isTabUnread("t1")).toBe(false);
    expect(isTabUnread("t2")).toBe(false);
  });

  it("clears the unread marker when a tab is activated", async () => {
    mocks.listLive.mockResolvedValue([live("T2", 1000, 1000)]);
    await pollUnreadTabsOnce();
    mocks.listLive.mockResolvedValue([live("T2", 1080, 80)]);
    await pollUnreadTabsOnce();
    expect(isTabUnread("t2")).toBe(true);

    useUi.getState().setActiveTab("t2");
    expect(isTabUnread("t2")).toBe(false);
    expect(useUi.getState().workspaces[0].panes[0].activeTabId).toBe("t2");
  });

  it("skips silently when the list is unavailable", async () => {
    mocks.listLive.mockRejectedValue(new Error("down"));
    await pollUnreadTabsOnce();
    expect(isTabUnread("t2")).toBe(false);
  });

  it("clears stale markers on the next poll after activation outside setActiveTab", async () => {
    mocks.listLive.mockResolvedValue([live("T2", 1000, 1000)]);
    await pollUnreadTabsOnce();
    mocks.listLive.mockResolvedValue([live("T2", 1080, 80)]);
    await pollUnreadTabsOnce();
    expect(isTabUnread("t2")).toBe(true);

    useUi.setState((s) => ({
      workspaces: s.workspaces.map((w) => ({
        ...w,
        panes: w.panes.map((p) => ({ ...p, activeTabId: "t2" })),
      })),
    }));
    await pollUnreadTabsOnce();
    expect(isTabUnread("t2")).toBe(false);
  });

  it("also marks tabs in inactive workspaces and clears them after switching back", async () => {
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
          closable: true,
        },
      ],
    }));
    mocks.listLive.mockResolvedValue([live("T9", 1000, 1000)]);
    await pollUnreadTabsOnce();
    mocks.listLive.mockResolvedValue([live("T9", 1080, 80)]);
    await pollUnreadTabsOnce();
    expect(isTabUnread("t9")).toBe(true);

    useUi.setState({ activeWorkspaceId: "ws2" });
    await pollUnreadTabsOnce();
    expect(isTabUnread("t9")).toBe(false);
  });
});
