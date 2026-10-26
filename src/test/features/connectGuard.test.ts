/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { deferred, flush } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  sessionConnect: vi.fn(),
  deviceConnect: vi.fn(),
  sessionConnectQuick: vi.fn(),
  sessionDisconnect: vi.fn(),
  sessionReconnect: vi.fn(),
  sessionList: vi.fn(),
  prepareDurable: vi.fn(),
  assetCreate: vi.fn(),
  probeHostKey: vi.fn(),
  dbConnect: vi.fn(),
  dbDisconnect: vi.fn(),
  vaultStatus: vi.fn(),
  vaultUnlock: vi.fn(),
  promptText: vi.fn(),
  ask: vi.fn(),
  askChoiceRemember: vi.fn(),
  closeTab: vi.fn(),
  toast: vi.fn(),
  setConnectProgress: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return {
    ...actual,
    promptText: mocks.promptText,
    ask: mocks.ask,
    askChoiceRemember: mocks.askChoiceRemember,
  };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    assetApi: { ...actual.assetApi, create: mocks.assetCreate },
    sessionApi: {
      connect: mocks.sessionConnect,
      connectDevice: mocks.deviceConnect,
      connectQuick: mocks.sessionConnectQuick,
      disconnect: mocks.sessionDisconnect,
      reconnect: mocks.sessionReconnect,
      list: mocks.sessionList,
      probeHostKey: mocks.probeHostKey,
      prepareDurable: mocks.prepareDurable,
    },
    terminalApi: { ...actual.terminalApi, closeTab: mocks.closeTab },
    dbApi: { connect: mocks.dbConnect, disconnect: mocks.dbDisconnect },
    vaultApi: { status: mocks.vaultStatus, unlock: mocks.vaultUnlock },
  };
});

import { connectAsset, connectDevice, connectQuickTarget, saveQuickConnectAsset, useUi, type ConnectOutcome, type SqlDraft } from "../../app/store";
import { useConnectHistory } from "../../features/explorer/connectHistory";
import { sessionApi, type SessionInfo } from "../../ipc/commands";

const SSH_ASSET = { id: "asset-1", name: "web-01", kind: "ssh" };

const SESSION: SessionInfo = {
  id: "s1",
  assetId: "asset-1",
  name: "web-01",
  kind: "ssh",
  status: "connected",
  tabs: [],
  createdAt: 0,
};

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  useConnectHistory.setState({ entries: {} });
  useUi.setState({
    sessions: [],
    workspaces: [],
    activeWorkspaceId: null,
    workspaceRuntime: {},
    connectionProblems: {},
    connectingAssetIds: [],
    connectFocusRevision: 0,
    pushToast: mocks.toast,
    setConnectProgress: mocks.setConnectProgress,
  });
  mocks.sessionConnect.mockResolvedValue(SESSION);
  mocks.sessionDisconnect.mockResolvedValue(undefined);
  mocks.deviceConnect.mockResolvedValue({ ...SESSION, id: "device-session", assetId: "device/device-1", kind: "device" });
  mocks.sessionConnectQuick.mockResolvedValue({ ...SESSION, id: "quick-session", assetId: null });
  mocks.sessionList.mockResolvedValue([]);
  mocks.ask.mockResolvedValue(true);
  mocks.askChoiceRemember.mockResolvedValue({ key: "detach", remember: false });
  mocks.closeTab.mockResolvedValue(undefined);
  mocks.dbDisconnect.mockResolvedValue(undefined);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
});

afterEach(() => {
  useUi.setState({
    workspaces: [],
    activeWorkspaceId: null,
    workspaceRuntime: {},
    connectionProblems: {},
    connectingAssetIds: [],
  });
});

