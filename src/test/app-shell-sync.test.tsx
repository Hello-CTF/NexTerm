/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { clickButton, flush, flushUntil, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  sessionList: vi.fn(),
  vaultStatus: vi.fn(),
  syncLinkGet: vi.fn(),
  syncLinkSet: vi.fn(),
  syncStatus: vi.fn(),
  syncNow: vi.fn(),
}));

const demoState = vi.hoisted(() => ({
  DEMO: false,
  WEB: false,
  TRANSPORT: "desktop" as "desktop" | "web" | "demo",
}));

vi.mock("../demo", () => demoState);

vi.mock("../ipc/commands", () => ({
  assetApi: { list: mocks.assetList, search: vi.fn(), groupList: vi.fn(), snippetList: vi.fn() },
  sessionApi: {
    list: mocks.sessionList,
    connectLocal: vi.fn(),
    connect: vi.fn(),
    reconnect: vi.fn(),
    probe: vi.fn(),
  },
  vaultApi: { status: mocks.vaultStatus, listCredentials: vi.fn() },
  syncApi: {
    linkGet: mocks.syncLinkGet,
    linkSet: mocks.syncLinkSet,
    status: mocks.syncStatus,
    syncNow: mocks.syncNow,
  },
  layoutApi: {
    get: vi.fn().mockResolvedValue({ revision: 0, updatedAt: 0, data: null }),
    put: vi.fn().mockResolvedValue({ saved: true, revision: 1, conflict: false }),
  },
  terminalApi: {},
  dbApi: {},
}));

vi.mock("../features/terminal/TerminalPane", () => ({ TerminalPane: () => null }));
vi.mock("../features/terminal/BackgroundSessions", () => ({ BackgroundSessions: () => null }));
vi.mock("../features/files/FileBrowser", () => ({ FileBrowser: () => null }));
vi.mock("../features/files/FileTree", () => ({ FileTree: () => null }));
vi.mock("../features/files/MountPanel", () => ({ MountPanel: () => null }));
vi.mock("../features/files/FileEditor", () => ({ FileEditor: () => null }));
vi.mock("../features/forward/ForwardPanel", () => ({ ForwardPanel: () => null }));
vi.mock("../features/docker/DockerPanel", () => ({ DockerPanel: () => null }));
vi.mock("../features/db/DbPanel", () => ({ DbPanel: () => null }));
vi.mock("../features/ai/AiSidebar", () => ({ AiSidebar: () => null }));
vi.mock("../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../features/credentials/CredentialsView", () => ({ CredentialsView: () => null }));
vi.mock("../features/explorer/AssetTree", () => ({ AssetTree: () => null }));
vi.mock("../app/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));
vi.mock("../features/settings/SettingsView", () => ({ SettingsView: () => null }));

import App from "../app/App";
import { useUi } from "../app/store";
import { SyncCard } from "../features/settings/SyncCard";

let mounted: MountedView | undefined;

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(App)));
}

