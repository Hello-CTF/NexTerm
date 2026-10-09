/** @vitest-environment jsdom */
// FLEET149 app 壳: 左侧导航「设备管理」打开 devices 标签并渲染真实视图
// (桌面端传输, 视图必须落显式不可用态且不发 HTTP)。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flushUntil, mount, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => {
  (window as unknown as Record<string, unknown>).__NEXTERM_TRANSPORT__ = "desktop";
  return {
    fetch: vi.fn(),
    assetList: vi.fn(),
    assetSearch: vi.fn(),
    sessionList: vi.fn(),
    vaultStatus: vi.fn(),
  };
});

vi.mock("../ipc/commands", () => ({
  assetApi: { list: mocks.assetList, search: mocks.assetSearch, groupList: vi.fn(), snippetList: vi.fn() },
  sessionApi: {
    list: mocks.sessionList,
    connectLocal: vi.fn(),
    connect: vi.fn(),
    reconnect: vi.fn(),
    probe: vi.fn(),
  },
  vaultApi: { status: mocks.vaultStatus, listCredentials: vi.fn() },
  syncApi: { linkGet: vi.fn().mockResolvedValue({ url: "" }) },
  terminalApi: {},
  dbApi: {},
}));

vi.mock("../features/terminal/TerminalPane", () => ({ TerminalPane: () => null }));
vi.mock("../app/layout", () => ({ layoutBootstrapped: Promise.resolve(), startLayoutSync: () => {} }));
vi.mock("../features/terminal/BackgroundSessions", () => ({ BackgroundSessions: () => null }));
vi.mock("../features/files/FileBrowser", () => ({ FileBrowser: () => null }));
vi.mock("../features/files/FileTree", () => ({ FileTree: () => null }));
vi.mock("../features/files/MountPanel", () => ({ MountPanel: () => null }));
vi.mock("../features/files/FileEditor", () => ({ FileEditor: () => null }));
vi.mock("../features/forward/ForwardPanel", () => ({ ForwardPanel: () => null }));
vi.mock("../features/docker/DockerPanel", () => ({ DockerPanel: () => null }));
vi.mock("../features/db/DbPanel", () => ({ DbPanel: () => null }));
vi.mock("../features/ai/AiSidebar", () => ({ AiSidebar: () => null }));
vi.mock("../features/settings/SettingsView", () => ({ SettingsView: () => null }));
vi.mock("../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../features/credentials/CredentialsView", () => ({ CredentialsView: () => null }));
vi.mock("../features/explorer/AssetTree", () => ({ AssetTree: () => null }));
vi.mock("../app/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));

import App from "../app/App";
import { useUi } from "../app/store";

let mounted: MountedView | undefined;

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(App)));
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  mocks.assetList.mockResolvedValue([]);
  mocks.assetSearch.mockResolvedValue([]);
  mocks.sessionList.mockResolvedValue([]);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  useUi.setState({ toasts: [], sessions: [], leftOpen: true, rightOpen: false, leftMode: "assets" });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
  vi.unstubAllGlobals();
});

describe("app 壳 · 设备管理导航", () => {
  it("导航按钮打开 devices 标签, 视图落显式不可用态且零 HTTP", async () => {
    vi.stubGlobal("fetch", mocks.fetch);
    mounted = mountApp();
    await flushUntil(() => document.querySelector('button[aria-label="设备管理"]') !== null);

    const railButton = document.querySelector('button[aria-label="设备管理"]');
    expect(railButton).not.toBeNull();
    click(railButton as HTMLButtonElement);

    await flushUntil(() =>
      useUi
        .getState()
        .workspaces.some((w) => w.panes.some((p) => p.tabs.some((t) => t.kind === "devices"))),
    );
    const tab = useUi
      .getState()
      .workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs))
      .find((t) => t.kind === "devices");
    expect(tab?.title).toBe("设备管理");

    await flushUntil(() => document.body.textContent?.includes("桌面端暂无设备管理") ?? false);
    expect(mocks.fetch).not.toHaveBeenCalled();
  });

  it("重复点击复用同一个 devices 标签", async () => {
    vi.stubGlobal("fetch", mocks.fetch);
    mounted = mountApp();
    await flushUntil(() => document.querySelector('button[aria-label="设备管理"]') !== null);

    const railButton = document.querySelector('button[aria-label="设备管理"]') as HTMLButtonElement;
    click(railButton);
    await flushUntil(() => document.body.textContent?.includes("桌面端暂无设备管理") ?? false);
    click(railButton);
    await flushUntil(() =>
      useUi
        .getState()
        .workspaces.flatMap((w) => w.panes.flatMap((p) => p.tabs))
        .filter((t) => t.kind === "devices").length === 1,
    );
  });
});
