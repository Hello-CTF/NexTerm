/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, mount, waitForRendered, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    list: vi.fn(),
    groupList: vi.fn(),
    snippetList: vi.fn(),
    assetGet: vi.fn(),
    assetCreate: vi.fn(),
    probeBatch: vi.fn(),
    knownHostAccept: vi.fn(),
    sessionConnect: vi.fn(),
    sessionConnectQuick: vi.fn(),
    deviceConnect: vi.fn(),
    sessionDisconnect: vi.fn(),
    sessionReconnect: vi.fn(),
    sessionList: vi.fn(),
    probeHostKey: vi.fn(),
    quickConnectDefaultUser: vi.fn(),
    dbConnect: vi.fn(),
    dbDisconnect: vi.fn(),
    vaultStatus: vi.fn(),
    vaultUnlock: vi.fn(),
    listCredentials: vi.fn(),
    setCredential: vi.fn(),
    deleteCredential: vi.fn(),
    promptText: vi.fn(),
    ask: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask, promptText: mocks.promptText };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: {
      ...actual.assetApi,
      list: mocks.list,
      groupList: mocks.groupList,
      snippetList: mocks.snippetList,
      get: mocks.assetGet,
      create: mocks.assetCreate,
      probeBatch: mocks.probeBatch,
      knownHostAccept: mocks.knownHostAccept,
    },
    sessionApi: {
      ...actual.sessionApi,
      connect: mocks.sessionConnect,
      connectQuick: mocks.sessionConnectQuick,
      connectDevice: mocks.deviceConnect,
      disconnect: mocks.sessionDisconnect,
      reconnect: mocks.sessionReconnect,
      list: mocks.sessionList,
      probeHostKey: mocks.probeHostKey,
      quickConnectDefaultUser: mocks.quickConnectDefaultUser,
    },
    dbApi: { ...actual.dbApi, connect: mocks.dbConnect, disconnect: mocks.dbDisconnect },
    vaultApi: {
      ...actual.vaultApi,
      status: mocks.vaultStatus,
      unlock: mocks.vaultUnlock,
      listCredentials: mocks.listCredentials,
      setCredential: mocks.setCredential,
      deleteCredential: mocks.deleteCredential,
    },
  };
});

import { JumpPalette } from "../../app/JumpPalette";
import { AssetTree } from "../../features/explorer/AssetTree";
import {
  assetConnectionProblemKey,
  connectAsset,
  connectDevice,
  connectQuickTarget,
  quickConnectionProblemKey,
  reconnectRestoredWorkspace,
  reconnectWorkspaceSession,
  saveQuickConnectAsset,
  selectConnectionProblem,
  useUi,
  type Workspace,
} from "../../app/store";
import { useAssetReachability } from "../../features/explorer/assetReachability";
import { useAssetVisibility } from "../../features/explorer/assetVisibility";
import { useConnectHistory } from "../../features/explorer/connectHistory";
import type { Asset, SessionInfo } from "../../ipc/commands";

const WEB: Asset = {
  id: "asset-b",
  groupId: null,
  kind: "ssh",
  name: "web-b",
  host: "10.0.0.8",
  port: 22,
  username: "root",
  authKind: "agent",
  keyPath: null,
  credId: null,
  options: {},
  tags: "",
  note: "",
  sort: 0,
  createdAt: 1,
  updatedAt: 1,
  deletedAt: null,
  builtin: false,
};

const QUICK_TARGET = { host: "EXAMPLE.INVALID", port: 22, username: "root" };
const NORMALIZED_QUICK_TARGET = { host: "example.invalid", port: 22, username: "root" };
const CREATED_ASSET: Asset = {
  ...WEB,
  id: "asset-created",
  name: "root@example.invalid",
  host: "example.invalid",
};

function sessionFor(asset: ConnectAssetInputForTest): SessionInfo {
  return {
    id: `session-${asset.id}`,
    assetId: asset.id,
    name: asset.name,
    kind: asset.kind,
    status: "connected",
    tabs: [],
    createdAt: 1,
  };
}

