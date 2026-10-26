/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  closeTab: vi.fn(),
  sessionList: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    terminalApi: { ...actual.terminalApi, closeTab: mocks.closeTab },
    sessionApi: { ...actual.sessionApi, list: mocks.sessionList },
  };
});

import { useUi } from "../../app/store";
import type { SessionInfo } from "../../ipc/commands";

const SESSION: SessionInfo = {
  id: "s1",
  assetId: "asset-1",
  name: "web-01",
  kind: "ssh",
  status: "connected",
  tabs: ["bt1"],
  createdAt: 0,
};

function seedWorkspaceWithTerminal() {
  useUi.setState({
    sessions: [SESSION],
    workspaces: [
      {
        id: "ws-1",
        kind: "session",
        sessionId: "s1",
        title: "web-01",
        assetId: "asset-1",
        assetKind: "ssh",
        panes: [
          {
            id: "p1",
            tabs: [
              {
                id: "t1",
                kind: "terminal" as const,
                title: "Terminal 1",
                sessionId: "s1",
                tabId: "bt1",
                closable: true,
              },
            ],
            activeTabId: "t1",
          },
        ],
        activePaneId: "p1",
        closable: true,
      },
    ],
    activeWorkspaceId: "ws-1",
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  useUi.setState({ sessions: [], workspaces: [], activeWorkspaceId: null, toasts: [] });
  mocks.closeTab.mockResolvedValue(undefined);
  mocks.sessionList.mockResolvedValue([{ ...SESSION, tabs: [] }]);
});

describe("session list resynchronization after closeTab", () => {
  it("closing the last terminal tab refreshes sessions with no tabs so the status bar can show idle", async () => {
    seedWorkspaceWithTerminal();
    const ok = await useUi.getState().closeTab("t1", "kill");
    expect(ok).toBe(true);
    expect(mocks.closeTab).toHaveBeenCalledWith("bt1", "kill", "s1");
    await vi.waitFor(() => {
      expect(useUi.getState().sessions[0]?.tabs).toEqual([]);
    });
  });

  it("detaching to the background refreshes sessions while retaining the tab for background-session takeover", async () => {
    seedWorkspaceWithTerminal();
    mocks.sessionList.mockResolvedValue([{ ...SESSION, tabs: ["bt1"] }]);
    const ok = await useUi.getState().closeTab("t1", "detach");
    expect(ok).toBe(true);
    await vi.waitFor(() => {
      expect(mocks.sessionList).toHaveBeenCalled();
    });
    expect(useUi.getState().sessions[0]?.tabs).toEqual(["bt1"]);
  });

  it("refreshes sessions after a failed close while preserving the tab and its error", async () => {
    seedWorkspaceWithTerminal();
    mocks.closeTab.mockRejectedValueOnce(new Error("boom"));
    const ok = await useUi.getState().closeTab("t1", "detach");
    expect(ok).toBe(false);
    await vi.waitFor(() => {
      expect(mocks.sessionList).toHaveBeenCalled();
    });
    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs[0]?.closeError).toBe("boom");
  });

  it.each(["dead", "exited"] as const)("removes an already %s container exec when its stream has disconnected", async (state) => {
    seedWorkspaceWithTerminal();
    useUi.getState().updateTab("t1", { containerId: "container-1", [state]: true });
    mocks.closeTab.mockRejectedValueOnce({ code: "disconnected", message: "docker stream closed" });

    await expect(useUi.getState().closeTab("t1", "kill")).resolves.toBe(true);

    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs).toEqual([]);
    expect(useUi.getState().toasts).toEqual([]);
  });

  it("removes a container exec that no longer exists on the server", async () => {
    seedWorkspaceWithTerminal();
    useUi.getState().updateTab("t1", { containerId: "container-1" });
    mocks.closeTab.mockRejectedValueOnce({ code: "not_found", message: "tab not found" });

    await expect(useUi.getState().closeTab("t1", "kill")).resolves.toBe(true);

    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs).toEqual([]);
    expect(useUi.getState().toasts).toEqual([]);
  });

  it.each(["ssh", "device"] as const)("detaches a dead %s tab by default after its session has disconnected", async (kind) => {
    seedWorkspaceWithTerminal();
    useUi.setState((state) => ({
      sessions: [{ ...SESSION, kind }],
      workspaces: state.workspaces.map((workspace) => ({ ...workspace, assetKind: kind })),
    }));
    useUi.getState().updateTab("t1", { dead: true });
    mocks.closeTab.mockRejectedValueOnce({ code: "disconnected", message: "session unavailable" });

    await expect(useUi.getState().closeTab("t1")).resolves.toBe(true);

    expect(mocks.closeTab).toHaveBeenCalledWith("bt1", "detach", "s1");
    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs).toEqual([]);
    expect(useUi.getState().toasts.map((toast) => toast.text)).toContain(
      "本地标签已关闭；远端持久终端可能仍在运行，可在后台会话中查看",
    );
  });

  it("preserves the local default close semantics", async () => {
    seedWorkspaceWithTerminal();
    useUi.setState((state) => ({
      sessions: [{ ...SESSION, kind: "local" }],
      workspaces: state.workspaces.map((workspace) => ({ ...workspace, assetKind: "local" })),
    }));

    await expect(useUi.getState().closeTab("t1")).resolves.toBe(true);

    expect(mocks.closeTab).toHaveBeenCalledWith("bt1", undefined);
    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs).toEqual([]);
  });

  it("closes an SSH tab that the server no longer knows", async () => {
    seedWorkspaceWithTerminal();
    mocks.closeTab.mockRejectedValueOnce({ code: "not_found", message: "tab not found" });

    await expect(useUi.getState().closeTab("t1", "kill")).resolves.toBe(true);

    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs).toEqual([]);
    expect(useUi.getState().toasts).toEqual([]);
  });

  it("preserves a running SSH tab when the close request only loses its connection", async () => {
    seedWorkspaceWithTerminal();
    mocks.closeTab.mockRejectedValueOnce({ code: "disconnected", message: "network unavailable" });

    await expect(useUi.getState().closeTab("t1", "kill")).resolves.toBe(false);

    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs[0]?.id).toBe("t1");
    expect(useUi.getState().toasts[0]?.text).toContain("network unavailable");
  });

  it("preserves a running container exec and reports a failed close when the transport is unavailable", async () => {
    seedWorkspaceWithTerminal();
    useUi.getState().updateTab("t1", { containerId: "container-1" });
    mocks.closeTab.mockRejectedValueOnce({ code: "disconnected", message: "network unavailable" });

    await expect(useUi.getState().closeTab("t1", "kill")).resolves.toBe(false);

    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs[0]?.id).toBe("t1");
    expect(useUi.getState().toasts[0]?.text).toContain("network unavailable");
  });
});