describe("connectAsset in-flight guard", () => {
  it("does not close the next account's workspace after an earlier close confirmation", async () => {
    const confirmation = deferred<boolean>();
    mocks.ask.mockReturnValueOnce(confirmation.promise);
    const workspace = {
      id: "shared-id", kind: "db" as const, title: "Database", closable: true,
      panes: [{ id: "pane", tabs: [], activeTabId: null }], activePaneId: "pane",
    };
    useUi.setState({
      workspaces: [workspace],
      workspaceRuntime: { "shared-id": {
        generation: 1,
        sqlDraft: { text: "SELECT 1", selection: { from: 0, to: 0 }, schema: "", connId: "old-db", dbKind: "mysql" },
      } },
    });

    const close = useUi.getState().closeWorkspace("shared-id");
    await vi.waitFor(() => expect(mocks.ask).toHaveBeenCalledTimes(1));
    useUi.getState().resetAccountWorkspaces();
    useUi.setState({ workspaces: [{ ...workspace, title: "New account database" }] });
    confirmation.resolve(true);

    expect(await close).toBe(false);
    expect(useUi.getState().workspaces).toMatchObject([{ id: "shared-id", title: "New account database" }]);
  });

  it("does not reuse or display an SSH response from a previous account", async () => {
    const oldResponse = deferred<SessionInfo>();
    mocks.sessionConnect.mockReturnValueOnce(oldResponse.promise).mockResolvedValueOnce({ ...SESSION, id: "new-session" });

    const oldConnect = connectAsset(SSH_ASSET);
    await flush();
    useUi.getState().resetAccountWorkspaces();
    expect(await connectAsset(SSH_ASSET)).toEqual({ ok: true });

    oldResponse.resolve(SESSION);
    expect(await oldConnect).toEqual({ ok: false, canceled: true });
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(2);
    expect(useUi.getState().sessions.map((session) => session.id)).toEqual(["new-session"]);
    expect(useUi.getState().workspaces).toHaveLength(1);
    expect(mocks.sessionDisconnect).toHaveBeenCalledWith("s1");
  });

  it("does not reuse or display a managed device response from a previous account", async () => {
    const oldResponse = deferred<SessionInfo>();
    mocks.deviceConnect.mockReturnValueOnce(oldResponse.promise).mockResolvedValueOnce({
      ...SESSION, id: "new-device-session", assetId: "device/device-1", kind: "device",
    });

    const oldConnect = connectDevice({ id: "device-1", name: "Device" });
    await flush();
    useUi.getState().resetAccountWorkspaces();
    expect(await connectDevice({ id: "device-1", name: "Device" })).toEqual({ ok: true });

    oldResponse.resolve({ ...SESSION, id: "old-device-session", assetId: "device/device-1", kind: "device" });
    expect(await oldConnect).toEqual({ ok: false, canceled: true });
    expect(mocks.deviceConnect).toHaveBeenCalledTimes(2);
    expect(useUi.getState().sessions.map((session) => session.id)).toEqual(["new-device-session"]);
    expect(mocks.sessionDisconnect).toHaveBeenCalledWith("old-device-session");
  });

  it("does not reuse or display a database response from a previous account", async () => {
    const oldResponse = deferred<{ connId: string }>();
    mocks.dbConnect.mockReturnValueOnce(oldResponse.promise).mockResolvedValueOnce({ connId: "new-connection" });
    const asset = { id: "db-1", name: "Database", kind: "mysql" };

    const oldConnect = connectAsset(asset);
    await flush();
    useUi.getState().resetAccountWorkspaces();
    expect(await connectAsset(asset)).toEqual({ ok: true });

    oldResponse.resolve({ connId: "old-connection" });
    expect(await oldConnect).toEqual({ ok: false, canceled: true });
    expect(mocks.dbConnect).toHaveBeenCalledTimes(2);
    expect(useUi.getState().workspaces).toMatchObject([{ connId: "new-connection" }]);
    expect(mocks.dbDisconnect).toHaveBeenCalledWith("old-connection");
  });

  it("does not reuse or display a quick SSH response from a previous account", async () => {
    const oldResponse = deferred<SessionInfo>();
    mocks.promptText.mockResolvedValue("");
    mocks.sessionConnectQuick.mockReturnValueOnce(oldResponse.promise).mockResolvedValueOnce({
      ...SESSION, id: "new-quick-session", assetId: null,
    });
    const target = { host: "example.invalid", port: 22, username: "operator" };

    const oldConnect = connectQuickTarget(target);
    await flush();
    useUi.getState().resetAccountWorkspaces();
    expect(await connectQuickTarget(target)).toEqual({ ok: true });

    oldResponse.resolve({ ...SESSION, id: "old-quick-session", assetId: null });
    expect(await oldConnect).toEqual({ ok: false, canceled: true });
    expect(mocks.sessionConnectQuick).toHaveBeenCalledTimes(2);
    expect(useUi.getState().sessions.map((session) => session.id)).toEqual(["new-quick-session"]);
    expect(mocks.sessionDisconnect).toHaveBeenCalledWith("old-quick-session");
  });

  it("does not save a quick SSH asset after its password prompt crosses an account switch", async () => {
    const password = deferred<string | null>();
    mocks.promptText.mockReturnValueOnce(password.promise);
    const target = { host: "example.invalid", port: 22, username: "operator" };

    const save = saveQuickConnectAsset(target);
    await flush();
    useUi.getState().resetAccountWorkspaces();
    password.resolve("test-password");

    expect(await save).toEqual({
      ok: false,
      canceled: true,
      problemKey: "quick:operator@example.invalid",
    });
    expect(mocks.assetCreate).not.toHaveBeenCalled();
  });

  it("duplicate calls for the same in-flight asset start only one connection", async () => {
    const gate = deferred<SessionInfo>();
    mocks.sessionConnect.mockReturnValueOnce(gate.promise);

    const first = connectAsset(SSH_ASSET);
    const second = connectAsset(SSH_ASSET);
    await flush();
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(1);
    expect(useUi.getState().connectingAssetIds).toContain("asset-1");

    gate.resolve(SESSION);
    const [a, b] = (await Promise.all([first, second])) as ConnectOutcome[];
    expect(a).toEqual({ ok: true });
    expect(b).toEqual({ ok: true });
    expect(useUi.getState().connectingAssetIds).not.toContain("asset-1");
  });

  it("connections to different assets do not block each other", async () => {
    const other = { id: "asset-2", name: "db-01", kind: "ssh" };
    const [a, b] = (await Promise.all([
      connectAsset(SSH_ASSET),
      connectAsset(other),
    ])) as ConnectOutcome[];
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(2);
    expect(a).toEqual({ ok: true });
    expect(b).toEqual({ ok: true });
  });

  it("clears the guard after failure so an explicit retry can connect again", async () => {
    mocks.sessionConnect.mockRejectedValueOnce({ code: "io", message: "connection refused" });
    const failed = (await connectAsset(SSH_ASSET)) as ConnectOutcome;
    expect(failed.ok).toBe(false);
    if (!failed.ok) expect(failed.error).toBe("connection refused");
    expect(useUi.getState().connectingAssetIds).not.toContain("asset-1");

    const retried = (await connectAsset(SSH_ASSET)) as ConnectOutcome;
    expect(retried).toEqual({ ok: true });
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(2);
  });

  it("does not block explicit disconnects while a connection is in flight", async () => {
    const gate = deferred<SessionInfo>();
    mocks.sessionConnect.mockReturnValueOnce(gate.promise);

    const pending = connectAsset(SSH_ASSET);
    await connectAsset({ id: "asset-2", name: "db-01", kind: "ssh" });
    await sessionApi.disconnect("s-other");
    expect(mocks.sessionDisconnect).toHaveBeenCalledWith("s-other");

    gate.resolve(SESSION);
    await pending;
  });

  it("successful connections are recorded in recent history", async () => {
    await connectAsset(SSH_ASSET);
    expect(useConnectHistory.getState().entries["asset-1"]?.count).toBe(1);
  });

  it("failed connections are not recorded in recent history", async () => {
    mocks.sessionConnect.mockRejectedValueOnce({ code: "io", message: "boom" });
    await connectAsset(SSH_ASSET);
    expect(useConnectHistory.getState().entries["asset-1"]).toBeUndefined();
  });

  it("database assets are protected by the same guard", async () => {
    const dbAsset = { id: "db-1", name: "mysql-01", kind: "mysql" };
    const gate = deferred<{ connId: string }>();
    mocks.dbConnect.mockReturnValueOnce(gate.promise);

    const first = connectAsset(dbAsset);
    const second = connectAsset(dbAsset);
    await flush();
    expect(mocks.dbConnect).toHaveBeenCalledTimes(1);

    gate.resolve({ connId: "conn-1" });
    const [a, b] = (await Promise.all([first, second])) as ConnectOutcome[];
    expect(a).toEqual({ ok: true });
    expect(b).toEqual({ ok: true });
  });

  it("returns canceled when credential unlock is canceled and does not connect", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: false });
    mocks.promptText.mockResolvedValue(null);

    const outcome = (await connectAsset({
      id: "asset-9",
      name: "locked-01",
      kind: "ssh",
      credId: "cred-1",
    })) as ConnectOutcome;
    expect(outcome).toEqual({ ok: false, canceled: true });
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
  });
});

