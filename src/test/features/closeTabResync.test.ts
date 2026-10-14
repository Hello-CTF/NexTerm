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

describe("session list resynchronization after closeTab", () => {
  it("closing the last terminal tab refreshes sessions with no tabs so the status bar can show idle", async () => {
    seedWorkspaceWithTerminal();
    const ok = await useUi.getState().closeTab("t1", "kill");
    expect(ok).toBe(true);
    expect(mocks.closeTab).toHaveBeenCalledWith("bt1", "kill");
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
});

describe("terminalApi.closeTab call-site static guard", () => {
  it("every file that closes a terminal tab resynchronizes the session list", () => {
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