function syncStatusText(): string | null {
  const side = document.querySelector<HTMLElement>(".nx-statusbar-side");
  if (!side) return null;
  for (const el of side.querySelectorAll<HTMLElement>("span")) {
    const text = el.textContent ?? "";
    if (text.includes("同步") || text.includes("服务端")) return text;
  }
  return null;
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  Element.prototype.scrollIntoView = vi.fn();
  demoState.DEMO = false;
  demoState.WEB = false;
  demoState.TRANSPORT = "desktop";
  mocks.assetList.mockResolvedValue([]);
  mocks.sessionList.mockResolvedValue([]);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  useUi.setState({
    toasts: [],
    leftOpen: true,
    rightOpen: false,
    leftMode: "assets",
    sessions: [],
    workspaces: [],
    activeWorkspaceId: null,
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("状态栏同步状态", () => {
  it("未配置同步链接时显示本地模式", async () => {
    mocks.syncLinkGet.mockResolvedValue({
      url: "",
      username: "",
      insecure: false,
      hasPassword: false,
      verifiedAt: 0,
      lastError: null,
    });
    mounted = mountApp();
    await flushUntil(() => syncStatusText() !== null && syncStatusText() !== "同步：…");

    expect(syncStatusText()).toBe("同步：本地模式");
  });

  it("已配置同步链接时显示已配置", async () => {
    mocks.syncLinkGet.mockResolvedValue({
      url: "https://sync.example.com",
      username: "alice",
      insecure: false,
      hasPassword: true,
      verifiedAt: 1,
      lastError: null,
    });
    mounted = mountApp();
    await flushUntil(() => syncStatusText() !== null && syncStatusText() !== "同步：…");

    expect(syncStatusText()).toBe("同步：已配置");
  });

  it("浏览器（服务端）模式显示服务端浏览器模式，不读取同步链接", async () => {
    demoState.WEB = true;
    demoState.TRANSPORT = "web";
    mounted = mountApp();
    await flush();

    expect(syncStatusText()).toBe("服务端：浏览器模式");
    expect(mocks.syncLinkGet).not.toHaveBeenCalled();
  });
});

const EMPTY_LINK = {
  url: "",
  username: "",
  insecure: false,
  hasPassword: false,
  verifiedAt: 0,
  lastError: null,
};
const SAVED_LINK = {
  url: "https://sync.example.com",
  username: "alice",
  insecure: false,
  hasPassword: true,
  verifiedAt: 1,
  lastError: null,
};

function mountAppWithSyncCard(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(
    createElement(
      QueryClientProvider,
      { client },
      createElement(App),
      createElement(SyncCard),
    ),
  );
}

async function saveSyncLink(): Promise<void> {
  await flushUntil(() => document.querySelector("#sync-url") !== null);
  setInputValue(
    document.querySelector<HTMLInputElement>("#sync-url")!,
    "https://sync.example.com",
  );
  setInputValue(document.querySelector<HTMLInputElement>("#sync-user")!, "alice");
  setInputValue(document.querySelector<HTMLInputElement>("#sync-pass")!, "correct horse battery staple");
  await flushUntil(() => {
    const btn = [...document.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "保存链接",
    ) as HTMLButtonElement | undefined;
    return !!btn && !btn.disabled;
  });
  clickButton(document.body, "保存链接");
}

describe("账号同步卡（桌面端）", () => {
  beforeEach(() => {
    mocks.syncStatus.mockResolvedValue({
      configured: false,
      loggedIn: false,
      seq: 0,
      verifiedAt: 0,
      lastError: "",
    });
    mocks.syncLinkGet.mockImplementation(async () =>
      mocks.syncLinkSet.mock.calls.length > 0 ? SAVED_LINK : EMPTY_LINK,
    );
    mocks.syncLinkSet.mockResolvedValue(SAVED_LINK);
    mocks.syncNow.mockResolvedValue({
      pulled: 2,
      applied: 1,
      pullSkipped: 1,
      decryptFailed: 0,
      pushed: 3,
      conflicts: 0,
      head: "head-1",
      seq: 7,
      warnings: [],
    });
  });

  it("保存链接时以账号+密码调用 linkSet,并刷新为已配置", async () => {
    mounted = mountAppWithSyncCard();
    await flush();
    expect(syncStatusText()).toBe("同步：本地模式");

    await saveSyncLink();

    await flushUntil(() => syncStatusText() === "同步：已配置");
    expect(mocks.syncLinkSet).toHaveBeenCalledWith(
      expect.objectContaining({
        url: "https://sync.example.com",
        username: "alice",
        password: "correct horse battery staple",
      }),
    );
  });

  it("立即同步调用 syncNow 并展示报告", async () => {
    mocks.syncStatus.mockResolvedValue({
      configured: true,
      loggedIn: true,
      username: "alice",
      seq: 7,
      verifiedAt: 1,
      lastError: "",
    });
    mounted = mountAppWithSyncCard();
    await flush();

    await flushUntil(() => {
      const btn = [...document.querySelectorAll("button")].find(
        (b) => b.textContent?.trim() === "立即同步",
      ) as HTMLButtonElement | undefined;
      return !!btn && !btn.disabled;
    });
    clickButton(document.body, "立即同步");

    await flushUntil(() => document.body.textContent?.includes("同步结果"));
    expect(mocks.syncNow).toHaveBeenCalled();
    expect(document.body.textContent).toContain("拉取 2");
    expect(document.body.textContent).toContain("推送 3");
  });

  it("同步失败时展示错误而不是报告", async () => {
    mocks.syncStatus.mockResolvedValue({
      configured: true,
      loggedIn: false,
      seq: 0,
      verifiedAt: 0,
      lastError: "会话已过期",
    });
    mocks.syncNow.mockRejectedValue(new Error("同步头不一致"));
    mounted = mountAppWithSyncCard();
    await flush();

    await flushUntil(() => {
      const btn = [...document.querySelectorAll("button")].find(
        (b) => b.textContent?.trim() === "立即同步",
      ) as HTMLButtonElement | undefined;
      return !!btn && !btn.disabled;
    });
    clickButton(document.body, "立即同步");

    await flushUntil(() => document.body.textContent?.includes("同步头不一致"));
    expect(document.body.textContent).not.toContain("同步结果");
  });
});
