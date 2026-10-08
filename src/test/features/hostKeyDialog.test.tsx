/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, flush, mount, waitFor, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  assetSearch: vi.fn(),
  sessionList: vi.fn(),
  vaultStatus: vi.fn(),
  connect: vi.fn(),
  reconnect: vi.fn(),
  probeHostKey: vi.fn(),
  knownHostAccept: vi.fn(),
}));

vi.mock("../../ipc/commands", () => ({
  assetApi: {
    list: mocks.assetList,
    search: mocks.assetSearch,
    knownHostAccept: mocks.knownHostAccept,
  },
  sessionApi: {
    list: mocks.sessionList,
    connectLocal: vi.fn(),
    connect: mocks.connect,
    reconnect: mocks.reconnect,
    probeHostKey: mocks.probeHostKey,
  },
  vaultApi: { status: mocks.vaultStatus, listCredentials: vi.fn() },
  terminalApi: {},
  dbApi: {},
}));

vi.mock("../../features/terminal/TerminalPane", () => ({ TerminalPane: () => null }));
vi.mock("../../features/terminal/BackgroundSessions", () => ({ BackgroundSessions: () => null }));
vi.mock("../../features/files/FileBrowser", () => ({ FileBrowser: () => null }));
vi.mock("../../features/files/FileTree", () => ({ FileTree: () => null }));
vi.mock("../../features/files/MountPanel", () => ({ MountPanel: () => null }));
vi.mock("../../features/files/FileEditor", () => ({ FileEditor: () => null }));
vi.mock("../../features/forward/ForwardPanel", () => ({ ForwardPanel: () => null }));
vi.mock("../../features/docker/DockerPanel", () => ({ DockerPanel: () => null }));
vi.mock("../../features/db/DbPanel", () => ({ DbPanel: () => null }));
vi.mock("../../features/ai/AiSidebar", () => ({ AiSidebar: () => null }));
vi.mock("../../features/settings/SettingsView", () => ({ SettingsView: () => null }));
vi.mock("../../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../../features/credentials/CredentialsView", () => ({ CredentialsView: () => null }));
vi.mock("../../features/explorer/AssetTree", () => ({ AssetTree: () => null }));
vi.mock("../../app/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));

import App from "../../app/App";
import { connectAsset, useUi } from "../../app/store";

const PENDING_DETAIL = {
  host: "10.0.0.8",
  port: 22,
  keyType: "ssh-ed25519",
  fingerprint: "SHA256:newfp",
  changed: false,
};
const SESSION = {
  id: "s1",
  assetId: "a1",
  name: "web-01",
  kind: "ssh",
  status: "failed" as const,
  tabs: [],
  createdAt: 0,
};
const FRESH_SESSION = { ...SESSION, id: "s2", status: "connected" };
// 重连成功后会话 id 不变; 后端 reconnect 先把状态置 reconnecting, 前端轮询读到终态。
const RECONNECTED_SESSION = { ...SESSION, status: "connected" as const };

// 重连发起前置为旧状态, reconnect 被调用后 list 才读到 connected,
// 避免 App 挂载时的 list 把会话刷成 connected 而走 alive 快速路径。
function listUntilReconnectThenConnected(): void {
  mocks.sessionList.mockImplementation(async () =>
    mocks.reconnect.mock.calls.length > 0 ? [RECONNECTED_SESSION] : [SESSION],
  );
}

function seedWorkspace(withSession: boolean): void {
  useUi.setState({
    sessions: withSession ? [SESSION] : [],
    toasts: [],
    leftOpen: false,
    rightOpen: false,
    leftMode: "assets",
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title: "ws-1",
        closable: true,
        ...(withSession ? { sessionId: "s1" } : {}),
        assetId: "a1",
        assetKind: "ssh",
        panes: [
          {
            id: "p1",
            activeTabId: "t1",
            tabs: [{ id: "t1", kind: "settings", title: "设置", closable: true }],
          },
        ],
        activePaneId: "p1",
        splitRatio: 0.5,
      },
    ],
    activeWorkspaceId: "ws1",
  });
}

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(App)));
}