type ConnectAssetInputForTest = Pick<Asset, "id" | "name" | "kind">;

function workspaceOf(id: string, assetId: string, restorePending = false): Workspace {
  return {
    id,
    kind: "session",
    title: id,
    assetId,
    assetKind: "ssh",
    restorePending: restorePending || undefined,
    panes: [{ id: `${id}-pane`, tabs: [], activeTabId: null }],
    activePaneId: `${id}-pane`,
    closable: true,
  };
}

function globalProblem(target: Parameters<typeof assetConnectionProblemKey>[0]) {
  return useUi.getState().connectionProblems[assetConnectionProblemKey(target)];
}

function connectionProblemCount(): number {
  const state = useUi.getState();
  const workspaceProblems = Object.values(state.workspaceRuntime).flatMap((runtime) => runtime.problems ?? []);
  return [...Object.values(state.connectionProblems), ...workspaceProblems]
    .filter((problem) => problem.connectionKey)
    .length;
}

const mountedViews: MountedView[] = [];

function mountWithClient(node: ReactNode): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = mount(createElement(QueryClientProvider, { client }, node));
  mountedViews.push(view);
  return view;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  useUi.getState().resetAccountWorkspaces();
  useUi.setState({
    leftOpen: true,
    leftWidth: 300,
    workspaceRuntime: {},
    connectionProblems: {},
    pushToast: mocks.toast,
  });
  useAssetVisibility.setState({ hiddenIds: [], showHidden: false });
  useConnectHistory.setState({ entries: {} });
  useAssetReachability.setState({ entries: {}, batchError: null });
  mocks.list.mockResolvedValue([{ ...WEB }]);
  mocks.groupList.mockResolvedValue([]);
  mocks.snippetList.mockResolvedValue([]);
  mocks.assetGet.mockImplementation(async (id: string) => ({ ...WEB, id, name: id }));
  mocks.probeBatch.mockResolvedValue({ results: [] });
  mocks.knownHostAccept.mockResolvedValue(undefined);
  mocks.sessionConnect.mockImplementation(async (id: string) => sessionFor({ id, name: id, kind: "ssh" }));
  mocks.sessionConnectQuick.mockResolvedValue({
    id: "quick-session",
    assetId: null,
    name: "root@example.invalid",
    kind: "ssh",
    status: "connected",
    tabs: [],
    createdAt: 1,
  });
  mocks.deviceConnect.mockResolvedValue({
    id: "device-session",
    assetId: "device/device-1",
    name: "device-1",
    kind: "device",
    status: "connected",
    tabs: [],
    createdAt: 1,
  });
  mocks.sessionDisconnect.mockResolvedValue(undefined);
  mocks.sessionList.mockResolvedValue([]);
  mocks.probeHostKey.mockResolvedValue({ state: "known" });
  mocks.quickConnectDefaultUser.mockResolvedValue("root");
  mocks.dbDisconnect.mockResolvedValue(undefined);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  mocks.listCredentials.mockResolvedValue([]);
  mocks.setCredential.mockResolvedValue({ id: "cred-1" });
  mocks.deleteCredential.mockResolvedValue(undefined);
  mocks.promptText.mockResolvedValue("");
  mocks.ask.mockResolvedValue(true);
});

afterEach(() => {
  for (const view of mountedViews.splice(0)) view.unmount();
  useUi.getState().resetAccountWorkspaces();
});

