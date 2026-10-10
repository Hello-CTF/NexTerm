/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, type MountedView } from "./reactTestUtils";
import type { SessionInfo } from "../../ipc/commands";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  assetSearch: vi.fn(),
  sessionList: vi.fn(),
  sessionReconnect: vi.fn(),
  probeHostKey: vi.fn(),
  vaultStatus: vi.fn(),
  ask: vi.fn(),
}));

vi.mock("../../ui/dialogs", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/dialogs")>();
  return { ...actual, ask: mocks.ask };
});

vi.mock("../../ipc/commands", () => ({
  assetApi: { list: mocks.assetList, search: mocks.assetSearch },
  sessionApi: {
    list: mocks.sessionList,
    connectLocal: vi.fn(),
    connect: vi.fn(),
    reconnect: mocks.sessionReconnect,
    probe: vi.fn(),
    probeHostKey: mocks.probeHostKey,
  },
  vaultApi: { status: mocks.vaultStatus, listCredentials: vi.fn() },
  layoutApi: {
    get: vi.fn().mockResolvedValue({ revision: 0, updatedAt: 0, data: null }),
    put: vi.fn().mockResolvedValue({ saved: true, revision: 1, conflict: false }),
  },
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
vi.mock("../../features/explorer/AssetTree", () => ({ AssetTree: () => null }));
vi.mock("../../app/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../../app/QuickConnect", () => ({ QuickConnect: () => null }));
vi.mock("../../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));

import App from "../../app/App";
import {
  isSessionReconnectPending,
  reconnectSessionAndWait,
  SESSION_RECONNECT_WAIT_MS,
  useUi,
  type AppTab,
} from "../../app/store";

function session(status: SessionInfo["status"]): SessionInfo {
  return { id: "s1", assetId: "a1", name: "web-01", kind: "ssh", status, tabs: [], createdAt: 0 };
}

let mounted: MountedView | undefined;

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(App)));
}

async function waitUntil(assertion: () => void, timeout = 5000): Promise<void> {
  await act(async () => {
    await vi.waitFor(assertion, { timeout, interval: 25 });
  });
}

function newTerminalButton(): HTMLButtonElement {
  const btn = document.querySelector<HTMLButtonElement>('button[aria-label="新建终端"]');
  if (!btn) throw new Error("rail 新建终端 button not found");
  return btn;
}

function terminalTabs(): AppTab[] {
  const tabs = useUi.getState().workspaces[0]?.panes[0]?.tabs ?? [];
  return tabs.filter((t) => t.kind === "terminal");
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.assetList.mockResolvedValue([]);
  mocks.assetSearch.mockResolvedValue([]);
  mocks.sessionList.mockResolvedValue([session("disconnected")]);
  mocks.sessionReconnect.mockResolvedValue(true);
  mocks.probeHostKey.mockResolvedValue({ state: "known" });
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  useUi.setState({
    sessions: [session("disconnected")],
    toasts: [],
    leftOpen: false,
    rightOpen: false,
    leftMode: "assets",
    workspaces: [
      {
        id: "ws1",
        kind: "session",
        title: "web-01",
        closable: true,
        sessionId: "s1",
        assetId: "a1",
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
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("断线后新建终端: 重连一次点击到底", () => {
  it("重连成功后自动打开终端标签, 无需二次点击", async () => {
    mocks.sessionList
      .mockResolvedValueOnce([session("disconnected")])
      .mockResolvedValueOnce([session("reconnecting")])
      .mockResolvedValue([session("connected")]);
    mounted = mountApp();
    await flush();

    click(newTerminalButton());
    await waitUntil(() => expect(terminalTabs()).toHaveLength(1));

    expect(mocks.probeHostKey).toHaveBeenCalledWith("a1");
    expect(mocks.sessionReconnect).toHaveBeenCalledTimes(1);
    expect(mocks.sessionReconnect).toHaveBeenCalledWith("s1");
    expect(useUi.getState().sessions.find((s) => s.id === "s1")?.status).toBe("connected");
    const toasts = useUi.getState().toasts;
    expect(toasts.some((t) => t.text.includes("正在重连"))).toBe(true);
    expect(toasts.filter((t) => t.kind === "error").map((t) => t.text)).toEqual([]);
  });

  it("重连未能启动时报错且不打开终端", async () => {
    mocks.sessionReconnect.mockResolvedValue(false);
    mounted = mountApp();
    await flush();

    click(newTerminalButton());
    await waitUntil(() =>
      expect(
        useUi.getState().toasts.some(
          (t) => t.kind === "error" && t.text === "重连失败：重连未能启动",
        ),
      ).toBe(true),
    );

    expect(terminalTabs()).toHaveLength(0);
    expect(mocks.sessionList).toHaveBeenCalledTimes(1);
    expect(isSessionReconnectPending("s1")).toBe(false);
  });

  it("重连等待期间的重复点击不会重复发起重连或开多个终端", async () => {
    mocks.sessionList
      .mockResolvedValueOnce([session("disconnected")])
      .mockResolvedValueOnce([session("reconnecting")])
      .mockResolvedValue([session("connected")]);
    mounted = mountApp();
    await flush();

    const btn = newTerminalButton();
    click(btn);
    click(btn);
    click(btn);
    await waitUntil(() => expect(terminalTabs()).toHaveLength(1));

    expect(mocks.sessionReconnect).toHaveBeenCalledTimes(1);
    const toasts = useUi.getState().toasts;
    expect(toasts.some((t) => t.text === "正在重连，连接成功后自动新建终端")).toBe(true);
  });

  it("重连等待中会话已 connected 时再点不会重复打开终端", async () => {
    mocks.sessionList
      .mockResolvedValueOnce([session("disconnected")])
      .mockResolvedValue([session("reconnecting")]);
    mounted = mountApp();
    await flush();

    click(newTerminalButton());
    await waitUntil(() => expect(mocks.sessionReconnect).toHaveBeenCalledTimes(1));

    // connected 事件先于 helper 的轮询到达 store, 此时连点走 pending 检查而不是 alive 快速路径
    act(() => useUi.setState({ sessions: [session("connected")] }));
    click(newTerminalButton());
    click(newTerminalButton());
    expect(terminalTabs()).toHaveLength(0);

    mocks.sessionList.mockResolvedValue([session("connected")]);
    await waitUntil(() => expect(terminalTabs()).toHaveLength(1));
    expect(mocks.sessionReconnect).toHaveBeenCalledTimes(1);
    expect(useUi.getState().toasts.some((t) => t.kind === "error")).toBe(false);
  });
});

describe("reconnectSessionAndWait", () => {
  it("默认等待上限覆盖后端重连生命周期(10 次尝试退避合计 181s)", () => {
    expect(SESSION_RECONNECT_WAIT_MS).toBeGreaterThanOrEqual(181_000);
  });

  it("首次 list 读到启动前的旧 disconnected 不误判, reconnecting 后 connected 正常返回", async () => {
    mocks.sessionList
      .mockResolvedValueOnce([session("disconnected")])
      .mockResolvedValueOnce([session("disconnected")])
      .mockResolvedValueOnce([session("reconnecting")])
      .mockResolvedValue([session("connected")]);

    const result = await reconnectSessionAndWait(session("disconnected"), {
      timeoutMs: 2000,
      intervalMs: 10,
    });
    expect(result?.status).toBe("connected");
  });

  it("进入重连后 failed 时以中文原因拒绝", async () => {
    mocks.sessionList
      .mockResolvedValueOnce([session("reconnecting")])
      .mockResolvedValue([session("failed")]);

    await expect(
      reconnectSessionAndWait(session("disconnected"), { timeoutMs: 500, intervalMs: 10 }),
    ).rejects.toThrow("会话连接失败");
    expect(isSessionReconnectPending("s1")).toBe(false);
  });

  it("一直未连上时按超时拒绝", async () => {
    mocks.sessionList.mockResolvedValue([session("reconnecting")]);

    await expect(
      reconnectSessionAndWait(session("disconnected"), { timeoutMs: 60, intervalMs: 10 }),
    ).rejects.toThrow("重连超时");
  });

  it("超时后 inflight 清理, 可再次发起重连", async () => {
    mocks.sessionList.mockResolvedValue([session("reconnecting")]);
    await expect(
      reconnectSessionAndWait(session("disconnected"), { timeoutMs: 30, intervalMs: 10 }),
    ).rejects.toThrow("重连超时");
    expect(isSessionReconnectPending("s1")).toBe(false);

    mocks.sessionList.mockResolvedValue([session("connected")]);
    await expect(
      reconnectSessionAndWait(session("disconnected"), { timeoutMs: 500, intervalMs: 10 }),
    ).resolves.toMatchObject({ status: "connected" });
    expect(mocks.sessionReconnect).toHaveBeenCalledTimes(2);
    expect(isSessionReconnectPending("s1")).toBe(false);
  });

  it("超过 20s 后成功仍返回(默认超时, 短轮询模拟长重连)", async () => {
    vi.useFakeTimers();
    try {
      let calls = 0;
      mocks.sessionList.mockImplementation(async () => {
        calls += 1;
        return [session(calls <= 25 ? "reconnecting" : "connected")];
      });

      const promise = reconnectSessionAndWait(session("disconnected"), { intervalMs: 1000 });
      await vi.advanceTimersByTimeAsync(0);
      await vi.advanceTimersByTimeAsync(26_000);

      await expect(promise).resolves.toMatchObject({ status: "connected" });
      expect(calls).toBeGreaterThan(20);
      expect(isSessionReconnectPending("s1")).toBe(false);
    } finally {
      vi.useRealTimers();
    }
  });

  it("主机指纹确认取消时返回 null 且不发起重连", async () => {
    mocks.probeHostKey.mockResolvedValue({
      state: "unknown",
      host: "10.0.0.8",
      port: 22,
      keyType: "ssh-ed25519",
      fingerprint: "ab",
      known: [],
    });
    mocks.ask.mockResolvedValue(false);

    const result = await reconnectSessionAndWait(session("disconnected"), {
      timeoutMs: 200,
      intervalMs: 10,
    });
    expect(result).toBeNull();
    expect(mocks.sessionReconnect).not.toHaveBeenCalled();
  });
});
