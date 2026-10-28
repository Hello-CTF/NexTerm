/** @vitest-environment jsdom */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, createElement } from "react";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { flush, mount, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  sessionList: vi.fn(),
  vaultStatus: vi.fn(),
  updateCheck: vi.fn(),
  connectLocal: vi.fn(),
  closeWindow: vi.fn(),
  minimiseWindow: vi.fn(),
  toggleMaximiseWindow: vi.fn(),
}));

const device = vi.hoisted(() => ({ mobileUserAgent: false }));

vi.mock("../ipc/env", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/env")>();
  return { ...actual, TRANSPORT: "desktop", WEB: false, DESKTOP: true };
});

// The mobile shell is selected by the iPhone/iPad/Android user agent, which platform.ts reads once at load.
vi.mock("../app/platform", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../app/platform")>();
  return { ...actual, isMobile: () => device.mobileUserAgent };
});

vi.mock("../ipc/commands", () => ({
  assetApi: { list: mocks.assetList, search: vi.fn(), groupList: vi.fn(), snippetList: vi.fn() },
  sessionApi: {
    list: mocks.sessionList,
    connectLocal: mocks.connectLocal,
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
    status: vi.fn().mockResolvedValue({ configured: false }),
  },
  layoutApi: {
    get: vi.fn().mockResolvedValue({ revision: 0, updatedAt: 0, data: null }),
    put: vi.fn().mockResolvedValue({ saved: true, revision: 1, conflict: false }),
  },
  terminalApi: {},
  dbApi: {},
}));

