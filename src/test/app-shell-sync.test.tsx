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
  syncRemoteDigest: vi.fn(),
  syncDigest: vi.fn(),
}));

const demoState = vi.hoisted(() => ({
  DEMO: false,
  WEB: false,
  TRANSPORT: "desktop" as "desktop" | "web" | "demo",
}));

vi.mock("../demo", () => demoState);

vi.mock("../ipc/commands", () => ({
  assetApi: { list: mocks.assetList, search: vi.fn() },
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
    remoteDigest: mocks.syncRemoteDigest,
    digest: mocks.syncDigest,
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
      tokenKind: "server",
      token: "",
      insecure: false,
      verifiedAt: 0,
      lastError: null,
    });
    mounted = mountApp();
    await flush();

    expect(syncStatusText()).toBe("同步：本地模式");
  });

  it("已配置同步链接时显示已配置", async () => {
    mocks.syncLinkGet.mockResolvedValue({
      url: "https://sync.example.com",
      tokenKind: "server",
      token: "",
      insecure: false,
      verifiedAt: 1,
      lastError: null,
    });
    mounted = mountApp();
    await flush();

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
  tokenKind: "server",
  token: "",
  insecure: false,
  verifiedAt: 0,
  lastError: null,
};
const SAVED_LINK = {
  url: "https://sync.example.com",
  tokenKind: "server",
  token: "saved-token",
  insecure: false,
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
  setInputValue(document.querySelector<HTMLInputElement>("#sync-token")!, "new-token");
  await flushUntil(() => {
    const btn = [...document.querySelectorAll("button")].find(
      (b) => b.textContent?.trim() === "保存并测试连接",
    ) as HTMLButtonElement | undefined;
    return !!btn && !btn.disabled;
  });
  clickButton(document.body, "保存并测试连接");
}

describe("状态栏同步状态（保存后刷新）", () => {
  beforeEach(() => {
    mocks.syncDigest.mockResolvedValue({ origin: "local", assets: [] });
    mocks.syncLinkGet.mockImplementation(async () =>
      mocks.syncLinkSet.mock.calls.length > 0 ? SAVED_LINK : EMPTY_LINK,
    );
    mocks.syncLinkSet.mockResolvedValue(SAVED_LINK);
  });

  it("保存成功但连接测试失败时，状态栏仍刷新为已配置", async () => {
    mocks.syncRemoteDigest.mockRejectedValue(new Error("对端不可达"));
    mounted = mountAppWithSyncCard();
    await flush();
    expect(syncStatusText()).toBe("同步：本地模式");

    await saveSyncLink();

    await flushUntil(() => syncStatusText() === "同步：已配置");
    expect(mocks.syncLinkSet).toHaveBeenCalledWith(
      expect.objectContaining({ url: "https://sync.example.com" }),
    );
    expect(document.body.textContent).toContain("对端不可达");
  });

  it("保存并连接成功时，状态栏刷新为已配置并提示已连接", async () => {
    mocks.syncRemoteDigest.mockResolvedValue({
      origin: "https://sync.example.com",
      assets: [],
    });
    mounted = mountAppWithSyncCard();
    await flush();
    expect(syncStatusText()).toBe("同步：本地模式");

    await saveSyncLink();

    await flushUntil(() => syncStatusText() === "同步：已配置");
    expect(document.body.textContent).toContain("已连接（对端标识");
  });
});
