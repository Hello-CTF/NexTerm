/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { deferred, flush } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  sessionConnect: vi.fn(),
  sessionDisconnect: vi.fn(),
  dbConnect: vi.fn(),
  vaultStatus: vi.fn(),
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
    sessionApi: { connect: mocks.sessionConnect, disconnect: mocks.sessionDisconnect },
    dbApi: { connect: mocks.dbConnect },
    vaultApi: { status: mocks.vaultStatus },
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
