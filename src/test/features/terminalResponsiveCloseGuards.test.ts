import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  ask: vi.fn(),
  askChoice: vi.fn(),
  closeTab: vi.fn(),
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask, askChoice: mocks.askChoice }));
vi.mock("../../ipc/commands", () => ({
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
  terminalApi: { closeTab: mocks.closeTab },
}));

import type { AppTab, Pane, Workspace } from "../../app/store";
import { requestCloseTab, useUi } from "../../app/store";

function terminal(id = "terminal"): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "s", tabId: `kernel-${id}`, closable: true };
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

describe("terminal close guards", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.askChoice.mockResolvedValue(null);
  });

  it("warns with an alertdialog-level choice before killing a running terminal", async () => {
    const tab = terminal();
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs: [tab], activeTabId: tab.id }])],
      activeWorkspaceId: "ws",
    });

    await requestCloseTab(tab.id);

    expect(mocks.askChoice).toHaveBeenCalledTimes(1);
    const [, options] = mocks.askChoice.mock.calls[0];
    expect(options.level).toBe("warning");
    expect(options.choices.some((c: { danger?: boolean }) => c.danger)).toBe(true);
    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(useUi.getState().workspaces[0].panes[0].tabs).toEqual([tab]);
  });

  it("warns once per scope when closing a workspace reclaims several terminals", async () => {
    const tabs = [terminal("t1"), terminal("t2")];
    useUi.setState({
      workspaces: [ws([{ id: "p", tabs, activeTabId: "t1" }])],
      activeWorkspaceId: "ws",
    });
    mocks.askChoice.mockResolvedValue("detach");

    await useUi.getState().closeWorkspace("ws");

    expect(mocks.askChoice).toHaveBeenCalledTimes(1);
    const [message, options] = mocks.askChoice.mock.calls[0];
    expect(message).toContain("2 个正在运行的终端");
    expect(options.level).toBe("warning");
    expect(options.choices.some((c: { danger?: boolean }) => c.danger)).toBe(true);
    expect(mocks.closeTab).toHaveBeenCalledTimes(2);
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t1", "detach");
    expect(mocks.closeTab).toHaveBeenCalledWith("kernel-t2", "detach");
  });
});
