/** @vitest-environment jsdom */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, deferred, flush, mount, type MountedView } from "./reactTestUtils";
import type { KnownHostDto } from "../../ipc/types";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "web";
  return {
    knownHostList: vi.fn(),
    knownHostRemove: vi.fn(),
    knownHostAccept: vi.fn(),
    sessionConnect: vi.fn(),
    syncDigest: vi.fn(),
    syncToken: vi.fn(),
    rotateToken: vi.fn(),
    ask: vi.fn(),
    toast: vi.fn(),
  };
});

vi.mock("../../ipc/commands", () => ({
  assetApi: {
    knownHostList: mocks.knownHostList,
    knownHostRemove: mocks.knownHostRemove,
    knownHostAccept: mocks.knownHostAccept,
  },
  syncApi: {
    digest: mocks.syncDigest,
    token: mocks.syncToken,
    rotateToken: mocks.rotateToken,
  },
  dbApi: {},
  sessionApi: { connect: mocks.sessionConnect },
  terminalApi: {},
  vaultApi: {},
}));
vi.mock("../../ui/dialogs", () => ({ ask: mocks.ask }));

import { KnownHostsCard } from "../../features/settings/KnownHostsCard";
import { SyncCard } from "../../features/settings/SyncCard";
import { connectAsset, useUi } from "../../app/store";

const KH1: KnownHostDto = {
  id: "kh1",
  host: "10.0.0.8",
  port: 22,
  keyType: "ssh-ed25519",
  fingerprint: "SHA256:aaa111",
  addedAt: 1,
};
const KH2: KnownHostDto = {
  id: "kh2",
  host: "db.internal",
  port: 2222,
  keyType: "ssh-rsa",
  fingerprint: "SHA256:bbb222",
  addedAt: 2,
};

function mountSyncCard(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(SyncCard)));
}

describe("KnownHostsCard", () => {
  let mounted: MountedView | undefined;

  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    useUi.setState({ pushToast: mocks.toast });
  });
  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("shows loading first, then the trusted hosts with fingerprints", async () => {
    const slow = deferred<KnownHostDto[]>();
    mocks.knownHostList.mockReturnValue(slow.promise);
    mounted = mount(createElement(KnownHostsCard));
    expect(mounted.container.textContent).toContain("读取中…");

    slow.resolve([KH1, KH2]);
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("10.0.0.8:22");
    expect(text).toContain("SHA256:aaa111");
    expect(text).toContain("db.internal:2222");
    expect(text).toContain("2 台");
  });

  it("shows an empty hint when nothing is trusted yet", async () => {
    mocks.knownHostList.mockResolvedValue([]);
    mounted = mount(createElement(KnownHostsCard));
    await flush();
    expect(mounted.container.textContent).toContain("还没有信任过任何主机");
  });

  it("surfaces a list failure with a working retry", async () => {
    mocks.knownHostList
      .mockRejectedValueOnce(new Error("磁盘炸了"))
      .mockResolvedValueOnce([KH1]);
    mounted = mount(createElement(KnownHostsCard));
    await flush();
    expect(mounted.container.textContent).toContain("磁盘炸了");

    clickButton(mounted.container, "重试");
    await flush();
    expect(mounted.container.textContent).toContain("10.0.0.8:22");
    expect(mocks.knownHostList).toHaveBeenCalledTimes(2);
  });

  it("drops a stale in-flight list result that lands after a newer one", async () => {
    const stale = deferred<KnownHostDto[]>();
    mocks.knownHostList.mockReturnValueOnce(stale.promise).mockResolvedValueOnce([KH2]);
    mounted = mount(createElement(KnownHostsCard));

    clickButton(mounted.container, "刷新");
    await flush();
    expect(mounted.container.textContent).toContain("db.internal:2222");

    stale.resolve([KH1]);
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("db.internal:2222");
    expect(text).not.toContain("10.0.0.8:22");
  });

  it("asks before revoking and does nothing when the confirmation is declined", async () => {
    mocks.knownHostList.mockResolvedValue([KH1]);
    mocks.ask.mockResolvedValue(false);
    mounted = mount(createElement(KnownHostsCard));
    await flush();

    clickButton(mounted.container, "撤销信任");
    await flush();
    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const question = String(mocks.ask.mock.calls[0]?.[0] ?? "");
    expect(question).toContain("10.0.0.8:22");
    expect(question).toContain("SHA256:aaa111");
    expect(mocks.knownHostRemove).not.toHaveBeenCalled();
    expect(mounted.container.textContent).toContain("10.0.0.8:22");
  });

  it("revokes only after confirmation, then reloads the list", async () => {
    mocks.knownHostList.mockResolvedValueOnce([KH1, KH2]).mockResolvedValueOnce([KH2]);
    mocks.ask.mockResolvedValue(true);
    mocks.knownHostRemove.mockResolvedValue(undefined);
    mounted = mount(createElement(KnownHostsCard));
    await flush();

    clickButton(mounted.container, "撤销信任");
    await flush();
    expect(mocks.knownHostRemove).toHaveBeenCalledWith("kh1");
    expect(mounted.container.textContent).not.toContain("10.0.0.8:22");
    expect(mounted.container.textContent).toContain("db.internal:2222");
    expect(mocks.knownHostList).toHaveBeenCalledTimes(2);
    expect(mocks.toast).toHaveBeenCalledWith("success", expect.stringContaining("已撤销"));
  });

  it("keeps the entry and reports when the revoke itself fails", async () => {
    mocks.knownHostList.mockResolvedValue([KH1]);
    mocks.ask.mockResolvedValue(true);
    mocks.knownHostRemove.mockRejectedValue(new Error("只读数据库"));
    mounted = mount(createElement(KnownHostsCard));
    await flush();

    clickButton(mounted.container, "撤销信任");
    await flush();
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.stringContaining("撤销失败"));
    expect(mocks.knownHostList).toHaveBeenCalledTimes(1);
    expect(mounted.container.textContent).toContain("10.0.0.8:22");
  });
});

