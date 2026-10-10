/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { deferred, flush } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  sessionConnect: vi.fn(),
  sessionDisconnect: vi.fn(),
  sessionReconnect: vi.fn(),
  sessionList: vi.fn(),
  probeHostKey: vi.fn(),
  dbConnect: vi.fn(),
  vaultStatus: vi.fn(),
  vaultUnlock: vi.fn(),
  promptText: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, promptText: mocks.promptText };
});

vi.mock("../../ipc/commands", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/commands")>();
  return {
    ...actual,
    sessionApi: {
      connect: mocks.sessionConnect,
      disconnect: mocks.sessionDisconnect,
      reconnect: mocks.sessionReconnect,
      list: mocks.sessionList,
      probeHostKey: mocks.probeHostKey,
    },
    dbApi: { connect: mocks.dbConnect },
    vaultApi: { status: mocks.vaultStatus, unlock: mocks.vaultUnlock },
  };
});

import { connectAsset, useUi, type ConnectOutcome } from "../../app/store";
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
    connectingAssetIds: [],
    pushToast: mocks.toast,
  });
  mocks.sessionConnect.mockResolvedValue(SESSION);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
});

afterEach(() => {
  useUi.setState({ workspaces: [], activeWorkspaceId: null, connectingAssetIds: [] });
});