describe("connectAsset asset workspace deduplication", () => {
  function seedSessionWorkspace(sessionStatus: SessionInfo["status"], withTerminalTab = false) {
    const tabs = withTerminalTab
      ? [{ id: "t1", kind: "terminal" as const, title: "Terminal 1", sessionId: "s1", closable: true }]
      : [];
    useUi.setState({
      sessions: [{ ...SESSION, status: sessionStatus }],
      workspaces: [
        {
          id: "ws-1",
          kind: "session" as const,
          sessionId: "s1",
          title: "web-01",
          assetId: "asset-1",
          assetKind: "ssh",
          panes: [{ id: "p1", tabs, activeTabId: tabs[0]?.id ?? null }],
          activePaneId: "p1",
          closable: true,
        },
      ],
      activeWorkspaceId: null,
    });
  }

  it("reconnecting with an existing live-session workspace jumps to it without starting a new connection", async () => {
    seedSessionWorkspace("connected", true);
    const outcome = (await connectAsset(SSH_ASSET)) as ConnectOutcome;
    expect(outcome).toEqual({ ok: true });
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
    const { workspaces, activeWorkspaceId } = useUi.getState();
    expect(workspaces).toHaveLength(1);
    expect(activeWorkspaceId).toBe("ws-1");
    expect(workspaces[0]?.panes[0]?.tabs).toHaveLength(1);
  });

  it("switches to the workspace without reopening a closed terminal tab and keeps preparing durable terminals", async () => {
    seedSessionWorkspace("connected");
    const outcome = (await connectAsset(SSH_ASSET)) as ConnectOutcome;
    expect(outcome).toEqual({ ok: true });
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
    const { workspaces, activeWorkspaceId } = useUi.getState();
    expect(activeWorkspaceId).toBe("ws-1");
    const tabs = workspaces[0]?.panes.flatMap((p) => p.tabs) ?? [];
    expect(tabs.some((t) => t.kind === "terminal")).toBe(false);
    await flush();
    expect(mocks.prepareDurable).toHaveBeenCalledWith("s1");
  });

  it("reconnects directly when the asset workspace session is disconnected and takes over the existing terminal tab", async () => {
    seedSessionWorkspace("disconnected");
    mocks.probeHostKey.mockResolvedValue({ state: "known" });
    mocks.sessionReconnect.mockResolvedValue(true);
    mocks.sessionList.mockResolvedValue([{ ...SESSION, status: "connected" }]);

    const outcome = (await connectAsset(SSH_ASSET)) as ConnectOutcome;
    expect(outcome).toEqual({ ok: true });
    expect(mocks.sessionReconnect).toHaveBeenCalledWith("s1");
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
    const { workspaces, activeWorkspaceId } = useUi.getState();
    expect(workspaces).toHaveLength(1);
    expect(workspaces[0]?.id).toBe("ws-1");
    expect(activeWorkspaceId).toBe("ws-1");
  });

  it("preserves the restored workspace without an empty-frame transition when reconnecting fails", async () => {
    seedSessionWorkspace("disconnected", true);
    useUi.getState().updateTab("t1", { tabId: "kernel-t1", retainable: true });
    mocks.probeHostKey.mockResolvedValue({ state: "known" });
    mocks.sessionReconnect.mockRejectedValue(new Error("首次连接需要确认主机指纹"));
    const tabStates: string[][] = [];
    const unsubscribe = useUi.subscribe((state) => {
      const workspace = state.workspaces.find((candidate) => candidate.id === "ws-1");
      tabStates.push(
        workspace?.panes.flatMap((pane) => pane.tabs).map((tab) => tab.id) ?? [],
      );
    });

    const outcome = (await connectAsset(SSH_ASSET)) as ConnectOutcome;
    unsubscribe();

    expect(outcome).toEqual({ ok: false, error: "首次连接需要确认主机指纹" });
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(mocks.askChoiceRemember).not.toHaveBeenCalled();
    expect(tabStates.every((ids) => ids.length === 1 && ids[0] === "t1")).toBe(true);
    expect(useUi.getState().workspaces[0]?.id).toBe("ws-1");
    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs[0]).toMatchObject({
      id: "t1",
      tabId: "kernel-t1",
    });
  });

  it("rebinds the existing workspace when the previous session was deleted", async () => {
    seedSessionWorkspace("disconnected");
    mocks.probeHostKey.mockResolvedValue({ state: "known" });
    mocks.sessionReconnect.mockRejectedValue({ code: "not_found", message: "Session not found" });

    const outcome = (await connectAsset(SSH_ASSET)) as ConnectOutcome;
    expect(outcome).toEqual({ ok: true });
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(1);
    const { workspaces } = useUi.getState();
    expect(workspaces).toHaveLength(1);
    expect(workspaces[0]?.id).toBe("ws-1");
  });

  it.each(["missing", "deleted"] as const)("preserves all restored SSH panes and shell identities with a %s session", async (state) => {
    seedSessionWorkspace("disconnected", true);
    const workspace = useUi.getState().workspaces[0];
    useUi.setState({
      sessions: state === "missing" ? [] : useUi.getState().sessions,
      workspaces: [{ ...workspace, activePaneId: "p2", layoutTree: { type: "split" as const, id: "split-root", dir: "row" as const, ratio: 0.37, a: { type: "leaf" as const, id: "leaf-a", paneId: "p1" }, b: { type: "leaf" as const, id: "leaf-b", paneId: "p2" } }, panes: [
        { id: "p1", activeTabId: "t1", tabs: [{ id: "t1", kind: "terminal", title: "one", sessionId: "s1", tabId: "shell-one", closable: true }] },
        { id: "p2", activeTabId: "t2", tabs: [{ id: "t2", kind: "terminal", title: "two", sessionId: "s1", tabId: "shell-two", closable: true }] },
      ] }],
    });
    mocks.sessionReconnect.mockRejectedValue({ code: "not_found", message: "Session not found" });
    mocks.sessionConnect.mockResolvedValue({ ...SESSION, id: "s-new" });

    expect(await connectAsset(SSH_ASSET)).toEqual({ ok: true });

    const restored = useUi.getState().workspaces[0];
    expect(restored).toMatchObject({ id: "ws-1", sessionId: "s-new", activePaneId: "p2", layoutTree: { type: "split" as const, id: "split-root", dir: "row" as const, ratio: 0.37, a: { type: "leaf" as const, id: "leaf-a", paneId: "p1" }, b: { type: "leaf" as const, id: "leaf-b", paneId: "p2" } } });
    expect(restored.panes.map((pane) => ({ id: pane.id, activeTabId: pane.activeTabId, tabs: pane.tabs.map((tab) => [tab.id, tab.tabId, tab.sessionId]) }))).toEqual([
      { id: "p1", activeTabId: "t1", tabs: [["t1", "shell-one", "s-new"]] },
      { id: "p2", activeTabId: "t2", tabs: [["t2", "shell-two", "s-new"]] },
    ]);
    expect(mocks.closeTab).not.toHaveBeenCalled();
    expect(mocks.askChoiceRemember).not.toHaveBeenCalled();
  });

  function seedDatabaseWorkspace(draft?: SqlDraft): void {
    useUi.setState({
      workspaces: [
        {
          id: "ws-db",
          kind: "db" as const,
          connId: "conn-1",
          dbKind: "mysql" as const,
          title: "db-01 · MySQL",
          assetId: "db-1",
          panes: [
            {
              id: "p1",
              tabs: [
                {
                  id: "db-tab",
                  kind: "db" as const,
                  title: "MySQL",
                  connId: "conn-1",
                  dbKind: "mysql" as const,
                  closable: true,
                },
              ],
              activeTabId: "db-tab",
            },
          ],
          activePaneId: "p1",
          closable: true,
        },
      ],
      activeWorkspaceId: null,
      workspaceRuntime: draft ? { "ws-db": { generation: 7, sqlDraft: draft } } : {},
    });
  }

  it("reconnecting a database asset with an existing workspace jumps to it without starting a new connection", async () => {
    seedDatabaseWorkspace();
    const outcome = (await connectAsset({ id: "db-1", name: "db-01", kind: "mysql" })) as ConnectOutcome;
    expect(outcome).toEqual({ ok: true });
    expect(mocks.dbConnect).not.toHaveBeenCalled();
    const { workspaces, activeWorkspaceId } = useUi.getState();
    expect(workspaces).toHaveLength(1);
    expect(activeWorkspaceId).toBe("ws-db");
  });

  it("restores a closed database tab in its original workspace and rebinds the preserved draft", async () => {
    const draft: SqlDraft = {
      text: "SELECT 1;\nSELECT 2;",
      selection: { from: 10, to: 19 },
      schema: "analytics",
      connId: "conn-1",
      dbKind: "mysql",
    };
    seedDatabaseWorkspace(draft);
    mocks.dbConnect.mockResolvedValue({ connId: "conn-2" });

    await expect(useUi.getState().closeTab("db-tab")).resolves.toBe(true);
    expect(mocks.dbDisconnect).toHaveBeenCalledWith("conn-1");
    expect(useUi.getState().workspaces[0]?.panes[0]?.tabs).toEqual([]);
    expect(useUi.getState().getWorkspaceRuntime("ws-db")).toEqual({
      generation: 7,
      sqlDraft: draft,
    });

    const outcome = (await connectAsset({ id: "db-1", name: "db-01", kind: "mysql" })) as ConnectOutcome;

    expect(outcome).toEqual({ ok: true });
    expect(mocks.dbConnect).toHaveBeenCalledExactlyOnceWith("db-1");
    const state = useUi.getState();
    expect(state.workspaces).toHaveLength(1);
    expect(state.activeWorkspaceId).toBe("ws-db");
    const workspace = state.workspaces[0];
    expect(workspace).toMatchObject({ id: "ws-db", connId: "conn-2", dbKind: "mysql" });
    expect(workspace?.panes[0]?.id).toBe("p1");
    expect(workspace?.panes[0]?.tabs).toHaveLength(1);
    expect(workspace?.panes[0]?.tabs[0]).toMatchObject({
      kind: "db",
      connId: "conn-2",
      dbKind: "mysql",
    });
    expect(state.getWorkspaceRuntime("ws-db")).toEqual({
      generation: 7,
      sqlDraft: { ...draft, connId: "conn-2" },
    });
  });

  it("keeps the empty database workspace and draft when reconnecting fails", async () => {
    const draft: SqlDraft = {
      text: "UPDATE orders SET paid = true;",
      selection: { from: 0, to: 29 },
      schema: "analytics",
      connId: "conn-1",
      dbKind: "mysql",
    };
    seedDatabaseWorkspace(draft);
    await expect(useUi.getState().closeTab("db-tab")).resolves.toBe(true);
    mocks.dbConnect.mockRejectedValue(new Error("connect failed"));

    const outcome = (await connectAsset({ id: "db-1", name: "db-01", kind: "mysql" })) as ConnectOutcome;

    expect(outcome).toEqual({ ok: false, error: "connect failed" });
    expect(mocks.dbConnect).toHaveBeenCalledExactlyOnceWith("db-1");
    const state = useUi.getState();
    expect(state.workspaces).toHaveLength(1);
    expect(state.workspaces[0]).toMatchObject({ id: "ws-db", connId: "conn-1" });
    expect(state.workspaces[0]?.panes[0]?.tabs).toEqual([]);
    expect(state.getWorkspaceRuntime("ws-db")).toMatchObject({
      generation: 7,
      sqlDraft: draft,
    });
    expect(state.getWorkspaceRuntime("ws-db")?.problems?.[0]).toMatchObject({
      source: "connect",
      connectionKey: "asset:db-1",
      summary: "connect failed",
    });
  });
});

