/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ listLive: vi.fn() }));
vi.mock("../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: {},
  terminalApi: { listLive: mocks.listLive },
  vaultApi: {},
}));

import {
  pollUnreadTabsOnce,
  resetUnreadTracking,
  startUnreadTracking,
  stopUnreadTrackingForTest,
} from "../app/unread";
import { useUi, type AppTab, type Pane, type Workspace } from "../app/store";
import { isTabUnread } from "../app/unreadStore";
import type { LiveTabInfo } from "../ipc/commands";
import { deferred } from "./features/reactTestUtils";

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

function setHidden(hidden: boolean): void {
  Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
  document.dispatchEvent(new Event("visibilitychange"));
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.clearAllMocks();
  stopUnreadTrackingForTest();
  resetUnreadTracking();
  useUi.setState({
    workspaces: [ws([{ id: "p", tabs: [terminal("t1", "T1"), terminal("t2", "T2")], activeTabId: "t1" }])],
    activeWorkspaceId: "ws",
    toasts: [],
  });
  mocks.listLive.mockResolvedValue([live("T1", 100), live("T2", 100)]);
});

afterEach(() => {
  stopUnreadTrackingForTest();
  setHidden(false);
  vi.useRealTimers();
});

describe("unread poll single-flight", () => {
  it("joins overlapping polls into one listLive request", async () => {
    const pending = deferred<LiveTabInfo[]>();
    mocks.listLive.mockReturnValue(pending.promise);

    const first = pollUnreadTabsOnce();
    const second = pollUnreadTabsOnce();
    await vi.advanceTimersByTimeAsync(0);
    expect(mocks.listLive).toHaveBeenCalledTimes(1);

    pending.resolve([live("T2", 100)]);
    await Promise.all([first, second]);
  });

  it("discards a stale response that lands after a tracking reset", async () => {
    const pending = deferred<LiveTabInfo[]>();
    mocks.listLive.mockReturnValue(pending.promise);

    const polling = pollUnreadTabsOnce();
    await vi.advanceTimersByTimeAsync(0);
    // Account switch tears the tracking down while the old account's poll is still in flight.
    resetUnreadTracking();
    pending.resolve([live("T2", 1)]);

    await polling;
    expect(isTabUnread("t2")).toBe(false);
  });
});

describe("unread tracking scheduler", () => {
  it("schedules the next poll only after the previous one completes", async () => {
    const pending = deferred<LiveTabInfo[]>();
    mocks.listLive.mockReturnValue(pending.promise);
    startUnreadTracking();

    await vi.advanceTimersByTimeAsync(60_000);
    // The first poll is still in flight, so no fixed-interval stacking happened.
    expect(mocks.listLive).toHaveBeenCalledTimes(1);

    pending.resolve([live("T1", 100)]);
    await vi.advanceTimersByTimeAsync(5_000);
    expect(mocks.listLive).toHaveBeenCalledTimes(2);
  });

  it("polls immediately when the page becomes visible again and pauses while hidden", async () => {
    startUnreadTracking();
    await vi.advanceTimersByTimeAsync(5_000);
    expect(mocks.listLive).toHaveBeenCalledTimes(1);

    setHidden(true);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(mocks.listLive).toHaveBeenCalledTimes(1);

    setHidden(false);
    await vi.advanceTimersByTimeAsync(0);
    expect(mocks.listLive).toHaveBeenCalledTimes(2);

    await vi.advanceTimersByTimeAsync(5_000);
    expect(mocks.listLive).toHaveBeenCalledTimes(3);
  });

  it("does not start a second loop when startUnreadTracking is called again", async () => {
    startUnreadTracking();
    startUnreadTracking();
    await vi.advanceTimersByTimeAsync(5_000);
    expect(mocks.listLive).toHaveBeenCalledTimes(1);
  });
});