function dialog(): HTMLElement | null {
  return document.querySelector<HTMLElement>('[role="alertdialog"]');
}

function clickRail(label: string): void {
  const button = document.querySelector<HTMLButtonElement>(
    `.nx-rail button[aria-label="${label}"]`,
  );
  if (!button) throw new Error(`Rail button not found: ${label}`);
  click(button);
}

function toastTexts(): string {
  return useUi
    .getState()
    .toasts.map((t) => t.text)
    .join("\n");
}

let mounted: MountedView | undefined;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.assetList.mockResolvedValue([]);
  mocks.assetSearch.mockResolvedValue([]);
  mocks.sessionList.mockResolvedValue([SESSION]);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  useUi.setState({ appDialog: null, toasts: [] });
});

describe("新建终端（App openNewTerminal）主机指纹确认", () => {
  it("断开的 ssh 会话：pending 时弹指纹确认，取消则不重连", async () => {
    seedWorkspace(true);
    mocks.probeHostKey.mockResolvedValue({
      host: "10.0.0.8",
      port: 22,
      keyType: "ssh-ed25519",
      fingerprint: "SHA256:newfp",
      state: "pending",
    });
    mounted = mountApp();
    await flush();

    clickRail("新建终端");
    await flush();
    await waitFor(() => expect(dialog()).not.toBeNull());

    const text = dialog()?.textContent ?? "";
    expect(text).toContain("首次连接 10.0.0.8:22");
    expect(text).toContain("SHA256:newfp");
    expect(mocks.reconnect).not.toHaveBeenCalled();

    clickButton(dialog() as HTMLElement, "取消");
    await waitFor(() => expect(dialog()).toBeNull());
    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
    expect(mocks.reconnect).not.toHaveBeenCalled();
    expect(toastTexts()).toContain("已取消重连");
  });

  it("changed 时弹出变更警告并展示原/新指纹，接受后记账并重连", async () => {
    seedWorkspace(true);
    mocks.probeHostKey.mockResolvedValue({
      host: "10.0.0.8",
      port: 22,
      keyType: "ssh-ed25519",
      fingerprint: "SHA256:newfp",
      state: "changed",
      known: [{ keyType: "ssh-ed25519", fingerprint: "SHA256:oldfp" }],
    });
    mocks.reconnect.mockResolvedValue(true);
    listUntilReconnectThenConnected();
    mounted = mountApp();
    await flush();

    clickRail("新建终端");
    await flush();
    await waitFor(() => expect(dialog()).not.toBeNull());

    const text = dialog()?.textContent ?? "";
    expect(text).toContain("主机密钥已变更 10.0.0.8:22");
    expect(text).toContain("SHA256:oldfp");
    expect(text).toContain("SHA256:newfp");

    clickButton(dialog() as HTMLElement, "确定");
    await waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("s1"));
    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "10.0.0.8",
      22,
      "ssh-ed25519",
      "SHA256:newfp",
    );
  });

  it("指纹未变化时不弹窗直接重连", async () => {
    seedWorkspace(true);
    mocks.probeHostKey.mockResolvedValue({
      host: "10.0.0.8",
      port: 22,
      keyType: "ssh-ed25519",
      fingerprint: "SHA256:newfp",
      state: "known",
    });
    mocks.reconnect.mockResolvedValue(true);
    listUntilReconnectThenConnected();
    mounted = mountApp();
    await flush();

    clickRail("新建终端");
    await flush();
    await waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("s1"));
    expect(dialog()).toBeNull();
    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
  });

  it("probe 自身报 host_key_pending（跳板机）时弹确认，取消则不重连", async () => {
    seedWorkspace(true);
    mocks.probeHostKey.mockRejectedValue({
      code: "host_key_pending",
      message: "unknown SSH host key for jump.example:22",
      detail: {
        host: "jump.example",
        port: 22,
        keyType: "ssh-ed25519",
        fingerprint: "SHA256:jumpfp",
        changed: false,
      },
    });
    mounted = mountApp();
    await flush();

    clickRail("新建终端");
    await flush();
    await waitFor(() => expect(dialog()).not.toBeNull());

    const text = dialog()?.textContent ?? "";
    expect(text).toContain("首次连接 jump.example:22");
    expect(text).toContain("SHA256:jumpfp");
    expect(mocks.reconnect).not.toHaveBeenCalled();

    clickButton(dialog() as HTMLElement, "取消");
    await waitFor(() => expect(dialog()).toBeNull());
    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
    expect(mocks.reconnect).not.toHaveBeenCalled();
    expect(toastTexts()).toContain("已取消重连");
  });

  it("probe 报 changed 时展示原/新指纹，接受后记账并重连", async () => {
    seedWorkspace(true);
    mocks.probeHostKey.mockRejectedValue({
      code: "host_key_pending",
      message: "SSH host key for jump.example:22 changed",
      detail: {
        host: "jump.example",
        port: 22,
        keyType: "ssh-ed25519",
        fingerprint: "SHA256:jumpnew",
        changed: true,
        known: [{ keyType: "ssh-ed25519", fingerprint: "SHA256:jumpold" }],
      },
    });
    mocks.reconnect.mockResolvedValue(true);
    listUntilReconnectThenConnected();
    mounted = mountApp();
    await flush();

    clickRail("新建终端");
    await flush();
    await waitFor(() => expect(dialog()).not.toBeNull());

    const text = dialog()?.textContent ?? "";
    expect(text).toContain("主机密钥已变更 jump.example:22");
    expect(text).toContain("SHA256:jumpold");
    expect(text).toContain("SHA256:jumpnew");

    clickButton(dialog() as HTMLElement, "确定");
    await waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("s1"));
    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "jump.example",
      22,
      "ssh-ed25519",
      "SHA256:jumpnew",
    );
  });

  it("探测不可用时回退为直接重连，不弹窗", async () => {
    seedWorkspace(true);
    mocks.probeHostKey.mockRejectedValue({ code: "not_found", message: "unknown command" });
    mocks.reconnect.mockResolvedValue(true);
    listUntilReconnectThenConnected();
    mounted = mountApp();
    await flush();

    clickRail("新建终端");
    await flush();
    await waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("s1"));
    expect(dialog()).toBeNull();
  });

  it("非 ssh 会话不探测指纹直接重连", async () => {
    seedWorkspace(true);
    mocks.sessionList.mockImplementation(async () =>
      mocks.reconnect.mock.calls.length > 0
        ? [{ ...SESSION, kind: "winrm", status: "connected" as const }]
        : [{ ...SESSION, kind: "winrm" }],
    );
    mocks.reconnect.mockResolvedValue(true);
    mounted = mountApp();
    await flush();

    clickRail("新建终端");
    await flush();
    await waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("s1"));
    expect(mocks.probeHostKey).not.toHaveBeenCalled();
    expect(dialog()).toBeNull();
  });

  it("工作区只剩资产时：connect 报 host_key_pending，取消不重试，接受后重试并开终端", async () => {
    seedWorkspace(false);
    mocks.sessionList.mockResolvedValue([]);
    mocks.connect
      .mockRejectedValueOnce({
        code: "host_key_pending",
        message: "unknown SSH host key for 10.0.0.8:22",
        detail: PENDING_DETAIL,
      })
      .mockRejectedValueOnce({
        code: "host_key_pending",
        message: "unknown SSH host key for 10.0.0.8:22",
        detail: PENDING_DETAIL,
      })
      .mockResolvedValueOnce(FRESH_SESSION);
    mounted = mountApp();
    await flush();

    clickRail("新建终端");
    await flush();
    await waitFor(() => expect(dialog()).not.toBeNull());
    expect(dialog()?.textContent).toContain("首次连接 10.0.0.8:22");
    expect(mocks.connect).toHaveBeenCalledTimes(1);

    clickButton(dialog() as HTMLElement, "取消");
    await waitFor(() => expect(dialog()).toBeNull());
    expect(mocks.connect).toHaveBeenCalledTimes(1);
    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
    expect(toastTexts()).toContain("已取消重连");

    clickRail("新建终端");
    await flush();
    await waitFor(() => expect(dialog()).not.toBeNull());
    clickButton(dialog() as HTMLElement, "确定");
    await waitFor(() => expect(mocks.connect).toHaveBeenCalledTimes(3));
    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "10.0.0.8",
      22,
      "ssh-ed25519",
      "SHA256:newfp",
    );
    await waitFor(() =>
      expect(useUi.getState().sessions.some((s) => s.id === "s2")).toBe(true),
    );
  });
});