describe("connectAsset unlocks the credential vault before connecting", () => {
  const lockedAsset = { id: "asset-9", name: "locked-01", kind: "ssh", credId: "cred-1" };

  it("reports an incorrect master password immediately and aborts the connection instead of continuing as if unlocked", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: false });
    mocks.promptText.mockResolvedValue("wrong-password");
    mocks.vaultUnlock.mockRejectedValue({ code: "bad_master_password", message: "Incorrect master password" });

    const outcome = (await connectAsset(lockedAsset)) as ConnectOutcome;
    expect(outcome).toEqual({ ok: false, canceled: true });
    expect(mocks.vaultUnlock).toHaveBeenCalledWith("wrong-password");
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
    expect(mocks.dbConnect).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("error", "解锁失败：保护密码错误：Incorrect master password");
  });

  it("continues connecting after a successful unlock", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: false });
    mocks.promptText.mockResolvedValue("right-password");
    mocks.vaultUnlock.mockResolvedValue(undefined);

    const outcome = (await connectAsset(lockedAsset)) as ConnectOutcome;
    expect(outcome).toEqual({ ok: true });
    expect(mocks.sessionConnect).toHaveBeenCalledWith("asset-9");
  });

  it("also unlocks the credential vault before connecting database assets", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: false });
    mocks.promptText.mockResolvedValue("right-password");
    mocks.vaultUnlock.mockResolvedValue(undefined);
    mocks.dbConnect.mockResolvedValue({ connId: "conn-1" });

    const outcome = (await connectAsset({
      id: "db-9",
      name: "mysql-locked",
      kind: "mysql",
      credId: "cred-1",
    })) as ConnectOutcome;
    expect(outcome).toEqual({ ok: true });
    expect(mocks.vaultUnlock).toHaveBeenCalledWith("right-password");
    expect(mocks.dbConnect).toHaveBeenCalledWith("db-9");
  });

  it("allows the connection when the status query fails", async () => {
    mocks.vaultStatus.mockRejectedValue(new Error("status down"));

    const outcome = (await connectAsset(lockedAsset)) as ConnectOutcome;
    expect(outcome).toEqual({ ok: true });
    expect(mocks.promptText).not.toHaveBeenCalled();
    expect(mocks.sessionConnect).toHaveBeenCalledWith("asset-9");
  });
});