describe("SyncCard（服务端一面）", () => {
  let mounted: MountedView | undefined;

  beforeEach(() => {
    vi.clearAllMocks();
    document.body.replaceChildren();
    useUi.setState({ pushToast: mocks.toast });
    mocks.syncDigest.mockResolvedValue({ origin: "local", assets: [] });
    mocks.syncToken.mockResolvedValue("sync-token-secret-xyz");
    mocks.rotateToken.mockResolvedValue("sync-token-rotated");
  });
  afterEach(() => {
    mounted?.unmount();
    mounted = undefined;
  });

  it("keeps the sync token masked until explicitly revealed", async () => {
    mounted = mountSyncCard();
    await flush();
    expect(mounted.container.textContent).toContain("这台是同步目标");
    expect(mounted.container.textContent).toContain("•••");
    expect(mounted.container.textContent).not.toContain("sync-token-secret-xyz");

    clickButton(mounted.container, "显示");
    expect(mounted.container.textContent).toContain("sync-token-secret-xyz");

    clickButton(mounted.container, "隐藏");
    expect(mounted.container.textContent).not.toContain("sync-token-secret-xyz");
  });

  it("describes the token as restricted sync-only and keeps the trusted-auth warning", async () => {
    mounted = mountSyncCard();
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("只能用于资产同步");
    expect(text).toContain("/sync/rpc");
    expect(text).not.toContain("完全控制权");
    expect(text).toContain("反向代理");
  });
});

