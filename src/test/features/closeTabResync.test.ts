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
                title: "终端 1",
                sessionId: "s1",
                tabId: "bt1",
                closable: true,
              },
            ],
            activeTabId: "t1",
          },
        ],
        activePaneId: "p1",
        splitRatio: 0.5,
        closable: true,
      },
    ],
    activeWorkspaceId: "ws-1",
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  useUi.setState({ sessions: [], workspaces: [], activeWorkspaceId: null });
  mocks.closeTab.mockResolvedValue(undefined);
  mocks.sessionList.mockResolvedValue([{ ...SESSION, tabs: [] }]);
});

describe("closeTab 后会话列表同步", () => {
  it("关闭最后一个终端标签后 sessions 刷新为无标签(状态栏可显示空闲)", async () => {
    seedWorkspaceWithTerminal();
    const ok = await useUi.getState().closeTab("t1", "kill");
    expect(ok).toBe(true);
    expect(mocks.closeTab).toHaveBeenCalledWith("bt1", "kill");
    await vi.waitFor(() => {
      expect(useUi.getState().sessions[0]?.tabs).toEqual([]);
    });
  });

  it("转入后台后 sessions 刷新仍保留该标签(后台会话可接管)", async () => {
    seedWorkspaceWithTerminal();
    mocks.sessionList.mockResolvedValue([{ ...SESSION, tabs: ["bt1"] }]);
    const ok = await useUi.getState().closeTab("t1", "detach");
    expect(ok).toBe(true);
    await vi.waitFor(() => {
      expect(mocks.sessionList).toHaveBeenCalled();
    });
    expect(useUi.getState().sessions[0]?.tabs).toEqual(["bt1"]);
  });
});

describe("terminalApi.closeTab 调用点静态守卫", () => {
  it("每个关闭终端的调用点所在文件都刷新会话列表", () => {
    const sources = import.meta.glob("../../**/*.{ts,tsx}", {
      query: "?raw",
      import: "default",
      eager: true,
    }) as Record<string, string>;
    const offenders = Object.entries(sources)
      .filter(([path]) => !path.includes("/test/") && !path.includes("/ipc/bindings/"))
      .filter(([, text]) => text.includes("terminalApi.closeTab(") && !text.includes("resyncSessions"))
      .map(([path]) => path);
    expect(offenders).toEqual([]);
  });
});
