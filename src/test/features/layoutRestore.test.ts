/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { captureDeviceLayout, deviceLayoutStorageKey } from "../../app/layoutFocus";
import { deferred, flushUntil } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  put: vi.fn(),
  assets: vi.fn(),
  asset: vi.fn(),
  connect: vi.fn(),
  disconnect: vi.fn(),
  choice: vi.fn(),
  user: { id: "review-user" } as { id: string } | null,
}));

vi.mock("../../ipc/commands", async (importOriginal) => {
  const { createCommandsMock } = await import("../helpers/mockCommands");
  return createCommandsMock(await importOriginal<typeof import("../../ipc/commands")>(), {
    layoutApi: { get: mocks.get, put: mocks.put },
    assetApi: { list: mocks.assets, get: mocks.asset },
    terminalApi: { listLive: vi.fn(async () => []) },
    sessionApi: { connect: mocks.connect, disconnect: mocks.disconnect },
  });
});
vi.mock("../../ipc/events", () => ({ listenEvent: vi.fn(async () => () => {}) }));
vi.mock("../../ui/dialogs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../ui/dialogs")>()),
  askChoice: mocks.choice,
}));
vi.mock("../../features/auth/store", () => ({
  useAuth: { getState: () => ({ user: mocks.user }) },
}));

function remoteLayout(assetId: string) {
  return {
    v: 4,
    leftOpen: true,
    leftMode: "assets",
    rightOpen: false,
    leftWidth: 248,
    rightWidth: 352,
    activeWorkspaceId: "remote-workspace",
    aiBoards: {},
    workspaces: [{
      id: "remote-workspace",
      kind: "session",
      title: "远端工作区",
      sessionId: "old-session",
      assetId,
      assetKind: "ssh",
      panes: [{
        id: "remote-pane",
        tabs: [{
          id: "remote-terminal",
          kind: "terminal",
          title: "终端",
          sessionId: "old-session",
          assetId,
          tabId: "old-tab",
          containerId: undefined as string | undefined,
          closable: true,
        }],
        activeTabId: "remote-terminal",
      }],
      activePaneId: "remote-pane",
      closable: true,
    }],
  };
}

async function boot(assetId: string, data: unknown = remoteLayout(assetId)) {
  vi.resetModules();
  mocks.get.mockResolvedValue({ revision: 1, updatedAt: 1, data });
  const layout = await import("../../app/layout");
  const { useUi } = await import("../../app/store");
  layout.startLayoutSync();
  await layout.layoutBootstrapped;
  return { layout, useUi };
}

beforeEach(() => {
  vi.clearAllMocks();
  window.localStorage.clear();
  mocks.user = { id: "review-user" };
  mocks.assets.mockResolvedValue([{ id: "known-asset", kind: "ssh" }]);
  mocks.asset.mockResolvedValue({ id: "known-asset", name: "已知资产", kind: "ssh", credId: null });
  mocks.connect.mockResolvedValue({ id: "new-session", assetId: "known-asset", kind: "ssh", status: "connected", tabs: [], createdAt: 2 });
  mocks.disconnect.mockResolvedValue(undefined);
  mocks.choice.mockResolvedValue("later");
  mocks.put.mockResolvedValue({ saved: true, revision: 2, conflict: false });
});

afterEach(() => {
  vi.resetModules();
});

