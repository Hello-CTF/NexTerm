/** @vitest-environment jsdom */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flush, mount, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  sessionList: vi.fn(),
  vaultStatus: vi.fn(),
  updateCheck: vi.fn(),
  closeWindow: vi.fn(),
  minimiseWindow: vi.fn(),
  toggleMaximiseWindow: vi.fn(),
}));

const envState = vi.hoisted(() => ({
  transport: "web" as "desktop" | "web",
}));

vi.mock("../ipc/env", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/env")>();
  return {
    ...actual,
    get TRANSPORT() {
      return envState.transport;
    },
    get WEB() {
      return envState.transport === "web";
    },
    get DESKTOP() {
      return envState.transport === "desktop";
    },
  };
});

vi.mock("../ipc/commands", () => ({
  assetApi: {
    list: mocks.assetList,
    search: vi.fn(),
    groupList: vi.fn(),
    snippetList: vi.fn(),
  },
  sessionApi: {
    list: mocks.sessionList,
    connectLocal: vi.fn(),
    connect: vi.fn(),
    reconnect: vi.fn(),
    probe: vi.fn(),
  },
  vaultApi: { status: mocks.vaultStatus, listCredentials: vi.fn() },
  syncApi: {
    linkGet: vi.fn().mockResolvedValue({
      url: "",
      username: "",
      insecure: false,
      hasPassword: false,
      verifiedAt: 0,
      lastError: null,
    }),
  },
  layoutApi: {
    get: vi.fn().mockResolvedValue({ revision: 0, updatedAt: 0, data: null }),
    put: vi.fn().mockResolvedValue({ saved: true, revision: 1, conflict: false }),
  },
  terminalApi: {},
  dbApi: {},
}));

vi.mock("../ipc/wails", () => ({
  closeWindow: mocks.closeWindow,
  minimiseWindow: mocks.minimiseWindow,
  toggleMaximiseWindow: mocks.toggleMaximiseWindow,
  listenWailsEvent: vi.fn(() => Promise.resolve(() => {})),
}));
vi.mock("../app/layout", () => ({
  layoutBootstrapped: Promise.resolve(),
  startLayoutSync: () => {},
  advanceLayoutAccount: () => {},
  finishLayoutRestore: vi.fn(),
  layoutRestoreStatus: () => ({ pending: false, preview: false, count: 0 }),
  onRemoteChange: () => {},
  reviewPendingLayoutRestore: vi.fn(),
  subscribeLayoutRestore: () => () => {},
  undoLayoutRestore: vi.fn(),
}));
vi.mock("../app/useUpdate", () => ({
  getUpdateState: () => ({ check: mocks.updateCheck }),
}));
vi.mock("../features/settings/useWebAutoSync", () => ({
  useDesktopAutoSync: () => {},
  useWebAutoSync: () => {},
}));

