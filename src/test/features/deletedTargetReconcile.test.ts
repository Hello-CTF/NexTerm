/** @vitest-environment jsdom */

import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../ipc/commands", () => ({
  assetApi: {},
  dbApi: {},
  sessionApi: {},
  terminalApi: {},
  vaultApi: {},
}));

import { useUi, type AppTab, type Workspace } from "../../app/store";

function terminalTab(id: string): AppTab {
  return { id, kind: "terminal", title: id, sessionId: `session-${id}`, tabId: `kernel-${id}`, closable: true };
}

function hostWorkspace(id: string, assetId: string | undefined, patch: Partial<Workspace> = {}): Workspace {
  const paneId = `${id}-pane`;
  const tabId = `${id}-t1`;
  return {
    id,
    kind: "session",
    title: id,
    sessionId: `session-${id}`,
    assetId,
    panes: [{ id: paneId, tabs: [terminalTab(tabId)], activeTabId: tabId }],
    activePaneId: paneId,
    splitRatio: 0.5,
    closable: true,
    ...patch,
  };
}

const assetOf = (id: string, extra: Record<string, unknown> = {}) => ({ id, name: id, kind: "ssh", ...extra });

beforeEach(() => {
  useUi.setState({
    workspaces: [],
    activeWorkspaceId: null,
    sessions: [],
    accountEpoch: 0,
    knownAssetIds: [],
    knownAssetsEpoch: -1,
  });
});

describe("reconcileDeletedTargets", () => {
  it("marks only the workspaces whose previously seen asset disappeared, and preserves workspace, output, session, and selection", () => {
    const gone = hostWorkspace("ws-gone", "asset-2");
    useUi.setState({
      workspaces: [hostWorkspace("ws-keep", "asset-1"), gone],
      sessions: [{ id: "session-ws-gone", assetId: "asset-2", name: "s", kind: "ssh", status: "connected", tabs: [], createdAt: 1 }],
      activeWorkspaceId: "ws-gone",
    });

    useUi.getState().reconcileDeletedTargets([assetOf("asset-1"), assetOf("asset-2")], 0);
    useUi.getState().reconcileDeletedTargets([assetOf("asset-1")], 0);

    const after = useUi.getState().workspaces;
    expect(after.find((w) => w.id === "ws-keep")?.deletedTarget).toBeUndefined();
    const marked = after.find((w) => w.id === "ws-gone");
    expect(marked?.deletedTarget).toBe(true);
    expect(marked?.sessionId).toBe(gone.sessionId);
    expect(marked?.panes).toEqual(gone.panes);
    expect(marked?.panes[0]?.tabs[0]?.sessionId).toBe(gone.panes[0]?.tabs[0]?.sessionId);
    expect(useUi.getState().activeWorkspaceId).toBe("ws-gone");
    expect(useUi.getState().sessions).toHaveLength(1);
  });

  it("clears the mark when the asset reappears in the authoritative list", () => {
    useUi.setState({ workspaces: [hostWorkspace("ws", "asset-1")] });

    useUi.getState().reconcileDeletedTargets([assetOf("asset-1")], 0);
    useUi.getState().reconcileDeletedTargets([], 0);
    expect(useUi.getState().workspaces[0]?.deletedTarget).toBe(true);

    useUi.getState().reconcileDeletedTargets([assetOf("asset-1")], 0);
    expect(useUi.getState().workspaces[0]?.deletedTarget).toBeUndefined();
  });

  it("ignores a snapshot from a stale account epoch so account switches never mislabel", () => {
    useUi.setState({ workspaces: [hostWorkspace("ws", "asset-1")], accountEpoch: 2 });

    useUi.getState().reconcileDeletedTargets([], 1);
    expect(useUi.getState().workspaces[0]?.deletedTarget).toBeUndefined();
    expect(useUi.getState().knownAssetsEpoch).toBe(-1);

    useUi.getState().reconcileDeletedTargets([assetOf("asset-1")], 2);
    useUi.getState().reconcileDeletedTargets([], 2);
    expect(useUi.getState().workspaces[0]?.deletedTarget).toBe(true);
  });

  it("treats an asset update with new fields as present, not deleted", () => {
    useUi.setState({ workspaces: [hostWorkspace("ws", "asset-1")] });

    useUi.getState().reconcileDeletedTargets([assetOf("asset-1")], 0);
    useUi.getState().reconcileDeletedTargets([assetOf("asset-1", { name: "renamed", updatedAt: 99, generation: 3 })], 0);

    expect(useUi.getState().workspaces[0]?.deletedTarget).toBeUndefined();
  });

  it("never marks targets that were never present in an authoritative snapshot", () => {
    useUi.setState({
      workspaces: [hostWorkspace("ws-quick", "quick-target"), hostWorkspace("ws-tools", undefined, { kind: "tools" })],
    });

    useUi.getState().reconcileDeletedTargets([], 0);
    useUi.getState().reconcileDeletedTargets([], 0);

    expect(useUi.getState().workspaces.every((w) => w.deletedTarget === undefined)).toBe(true);
  });

  it("marks db workspaces bound to a deleted asset", () => {
    useUi.setState({ workspaces: [hostWorkspace("ws-db", "asset-db", { kind: "db", connId: "conn-1" })] });

    useUi.getState().reconcileDeletedTargets([assetOf("asset-db")], 0);
    useUi.getState().reconcileDeletedTargets([], 0);

    expect(useUi.getState().workspaces[0]?.deletedTarget).toBe(true);
  });

  it("keeps a restore-time mark while the asset stays absent and clears it on reappearance", () => {
    useUi.setState({ workspaces: [hostWorkspace("ws", "asset-1", { deletedTarget: true })] });

    useUi.getState().reconcileDeletedTargets([], 0);
    expect(useUi.getState().workspaces[0]?.deletedTarget).toBe(true);

    useUi.getState().reconcileDeletedTargets([assetOf("asset-1")], 0);
    expect(useUi.getState().workspaces[0]?.deletedTarget).toBeUndefined();
  });

  it("keeps the local delete path marking only the deleted asset", () => {
    useUi.setState({ workspaces: [hostWorkspace("ws-a", "asset-1"), hostWorkspace("ws-b", "asset-2")] });

    useUi.getState().markDeletedAssetWorkspaces("asset-1");

    const after = useUi.getState().workspaces;
    expect(after.find((w) => w.id === "ws-a")?.deletedTarget).toBe(true);
    expect(after.find((w) => w.id === "ws-b")?.deletedTarget).toBeUndefined();
  });

  it("keeps the same state reference when nothing changes", () => {
    useUi.setState({ workspaces: [hostWorkspace("ws", "asset-1")] });
    useUi.getState().reconcileDeletedTargets([assetOf("asset-1")], 0);
    const before = useUi.getState();

    useUi.getState().reconcileDeletedTargets([assetOf("asset-1")], 0);

    expect(useUi.getState()).toBe(before);
  });

  it("never marks on a session reconnect; only the authoritative list marks", () => {
    useUi.setState({ workspaces: [hostWorkspace("ws", "asset-1")] });

    useUi.getState().updateWorkspaceSession("ws", "session-reconnected");

    const workspace = useUi.getState().workspaces[0];
    expect(workspace?.sessionId).toBe("session-reconnected");
    expect(workspace?.deletedTarget).toBeUndefined();
  });
});