describe("new-device layout restore", () => {
  it("keeps the locally closed AI panel closed across boot and remote layout updates", async () => {
    window.localStorage.setItem("nexterm.layout.v1", JSON.stringify({ rightOpen: false }));
    const cloud = { ...remoteLayout("known-asset"), rightOpen: true, workspaces: [], activeWorkspaceId: null };
    const { layout, useUi } = await boot("known-asset", cloud);
    expect(useUi.getState().rightOpen).toBe(false);

    mocks.get.mockResolvedValue({ revision: 2, updatedAt: 2, data: { ...cloud, leftWidth: 260 } });
    layout.onRemoteChange(null);
    await flushUntil(() => mocks.get.mock.calls.length === 2);
    expect(useUi.getState().leftWidth).toBe(248);
    expect(useUi.getState().rightOpen).toBe(false);
  });

  it("keeps device focus when previewing a restored account layout", async () => {
    const local = remoteLayout("known-asset");
    local.workspaces[0].id = "local-workspace";
    local.activeWorkspaceId = "local-workspace";
    window.localStorage.setItem(
      deviceLayoutStorageKey("review-user"),
      JSON.stringify(captureDeviceLayout(local as unknown as Parameters<typeof captureDeviceLayout>[0])),
    );
    const cloud = remoteLayout("known-asset");
    const second = remoteLayout("second-asset");
    second.workspaces[0].id = "second-workspace";
    second.workspaces[0].panes[0].id = "second-pane";
    second.workspaces[0].panes[0].tabs[0].id = "second-terminal";
    second.workspaces[0].activePaneId = "second-pane";
    second.workspaces[0].panes[0].activeTabId = "second-terminal";
    cloud.workspaces.push(...second.workspaces);
    cloud.activeWorkspaceId = "second-workspace";
    mocks.choice.mockResolvedValue("restore");

    const { layout, useUi } = await boot("known-asset", cloud);
    await flushUntil(() => layout.layoutRestoreStatus().preview);
    expect(useUi.getState().activeWorkspaceId).toBe("remote-workspace");
  });

  it("holds cloud layout until confirmation, previews it, and supports undo", async () => {
    const { layout, useUi } = await boot("known-asset");
    await flushUntil(() => mocks.choice.mock.calls.length === 1);
    expect(layout.layoutRestoreStatus()).toEqual({ pending: true, preview: false, count: 1 });
    expect(useUi.getState().workspaces).toEqual([]);
    await layout.flushLayout();
    expect(mocks.put).not.toHaveBeenCalled();
    expect(mocks.choice.mock.calls[0][0]).toContain("远端工作区：资产目标，连接状态需重新确认");

    mocks.choice.mockResolvedValue("restore");
    await layout.reviewPendingLayoutRestore();
    expect(layout.layoutRestoreStatus()).toEqual({ pending: false, preview: true, count: 1 });
    expect(useUi.getState().workspaces[0]?.id).toBe("remote-workspace");
    expect(useUi.getState().workspaces[0]?.restorePending).toBe(true);
    expect(useUi.getState().workspaces[0]?.sessionId).toBeUndefined();
    expect(useUi.getState().workspaces[0]?.panes[0].tabs[0]?.tabId).toBeUndefined();
    expect(window.localStorage.getItem("nexterm:layout-restored-v1:review-user")).toBeNull();

    layout.undoLayoutRestore();
    expect(useUi.getState().workspaces).toEqual([]);
    expect(layout.layoutRestoreStatus().pending).toBe(true);
    await layout.flushLayout();
    expect(mocks.put).not.toHaveBeenCalled();

    await layout.reviewPendingLayoutRestore();
    await layout.finishLayoutRestore();
    expect(layout.layoutRestoreStatus()).toEqual({ pending: false, preview: false, count: 0 });
    expect(window.localStorage.getItem("nexterm:layout-restored-v1:review-user")).toBe("accepted");
  });

  it("keeps missing targets visible without restoring their terminal attachment", async () => {
    mocks.choice.mockResolvedValue("restore");
    const { layout, useUi } = await boot("deleted-asset");
    await flushUntil(() => layout.layoutRestoreStatus().preview);
    const restored = useUi.getState().workspaces[0];
    expect(restored?.deletedTarget).toBe(true);
    expect(restored?.sessionId).toBeUndefined();
    expect(restored?.panes[0].tabs).toHaveLength(1);
    expect(restored?.panes[0].tabs[0]?.restorePending).toBe(true);
    expect(restored?.panes[0].tabs[0]?.sessionId).toBeUndefined();
    expect(restored?.panes[0].tabs[0]?.tabId).toBeUndefined();
    expect(mocks.choice.mock.calls[0][0]).toContain("原资产已不存在，旧连接将跳过");
  });

  it("keeps container identity but does not attach an old exec tab after reconnect", async () => {
    mocks.choice.mockResolvedValue("restore");
    const candidate = remoteLayout("known-asset");
    candidate.workspaces[0].panes[0].tabs[0].containerId = "old-container";
    const { layout, useUi } = await boot("known-asset", candidate);
    await flushUntil(() => layout.layoutRestoreStatus().preview);
    const { reconnectRestoredWorkspace } = await import("../../app/store");
    const outcome = await reconnectRestoredWorkspace("remote-workspace");
    expect(outcome.ok).toBe(true);
    const tab = useUi.getState().workspaces[0].panes[0].tabs[0];
    expect(tab.containerId).toBe("old-container");
    expect(tab.sessionId).toBe("new-session");
    expect(tab.tabId).toBeUndefined();
    expect(tab.restorePending).toBe(true);
  });

  it("does not carry an old account's pending dialog or layout into the next account", async () => {
    const oldChoice = deferred<string | null>();
    mocks.choice.mockReturnValueOnce(oldChoice.promise).mockResolvedValue("later");
    const { layout, useUi } = await boot("known-asset");
    await flushUntil(() => mocks.choice.mock.calls.length === 1);
    useUi.getState().openAppChoice({
      title: "恢复账号工作区", message: "旧账号布局", options: [], level: "info",
      resolve: vi.fn(),
    });
    const closeOld = useUi.getState().appChoice!.resolve;

    mocks.user = { id: "next-user" };
    layout.advanceLayoutAccount();
    useUi.getState().resetAccountWorkspaces();
    expect(closeOld).toHaveBeenCalledWith(null, false);
    expect(useUi.getState().appChoice).toBeNull();
    expect(layout.layoutRestoreStatus()).toEqual({ pending: false, preview: false, count: 0 });

    mocks.get.mockResolvedValue({ revision: 4, updatedAt: 4, data: remoteLayout("second-asset") });
    layout.onRemoteChange(null);
    await flushUntil(() => mocks.choice.mock.calls.length === 2);
    oldChoice.resolve("restore");
    await flushUntil(() => layout.layoutRestoreStatus().pending);
    expect(layout.layoutRestoreStatus().preview).toBe(false);
    expect(useUi.getState().workspaces).toEqual([]);
    expect(window.localStorage.getItem("nexterm:layout-restored-v1:review-user")).toBeNull();
    expect(window.localStorage.getItem("nexterm:layout-restored-v1:next-user")).toBeNull();
  });

  it("reverts a preview when the cloud revision changes and requires a fresh review", async () => {
    mocks.choice.mockResolvedValueOnce("restore").mockResolvedValue("later");
    const { layout, useUi } = await boot("known-asset");
    await flushUntil(() => layout.layoutRestoreStatus().preview);
    mocks.get.mockResolvedValue({ revision: 2, updatedAt: 2, data: remoteLayout("second-asset") });
    layout.onRemoteChange({ revision: 2 });
    await flushUntil(() => layout.layoutRestoreStatus().pending && layout.layoutRestoreStatus().preview);
    await layout.finishLayoutRestore();
    await flushUntil(() => mocks.choice.mock.calls.length === 2);
    expect(layout.layoutRestoreStatus()).toEqual({ pending: true, preview: false, count: 1 });
    expect(useUi.getState().workspaces).toEqual([]);
    expect(mocks.put).not.toHaveBeenCalled();
    expect(window.localStorage.getItem("nexterm:layout-restored-v1:review-user")).toBeNull();
  });

  it("checks the cloud revision on Finish even when its change event was missed", async () => {
    mocks.choice.mockResolvedValueOnce("restore").mockResolvedValue("later");
    const { layout, useUi } = await boot("known-asset");
    await flushUntil(() => layout.layoutRestoreStatus().preview);
    mocks.get.mockResolvedValue({ revision: 2, updatedAt: 2, data: remoteLayout("second-asset") });
    await layout.finishLayoutRestore();
    expect(layout.layoutRestoreStatus()).toEqual({ pending: true, preview: false, count: 1 });
    expect(useUi.getState().workspaces).toEqual([]);
    expect(mocks.put).not.toHaveBeenCalled();
  });

  it("does not overwrite a newer cloud layout after a Finish write conflict", async () => {
    mocks.choice.mockResolvedValueOnce("restore").mockResolvedValue("later");
    const { layout } = await boot("known-asset");
    await flushUntil(() => layout.layoutRestoreStatus().preview);
    mocks.get
      .mockResolvedValueOnce({ revision: 1, updatedAt: 1, data: remoteLayout("known-asset") })
      .mockResolvedValueOnce({ revision: 2, updatedAt: 2, data: remoteLayout("second-asset") });
    mocks.put.mockResolvedValueOnce({ saved: false, revision: 2, conflict: true });
    await layout.finishLayoutRestore();
    expect(mocks.put).toHaveBeenCalledTimes(1);
    expect(mocks.put.mock.calls[0][1]).toBe(1);
    expect(layout.layoutRestoreStatus()).toEqual({ pending: true, preview: false, count: 1 });
    expect(window.localStorage.getItem("nexterm:layout-restored-v1:review-user")).toBeNull();
  });

  it("does not accept a Finish write when a newer cloud revision arrives during the write", async () => {
    mocks.choice.mockResolvedValueOnce("restore").mockResolvedValue("later");
    const { layout, useUi } = await boot("known-asset");
    await flushUntil(() => layout.layoutRestoreStatus().preview);
    const pendingWrite = deferred<{ saved: boolean; revision: number; conflict: boolean }>();
    mocks.put.mockReturnValueOnce(pendingWrite.promise);

    const finishing = layout.finishLayoutRestore();
    await flushUntil(() => mocks.put.mock.calls.length === 1);
    mocks.get.mockResolvedValue({ revision: 3, updatedAt: 3, data: remoteLayout("second-asset") });
    layout.onRemoteChange({ revision: 3 });
    await flushUntil(() => layout.layoutRestoreStatus().pending && layout.layoutRestoreStatus().preview);
    pendingWrite.resolve({ saved: true, revision: 2, conflict: false });
    await finishing;

    expect(layout.layoutRestoreStatus()).toEqual({ pending: true, preview: false, count: 1 });
    expect(useUi.getState().workspaces).toEqual([]);
    expect(window.localStorage.getItem("nexterm:layout-restored-v1:review-user")).toBeNull();
    expect(mocks.put).toHaveBeenCalledTimes(1);
  });

  it("applies an empty cloud layout without showing a restore prompt", async () => {
    const empty = { ...remoteLayout("known-asset"), rightOpen: true, activeWorkspaceId: null, workspaces: [] };
    const { layout, useUi } = await boot("known-asset", empty);
    expect(layout.layoutRestoreStatus()).toEqual({ pending: false, preview: false, count: 0 });
    expect(useUi.getState().workspaces).toEqual([]);
    expect(mocks.choice).not.toHaveBeenCalled();
    await layout.flushLayout();
    expect(mocks.put).not.toHaveBeenCalled();
  });

  it("serializes disconnected preview tabs with restorePending so they survive reload", async () => {
    mocks.choice.mockResolvedValue("restore");
    const { layout, useUi } = await boot("known-asset");
    await flushUntil(() => layout.layoutRestoreStatus().preview);
    const serialized = layout.serializeLayout(useUi.getState());
    const reloaded = layout.sanitizeLayout(JSON.parse(JSON.stringify(serialized)));
    expect(reloaded?.workspaces[0]?.restorePending).toBe(true);
    expect(reloaded?.workspaces[0]?.panes[0].tabs[0]).toMatchObject({
      kind: "terminal", restorePending: true,
    });
    expect(reloaded?.workspaces[0]?.panes[0].tabs[0]?.tabId).toBeUndefined();
    await layout.finishLayoutRestore();
    const saved = JSON.parse(String(mocks.put.mock.calls[0]?.[0]));
    expect(saved.workspaces[0].restorePending).toBe(true);
    expect(saved.workspaces[0].panes[0].tabs[0].restorePending).toBe(true);
    expect(saved.workspaces[0].panes[0].tabs[0].tabId).toBeUndefined();
  });

  it("reconnects only after explicit action and cancels a connection that finishes after undo", async () => {
    mocks.choice.mockResolvedValue("restore");
    const { layout, useUi } = await boot("known-asset");
    await flushUntil(() => layout.layoutRestoreStatus().preview);
    expect(mocks.connect).not.toHaveBeenCalled();
    const pending = deferred<Awaited<ReturnType<typeof mocks.connect>>>();
    mocks.connect.mockReturnValue(pending.promise);
    const { reconnectRestoredWorkspace } = await import("../../app/store");
    const attempt = reconnectRestoredWorkspace("remote-workspace");
    const duplicate = reconnectRestoredWorkspace("remote-workspace");
    expect(duplicate).toBe(attempt);
    await flushUntil(() => mocks.connect.mock.calls.length === 1);
    layout.undoLayoutRestore();
    pending.resolve({ id: "late-session", assetId: "known-asset", kind: "ssh", status: "connected", tabs: [], createdAt: 2 });
    expect(await attempt).toMatchObject({ ok: false, canceled: true });
    expect(mocks.disconnect).toHaveBeenCalledWith("late-session");
    expect(useUi.getState().workspaces).toEqual([]);
    expect(useUi.getState().sessions).not.toContainEqual(expect.objectContaining({ id: "late-session" }));
  });

  it("attaches an explicit reconnect and leaves terminal tabs for fresh attachment", async () => {
    mocks.choice.mockResolvedValue("restore");
    const { layout, useUi } = await boot("known-asset");
    await flushUntil(() => layout.layoutRestoreStatus().preview);
    const { reconnectRestoredWorkspace } = await import("../../app/store");
    expect(await reconnectRestoredWorkspace("remote-workspace")).toMatchObject({ ok: true });
    expect(mocks.connect).toHaveBeenCalledTimes(1);
    const workspace = useUi.getState().workspaces[0];
    expect(workspace.sessionId).toBe("new-session");
    expect(workspace.restorePending).toBeUndefined();
    expect(workspace.panes[0].tabs[0]).toMatchObject({ sessionId: "new-session", restorePending: true });
    expect(workspace.panes[0].tabs[0].tabId).toBeUndefined();
  });
});
