/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  disconnect: vi.fn(),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));
vi.mock("../../ipc/commands", () => ({
  dbApi: {},
  vaultApi: {},
  terminalApi: {},
  sessionApi: { disconnect: mocks.disconnect },
}));

import type { AppTab, Pane, Workspace } from "../../app/store";
import { useUi } from "../../app/store";
import {
  disconnectSessionWithConfirm,
  runningTerminalCount,
} from "../../features/terminal/sessionDisconnect";

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

describe("session disconnect confirmation", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.ask.mockResolvedValue(true);
    mocks.disconnect.mockResolvedValue(undefined);
  });

  it("counts only running terminals of the affected session", () => {
    seed([
      terminal("a"),
      terminal("b"),
      { ...terminal("c"), exited: true },
      { id: "d", kind: "terminal", title: "d", sessionId: "s1", closable: true, dead: true },
      terminal("other", "s2"),
      { id: "f", kind: "files", title: "f", sessionId: "s1", path: "/f", closable: true },
    ]);
    expect(runningTerminalCount("s1")).toBe(2);
    expect(runningTerminalCount("s2")).toBe(1);
    expect(runningTerminalCount("nope")).toBe(0);
  });

  it("asks with the affected terminal count before disconnecting", async () => {
    seed([terminal("a"), terminal("b")]);

    await disconnectSessionWithConfirm("s1", "web-01");

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const [message, options] = mocks.ask.mock.calls[0];
    expect(message).toContain("2 个正在运行的终端");
    expect(message).toContain("web-01");
    expect(options.kind).toBe("warning");
    expect(mocks.disconnect).toHaveBeenCalledWith("s1");
  });

  it("cancelling the confirmation keeps the connection", async () => {
    seed([terminal("a")]);
    mocks.ask.mockResolvedValueOnce(false);

    await disconnectSessionWithConfirm("s1", "web-01");

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
