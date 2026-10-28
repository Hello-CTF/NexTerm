/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  probeLiveness: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  vaultApi: {},
  terminalApi: { recordingStatus: vi.fn() },
  sessionApi: { probeLiveness: mocks.probeLiveness },
}));

import {
  applySessionStatusEvent,
  foregroundProbeTargets,
  probeForegroundSessions,
  sessionStatusText,
  useUi,
  type SessionRuntimeInfo,
  type Workspace,
} from "../../app/store";

function session(id: string, status: SessionRuntimeInfo["status"], extra: Partial<SessionRuntimeInfo> = {}): SessionRuntimeInfo {
  return { id, assetId: `a-${id}`, name: id, kind: "ssh", status, tabs: [], createdAt: 1, ...extra };
}

function workspace(id: string, sessionId: string, paneTabs: Array<{ active: string; tabs: Array<{ id: string; sessionId?: string }> }>): Workspace {
  return {
    id,
    kind: "session",
    title: id,
    sessionId,
    panes: paneTabs.map((pane, index) => ({
      id: `${id}-pane-${index}`,
      activeTabId: pane.active,
      tabs: pane.tabs.map((tab) => ({ id: tab.id, kind: "terminal", title: tab.id, sessionId: tab.sessionId, closable: true })),
    })),
    activePaneId: `${id}-pane-0`,
    closable: true,
  } as Workspace;
}

beforeEach(() => {
  vi.clearAllMocks();
  useUi.getState().resetSessions();
  useUi.setState({ sessions: [session("s1", "connected")], workspaces: [], activeWorkspaceId: null, toasts: [], connectProgress: {} });
  mocks.probeLiveness.mockResolvedValue(undefined);
});

describe("session liveness", () => {
  it("shows a connected session with unanswered heartbeats as unstable", () => {
    expect(sessionStatusText("connected")).toBe("已连接");
    expect(sessionStatusText("connected", "suspect")).toBe("连接不稳定");
    expect(sessionStatusText("reconnecting", "suspect")).toBe("重连中");
  });

  it("sets and clears liveness from status events", () => {
    applySessionStatusEvent({ sessionId: "s1", status: "connected", error: null, liveness: "suspect", version: 2 });
    expect(useUi.getState().sessions[0].liveness).toBe("suspect");
    applySessionStatusEvent({ sessionId: "s1", status: "connected", error: null, version: 3 });
    expect(useUi.getState().sessions[0].liveness).toBeNull();
  });

  it("targets the visible connected or reconnecting sessions of the active workspace", () => {
    const state = {
      activeWorkspaceId: "w1",
      sessions: [
        session("s1", "connected"),
        session("s2", "reconnecting"),
        session("s3", "failed"),
        session("s4", "connected"),
        session("s5", "connected"),
      ],
      workspaces: [
        workspace("w1", "s1", [
          { active: "t2", tabs: [{ id: "t1", sessionId: "s4" }, { id: "t2", sessionId: "s2" }] },
          { active: "t3", tabs: [{ id: "t3", sessionId: "s3" }] },
          { active: "t4", tabs: [{ id: "t4", sessionId: "s1" }] },
        ]),
        workspace("w2", "s5", []),
      ],
    };
    // s4 sits behind another tab, s3 failed (manual retry), s5 belongs to a hidden workspace.
    expect(foregroundProbeTargets(state).sort()).toEqual(["s1", "s2"]);
    expect(foregroundProbeTargets({ ...state, activeWorkspaceId: null })).toEqual([]);
  });

  it("probes each visible session once and tolerates probe failures", async () => {
    useUi.setState({
      sessions: [session("s1", "connected"), session("s2", "reconnecting")],
      workspaces: [workspace("w1", "s1", [{ active: "t1", tabs: [{ id: "t1", sessionId: "s2" }] }])],
      activeWorkspaceId: "w1",
    });
    mocks.probeLiveness.mockImplementation((id: string) => (id === "s2" ? Promise.reject(new Error("gone")) : Promise.resolve()));
    await probeForegroundSessions();
    expect(mocks.probeLiveness.mock.calls.map(([id]) => String(id)).sort()).toEqual(["s1", "s2"]);
  });

  it("joins a probe round that is still running instead of stacking another", async () => {
    useUi.setState({
      sessions: [session("s1", "connected")],
      workspaces: [workspace("w1", "s1", [])],
      activeWorkspaceId: "w1",
    });
    let release: () => void = () => undefined;
    mocks.probeLiveness.mockImplementation(() => new Promise<void>((resolve) => {
      release = resolve;
    }));
    const first = probeForegroundSessions();
    const second = probeForegroundSessions();
    expect(second).toBe(first);
    expect(mocks.probeLiveness).toHaveBeenCalledTimes(1);
    mocks.probeLiveness.mockResolvedValue(undefined);
    release();
    await first;
    // The joined trigger re-runs once after the round; the next idle trigger starts a fresh round.
    expect(mocks.probeLiveness).toHaveBeenCalledTimes(2);
    await probeForegroundSessions();
    expect(mocks.probeLiveness).toHaveBeenCalledTimes(3);
  });

  it("runs one trailing round for triggers that arrive while a round is in flight", async () => {
    useUi.setState({
      sessions: [session("s1", "connected"), session("s2", "connected")],
      workspaces: [workspace("w1", "s1", [])],
      activeWorkspaceId: "w1",
    });
    const releases: Array<() => void> = [];
    mocks.probeLiveness.mockImplementation(() => new Promise<void>((resolve) => {
      releases.push(resolve);
    }));
    const first = probeForegroundSessions();
    expect(mocks.probeLiveness.mock.calls.map(([id]) => String(id))).toEqual(["s1"]);
    // The visible set changes mid-round, then several triggers pile up.
    useUi.setState({ workspaces: [workspace("w1", "s2", [])] });
    void probeForegroundSessions();
    void probeForegroundSessions();
    void probeForegroundSessions();
    releases.shift()?.();
    await vi.waitFor(() => expect(releases).toHaveLength(1));
    expect(mocks.probeLiveness.mock.calls.map(([id]) => String(id))).toEqual(["s1", "s2"]);
    releases.shift()?.();
    await first;
    expect(mocks.probeLiveness).toHaveBeenCalledTimes(2);
  });
});
