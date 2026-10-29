/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  listDurable: vi.fn(),
  closeTab: vi.fn(),
  listSessions: vi.fn(),
}));
vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    sessionApi: { list: mocks.listSessions },
    terminalApi: { listDurable: mocks.listDurable, closeTab: mocks.closeTab, listLive: vi.fn().mockResolvedValue([]) },
  });
});

import { reattachLostTabs } from "../../features/terminal/backgroundSessionsAccess";
import { useUi, type AppTab, type SessionRuntimeInfo, type Workspace } from "../../app/store";

const session = (status: SessionRuntimeInfo["status"]): SessionRuntimeInfo =>
  ({ id: "s1", assetId: "a1", name: "box", kind: "device", status, tabs: [], createdAt: 1 }) as SessionRuntimeInfo;
const lost = { id: "t1", kind: "terminal", title: "shell", sessionId: "s1", tabId: "k1", closable: true, durable: true, exited: true } as AppTab;

function seed(status: SessionRuntimeInfo["status"]) {
  const ws: Workspace = {
    id: "ws", kind: "session", title: "box", sessionId: "s1", assetId: "a1", assetKind: "device",
    panes: [{ id: "p", tabs: [lost], activeTabId: "t1" }], activePaneId: "p", closable: true,
  };
  useUi.setState({ workspaces: [ws], activeWorkspaceId: "ws", sessions: [session(status)], toasts: [] });
  mocks.listSessions.mockResolvedValue([session(status)]);
}
const tabs = () => useUi.getState().workspaces[0].panes.flatMap((pane) => pane.tabs);

beforeEach(() => {
  vi.resetAllMocks();
  mocks.closeTab.mockResolvedValue(undefined);
});

describe("reattachLostTabs", () => {
  it("detaches the stale tab and opens the same host terminal again", async () => {
    seed("connected");
    mocks.listDurable.mockResolvedValue([{ id: "k1", dead: false, cols: 80, rows: 24 }]);
    const outcome = await reattachLostTabs(["t1"]);
    expect(outcome).toEqual({ taken: 1, failed: 0 });
    expect(mocks.closeTab).toHaveBeenCalledWith("k1", "detach", "s1");
    expect(tabs().map((tab) => [tab.kind, tab.tabId, tab.exited])).toEqual([["terminal", "k1", undefined]]);
  });

  it("marks the terminal lost and keeps the tab when the host no longer lists it", async () => {
    seed("connected");
    mocks.listDurable.mockResolvedValue([]);
    const outcome = await reattachLostTabs(["t1"]);
    expect(outcome.failed).toBe(1);
    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(tabs()[0]).toMatchObject({ id: "t1", durableLost: true });
  });

  it("changes nothing while the host is not connected", async () => {
    seed("disconnected");
    const outcome = await reattachLostTabs(["t1"]);
    expect(outcome.failed).toBe(1);
    expect(outcome.error).toContain("主机未连接");
    expect(mocks.listDurable).not.toHaveBeenCalled();
    expect(tabs()).toHaveLength(1);
  });
});