describe("connectAsset in-flight guard", () => {
  it("同一资产在途时重复调用只发起一次连接", async () => {
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

  it("不同资产的连接互不阻塞", async () => {
    const other = { id: "asset-2", name: "db-01", kind: "ssh" };
    const [a, b] = (await Promise.all([
      connectAsset(SSH_ASSET),
      connectAsset(other),
    ])) as ConnectOutcome[];
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(2);
    expect(a).toEqual({ ok: true });
    expect(b).toEqual({ ok: true });
  });

  it("失败后守卫清除，显式重试可以再次连接", async () => {
    mocks.sessionConnect.mockRejectedValueOnce({ code: "io", message: "connection refused" });
    const failed = (await connectAsset(SSH_ASSET)) as ConnectOutcome;
    expect(failed.ok).toBe(false);
    if (!failed.ok) expect(failed.error).toBe("connection refused");
    expect(useUi.getState().connectingAssetIds).not.toContain("asset-1");

    const retried = (await connectAsset(SSH_ASSET)) as ConnectOutcome;
    expect(retried).toEqual({ ok: true });
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(2);
  });

  it("在途期间不阻塞显式断开", async () => {
    const gate = deferred<SessionInfo>();
    mocks.sessionConnect.mockReturnValueOnce(gate.promise);

    const pending = connectAsset(SSH_ASSET);
    await connectAsset({ id: "asset-2", name: "db-01", kind: "ssh" });
    await sessionApi.disconnect("s-other");
    expect(mocks.sessionDisconnect).toHaveBeenCalledWith("s-other");

    gate.resolve(SESSION);
    await pending;
  });

  it("成功连接写入最近使用记录", async () => {
    await connectAsset(SSH_ASSET);
    expect(useConnectHistory.getState().entries["asset-1"]?.count).toBe(1);
  });

  it("失败连接不写入最近使用记录", async () => {
    mocks.sessionConnect.mockRejectedValueOnce({ code: "io", message: "boom" });
    await connectAsset(SSH_ASSET);
    expect(useConnectHistory.getState().entries["asset-1"]).toBeUndefined();
  });

  it("数据库资产同样受守卫保护", async () => {
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

  it("凭据解锁被取消时返回 canceled 且不发起连接", async () => {
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

describe("connectAsset 资产工作区去重", () => {
  function seedSessionWorkspace(sessionStatus: SessionInfo["status"]) {
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
          panes: [{ id: "p1", tabs: [], activeTabId: null }],
          activePaneId: "p1",
          splitRatio: 0.5,
          closable: true,
        },
      ],
      activeWorkspaceId: null,
    });
  }

  it("资产已有活会话工作区时再次连接直接跳转，不发起新连接", async () => {
    seedSessionWorkspace("connected");
    const outcome = (await connectAsset(SSH_ASSET)) as ConnectOutcome;
    expect(outcome).toEqual({ ok: true });
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
    const { workspaces, activeWorkspaceId } = useUi.getState();
    expect(workspaces).toHaveLength(1);
    expect(activeWorkspaceId).toBe("ws-1");
  });

  it("资产工作区会话已断开时直接重连，终端标签原地接管", async () => {
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

  it("重连失败（会话已删除）时清掉残留工作区走全新连接", async () => {
    seedSessionWorkspace("disconnected");
    mocks.probeHostKey.mockResolvedValue({ state: "known" });
    mocks.sessionReconnect.mockRejectedValue({ code: "not_found", message: "会话不存在" });

    const outcome = (await connectAsset(SSH_ASSET)) as ConnectOutcome;
    expect(outcome).toEqual({ ok: true });
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(1);
    const { workspaces } = useUi.getState();
    expect(workspaces).toHaveLength(1);
    expect(workspaces[0]?.id).not.toBe("ws-1");
  });

  it("数据库资产已有工作区时再次连接直接跳转，不发起新连接", async () => {
    useUi.setState({
      workspaces: [
        {
          id: "ws-db",
          kind: "db" as const,
          connId: "conn-1",
          dbKind: "mysql" as const,
          title: "db-01 · MySQL",
          assetId: "db-1",
          panes: [{ id: "p1", tabs: [], activeTabId: null }],
          activePaneId: "p1",
          splitRatio: 0.5,
          closable: true,
        },
      ],
      activeWorkspaceId: null,
    });
    const outcome = (await connectAsset({ id: "db-1", name: "db-01", kind: "mysql" })) as ConnectOutcome;
    expect(outcome).toEqual({ ok: true });
    expect(mocks.dbConnect).not.toHaveBeenCalled();
    const { workspaces, activeWorkspaceId } = useUi.getState();
    expect(workspaces).toHaveLength(1);
    expect(activeWorkspaceId).toBe("ws-db");
  });
});

describe("connectAsset 连接前解锁凭据库", () => {
  const lockedAsset = { id: "asset-9", name: "locked-01", kind: "ssh", credId: "cred-1" };

  it("保护密码错误时当场报错并中止连接，不当作解锁成功继续", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: false });
    mocks.promptText.mockResolvedValue("wrong-password");
    mocks.vaultUnlock.mockRejectedValue({ code: "bad_master_password", message: "保护密码错误" });

    const outcome = (await connectAsset(lockedAsset)) as ConnectOutcome;
    expect(outcome).toEqual({ ok: false, canceled: true });
    expect(mocks.vaultUnlock).toHaveBeenCalledWith("wrong-password");
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
    expect(mocks.dbConnect).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("error", "解锁失败：保护密码错误");
  });

  it("解锁成功后继续发起连接", async () => {
    mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: false });
    mocks.promptText.mockResolvedValue("right-password");
    mocks.vaultUnlock.mockResolvedValue(undefined);

    const outcome = (await connectAsset(lockedAsset)) as ConnectOutcome;
    expect(outcome).toEqual({ ok: true });
    expect(mocks.sessionConnect).toHaveBeenCalledWith("asset-9");
  });

  it("数据库资产连接前同样先解锁凭据库", async () => {
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

  it("status 查询失败时保持放行，不阻塞连接", async () => {
    mocks.vaultStatus.mockRejectedValue(new Error("status down"));

    const outcome = (await connectAsset(lockedAsset)) as ConnectOutcome;
    expect(outcome).toEqual({ ok: true });
    expect(mocks.promptText).not.toHaveBeenCalled();
    expect(mocks.sessionConnect).toHaveBeenCalledWith("asset-9");
  });
});