describe("target-owned connection problems", () => {
  it("records a failure on target workspace B while workspace A is active and clears it on success", async () => {
    const a = workspaceOf("ws-a", "asset-a");
    const b = workspaceOf("ws-b", WEB.id);
    useUi.setState({ workspaces: [a, b], activeWorkspaceId: a.id });
    mocks.sessionConnect.mockRejectedValueOnce(new Error("B unavailable"));

    expect(await connectAsset(WEB)).toEqual({ ok: false, error: "B unavailable" });

    let state = useUi.getState();
    expect(state.activeWorkspaceId).toBe(a.id);
    expect(state.workspaceRuntime[a.id]?.problems ?? []).toEqual([]);
    expect(state.workspaceRuntime[b.id]?.problems?.[0]).toMatchObject({
      source: "connect",
      connectionKey: assetConnectionProblemKey(WEB.id),
      target: { assetId: WEB.id, title: WEB.name },
      summary: "B unavailable",
    });
    expect(state.connectionProblems).toEqual({});
    expect(state.workspaces.map((workspace) => workspace.id)).toEqual([a.id, b.id]);
    expect(mocks.toast).toHaveBeenCalledWith("error", "B unavailable");

    mocks.sessionConnect.mockResolvedValueOnce(sessionFor(WEB));
    expect(await connectAsset(WEB)).toEqual({ ok: true });
    state = useUi.getState();
    expect(state.workspaceRuntime[b.id]?.problems ?? []).toEqual([]);
    expect(selectConnectionProblem(state, assetConnectionProblemKey(WEB.id))).toBeUndefined();
  });

  it("keeps a failure global without creating a workspace and clears it manually by the same key", async () => {
    mocks.sessionConnect.mockRejectedValueOnce(new Error("no workspace yet"));

    expect(await connectAsset(WEB)).toEqual({ ok: false, error: "no workspace yet" });

    const key = assetConnectionProblemKey(WEB.id);
    expect(useUi.getState().workspaces).toEqual([]);
    expect(globalProblem(WEB.id)).toMatchObject({ connectionKey: key, summary: "no workspace yet" });
    useUi.getState().clearConnectionProblem(key);
    expect(useUi.getState().connectionProblems).toEqual({});
  });

  it("uses a normalized quick target for recording and successful cleanup", async () => {
    mocks.sessionConnectQuick.mockRejectedValueOnce(new Error("quick unavailable"));

    expect(await connectQuickTarget(QUICK_TARGET)).toEqual({ ok: false, error: "quick unavailable" });

    const key = quickConnectionProblemKey(NORMALIZED_QUICK_TARGET);
    expect(quickConnectionProblemKey(QUICK_TARGET)).toBe(key);
    expect(useUi.getState().connectionProblems[key]).toMatchObject({
      connectionKey: key,
      connection: { kind: "quick", mode: "temporary", target: QUICK_TARGET },
    });
    expect(useUi.getState().workspaces).toEqual([]);

    expect(await connectQuickTarget(NORMALIZED_QUICK_TARGET)).toEqual({ ok: true });
    expect(useUi.getState().connectionProblems[key]).toBeUndefined();
  });

  it("routes a quick failure to the existing workspace with the same normalized target", async () => {
    const a = workspaceOf("ws-a", "asset-a");
    const quickWorkspace: Workspace = {
      ...workspaceOf("ws-quick", "unused"),
      assetId: undefined,
      quickTarget: { id: "quick-target", ...NORMALIZED_QUICK_TARGET, authKind: "agent" },
    };
    useUi.setState({ workspaces: [a, quickWorkspace], activeWorkspaceId: a.id });
    mocks.sessionConnectQuick.mockRejectedValueOnce(new Error("quick workspace unavailable"));

    expect(await connectQuickTarget(QUICK_TARGET)).toEqual({
      ok: false,
      error: "quick workspace unavailable",
    });

    const state = useUi.getState();
    expect(state.workspaceRuntime[a.id]?.problems ?? []).toEqual([]);
    expect(state.workspaceRuntime[quickWorkspace.id]?.problems?.[0]).toMatchObject({
      connectionKey: quickConnectionProblemKey(QUICK_TARGET),
      summary: "quick workspace unavailable",
    });
    expect(state.connectionProblems).toEqual({});
    expect(state.workspaces.map((workspace) => workspace.id)).toEqual([a.id, quickWorkspace.id]);
  });

  it("keeps pre-creation save failures on the quick key only", async () => {
    mocks.assetCreate.mockRejectedValueOnce(new Error("create failed"));

    const outcome = await saveQuickConnectAsset(QUICK_TARGET);

    expect(outcome).toMatchObject({
      ok: false,
      error: "create failed",
      problemKey: quickConnectionProblemKey(QUICK_TARGET),
    });
    expect(outcome.asset).toBeUndefined();
    expect(useUi.getState().connectionProblems[quickConnectionProblemKey(QUICK_TARGET)]).toMatchObject({
      summary: "create failed",
    });
    expect(connectionProblemCount()).toBe(1);
  });

  it("migrates a post-creation save failure to one global asset problem and clears it manually", async () => {
    mocks.sessionConnectQuick.mockRejectedValueOnce(new Error("previous quick failure"));
    await connectQuickTarget(QUICK_TARGET);
    mocks.assetCreate.mockResolvedValue(CREATED_ASSET);
    mocks.sessionConnect.mockRejectedValueOnce(new Error("created asset unavailable"));

    const outcome = await saveQuickConnectAsset(QUICK_TARGET);

    expect(outcome).toMatchObject({
      ok: false,
      error: "created asset unavailable",
      problemKey: assetConnectionProblemKey(CREATED_ASSET.id),
      asset: { id: CREATED_ASSET.id },
    });
    expect(useUi.getState().connectionProblems[quickConnectionProblemKey(QUICK_TARGET)]).toBeUndefined();
    expect(globalProblem(CREATED_ASSET.id)).toMatchObject({ summary: "created asset unavailable" });
    expect(connectionProblemCount()).toBe(1);

    useUi.getState().clearConnectionProblem(outcome.problemKey);
    expect(connectionProblemCount()).toBe(0);
  });

  it("removes the old quick-workspace problem after creation and leaves no residue after asset retry", async () => {
    const quickWorkspace: Workspace = {
      ...workspaceOf("ws-quick-save", "unused"),
      assetId: undefined,
      quickTarget: { id: "quick-target", ...NORMALIZED_QUICK_TARGET, authKind: "agent" },
    };
    useUi.setState({ workspaces: [quickWorkspace], activeWorkspaceId: quickWorkspace.id });
    mocks.sessionConnectQuick.mockRejectedValueOnce(new Error("quick workspace failure"));
    await connectQuickTarget(QUICK_TARGET);
    expect(useUi.getState().workspaceRuntime[quickWorkspace.id]?.problems).toHaveLength(1);
    mocks.assetCreate.mockResolvedValue(CREATED_ASSET);
    mocks.sessionConnect.mockRejectedValueOnce(new Error("created asset unavailable"));

    const outcome = await saveQuickConnectAsset(QUICK_TARGET);

    expect(outcome.problemKey).toBe(assetConnectionProblemKey(CREATED_ASSET.id));
    expect(useUi.getState().workspaceRuntime[quickWorkspace.id]?.problems ?? []).toEqual([]);
    expect(globalProblem(CREATED_ASSET.id)).toMatchObject({ summary: "created asset unavailable" });
    expect(connectionProblemCount()).toBe(1);

    expect(await connectAsset(CREATED_ASSET)).toEqual({ ok: true });
    expect(connectionProblemCount()).toBe(0);
    expect(mocks.assetCreate).toHaveBeenCalledTimes(1);
  });

  it("records device failures through the same asset-keyed entry and clears them after retry", async () => {
    mocks.deviceConnect.mockRejectedValueOnce(new Error("device unavailable"));

    expect(await connectDevice({ id: "device-1", name: "Device" })).toEqual({
      ok: false,
      error: "device unavailable",
    });
    expect(globalProblem("device/device-1")).toMatchObject({
      target: { assetId: "device/device-1", kind: "device" },
      summary: "device unavailable",
    });
    expect(useUi.getState().workspaces).toEqual([]);

    expect(await connectDevice({ id: "device-1", name: "Device" })).toEqual({ ok: true });
    expect(globalProblem("device/device-1")).toBeUndefined();
  });

  it("records restored-workspace failures on that workspace rather than the active one", async () => {
    const a = workspaceOf("ws-a", "asset-a");
    const restored = workspaceOf("ws-restored", WEB.id, true);
    useUi.setState({ workspaces: [a, restored], activeWorkspaceId: a.id });
    mocks.sessionConnect.mockRejectedValueOnce(new Error("restore unavailable"));

    expect(await reconnectRestoredWorkspace(restored.id)).toEqual({
      ok: false,
      error: "restore unavailable",
    });

    const state = useUi.getState();
    expect(state.workspaceRuntime[a.id]?.problems ?? []).toEqual([]);
    expect(state.workspaceRuntime[restored.id]?.problems?.[0]).toMatchObject({
      connectionKey: assetConnectionProblemKey(WEB.id),
      summary: "restore unavailable",
    });
    expect(state.workspaceRuntime[restored.id]?.problems).toHaveLength(1);
    expect(state.connectionProblems).toEqual({});
    expect(state.workspaces.find((workspace) => workspace.id === restored.id)?.restorePending).toBe(true);
  });

  it("settles existing-session recovery on its target with failure, cancel and success semantics", async () => {
    const a = workspaceOf("ws-a", "asset-a");
    const existing: SessionInfo = { ...sessionFor(WEB), id: "existing-session", status: "disconnected" };
    const target: Workspace = { ...workspaceOf("ws-existing", WEB.id), sessionId: existing.id };
    useUi.setState({
      sessions: [existing],
      workspaces: [a, target],
      activeWorkspaceId: a.id,
    });
    mocks.sessionReconnect.mockResolvedValue(true);
    mocks.sessionList
      .mockResolvedValueOnce([{ ...existing, status: "reconnecting" }])
      .mockResolvedValue([{ ...existing, status: "failed" }]);

    await expect(reconnectWorkspaceSession(target, existing, { timeoutMs: 50, intervalMs: 1 }))
      .rejects.toThrow("会话连接失败");

    expect(useUi.getState().workspaceRuntime[a.id]?.problems ?? []).toEqual([]);
    expect(useUi.getState().workspaceRuntime[target.id]?.problems?.[0]).toMatchObject({
      connectionKey: assetConnectionProblemKey(WEB.id),
      summary: "会话连接失败",
    });
    expect(connectionProblemCount()).toBe(1);

    mocks.probeHostKey.mockResolvedValueOnce({
      state: "unknown",
      host: WEB.host,
      port: WEB.port,
      keyType: "ssh-ed25519",
      fingerprint: "fixture-fingerprint",
      known: [],
    });
    mocks.ask.mockResolvedValueOnce(false);
    expect(await reconnectWorkspaceSession(target, existing, { timeoutMs: 50, intervalMs: 1 })).toBeNull();
    expect(mocks.sessionReconnect).toHaveBeenCalledTimes(1);
    expect(useUi.getState().workspaceRuntime[target.id]?.problems).toHaveLength(1);

    mocks.sessionList.mockResolvedValue([{ ...existing, status: "connected" }]);
    expect(await reconnectWorkspaceSession(target, existing, { timeoutMs: 50, intervalMs: 1 }))
      .toMatchObject({ status: "connected" });
    expect(connectionProblemCount()).toBe(0);
  });

  it("clears global and workspace connection problems on account switch", async () => {
    mocks.sessionConnectQuick.mockRejectedValueOnce(new Error("old account quick"));
    await connectQuickTarget(QUICK_TARGET);
    const restored = workspaceOf("ws-old", WEB.id);
    useUi.setState({ workspaces: [restored], activeWorkspaceId: restored.id });
    mocks.sessionConnect.mockRejectedValueOnce(new Error("old account asset"));
    await connectAsset(WEB);
    expect(useUi.getState().connectionProblems).not.toEqual({});
    expect(useUi.getState().workspaceRuntime[restored.id]?.problems).not.toEqual([]);

    useUi.getState().resetAccountWorkspaces();

    const state = useUi.getState();
    expect(state.connectionProblems).toEqual({});
    expect(state.workspaceRuntime).toEqual({});
    expect(state.workspaces).toEqual([]);
  });
});

