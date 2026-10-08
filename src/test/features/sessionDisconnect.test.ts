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
  runningTerminalCount,
} from "../../features/terminal/sessionDisconnect";
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

    expect(await runningTerminalCount("s1")).toBe(4);
    expect(await runningTerminalCount("s2")).toBe(1);
    expect(await runningTerminalCount("nope")).toBe(0);
  });

  it("falls back to the local count when the backend list is unavailable", async () => {
    seed([terminal("a")]);
    mocks.listLive.mockRejectedValue(new Error("offline"));

    expect(await runningTerminalCount("s1")).toBe(1);
  });

  it("asks with the total count and states consequences per terminal kind", async () => {
    seed([terminal("a"), terminal("b")]);

    await disconnectSessionWithConfirm("s1", "web-01");

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const [message, options] = mocks.ask.mock.calls[0];
    expect(message).toContain("2 个正在运行的终端里");
    expect(message).toContain("普通终端的进程会被结束，无法恢复");
    expect(message).toContain("守护终端会留在「后台会话」，之后可接管");
    expect(message).toContain("web-01");
    expect(options.kind).toBe("warning");
    expect(mocks.disconnect).toHaveBeenCalledWith("s1");
  });

  it("includes an already-detached background terminal in the confirmation count", async () => {
    seed([terminal("a")]);
    mocks.listLive.mockResolvedValue([liveTab("kernel-a", "s1"), liveTab("kernel-bg", "s1")]);

    await disconnectSessionWithConfirm("s1", "web-01");

    const [message] = mocks.ask.mock.calls[0];
    expect(message).toContain("2 个正在运行的终端里");
  });

  it("does not recount a local tab whose backend twin already exited", async () => {
    seed([terminal("a")]);
    mocks.listLive.mockResolvedValue([
      liveTab("kernel-a", "s1", true),
      liveTab("kernel-bg", "s1"),
      liveTab("kernel-other", "s2"),
    ]);

    expect(await runningTerminalCount("s1")).toBe(1);

    await disconnectSessionWithConfirm("s1", "web-01");

    const [message] = mocks.ask.mock.calls[0];
    expect(message).toContain("1 个正在运行的终端里");
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
    expect(message).not.toContain("正在运行的终端");
    expect(mocks.disconnect).toHaveBeenCalledWith("s1");
  });
});