vi.mock("../features/terminal/TerminalPane", () => ({ TerminalPane: () => null }));
vi.mock("../features/terminal/BackgroundSessions", () => ({ BackgroundSessions: () => null }));
vi.mock("../features/terminal/TranscriptHistoryPanel", () => ({ TranscriptHistoryPanel: () => null }));
vi.mock("../features/files/FileTree", () => ({ FileTree: () => null }));
vi.mock("../features/files/MountPanel", () => ({ MountPanel: () => null }));
vi.mock("../features/files/FileEditor", () => ({ FileEditor: () => null }));
vi.mock("../features/files/LogViewer", () => ({ LogViewer: () => null }));
vi.mock("../features/forward/ForwardPanel", () => ({ ForwardPanel: () => null }));
vi.mock("../features/docker/DockerPanel", () => ({ DockerPanel: () => null }));
vi.mock("../features/db/DbPanel", () => ({ DbPanel: () => null }));
vi.mock("../features/ai/AiSidebar", () => ({ AiSidebar: () => null }));
vi.mock("../features/settings/SettingsView", () => ({ SettingsView: () => null }));
vi.mock("../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../features/explorer/AssetTree", () => ({ AssetTree: () => null, AssetEditor: () => null }));
vi.mock("../features/fleet/DeviceSessionBoundary", () => ({ DeviceSessionBoundary: () => null }));
vi.mock("../app/JumpPalette", () => ({ JumpPalette: () => null }));
vi.mock("../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));
vi.mock("../app/UpdateBanner", () => ({ UpdateBanner: () => null }));
vi.mock("../app/DekUnlockBanner", () => ({ DekUnlockBanner: () => null }));

import App from "../app/App";
import { setMacPlatform } from "../app/platform";
import { useUi } from "../app/store";

let mounted: MountedView | undefined;

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(App)));
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  envState.transport = "desktop";
  setMacPlatform(false);
  mocks.assetList.mockResolvedValue([]);
  mocks.sessionList.mockResolvedValue([]);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  mocks.updateCheck.mockResolvedValue(undefined);
  useUi.setState({
    sessions: [],
    toasts: [],
    leftOpen: false,
    rightOpen: false,
    leftMode: "assets",
    workspaces: [],
    activeWorkspaceId: null,
    connectingAssetIds: [],
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("App semantic tokens", () => {
  it("renders the success toast with semantic color and runtime shadow tokens", async () => {
    useUi.setState({ toasts: [{ id: 1, kind: "success", text: "保存成功" }] });
    mounted = mountApp();
    await flush();

    const toast = [...mounted.container.querySelectorAll<HTMLElement>(".nx-toasts > div")].find(
      (element) => element.textContent?.includes("保存成功"),
    );
    expect(toast).toBeDefined();
    expect(toast?.classList.contains("nx-alert-success")).toBe(true);
    expect(toast?.classList.contains("shadow-[var(--shadow-pop)]")).toBe(true);
    expect(toast?.classList.contains("shadow-pop")).toBe(false);
  });

  it("does not render raw color literals in App utility classes", async () => {
    useUi.setState({ toasts: [{ id: 1, kind: "success", text: "保存成功" }] });
    mounted = mountApp();
    await flush();

    const rawColors = [...mounted.container.querySelectorAll("*")]
      .flatMap((element) => [...element.classList])
      .filter((name) => name.includes("#") || /rgba?\(/.test(name));
    expect(rawColors).toEqual([]);
  });

  it("keeps every header button interactive and every blank region draggable", async () => {
    useUi.setState({
      workspaces: [{
        id: "ws",
        kind: "session",
        title: "workspace",
        closable: true,
        panes: [{ id: "pane", tabs: [], activeTabId: null }],
        activePaneId: "pane",
      }],
      activeWorkspaceId: "ws",
    });
    mounted = mountApp();
    await flush();

    const header = mounted.container.querySelector<HTMLElement>(".nx-app-header");
    expect(header).not.toBeNull();
    expect(header?.style.getPropertyValue("--wails-draggable")).toBe("drag");
    for (const selector of [
      'button[aria-label^="切换工作区"]',
      'button[aria-label="AI 助手"]',
      'button[aria-label^="跳转面板"]',
      'button[aria-label="最小化窗口"]',
      'button[aria-label="最大化或还原窗口"]',
      'button[aria-label="关闭窗口"]',
    ]) {
      expect(header?.querySelector(selector), selector).not.toBeNull();
    }
    const buttons = header?.querySelectorAll<HTMLButtonElement>("button") ?? [];
    expect(buttons.length).toBeGreaterThan(0);
    for (const button of buttons) {
      expect(
        button.style.getPropertyValue("--wails-draggable"),
        button.getAttribute("aria-label") ?? undefined,
      ).toBe("no-drag");
    }
    const regions = header?.querySelectorAll<HTMLElement>("[data-wails-drag-region]") ?? [];
    expect(regions.length).toBeGreaterThan(0);
    for (const region of regions) {
      expect(region.style.getPropertyValue("--wails-draggable")).toBe("drag");
    }
  });
});
