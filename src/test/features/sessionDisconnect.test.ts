/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  disconnect: vi.fn(),
  listLive: vi.fn(),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));
vi.mock("../../ipc/commands", () => ({
  dbApi: {},
  vaultApi: {},
  terminalApi: { listLive: mocks.listLive },
  sessionApi: { disconnect: mocks.disconnect },
}));

import type { AppTab, Pane, Workspace } from "../../app/store";
import { useUi } from "../../app/store";
import {
  disconnectSessionWithConfirm,
  runningTerminalCounts,
} from "../../features/terminal/sessionDisconnect";
import { noteTabDurable, resetDurableTabs } from "../../features/terminal/durableTabs";
import type { LiveTabInfo } from "../../ipc/commands";

function terminal(id: string, sessionId = "s1"): AppTab {
  return { id, kind: "terminal", title: id, sessionId, tabId: `kernel-${id}`, closable: true };
}
function ws(panes: Pane[]): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "workspace",
    panes,
    activePaneId: panes[0].id,
    splitRatio: 0.5,
    closable: true,
  };
}
function seed(tabs: AppTab[]) {
  useUi.setState({
    workspaces: [ws([{ id: "p", tabs, activeTabId: tabs[0]?.id ?? null }])],
    activeWorkspaceId: "ws",
    toasts: [],
  });
}
function liveTab(tabId: string, sessionId: string, exited = false): LiveTabInfo {
  return {
    tabId,
    sessionId,
    sessionName: "session",
    sessionKind: "ssh",
    cols: 80,
    rows: 24,
    controller: null,
    subscribers: 1,
    viewers: 1,
    exited,
    lastOutputMsAgo: 0,
  };
}

describe("session disconnect confirmation", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    resetDurableTabs();
    mocks.ask.mockResolvedValue(true);
    mocks.disconnect.mockResolvedValue(undefined);
    mocks.listLive.mockResolvedValue([]);
  });

  it("counts open plus backend-detached non-exited terminals of the session", async () => {
    seed([terminal("a"), terminal("b")]);
    mocks.listLive.mockResolvedValue([
      liveTab("kernel-a", "s1"),
      liveTab("kernel-bg1", "s1"),
      liveTab("kernel-bg2", "s1"),
      liveTab("kernel-dead", "s1", true),
      liveTab("kernel-other", "s2"),
    ]);

    expect(await runningTerminalCounts("s1")).toEqual({ ordinary: 4, durable: 0 });
    expect(await runningTerminalCounts("s2")).toEqual({ ordinary: 1, durable: 0 });
    expect(await runningTerminalCounts("nope")).toEqual({ ordinary: 0, durable: 0 });
  });

  it("falls back to the local count when the backend list is unavailable", async () => {
    seed([terminal("a")]);
    mocks.listLive.mockRejectedValue(new Error("offline"));

    expect(await runningTerminalCounts("s1")).toEqual({ ordinary: 1, durable: 0 });
  });

  it("asks with the affected ordinary terminal count before disconnecting", async () => {
    seed([terminal("a"), terminal("b")]);

    await disconnectSessionWithConfirm("s1", "web-01");

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const [message, options] = mocks.ask.mock.calls[0];
    expect(message).toContain("2 个普通终端进程会被结束，无法恢复");
    expect(message).not.toContain("守护终端");
    expect(message).toContain("web-01");
    expect(options.kind).toBe("warning");
    expect(mocks.disconnect).toHaveBeenCalledWith("s1");
  });

  it("includes an already-detached background terminal in the confirmation count", async () => {
    seed([terminal("a")]);
    mocks.listLive.mockResolvedValue([liveTab("kernel-a", "s1"), liveTab("kernel-bg", "s1")]);

    await disconnectSessionWithConfirm("s1", "web-01");

    const [message] = mocks.ask.mock.calls[0];
    expect(message).toContain("2 个普通终端进程会被结束");
  });

  it("does not recount a local tab whose backend twin already exited", async () => {
    seed([terminal("a")]);
    mocks.listLive.mockResolvedValue([
      liveTab("kernel-a", "s1", true),
      liveTab("kernel-bg", "s1"),
      liveTab("kernel-other", "s2"),
    ]);

    expect(await runningTerminalCounts("s1")).toEqual({ ordinary: 1, durable: 0 });

    await disconnectSessionWithConfirm("s1", "web-01");

    const [message] = mocks.ask.mock.calls[0];
    expect(message).toContain("1 个普通终端进程会被结束");
  });

  it("separates durable terminals from the ones that will be killed", async () => {
    seed([terminal("a"), terminal("b")]);
    mocks.listLive.mockResolvedValue([liveTab("kernel-a", "s1"), liveTab("kernel-bg", "s1")]);
    noteTabDurable("kernel-b", true);
    noteTabDurable("kernel-bg", true);

    expect(await runningTerminalCounts("s1")).toEqual({ ordinary: 1, durable: 2 });

    await disconnectSessionWithConfirm("s1", "web-01");

    const [message] = mocks.ask.mock.calls[0];
    expect(message).toContain("1 个普通终端进程会被结束，无法恢复");
    expect(message).toContain("2 个守护终端会留在「后台会话」");
    expect(message.split("无法恢复")).toHaveLength(2);
  });

  it("does not claim processes die when only durable terminals remain", async () => {
    seed([terminal("a")]);
    mocks.listLive.mockResolvedValue([liveTab("kernel-a", "s1")]);
    noteTabDurable("kernel-a", true);

    await disconnectSessionWithConfirm("s1", "web-01");

    const [message] = mocks.ask.mock.calls[0];
    expect(message).toContain("1 个守护终端会留在「后台会话」");
    expect(message).toContain("进程不会被结束");
    expect(message).not.toContain("无法恢复");
  });

  it("cancelling the confirmation keeps the connection", async () => {
    seed([terminal("a")]);
    mocks.listLive.mockResolvedValue([liveTab("kernel-a", "s1"), liveTab("kernel-bg", "s1")]);
    mocks.ask.mockResolvedValueOnce(false);

    await disconnectSessionWithConfirm("s1", "web-01");

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    expect(mocks.disconnect).not.toHaveBeenCalled();
  });

  it("omits the count when no terminal is running", async () => {
    seed([{ id: "f", kind: "files", title: "f", sessionId: "s1", path: "/f", closable: true }]);

    await disconnectSessionWithConfirm("s1", "web-01");

    const [message] = mocks.ask.mock.calls[0];
    expect(message).not.toContain("终端进程");
    expect(mocks.disconnect).toHaveBeenCalledWith("s1");
  });
});