describe("connectAsset focus guard", () => {
  it("only registers the session when the user opens a workspace during a guarded connection", async () => {
    const gate = deferred<SessionInfo>();
    mocks.sessionConnect.mockReturnValueOnce(gate.promise);
    const pending = connectAsset(SSH_ASSET, { preserveUserFocus: true });
    await flush();
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(1);

    useUi.getState().addTab({ id: "settings", kind: "settings", title: "Settings", closable: true });
    const toolsId = useUi.getState().activeWorkspaceId;
    expect(useUi.getState().workspaces.find((w) => w.id === toolsId)?.kind).toBe("tools");

    gate.resolve(SESSION);
    expect(await pending).toEqual({ ok: true });
    const state = useUi.getState();
    expect(state.sessions).toEqual([SESSION]);
    expect(state.workspaces.some((w) => w.sessionId === SESSION.id)).toBe(false);
    expect(state.workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs)).some((t) => t.kind === "terminal")).toBe(false);
    expect(state.activeWorkspaceId).toBe(toolsId);
    expect(state.connectFocusRevision).toBe(0);
  });

  it("opens the host workspace without a terminal when a guarded connection is undisturbed", async () => {
    expect(await connectAsset(SSH_ASSET, { preserveUserFocus: true })).toEqual({ ok: true });

    const state = useUi.getState();
    const workspace = state.workspaces.find((w) => w.sessionId === SESSION.id);
    expect(workspace?.panes[0].tabs).toEqual([]);
    expect(state.activeWorkspaceId).toBe(workspace?.id);
    expect(state.connectFocusRevision).toBe(1);
  });

  it("preserves the normal focus behavior for an unguarded manual connection", async () => {
    useUi.getState().addTab({ id: "settings", kind: "settings", title: "Settings", closable: true });

    expect(await connectAsset(SSH_ASSET)).toEqual({ ok: true });

    const state = useUi.getState();
    const workspace = state.workspaces.find((w) => w.sessionId === SESSION.id);
    expect(workspace?.panes[0].tabs.some((t) => t.kind === "terminal")).toBe(false);
    expect(state.activeWorkspaceId).toBe(workspace?.id);
    expect(state.connectFocusRevision).toBe(1);
  });

  it("separates mixed-options in-flight completions and keeps the asset connecting until all finish", async () => {
    const automaticGate = deferred<SessionInfo>();
    const manualGate = deferred<SessionInfo>();
    const manualSession: SessionInfo = { ...SESSION, id: "s2", name: "web-02" };
    mocks.sessionConnect
      .mockReturnValueOnce(automaticGate.promise)
      .mockReturnValueOnce(manualGate.promise);

    const automatic = connectAsset(SSH_ASSET, { preserveUserFocus: true });
    const automaticDuplicate = connectAsset(SSH_ASSET, { preserveUserFocus: true });
    const manual = connectAsset(SSH_ASSET);
    await flush();
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(2);

    useUi.getState().addTab({ id: "settings", kind: "settings", title: "Settings", closable: true });
    const toolsId = useUi.getState().activeWorkspaceId;
    automaticGate.resolve(SESSION);
    expect(await Promise.all([automatic, automaticDuplicate])).toEqual([{ ok: true }, { ok: true }]);
    expect(mocks.setConnectProgress).not.toHaveBeenCalled();
    expect(useUi.getState().connectingAssetIds).toContain("asset-1");
    expect(useUi.getState().activeWorkspaceId).toBe(toolsId);
    expect(useUi.getState().workspaces.some((w) => w.sessionId === SESSION.id)).toBe(false);

    manualGate.resolve(manualSession);
    expect(await manual).toEqual({ ok: true });
    expect(mocks.setConnectProgress).toHaveBeenCalledExactlyOnceWith("asset-1", null);
    const state = useUi.getState();
    const manualWorkspace = state.workspaces.find((w) => w.sessionId === manualSession.id);
    expect(state.connectingAssetIds).not.toContain("asset-1");
    expect(state.activeWorkspaceId).toBe(manualWorkspace?.id);
    expect(manualWorkspace?.panes[0].tabs.some((t) => t.kind === "terminal")).toBe(false);
  });
});