describe("connection problem visibility and retry", () => {
  it("shows an asset failure without a workspace and retries from the asset-tree badge", async () => {
    mocks.sessionConnect.mockRejectedValueOnce(new Error("tree connection down"));
    await connectAsset(WEB);
    expect(useUi.getState().workspaces).toEqual([]);

    const view = mountWithClient(createElement(AssetTree));
    await waitForRendered(() => expect(view.container.textContent).toContain(WEB.name));
    const retry = view.container.querySelector<HTMLButtonElement>(`button[aria-label="重试连接 ${WEB.name}"]`);
    expect(retry).not.toBeNull();
    expect(retry?.title).toContain("tree connection down");

    mocks.sessionConnect.mockResolvedValueOnce(sessionFor(WEB));
    click(retry!);
    await waitForRendered(() => expect(mocks.sessionConnect).toHaveBeenCalledTimes(2));
    await waitForRendered(() => expect(globalProblem(WEB.id)).toBeUndefined());
  });

  it("hides the asset-tree failure badge a minute after the failure", async () => {
    mocks.sessionConnect.mockRejectedValueOnce(new Error("tree connection down"));
    await connectAsset(WEB);
    const view = mountWithClient(createElement(AssetTree));
    const selector = `button[aria-label="重试连接 ${WEB.name}"]`;
    await waitForRendered(() => expect(view.container.querySelector(selector)).not.toBeNull());

    const key = assetConnectionProblemKey(WEB.id);
    act(() => {
      useUi.setState((state) => ({
        connectionProblems: {
          ...state.connectionProblems,
          [key]: { ...state.connectionProblems[key], createdAt: Date.now() - 61_000 },
        },
      }));
    });

    await waitForRendered(() => expect(view.container.querySelector(selector)).toBeNull());
  });

  it("shows a global quick-target problem after reopening and retries the original target", async () => {
    mocks.sessionConnectQuick.mockRejectedValueOnce(new Error("quick still visible"));
    await connectQuickTarget(QUICK_TARGET);
    expect(useUi.getState().workspaces).toEqual([]);
    const onClose = vi.fn();

    const view = mountWithClient(createElement(JumpPalette, { onClose, scope: "hosts" }));
    await waitForRendered(() => {
      const section = view.container.querySelector('section[aria-label="本次问题"]');
      expect(section?.textContent).toContain("quick still visible");
    });
    const reconnect = [...view.container.querySelectorAll<HTMLButtonElement>("button")].find(
      (button) => button.textContent?.trim() === "重新连接",
    );
    expect(reconnect).toBeDefined();

    click(reconnect!);
    await waitForRendered(() => expect(mocks.sessionConnectQuick).toHaveBeenCalledTimes(2));
    await waitForRendered(() => expect(onClose).toHaveBeenCalledTimes(1));
    expect(useUi.getState().connectionProblems[quickConnectionProblemKey(QUICK_TARGET)]).toBeUndefined();
  });

  it("dismisses a global quick-target problem through the same-key manual cleanup", async () => {
    mocks.sessionConnectQuick.mockRejectedValueOnce(new Error("dismiss quick"));
    await connectQuickTarget(QUICK_TARGET);
    const key = quickConnectionProblemKey(QUICK_TARGET);
    const view = mountWithClient(createElement(JumpPalette, { onClose: vi.fn(), scope: "hosts" }));
    await waitForRendered(() => expect(view.container.textContent).toContain("dismiss quick"));

    const dismiss = view.container.querySelector<HTMLButtonElement>(
      `button[aria-label="关闭问题：dismiss quick"]`,
    );
    expect(dismiss).not.toBeNull();
    click(dismiss!);

    await waitForRendered(() => expect(useUi.getState().connectionProblems[key]).toBeUndefined());
    expect(view.container.querySelector('section[aria-label="本次问题"]')).toBeNull();
  });
});