describe("connectAsset 主机指纹确认", () => {
  const asset = { id: "asset-1", name: "测试机", kind: "ssh" };
  const pendingDetail = {
    host: "10.0.0.8",
    port: 22,
    keyType: "ssh-ed25519",
    fingerprint: "SHA256:newfp",
    changed: false,
  };
  const sessionInfo = {
    id: "s1",
    assetId: "asset-1",
    name: "测试机",
    kind: "ssh",
    status: "connected",
    tabs: [],
    createdAt: 0,
  };

  beforeEach(() => {
    vi.clearAllMocks();
    useUi.setState({ pushToast: mocks.toast });
  });

  it("首次连接时弹出指纹确认，显式接受后记录信任并重连", async () => {
    mocks.sessionConnect
      .mockRejectedValueOnce({
        code: "host_key_pending",
        message: "unknown SSH host key for 10.0.0.8:22",
        detail: pendingDetail,
      })
      .mockResolvedValueOnce(sessionInfo);
    mocks.ask.mockResolvedValue(true);
    mocks.knownHostAccept.mockResolvedValue(undefined);

    await connectAsset(asset);

    expect(mocks.ask).toHaveBeenCalledTimes(1);
    const [question, options] = mocks.ask.mock.calls[0] as [string, Record<string, unknown>];
    expect(question).toContain("首次连接 10.0.0.8:22");
    expect(question).toContain("ssh-ed25519");
    expect(question).toContain("SHA256:newfp");
    expect(options).toMatchObject({ title: "确认主机指纹", kind: "warning" });
    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "10.0.0.8",
      22,
      "ssh-ed25519",
      "SHA256:newfp",
    );
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(2);
    expect(mocks.sessionConnect).toHaveBeenLastCalledWith("asset-1", true);
    expect(useUi.getState().sessions.some((s) => s.id === "s1")).toBe(true);
  });

  it("密钥变更时弹出变更警告并展示原指纹与新指纹", async () => {
    const changedDetail = {
      ...pendingDetail,
      changed: true,
      known: [{ keyType: "ssh-ed25519", fingerprint: "SHA256:oldfp" }],
    };
    mocks.sessionConnect
      .mockRejectedValueOnce({
        code: "host_key_pending",
        message: "SSH host key for 10.0.0.8:22 changed",
        detail: changedDetail,
      })
      .mockResolvedValueOnce(sessionInfo);
    mocks.ask.mockResolvedValue(true);
    mocks.knownHostAccept.mockResolvedValue(undefined);

    await connectAsset(asset);

    const [question, options] = mocks.ask.mock.calls[0] as [string, Record<string, unknown>];
    expect(question).toContain("主机密钥已变更 10.0.0.8:22");
    expect(question).toContain("SHA256:oldfp");
    expect(question).toContain("SHA256:newfp");
    expect(options).toMatchObject({ title: "主机密钥变更警告", kind: "warning" });
    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "10.0.0.8",
      22,
      "ssh-ed25519",
      "SHA256:newfp",
    );
    expect(mocks.sessionConnect).toHaveBeenLastCalledWith("asset-1", true);
  });

  it("拒绝确认时不记录信任、不再重连", async () => {
    mocks.sessionConnect.mockRejectedValueOnce({
      code: "host_key_pending",
      message: "unknown SSH host key for 10.0.0.8:22",
      detail: pendingDetail,
    });
    mocks.ask.mockResolvedValue(false);

    await connectAsset(asset);

    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
    expect(mocks.sessionConnect).toHaveBeenCalledTimes(1);
    expect(mocks.toast).toHaveBeenCalledWith("info", "已取消连接");
  });

  it("记录信任后重连失败时上报错误", async () => {
    mocks.sessionConnect
      .mockRejectedValueOnce({
        code: "host_key_pending",
        message: "unknown SSH host key for 10.0.0.8:22",
        detail: pendingDetail,
      })
      .mockRejectedValueOnce({ code: "ssh", message: "SSH handshake: broken" });
    mocks.ask.mockResolvedValue(true);
    mocks.knownHostAccept.mockResolvedValue(undefined);

    await connectAsset(asset);

    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "10.0.0.8",
      22,
      "ssh-ed25519",
      "SHA256:newfp",
    );
    expect(mocks.toast).toHaveBeenCalledWith("error", expect.any(String));
  });
});