vi.mock("../ipc/wails", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../ipc/wails")>()),
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
vi.mock("../app/useUpdate", () => ({ getUpdateState: () => ({ check: mocks.updateCheck }) }));
vi.mock("../features/settings/useWebAutoSync", () => ({
  useDesktopAutoSync: () => {},
  useWebAutoSync: () => {},
}));
vi.mock("../features/terminal/TerminalPane", () => ({
  TerminalPane: (props: { resumeTabId?: string; visible?: boolean }) =>
    createElement("div", { "data-terminal-pane": props.resumeTabId ?? "", "data-visible": String(props.visible) }),
}));
vi.mock("../features/terminal/BackgroundSessions", () => ({ BackgroundSessions: () => null }));
vi.mock("../features/terminal/TranscriptHistoryPanel", () => ({ TranscriptHistoryPanel: () => null }));
vi.mock("../features/files/FileBrowser", () => ({ FileBrowser: () => null }));
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
vi.mock("../features/explorer/AssetTree", () => ({
  AssetTree: () => createElement("div", { "data-testid": "asset-tree" }),
  AssetEditor: () => null,
}));
vi.mock("../features/fleet/DeviceSessionBoundary", () => ({ DeviceSessionBoundary: () => null }));
vi.mock("../app/JumpPalette", () => ({ JumpPalette: () => null }));
vi.mock("../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));
vi.mock("../app/UpdateBanner", () => ({ UpdateBanner: () => null }));
vi.mock("../app/DekUnlockBanner", () => ({ DekUnlockBanner: () => null }));

import App from "../app/App";
import { setMacPlatform } from "../app/platform";
import { useShell } from "../app/shell";
import { useUi } from "../app/store";
import { resetMobileHomeForTest } from "../app/shell/mobileHome";

interface DeviceProfile {
  mobileUserAgent: boolean;
  coarsePointer: boolean;
  width: number;
  height: number;
}

const DESKTOP: DeviceProfile = { mobileUserAgent: false, coarsePointer: false, width: 1440, height: 900 };
const NARROW_WINDOW: DeviceProfile = { mobileUserAgent: false, coarsePointer: false, width: 700, height: 900 };
const PHONE: DeviceProfile = { mobileUserAgent: true, coarsePointer: true, width: 390, height: 844 };
const TABLET_LANDSCAPE: DeviceProfile = { mobileUserAgent: true, coarsePointer: true, width: 1180, height: 820 };

function stubDevice(profile: DeviceProfile): void {
  device.mobileUserAgent = profile.mobileUserAgent;
  window.matchMedia = ((query: string) => ({
    matches: profile.coarsePointer && query === "(pointer: coarse)",
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
  Object.defineProperty(window, "innerWidth", { configurable: true, value: profile.width });
  Object.defineProperty(window, "innerHeight", { configurable: true, value: profile.height });
}

let mounted: MountedView | undefined;

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(App)));
}

function button(label: string): HTMLButtonElement | null {
  return document.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`);
}

async function openAssetsSidebar(): Promise<void> {
  act(() => button("资产")?.click());
  await flush();
}

function doubleClickHeaderBlank(): void {
  const blank = document.querySelector<HTMLElement>(".nx-app-header .nx-spacer");
  expect(blank).not.toBeNull();
  act(() => {
    blank?.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
  });
}

const realMatchMedia = window.matchMedia;
const realInnerWidth = window.innerWidth;
const realInnerHeight = window.innerHeight;

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  setMacPlatform(false);
  resetMobileHomeForTest();
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
  device.mobileUserAgent = false;
  window.matchMedia = realMatchMedia;
  Object.defineProperty(window, "innerWidth", { configurable: true, value: realInnerWidth });
  Object.defineProperty(window, "innerHeight", { configurable: true, value: realInnerHeight });
});

// The mobile home loads the status wall through React.lazy. Warm it so the awaited flushes only cover mocked work.
beforeAll(async () => {
  await import("../features/mobile/StatusWall");
});

describe("useShell", () => {
  function ShellProbe() {
    return createElement("output", { "data-shell": useShell() });
  }

  function shellOf(profile: DeviceProfile): string | null {
    stubDevice(profile);
    const view = mount(createElement(ShellProbe));
    const mode = view.container.querySelector("output")?.getAttribute("data-shell") ?? null;
    view.unmount();
    return mode;
  }

  it("classifies devices into desktop, compact and mobile shells", () => {
    expect(shellOf(DESKTOP)).toBe("desktop");
    expect(shellOf(NARROW_WINDOW)).toBe("compact");
    expect(shellOf({ ...DESKTOP, coarsePointer: true, width: 820, height: 400 })).toBe("compact");
    expect(shellOf(PHONE)).toBe("mobile");
    expect(shellOf(TABLET_LANDSCAPE)).toBe("mobile");
  });

  it("follows the viewport across the compact threshold", () => {
    stubDevice(DESKTOP);
    const view = mount(createElement(ShellProbe));
    const mode = () => view.container.querySelector("output")?.getAttribute("data-shell");
    expect(mode()).toBe("desktop");
    act(() => {
      stubDevice(NARROW_WINDOW);
      window.dispatchEvent(new Event("resize"));
    });
    expect(mode()).toBe("compact");
    view.unmount();
  });
});

describe("app shell modes", () => {
  it("desktop: docked sidebar, window controls and double-click maximise", async () => {
    stubDevice(DESKTOP);
    mounted = mountApp();
    await flush();

    expect(button("最小化窗口")).not.toBeNull();
    expect(button("关闭窗口")).not.toBeNull();
    doubleClickHeaderBlank();
    expect(mocks.toggleMaximiseWindow).toHaveBeenCalledTimes(1);

    await openAssetsSidebar();
    expect(document.querySelector('[role="navigation"][aria-label="左栏"]')).not.toBeNull();
    expect(button("收起侧栏")).toBeNull();
    expect(document.documentElement.querySelector(".nx-app.is-compact")).toBeNull();
    expect([...document.querySelectorAll("button")].some((b) => b.textContent?.includes("打开本地终端"))).toBe(true);
  });

  it("compact: the desktop chrome stays but sidebars open as overlays", async () => {
    stubDevice(NARROW_WINDOW);
    mounted = mountApp();
    await flush();

    expect(button("最小化窗口")).not.toBeNull();
    await openAssetsSidebar();
    expect(document.querySelector('[role="navigation"][aria-label="左栏"]')).not.toBeNull();
    const backdrop = button("收起侧栏");
    expect(backdrop).not.toBeNull();
    act(() => backdrop?.click());
    expect(document.querySelector('[role="navigation"][aria-label="左栏"]')).toBeNull();
  });

  it("mobile: overlay sidebars, no window controls, no double-click maximise and no local terminal", async () => {
    stubDevice(PHONE);
    mounted = mountApp();
    await flush();

    expect(button("最小化窗口")).toBeNull();
    expect(button("最大化或还原窗口")).toBeNull();
    expect(button("关闭窗口")).toBeNull();
    doubleClickHeaderBlank();
    expect(mocks.toggleMaximiseWindow).not.toHaveBeenCalled();

    expect(document.querySelector(".nx-app.is-compact")).not.toBeNull();
    await openAssetsSidebar();
    expect(document.querySelector('[role="navigation"][aria-label="左栏"]')).not.toBeNull();
    expect(button("收起侧栏")).not.toBeNull();
    expect([...document.querySelectorAll("button")].some((b) => b.textContent?.includes("打开本地终端"))).toBe(false);
  });

  it("mobile: launches on the status wall in the workspace area; the docks stay usable and the home button toggles it", async () => {
    stubDevice(PHONE);
    mounted = mountApp();
    await flush();

    const wall = () => document.querySelector("[data-status-wall]");
    const area = () => document.querySelector("[data-workspace-area]");
    const home = () => button("会话状态首页");
    expect(wall()).not.toBeNull();
    expect(area()?.classList.contains("hidden")).toBe(true);
    expect(home()?.getAttribute("aria-pressed")).toBe("true");
    // The asset drawer opens over the wall without closing it.
    await openAssetsSidebar();
    expect(wall()).not.toBeNull();
    expect(document.querySelector('[role="navigation"][aria-label="左栏"]')).not.toBeNull();
    act(() => home()?.click());
    expect(wall()).toBeNull();
    expect(area()?.classList.contains("hidden")).toBe(false);
    expect(home()?.getAttribute("aria-pressed")).toBe("false");
  });

  it("mobile: a saved layout with an active terminal keeps the wall open at launch, with the terminal hidden under it", async () => {
    stubDevice(PHONE);
    useUi.setState({
      workspaces: [{
        id: "ws",
        kind: "session",
        title: "web-01",
        sessionId: "s1",
        closable: true,
        panes: [{
          id: "pane",
          tabs: [
            { id: "t1", kind: "terminal", title: "构建", sessionId: "s1", tabId: "T1", closable: true },
            { id: "t2", kind: "terminal", title: "日志", sessionId: "s1", tabId: "T2", closable: true },
          ],
          activeTabId: "t1",
        }],
        activePaneId: "pane",
      }],
      activeWorkspaceId: "ws",
    });
    mounted = mountApp();
    await flush();
    await flush();

    const wall = () => document.querySelector("[data-status-wall]");
    const visible = (tabId: string) => document.querySelector(`[data-terminal-pane="${tabId}"]`)?.getAttribute("data-visible");
    expect(wall()).not.toBeNull();
    expect(visible("T1")).toBe("false");
    // A layout restored after launch brings tabs this client never showed and activates one: not the user going somewhere.
    act(() => useUi.setState((state) => ({
      workspaces: state.workspaces.map((workspace) => ({
        ...workspace,
        panes: workspace.panes.map((pane) => ({
          ...pane,
          tabs: [...pane.tabs, { id: "t3", kind: "terminal", title: "同步来的", sessionId: "s1", tabId: "T3", closable: true }],
          activeTabId: "t3",
        })),
      })),
    })));
    await flush();
    expect(wall()).not.toBeNull();
    expect(visible("T3")).toBe("false");
    // Opening a session from the wall shows it.
    act(() => document.querySelector<HTMLButtonElement>('[data-wall-card="T1"] button')?.click());
    await flush();
    expect(wall()).toBeNull();
    expect(visible("T1")).toBe("true");
  });

  it("desktop and compact windows have no status wall", async () => {
    stubDevice(NARROW_WINDOW);
    mounted = mountApp();
    await flush();
    expect(document.querySelector("[data-status-wall]")).toBeNull();
    expect(button("会话状态首页")).toBeNull();
  });

  it("mobile on a wide tablet keeps docked sidebars and still hides the desktop window chrome", async () => {
    stubDevice(TABLET_LANDSCAPE);
    mounted = mountApp();
    await flush();

    expect(button("最小化窗口")).toBeNull();
    await openAssetsSidebar();
    expect(document.querySelector('[role="navigation"][aria-label="左栏"]')).not.toBeNull();
    expect(button("收起侧栏")).toBeNull();
  });

  it("mobile: entering a terminal tab collapses the overlay sidebar and leaving it restores the sidebar", async () => {
    stubDevice(PHONE);
    useUi.setState({
      workspaces: [{
        id: "ws",
        kind: "tools",
        title: "工具",
        closable: true,
        panes: [{
          id: "pane",
          tabs: [
            { id: "settings", kind: "settings", title: "设置", closable: true },
            { id: "term", kind: "terminal", title: "终端", closable: true },
          ],
          activeTabId: "settings",
        }],
        activePaneId: "pane",
      }],
      activeWorkspaceId: "ws",
    });
    mounted = mountApp();
    await flush();

    await openAssetsSidebar();
    expect(button("收起侧栏")).not.toBeNull();
    act(() => useUi.getState().setActiveTab("term"));
    expect(button("收起侧栏")).toBeNull();
    act(() => useUi.getState().setActiveTab("settings"));
    expect(button("收起侧栏")).not.toBeNull();
  });

  it("compact on a non-mobile device does not collapse the overlay sidebar for terminal tabs", async () => {
    stubDevice(NARROW_WINDOW);
    useUi.setState({
      workspaces: [{
        id: "ws",
        kind: "tools",
        title: "工具",
        closable: true,
        panes: [{
          id: "pane",
          tabs: [
            { id: "settings", kind: "settings", title: "设置", closable: true },
            { id: "term", kind: "terminal", title: "终端", closable: true },
          ],
          activeTabId: "settings",
        }],
        activePaneId: "pane",
      }],
      activeWorkspaceId: "ws",
    });
    mounted = mountApp();
    await flush();

    await openAssetsSidebar();
    act(() => useUi.getState().setActiveTab("term"));
    expect(button("收起侧栏")).not.toBeNull();
  });
});
