/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  sessionList: vi.fn(),
  sessionReconnect: vi.fn(),
  probeHostKey: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  vaultApi: {},
  terminalApi: { recordingStatus: vi.fn() },
  sessionApi: {
    list: mocks.sessionList,
    reconnect: mocks.sessionReconnect,
    probeHostKey: mocks.probeHostKey,
  },
}));

import {
  applySessionStatusEvent,
  assetConnectionProblemKey,
  reconnectSessionAndWait,
  useUi,
  type SessionRuntimeInfo,
  type Workspace,
} from "../../app/store";
import { deferred } from "./reactTestUtils";

function session(
  status: SessionRuntimeInfo["status"],
  extra: Partial<SessionRuntimeInfo> = {},
): SessionRuntimeInfo {
  return {
    id: "s1",
    assetId: "a1",
    name: "web-01",
    kind: "ssh",
    status,
    tabs: [],
    createdAt: 1,
    ...extra,
  };
}

function workspace(): Workspace {
  return {
    id: "ws1",
    kind: "session",
    title: "web-01",
    sessionId: "s1",
    assetId: "a1",
    assetKind: "ssh",
    panes: [{ id: "p1", tabs: [], activeTabId: null }],
    activePaneId: "p1",
    splitRatio: 0.5,
    closable: true,
  };
}

function recordProblem(): void {
  useUi.getState().recordConnectionProblem({
    connectionKey: assetConnectionProblemKey("a1"),
    createdAt: Date.now(),
    source: "connect",
    operation: "连接",
    target: { title: "web-01", identity: "web-01", kind: "ssh", assetId: "a1" },
    summary: "此前连接失败",
    detail: "此前连接失败",
    canRetryRead: false,
    connection: { kind: "asset", asset: { id: "a1", name: "web-01", kind: "ssh" } },
  });
}

function workspaceProblems(): unknown[] {
  return useUi.getState().workspaceRuntime["ws1"]?.problems ?? [];
}

function disconnectToast(): { text: string; action?: { label: string; onSelect: () => void } } {
  const toast = useUi.getState().toasts.find((item) => item.action?.label === "重新连接");
  if (!toast) throw new Error("disconnect toast not found");
  return toast;
}

beforeEach(() => {
  vi.clearAllMocks();
  useUi.getState().resetSessions();
  useUi.setState({
    sessions: [session("disconnected")],
    workspaces: [workspace()],
    activeWorkspaceId: "ws1",
    toasts: [],
    connectProgress: {},
  });
  mocks.sessionReconnect.mockResolvedValue(true);
  mocks.probeHostKey.mockResolvedValue({ state: "known" });
  applySessionStatusEvent({
    sessionId: "s1",
    status: "disconnected",
    error: "connection reset",
    errorCode: "disconnected",
    version: 1,
  });
});

describe("disconnect toast reconnect", () => {
  it("clears the target connection problem and merges the fresh session on success", async () => {
    recordProblem();
    expect(workspaceProblems()).toHaveLength(1);
    mocks.sessionList
      .mockResolvedValueOnce([session("reconnecting")])
      .mockResolvedValue([session("connected")]);

    disconnectToast().action?.onSelect();

    await vi.waitFor(() => expect(useUi.getState().sessions[0]?.status).toBe("connected"));
    expect(mocks.sessionReconnect).toHaveBeenCalledTimes(1);
    expect(mocks.sessionReconnect).toHaveBeenCalledWith("s1");
    expect(workspaceProblems()).toHaveLength(0);
    expect(useUi.getState().connectionProblems).toEqual({});
    expect(useUi.getState().toasts.some((toast) => toast.text.includes("重连失败"))).toBe(false);
  });

  it("records the target problem and keeps the failure visible when the reconnect fails", async () => {
    mocks.sessionList
      .mockResolvedValueOnce([session("reconnecting")])
      .mockResolvedValue([session("failed", { error: "认证失败", errorCode: "auth", attempt: 1 })]);

    disconnectToast().action?.onSelect();

    await vi.waitFor(() => expect(useUi.getState().toasts.some((toast) => toast.text.includes("重连失败"))).toBe(true));
    const problems = workspaceProblems();
    expect(problems).toHaveLength(1);
    expect(problems[0]).toMatchObject({ summary: "认证失败", source: "connect" });
    // The failed list frame settles the problem, not the session list: the entry stays disconnected.
    expect(useUi.getState().sessions[0]?.status).toBe("disconnected");
  });

  it("adds no problem, no failure toast, and no stale session when the reconnect is canceled by an account switch", async () => {
    recordProblem();
    const pendingList = deferred<SessionRuntimeInfo[]>();
    mocks.sessionList.mockReturnValue(pendingList.promise);

    disconnectToast().action?.onSelect();
    await vi.waitFor(() => expect(mocks.sessionReconnect).toHaveBeenCalledTimes(1));

    useUi.getState().resetAccountWorkspaces();
    pendingList.resolve([session("failed")]);

    await flushMicrotasks();
    expect(useUi.getState().connectionProblems).toEqual({});
    expect(useUi.getState().workspaceRuntime).toEqual({});
    expect(useUi.getState().sessions).toEqual([]);
    expect(useUi.getState().toasts.some((toast) => toast.text.includes("重连失败"))).toBe(false);
  });

  it("joins the in-flight reconnect instead of starting a duplicate", async () => {
    const pendingList = deferred<SessionRuntimeInfo[]>();
    mocks.sessionList.mockReturnValue(pendingList.promise);

    const direct = reconnectSessionAndWait(session("disconnected"), { timeoutMs: 2_000, intervalMs: 1 });
    disconnectToast().action?.onSelect();
    await vi.waitFor(() => expect(mocks.sessionReconnect).toHaveBeenCalledTimes(1));

    pendingList.resolve([session("reconnecting")]);
    await vi.waitFor(() => expect(mocks.sessionList.mock.calls.length).toBeGreaterThanOrEqual(2));
    mocks.sessionList.mockResolvedValue([session("connected")]);

    await expect(direct).resolves.toMatchObject({ status: "connected" });
    await vi.waitFor(() => expect(useUi.getState().sessions[0]?.status).toBe("connected"));
    expect(mocks.sessionReconnect).toHaveBeenCalledTimes(1);
  });
});

async function flushMicrotasks(): Promise<void> {
  for (let i = 0; i < 10; i++) await Promise.resolve();
}
