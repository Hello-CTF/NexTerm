/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  disconnect: vi.fn(),
  list: vi.fn(),
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    sessionApi: { ...actual.sessionApi, disconnect: mocks.disconnect, list: mocks.list },
  };
});

import { useUi, type SessionRuntimeInfo, type Workspace } from "../../app/store";

function session(status: SessionRuntimeInfo["status"]): SessionRuntimeInfo {
  return { id: "s1", assetId: "a1", name: "web-01", kind: "ssh", status, tabs: [], createdAt: 1 };
}

function workspace(id: string): Workspace {
  return {
    id,
    kind: "session",
    title: "web-01",
    sessionId: "s1",
    assetId: "a1",
    assetKind: "ssh",
    panes: [{ id: `${id}-p`, tabs: [], activeTabId: null }],
    activePaneId: `${id}-p`,
    closable: true,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.disconnect.mockResolvedValue(undefined);
  mocks.list.mockResolvedValue([]);
  useUi.setState({ toasts: [], workspaceRuntime: {}, activeWorkspaceId: "w1" });
});

describe("closing the last workspace of a session", () => {
  it.each(["failed", "disconnected"] as const)("releases a %s session no workspace uses", async (status) => {
    mocks.list.mockResolvedValue([session(status)]);
    useUi.setState({ sessions: [session(status)], workspaces: [workspace("w1")] });
    expect(await useUi.getState().closeWorkspace("w1")).toBe(true);
    await vi.waitFor(() => expect(mocks.disconnect).toHaveBeenCalledWith("s1"));
  });

  it("keeps a dead session another workspace still uses", async () => {
    mocks.list.mockResolvedValue([session("failed")]);
    useUi.setState({ sessions: [session("failed")], workspaces: [workspace("w1"), workspace("w2")] });
    expect(await useUi.getState().closeWorkspace("w1")).toBe(true);
    await Promise.resolve();
    expect(mocks.disconnect).not.toHaveBeenCalled();
  });

  it("leaves a connected session to the idle sweep", async () => {
    mocks.list.mockResolvedValue([session("connected")]);
    useUi.setState({ sessions: [session("connected")], workspaces: [workspace("w1")] });
    expect(await useUi.getState().closeWorkspace("w1")).toBe(true);
    await Promise.resolve();
    expect(mocks.disconnect).not.toHaveBeenCalled();
  });
});