describe("指纹确认弹窗去重", () => {
  it("connectAsset 已持有弹窗时，新建终端加入同一弹窗而不是再弹一次", async () => {
    seedWorkspace(true);
    mocks.probeHostKey.mockResolvedValue({
      host: "10.0.0.8",
      port: 22,
      keyType: "ssh-ed25519",
      fingerprint: "SHA256:newfp",
      state: "pending",
    });
    mocks.connect
      .mockRejectedValueOnce({
        code: "host_key_pending",
        message: "unknown SSH host key for 10.0.0.8:22",
        detail: PENDING_DETAIL,
      })
      .mockResolvedValueOnce(FRESH_SESSION);
    mocks.reconnect.mockResolvedValue(true);
    listUntilReconnectThenConnected();
    mounted = mountApp();
    await flush();

    clickRail("新建终端");
    await flush();
    await waitFor(() => expect(dialog()).not.toBeNull());

    const connectPromise = connectAsset({ id: "a1", name: "web-01", kind: "ssh" });
    await flush();
    expect(document.querySelectorAll('[role="alertdialog"]')).toHaveLength(1);

    clickButton(dialog() as HTMLElement, "确定");
    await connectPromise;
    await waitFor(() => expect(mocks.reconnect).toHaveBeenCalledWith("s1"));

    expect(document.querySelectorAll('[role="alertdialog"]')).toHaveLength(0);
    expect(mocks.knownHostAccept).toHaveBeenCalledWith(
      "10.0.0.8",
      22,
      "ssh-ed25519",
      "SHA256:newfp",
    );
    expect(mocks.connect).toHaveBeenCalledTimes(2);
    expect(mocks.probeHostKey).toHaveBeenCalledTimes(1);
  });

  it("共享弹窗取消时，两条路径都不连接", async () => {
    seedWorkspace(true);
    mocks.probeHostKey.mockResolvedValue({
      host: "10.0.0.8",
      port: 22,
      keyType: "ssh-ed25519",
      fingerprint: "SHA256:newfp",
      state: "pending",
    });
    mocks.connect.mockRejectedValue({
      code: "host_key_pending",
      message: "unknown SSH host key for 10.0.0.8:22",
      detail: PENDING_DETAIL,
    });
    mounted = mountApp();
    await flush();

    clickRail("新建终端");
    await flush();
    await waitFor(() => expect(dialog()).not.toBeNull());

    const connectPromise = connectAsset({ id: "a1", name: "web-01", kind: "ssh" });
    await flush();
    expect(document.querySelectorAll('[role="alertdialog"]')).toHaveLength(1);

    clickButton(dialog() as HTMLElement, "取消");
    await connectPromise;
    await flush();

    expect(mocks.knownHostAccept).not.toHaveBeenCalled();
    expect(mocks.connect).toHaveBeenCalledTimes(1);
    expect(mocks.reconnect).not.toHaveBeenCalled();
    expect(toastTexts()).toContain("已取消重连");
    expect(toastTexts()).toContain("已取消连接");
  });
});
