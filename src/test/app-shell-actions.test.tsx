/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, clickButton, flush, flushMacrotask, mount, setInputValue, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  sessionList: vi.fn(),
  vaultStatus: vi.fn(),
  connectAsset: vi.fn(),
  connectLocal: vi.fn(),
  dbConnect: vi.fn(),
  updateCheck: vi.fn(),
  closeWindow: vi.fn(),
  minimiseWindow: vi.fn(),
  toggleMaximiseWindow: vi.fn(),
  banners: { takeover: false, update: false, dek: false },
  aiSidebarProps: [] as { sessionId?: string }[],
  backgroundProps: [] as { workspaceId?: string; sessionId?: string }[],
  historyProps: [] as { workspaceId?: string; assetId?: string }[],
  terminalPaneProps: [] as { storeTabId: string }[],
  renameTab: vi.fn(),
}));

const envState = vi.hoisted(() => ({
  transport: "web" as "desktop" | "web",
}));

vi.mock("../app/store", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../app/store")>();
  return { ...actual, connectAsset: mocks.connectAsset };
});

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
  },
  terminalApi: { renameTab: mocks.renameTab },
  dbApi: { connect: mocks.dbConnect },
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

vi.mock("../features/terminal/TerminalPane", () => ({
  TerminalPane: (props: { storeTabId: string }) => {
    mocks.terminalPaneProps.push(props);
    return null;
  },
}));
vi.mock("../features/terminal/BackgroundSessions", () => ({
  BackgroundSessions: (props: { workspaceId?: string; sessionId?: string }) => {
    mocks.backgroundProps.push(props);
    return null;
  },
}));
vi.mock("../features/terminal/TranscriptHistoryPanel", () => ({
  TranscriptHistoryPanel: (props: { workspaceId?: string; assetId?: string }) => {
    mocks.historyProps.push(props);
    return null;
  },
}));
vi.mock("../features/files/FileBrowser", () => ({ FileBrowser: () => null }));
vi.mock("../features/files/FileTree", () => ({ FileTree: () => null }));
vi.mock("../features/files/MountPanel", () => ({ MountPanel: () => null }));
vi.mock("../features/files/FileEditor", () => ({ FileEditor: () => null }));
vi.mock("../features/files/LogViewer", () => ({ LogViewer: () => null }));
vi.mock("../features/forward/ForwardPanel", () => ({ ForwardPanel: () => null }));
vi.mock("../features/docker/DockerPanel", () => ({ DockerPanel: () => null }));
vi.mock("../features/db/DbPanel", () => ({ DbPanel: () => null }));
vi.mock("../features/ai/AiSidebar", () => ({
  AiSidebar: (props: { sessionId?: string }) => {
    mocks.aiSidebarProps.push(props);
    return null;
  },
}));
vi.mock("../features/settings/SettingsView", () => ({ SettingsView: () => null }));
vi.mock("../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../features/explorer/AssetTree", () => ({ AssetTree: () => null, AssetEditor: () => null }));
vi.mock("../app/CommandPalette", () => ({
  CommandPalette: (props: { onOpenFiles?: () => void }) => (
    <button type="button" data-testid="palette-files" onClick={props.onOpenFiles}>
      Open file browser
    </button>
  ),
}));
vi.mock("../app/QuickConnect", () => ({
  QuickConnect: () => <div data-testid="quick-connect" />,
}));
vi.mock("../app/TakeoverBanner", () => ({
  TakeoverBanner: () =>
    mocks.banners.takeover ? <div data-testid="takeover-banner" /> : null,
}));
vi.mock("../app/UpdateBanner", () => ({
  UpdateBanner: () => (mocks.banners.update ? <div data-testid="update-banner" /> : null),
}));
vi.mock("../app/DekUnlockBanner", () => ({
  DekUnlockBanner: () => (mocks.banners.dek ? <div data-testid="dek-banner" /> : null),
}));

import App from "../app/App";
import { resetAllKeybindings, setKeybinding } from "../app/keybindings";
import { setMacPlatform } from "../app/platform";
import { useUi, type AppTab, type Workspace } from "../app/store";
import { resetUnreadTabs, setTabUnread } from "../app/unreadStore";
import type { Asset, SessionInfo } from "../ipc/commands";

function assetOf(extra: Partial<Asset> & { id: string; name: string }): Asset {
  return {
    groupId: null,
    kind: "ssh",
    host: "127.0.0.1",
    port: 22,
    username: "root",
    authKind: "password",
    keyPath: null,
    credId: null,
    options: {},
    tags: "",
    note: "",
    sort: 0,
    createdAt: 1,
    updatedAt: 1,
    deletedAt: null,
    builtin: false,
    ...extra,
  };
}

function sessionOf(id: string): SessionInfo {
  return { id, assetId: null, name: id, kind: "ssh", status: "connected", tabs: [], createdAt: 1 };
}

function workspaceOf(sessionId?: string, tabs: AppTab[] = []): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "workspace",
    sessionId,
    panes: [{ id: "pane", tabs, activeTabId: tabs[0]?.id ?? null }],
    activePaneId: "pane",
    splitRatio: 0.5,
    closable: true,
  };
}

let mounted: MountedView | undefined;
let client: QueryClient | undefined;

function appElement(queryClient: QueryClient) {
  return createElement(QueryClientProvider, { client: queryClient }, createElement(App));
}

function mountApp(): MountedView {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(appElement(client));
}

function setBanners(patch: Partial<typeof mocks.banners>): void {
  Object.assign(mocks.banners, patch);
  if (!mounted || !client) throw new Error("App is not mounted");
  const root = mounted.root;
  const queryClient = client;
  act(() => root.render(appElement(queryClient)));
}

function expectShellOrder(expected: string[]): void {
  const container = mounted?.container;
  if (!container) throw new Error("App is not mounted");
  const elements = [
    ...container.querySelectorAll<HTMLElement>(
      ".nx-app-header, [data-testid='takeover-banner'], [data-testid='update-banner'], [data-testid='dek-banner'], .nx-workspace-body",
    ),
  ];
  const actual = elements.map((element) => {
    if (element.classList.contains("nx-app-header")) return "header";
    if (element.classList.contains("nx-workspace-body")) return "workspace-body";
    return element.dataset.testid?.replace("-banner", "") ?? "unknown";
  });
  expect(actual).toEqual(expected);

  const column = container.querySelector(".nx-app-header")?.parentElement;
  expect(column).not.toBeNull();
  for (const element of elements) expect(element.parentElement).toBe(column);
}

async function openPaletteFiles(): Promise<void> {
  const paletteButton = mounted?.container.querySelector('button[aria-label^="命令面板"]');
  if (!paletteButton) throw new Error("Command palette button not found");
  click(paletteButton);
  await flush();
  const filesButton = mounted?.container.querySelector('[data-testid="palette-files"]');
  if (!filesButton) throw new Error("Palette files action not found");
  click(filesButton);
  await flush();
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  Element.prototype.scrollIntoView = vi.fn();
  envState.transport = "web";
  setMacPlatform(false);
  Object.assign(mocks.banners, { takeover: false, update: false, dek: false });
  mocks.aiSidebarProps.length = 0;
  mocks.backgroundProps.length = 0;
  mocks.historyProps.length = 0;
  mocks.terminalPaneProps.length = 0;
  resetUnreadTabs();
  mocks.assetList.mockResolvedValue([]);
  mocks.sessionList.mockResolvedValue([]);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  mocks.connectAsset.mockResolvedValue({ ok: true });
  mocks.dbConnect.mockResolvedValue({ connId: "db-conn" });
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
  resetAllKeybindings();
});

function hostWorkspace(id: string, sessionId: string, assetId?: string): Workspace {
  const paneId = `${id}-pane`;
  return {
    id,
    kind: "session",
    title: id,
    sessionId,
    assetId,
    panes: [{ id: paneId, tabs: [], activeTabId: null }],
    activePaneId: paneId,
    splitRatio: 0.5,
    closable: true,
  };
}

function nonHostWorkspace(kind: "tools" | "db"): Workspace {
  const id = `ws-${kind}`;
  const paneId = `${id}-pane`;
  return {
    id,
    kind,
    title: kind,
    connId: kind === "db" ? "db-conn" : undefined,
    panes: [{ id: paneId, tabs: [], activeTabId: null }],
    activePaneId: paneId,
    splitRatio: 0.5,
    closable: true,
  };
}

function railButton(label: string): HTMLButtonElement {
  const button = mounted?.container.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`);
  if (!button) throw new Error(`Rail button not found: ${label}`);
  return button;
}

async function clickRail(label: string): Promise<void> {
  click(railButton(label));
  await flush();
}

function allTabs(): AppTab[] {
  return useUi.getState().workspaces.flatMap((workspace) =>
    workspace.panes.flatMap((pane) => pane.tabs),
  );
}

const needSessionText = "先连接一台主机（双击左侧资产）";

describe("App workspace actions", () => {
  it("openFiles does not fall back to the first session without an explicit workspace", async () => {
    useUi.setState({ sessions: [sessionOf("s1")] });
    mounted = mountApp();
    await flush();

    await openPaletteFiles();

    expect(useUi.getState().workspaces).toEqual([]);
    expect(useUi.getState().toasts.some((toast) => toast.text === needSessionText)).toBe(true);
  });

  it("openFiles uses the explicit workspace session", async () => {
    useUi.setState({
      sessions: [sessionOf("s1"), sessionOf("s2")],
      workspaces: [workspaceOf("s2")],
      activeWorkspaceId: "ws",
    });
    mounted = mountApp();
    await flush();

    await openPaletteFiles();

    const tabs = useUi.getState().workspaces[0]?.panes[0]?.tabs ?? [];
    expect(tabs.some((tab) => tab.kind === "files" && tab.sessionId === "s2")).toBe(true);
  });

  it.each([
    { name: "no workspace", workspace: null },
    { name: "tools workspace", workspace: nonHostWorkspace("tools") },
    { name: "database workspace", workspace: nonHostWorkspace("db") },
  ])("host actions require an explicit host: $name", async ({ workspace }) => {
    mocks.sessionList.mockResolvedValue([sessionOf("s1")]);
    useUi.setState({
      sessions: [sessionOf("s1")],
      workspaces: workspace ? [workspace] : [],
      activeWorkspaceId: workspace?.id ?? null,
    });
    mounted = mountApp();
    await flush();

    for (const label of ["容器", "磁盘挂载", "端口转发", "后台会话"]) {
      await clickRail(label);
    }

    expect(allTabs().filter((tab) => ["docker", "mount", "forward", "background"].includes(tab.kind))).toEqual([]);
    expect(useUi.getState().workspaces).toHaveLength(workspace ? 1 : 0);
    expect(useUi.getState().toasts.some((toast) => toast.text === needSessionText)).toBe(true);
  });

  it("opens mount, docker, and forward in the explicit current host", async () => {
    const first = hostWorkspace("ws-a", "s1");
    const second = hostWorkspace("ws-b", "s2");
    useUi.setState({
      sessions: [sessionOf("s1"), sessionOf("s2")],
      workspaces: [first, second],
      activeWorkspaceId: second.id,
    });
    mounted = mountApp();
    await flush();

    for (const label of ["磁盘挂载", "容器", "端口转发"]) {
      await clickRail(label);
    }

    const secondTabs = useUi
      .getState()
      .workspaces.find((workspace) => workspace.id === second.id)
      ?.panes.flatMap((pane) => pane.tabs);
    expect(secondTabs).toEqual([
      expect.objectContaining({ kind: "mount", sessionId: "s2" }),
      expect.objectContaining({ kind: "docker", sessionId: "s2" }),
      expect.objectContaining({ kind: "forward", sessionId: "s2" }),
    ]);
    expect(
      useUi
        .getState()
        .workspaces.find((workspace) => workspace.id === first.id)
        ?.panes.flatMap((pane) => pane.tabs),
    ).toEqual([]);
  });

  it("keeps one background tab per owning host across A/B and repeated opens", async () => {
    const first = hostWorkspace("ws-a", "s1", "asset-1");
    const second = hostWorkspace("ws-b", "s2", "asset-2");
    useUi.setState({
      sessions: [sessionOf("s1"), sessionOf("s2")],
      workspaces: [first, second],
      activeWorkspaceId: first.id,
    });
    mounted = mountApp();
    await flush();

    await clickRail("后台会话");
    act(() => useUi.getState().setActiveWorkspace(second.id));
    await flush();
    await clickRail("后台会话");
    await clickRail("后台会话");

    for (const [workspaceId, sessionId, assetId] of [
      [first.id, "s1", "asset-1"],
      [second.id, "s2", "asset-2"],
    ] as const) {
      const backgrounds = useUi
        .getState()
        .workspaces.find((workspace) => workspace.id === workspaceId)
        ?.panes.flatMap((pane) => pane.tabs)
        .filter((tab) => tab.kind === "background");
      expect(backgrounds).toHaveLength(1);
      expect(backgrounds?.[0]).toMatchObject({
        id: `background-${workspaceId}`,
        sessionId,
        assetId,
      });
    }
    expect(useUi.getState().workspaces.some((workspace) => workspace.kind === "tools")).toBe(false);
    expect(useUi.getState().activeWorkspaceId).toBe(second.id);
  });

  it("ordinary history opens preserve the existing filter", async () => {
    const tools = nonHostWorkspace("tools");
    tools.panes[0] = {
      ...tools.panes[0],
      tabs: [
        {
          id: "history",
          kind: "history",
          title: "终端历史",
          assetId: "asset-user",
          closable: true,
        },
      ],
      activeTabId: "history",
    };
    const host = hostWorkspace("ws-b", "s2", "asset-2");
    useUi.setState({
      sessions: [sessionOf("s2")],
      workspaces: [tools, host],
      activeWorkspaceId: host.id,
    });
    mounted = mountApp();
    await flush();

    await clickRail("终端历史");

    const histories = allTabs().filter((tab) => tab.kind === "history");
    expect(histories).toHaveLength(1);
    expect(histories[0]).toMatchObject({ assetId: "asset-user" });
    expect(histories[0].sessionId).toBeUndefined();
  });

  it("a single database asset uses the standard connectAsset flow", async () => {
    mocks.assetList.mockResolvedValue([
      assetOf({ id: "pg", name: "postgres", kind: "postgres", port: 5432 }),
    ]);
    mounted = mountApp();
    await flush();
    await flush();

    const databaseButton = mounted.container.querySelector<HTMLButtonElement>(
      'button[aria-label="数据库"]',
    );
    expect(databaseButton?.getAttribute("data-tip")).toContain("MySQL / PostgreSQL / Redis");
    if (!databaseButton) throw new Error("Database button not found");
    click(databaseButton);
    await flush();

    expect(mocks.connectAsset).toHaveBeenCalledWith(expect.objectContaining({ id: "pg" }));
    expect(mocks.dbConnect).not.toHaveBeenCalled();
  });

  it("multiple database assets open the existing quick connect picker", async () => {
    mocks.assetList.mockResolvedValue([
      assetOf({ id: "mysql", name: "mysql", kind: "mysql", port: 3306 }),
      assetOf({ id: "pg", name: "postgres", kind: "postgres", port: 5432 }),
    ]);
    mounted = mountApp();
    await flush();
    await flush();

    const databaseButton = mounted.container.querySelector('button[aria-label="数据库"]');
    if (!databaseButton) throw new Error("Database button not found");
    click(databaseButton);
    await flush();

    expect(mounted.container.querySelector('[data-testid="quick-connect"]')).not.toBeNull();
    expect(mocks.connectAsset).not.toHaveBeenCalled();
    expect(mocks.dbConnect).not.toHaveBeenCalled();
  });
});

describe("App workspace scope", () => {
  it("closes a workspace from its row and offers a bulk close action", async () => {
    useUi.setState({
      sessions: [sessionOf("s1")],
      workspaces: [workspaceOf("s1"), { ...workspaceOf(), id: "ws-2", title: "second" }],
      activeWorkspaceId: "ws",
    });
    mounted = mountApp();
    await flush();
    const switcher = mounted.container.querySelector('button[aria-label^="切换工作区"]');
    if (!switcher) throw new Error("Workspace switcher not found");
    click(switcher);
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("关闭所有工作区");
    expect(text).not.toContain("结束全部终端进程");
    expect(text).not.toContain("新建工作区");
    const rowClose = mounted.container.querySelector<HTMLButtonElement>('button[aria-label="关闭工作区 workspace"]');
    if (!rowClose) throw new Error("Workspace row close action not found");
    click(rowClose);
    await flush();
    expect(useUi.getState().workspaces.map((workspace) => workspace.id)).toEqual(["ws-2"]);

    const nextSwitcher = mounted.container.querySelector('button[aria-label^="切换工作区"]');
    if (!nextSwitcher) throw new Error("Workspace switcher not found after close");
    click(nextSwitcher);
    await flush();
    const closeAll = [...mounted.container.querySelectorAll<HTMLButtonElement>(".nx-menu-item")].find(
      (button) => button.querySelector(".nx-menu-label")?.textContent === "关闭所有工作区",
    );
    if (!closeAll) throw new Error("Bulk workspace close action not found");
    click(closeAll);
    await flush();
    expect(useUi.getState().workspaces).toEqual([]);
  });

  it("passes stored source ids after the source workspace closes", async () => {
    useUi.setState({
      sessions: [sessionOf("s2")],
      workspaces: [
        {
          ...workspaceOf(),
          id: "ws-tools",
          kind: "tools",
          title: "工具",
          panes: [
            {
              id: "pane",
              tabs: [
                {
                  id: "background",
                  kind: "background",
                  title: "Background sessions",
                  sessionId: "s2",
                  assetId: "asset-2",
                  closable: true,
                },
                {
                  id: "history",
                  kind: "history",
                  title: "Terminal history",
                  sessionId: "s2",
                  assetId: "asset-2",
                  closable: true,
                },
              ],
              activeTabId: "background",
            },
          ],
        },
      ],
      activeWorkspaceId: "ws-tools",
    });
    mounted = mountApp();
    await flush();
    expect(mocks.backgroundProps[mocks.backgroundProps.length - 1]).toMatchObject({
      workspaceId: "ws-tools",
      sessionId: "s2",
    });
    expect(mocks.historyProps[mocks.historyProps.length - 1]).toMatchObject({
      workspaceId: "ws-tools",
      assetId: "asset-2",
    });
  });

  it("does not bind the AI sidebar to a session in a database workspace", async () => {
    useUi.setState({
      sessions: [sessionOf("s1")],
      workspaces: [
        {
          id: "ws",
          kind: "db",
          title: "db-01 · MySQL",
          connId: "c1",
          panes: [{ id: "pane", tabs: [], activeTabId: null }],
          activePaneId: "pane",
          splitRatio: 0.5,
          closable: true,
        },
      ],
      activeWorkspaceId: "ws",
    });
    mounted = mountApp();
    await flush();
    expect(mocks.aiSidebarProps[mocks.aiSidebarProps.length - 1]?.sessionId).toBeUndefined();
  });

  it("binds the AI sidebar only to an explicit workspace session", async () => {
    mocks.sessionList.mockResolvedValue([sessionOf("s1"), sessionOf("s2")]);
    useUi.setState({
      sessions: [sessionOf("s1"), sessionOf("s2")],
      workspaces: [workspaceOf("s2")],
      activeWorkspaceId: "ws",
    });
    mounted = mountApp();
    await flush();
    expect(mocks.aiSidebarProps[mocks.aiSidebarProps.length - 1]?.sessionId).toBe("s2");

    mounted.unmount();
    mounted = undefined;
    mocks.sessionList.mockResolvedValue([sessionOf("s1")]);
    useUi.setState({ sessions: [sessionOf("s1")], workspaces: [], activeWorkspaceId: null });
    mounted = mountApp();
    await flush();
    expect(mocks.aiSidebarProps[mocks.aiSidebarProps.length - 1]?.sessionId).toBeUndefined();
  });

  it("does not bind the AI sidebar to the first session in a tools workspace", async () => {
    const tools = nonHostWorkspace("tools");
    mocks.sessionList.mockResolvedValue([sessionOf("s1")]);
    useUi.setState({
      sessions: [sessionOf("s1")],
      workspaces: [tools],
      activeWorkspaceId: tools.id,
    });
    mounted = mountApp();
    await flush();

    expect(mocks.aiSidebarProps[mocks.aiSidebarProps.length - 1]?.sessionId).toBeUndefined();
  });
});

describe("App empty states", () => {
  it("points the top guidance to assets when no workspace exists", async () => {
    mounted = mountApp();
    await flush();
    expect(mounted.container.querySelector("header")?.textContent).toContain(
      "还没有工作区，双击左侧资产连接一台主机",
    );
  });

  it("keeps the palette action when onPalette is provided and mentions PostgreSQL", async () => {
    mounted = mountApp();
    await flush();

    const main = mounted.container.querySelector("main");
    expect(
      [...(main?.querySelectorAll("button") ?? [])].some(
        (button) => button.textContent?.trim() === "命令面板",
      ),
    ).toBe(true);
    expect(main?.textContent).toContain("MySQL / PostgreSQL / Redis");
  });

  it("hides the palette action without onPalette and uses stable keys for duplicate bindings", async () => {
    setKeybinding("newTerminal", "Mod+Shift+x");
    setKeybinding("quickConnect", "Mod+Shift+x");
    const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    useUi.setState({
      workspaces: [
        workspaceOf(undefined, [
          { id: "broken-files", kind: "files", title: "Files", closable: true },
        ]),
      ],
      activeWorkspaceId: "ws",
    });

    try {
      mounted = mountApp();
      await flush();

      const main = mounted.container.querySelector("main");
      expect(
        [...(main?.querySelectorAll("button") ?? [])].some(
          (button) => button.textContent?.trim() === "命令面板",
        ),
      ).toBe(false);
      expect(main?.textContent).toContain("MySQL / PostgreSQL / Redis");
      const duplicateKeyWarnings = errorSpy.mock.calls.filter((args) =>
        args.some((arg) => String(arg).includes("same key")),
      );
      expect(duplicateKeyWarnings).toEqual([]);
    } finally {
      errorSpy.mockRestore();
    }
  });
});

function pressCtrlT(): void {
  act(() => {
    window.dispatchEvent(
      new KeyboardEvent("keydown", { key: "t", code: "KeyT", ctrlKey: true, cancelable: true }),
    );
  });
}

describe("App Ctrl+T new terminal", () => {
  it("creates a terminal tab in the current workspace when its session is connected", async () => {
    mocks.sessionList.mockResolvedValue([sessionOf("s1")]);
    useUi.setState({
      sessions: [sessionOf("s1")],
      workspaces: [workspaceOf("s1")],
      activeWorkspaceId: "ws",
    });
    mounted = mountApp();
    await flush();

    pressCtrlT();
    await flush();

    const tabs = useUi.getState().workspaces[0]?.panes[0]?.tabs ?? [];
    expect(tabs.filter((t) => t.kind === "terminal" && t.sessionId === "s1")).toHaveLength(1);
    expect(mocks.connectLocal).not.toHaveBeenCalled();
  });

  it("falls back to a local terminal when no workspace exists", async () => {
    mocks.connectLocal.mockResolvedValue(sessionOf("local-1"));
    mounted = mountApp();
    await flush();

    pressCtrlT();
    await flush();

    expect(mocks.connectLocal).toHaveBeenCalledTimes(1);
    const st = useUi.getState();
    expect(st.workspaces).toHaveLength(1);
    expect(
      st.workspaces[0].panes[0].tabs.some((t) => t.kind === "terminal" && t.sessionId === "local-1"),
    ).toBe(true);
  });

  it("also falls back to a local terminal for a database workspace", async () => {
    mocks.connectLocal.mockResolvedValue(sessionOf("local-2"));
    useUi.setState({
      workspaces: [
        {
          id: "ws",
          kind: "db",
          title: "db-01 · MySQL",
          assetId: "a-db",
          connId: "c1",
          panes: [{ id: "pane", tabs: [], activeTabId: null }],
          activePaneId: "pane",
          splitRatio: 0.5,
          closable: true,
        },
      ],
      activeWorkspaceId: "ws",
    });
    mounted = mountApp();
    await flush();

    pressCtrlT();
    await flush();

    expect(mocks.connectLocal).toHaveBeenCalledTimes(1);
  });
});

async function pressNavigationKey(
  key: string,
  code: string,
  init: KeyboardEventInit = {},
): Promise<KeyboardEvent> {
  const event = new KeyboardEvent("keydown", {
    key,
    code,
    bubbles: true,
    cancelable: true,
    ...init,
  });
  await act(async () => {
    window.dispatchEvent(event);
  });
  return event;
}

describe("App workspace navigation in browser mode", () => {
  it("cycles workspaces with the non-reserved browser bindings", async () => {
    useUi.setState({
      workspaces: [
        hostWorkspace("ws-a", "s1"),
        hostWorkspace("ws-b", "s2"),
        hostWorkspace("ws-c", "s3"),
      ],
      activeWorkspaceId: "ws-b",
    });
    mounted = mountApp();
    await flush();

    expect((await pressNavigationKey("ArrowRight", "ArrowRight", { ctrlKey: true, altKey: true })).defaultPrevented).toBe(true);
    expect(useUi.getState().activeWorkspaceId).toBe("ws-c");
    await pressNavigationKey("ArrowRight", "ArrowRight", { ctrlKey: true, altKey: true });
    expect(useUi.getState().activeWorkspaceId).toBe("ws-a");
    await pressNavigationKey("ArrowLeft", "ArrowLeft", { ctrlKey: true, altKey: true });
    expect(useUi.getState().activeWorkspaceId).toBe("ws-c");
  });

  it("cycles tabs with browser alternatives and leaves Ctrl+Tab unclaimed", async () => {
    const current = workspaceOf("s1", [
      { id: "t1", kind: "terminal", title: "Terminal 1", sessionId: "s1", closable: true },
      { id: "t2", kind: "terminal", title: "Terminal 2", sessionId: "s1", closable: true },
    ]);
    useUi.setState({ workspaces: [current], activeWorkspaceId: "ws" });
    mounted = mountApp();
    await flush();

    const reserved = await pressNavigationKey("Tab", "Tab", { ctrlKey: true });
    expect(reserved.defaultPrevented).toBe(false);
    expect(useUi.getState().workspaces[0].panes[0].activeTabId).toBe("t1");

    expect((await pressNavigationKey("]", "BracketRight", { ctrlKey: true, altKey: true })).defaultPrevented).toBe(true);
    expect(useUi.getState().workspaces[0].panes[0].activeTabId).toBe("t2");
    await pressNavigationKey("]", "BracketRight", { ctrlKey: true, altKey: true });
    expect(useUi.getState().workspaces[0].panes[0].activeTabId).toBe("t1");
    await pressNavigationKey("[", "BracketLeft", { ctrlKey: true, altKey: true });
    expect(useUi.getState().workspaces[0].panes[0].activeTabId).toBe("t2");

    const reservedPrevious = await pressNavigationKey("Tab", "Tab", { ctrlKey: true, shiftKey: true });
    expect(reservedPrevious.defaultPrevented).toBe(false);
    expect(useUi.getState().workspaces[0].panes[0].activeTabId).toBe("t2");
  });

  it("leaves Safari's macOS bracket shortcuts unclaimed and uses the browser alternative", async () => {
    setMacPlatform(true);
    const current = workspaceOf("s1", [
      { id: "t1", kind: "terminal", title: "Terminal 1", sessionId: "s1", closable: true },
      { id: "t2", kind: "terminal", title: "Terminal 2", sessionId: "s1", closable: true },
    ]);
    useUi.setState({ workspaces: [current], activeWorkspaceId: "ws" });
    mounted = mountApp();
    await flush();

    const safariNext = await pressNavigationKey("}", "BracketRight", { metaKey: true, shiftKey: true });
    expect(safariNext.defaultPrevented).toBe(false);
    expect(useUi.getState().workspaces[0].panes[0].activeTabId).toBe("t1");
    await pressNavigationKey("]", "BracketRight", { ctrlKey: true, altKey: true });
    expect(useUi.getState().workspaces[0].panes[0].activeTabId).toBe("t2");
    const safariPrevious = await pressNavigationKey("{", "BracketLeft", { metaKey: true, shiftKey: true });
    expect(safariPrevious.defaultPrevented).toBe(false);
    expect(useUi.getState().workspaces[0].panes[0].activeTabId).toBe("t2");
  });

  it("cycles focus between the two rendered panes only", async () => {
    const current = workspaceOf("s1", [
      { id: "t1", kind: "terminal", title: "Terminal 1", sessionId: "s1", closable: true },
    ]);
    current.panes.push(
      {
        id: "lower",
        tabs: [{ id: "t2", kind: "terminal", title: "Terminal 2", sessionId: "s1", closable: true }],
        activeTabId: "t2",
      },
      { id: "hidden-third", tabs: [], activeTabId: null },
    );
    useUi.setState({ workspaces: [current], activeWorkspaceId: "ws" });
    mounted = mountApp();
    await flush();

    expect((await pressNavigationKey("ArrowDown", "ArrowDown", { ctrlKey: true, altKey: true })).defaultPrevented).toBe(true);
    expect(useUi.getState().workspaces[0].activePaneId).toBe("lower");
    await pressNavigationKey("ArrowDown", "ArrowDown", { ctrlKey: true, altKey: true });
    expect(useUi.getState().workspaces[0].activePaneId).toBe("pane");
    await pressNavigationKey("ArrowUp", "ArrowUp", { ctrlKey: true, altKey: true });
    expect(useUi.getState().workspaces[0].activePaneId).toBe("lower");
  });
});

describe("App tab markers", () => {
  function renderCount(tabId: string): number {
    return mocks.terminalPaneProps.filter((props) => props.storeTabId === tabId).length;
  }

  it("renders unread and close-failure markers on the tab and in its tooltip", async () => {
    useUi.setState({
      sessions: [sessionOf("s1")],
      workspaces: [
        workspaceOf("s1", [
          {
            id: "t1",
            kind: "terminal",
            title: "Terminal 1",
            sessionId: "s1",
            tabId: "T1",
            closable: true,
            closeError: "资源不存在：tab not found",
          },
        ]),
      ],
      activeWorkspaceId: "ws",
    });
    setTabUnread("t1", true);
    mounted = mountApp();
    await flush();

    const tabEl = mounted.container.querySelector('[data-tab-id="t1"]');
    expect(tabEl?.getAttribute("title")).toContain("有新输出");
    expect(tabEl?.getAttribute("title")).toContain("上次关闭失败：资源不存在：tab not found");
    expect(tabEl?.querySelector('[role="img"][aria-label="有新输出"]')).not.toBeNull();
    expect(tabEl?.querySelector('[role="img"][aria-label="关闭失败：资源不存在：tab not found"]')).not.toBeNull();
  });

  it("updates unread markers without rerendering terminal panes", async () => {
    useUi.setState({
      sessions: [sessionOf("s1")],
      workspaces: [
        workspaceOf("s1", [
          { id: "t1", kind: "terminal", title: "Terminal 1", sessionId: "s1", tabId: "T1", closable: true },
          { id: "t2", kind: "terminal", title: "Terminal 2", sessionId: "s1", tabId: "T2", closable: true },
        ]),
      ],
      activeWorkspaceId: "ws",
    });
    mounted = mountApp();
    await flush();
    const before = mocks.terminalPaneProps.length;

    act(() => setTabUnread("t2", true));
    await flush();

    const tabEl = mounted.container.querySelector('[data-tab-id="t2"]');
    expect(tabEl?.getAttribute("title")).toContain("有新输出");
    expect(tabEl?.querySelector('[aria-label="有新输出"]')).not.toBeNull();
    expect(mocks.terminalPaneProps.length).toBe(before);
    expect(renderCount("t1")).toBeGreaterThan(0);
    expect(renderCount("t2")).toBeGreaterThan(0);

    act(() => setTabUnread("t2", false));
    await flush();
    expect(mocks.terminalPaneProps.length).toBe(before);
  });

  it("rerenders only the updated terminal pane when a title changes", async () => {
    useUi.setState({
      sessions: [sessionOf("s1")],
      workspaces: [
        workspaceOf("s1", [
          { id: "t1", kind: "terminal", title: "Terminal 1", sessionId: "s1", tabId: "T1", closable: true },
          { id: "t2", kind: "terminal", title: "Terminal 2", sessionId: "s1", tabId: "T2", closable: true },
        ]),
      ],
      activeWorkspaceId: "ws",
    });
    mounted = mountApp();
    await flush();
    const beforeTarget = renderCount("t2");
    const beforeOther = renderCount("t1");

    act(() => useUi.getState().updateTab("t2", { title: "Renamed" }));
    await flush();

    expect(renderCount("t2")).toBe(beforeTarget + 1);
    expect(renderCount("t1")).toBe(beforeOther);
  });
});

async function submitRenameDraft(draft: string): Promise<void> {
  useUi.setState({
    sessions: [sessionOf("s1")],
    workspaces: [
      workspaceOf("s1", [
        {
          id: "t1",
          kind: "terminal",
          title: "original",
          sessionId: "s1",
          tabId: "T1",
          closable: true,
        },
      ]),
    ],
    activeWorkspaceId: "ws",
  });
  mounted = mountApp();
  await flush();
  const tab = mounted.container.querySelector('[data-tab-id="t1"]');
  if (!tab) throw new Error("terminal tab not found");
  act(() => {
    tab.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 12, clientY: 12 }));
  });
  await flush();
  clickButton(mounted.container, "重命名");
  await flush();
  const input = mounted.container.querySelector<HTMLInputElement>(".nx-modal input");
  if (!input) throw new Error("rename input not found");
  setInputValue(input, draft);
  clickButton(mounted.container, "确定");
  await flush();
}

describe("App terminal rename", () => {
  it("uses the remotely confirmed canonical title", async () => {
    mocks.renameTab.mockResolvedValue({ title: "remote-canonical" });
    await submitRenameDraft("draft-title");
    expect(mocks.renameTab).toHaveBeenCalledWith("T1", "draft-title");
    const tabs = useUi.getState().workspaces[0]?.panes[0]?.tabs ?? [];
    expect(tabs[0]?.title).toBe("remote-canonical");
    expect(tabs[0]?.renamedByUser).toBe(true);
  });

  it("keeps the edit draft and shows the conflict instead of swallowing it", async () => {
    mocks.renameTab.mockRejectedValue({
      code: "metadata_conflict",
      message: "metadata changed",
      detail: { title: "remote-current", metadataRevision: 4 },
    });
    await submitRenameDraft("draft-title");
    await flush();
    const modal = mounted?.container.querySelector(".nx-modal");
    expect(modal?.textContent).toContain("metadata changed");
    const input = modal?.querySelector<HTMLInputElement>("input");
    expect(input?.value).toBe("draft-title");
    const tabs = useUi.getState().workspaces[0]?.panes[0]?.tabs ?? [];
    expect(tabs[0]?.title).toBe("original");
  });
});

describe("App shell notification layout", () => {
  it("keeps the header first and preserves notification order as each one disappears", async () => {
    const resize = async (width: number, height: number): Promise<void> => {
      Object.defineProperty(window, "innerWidth", {
        value: width,
        configurable: true,
        writable: true,
      });
      Object.defineProperty(window, "innerHeight", {
        value: height,
        configurable: true,
        writable: true,
      });
      await act(async () => {
        window.dispatchEvent(new Event("resize"));
        await flushMacrotask();
      });
    };

    Object.assign(mocks.banners, { takeover: true, update: true, dek: true });
    mounted = mountApp();
    await flush();

    expectShellOrder(["header", "takeover", "update", "dek", "workspace-body"]);
    await resize(960, 600);
    expectShellOrder(["header", "takeover", "update", "dek", "workspace-body"]);

    setBanners({ takeover: false });
    expectShellOrder(["header", "update", "dek", "workspace-body"]);
    await resize(1280, 720);
    expectShellOrder(["header", "update", "dek", "workspace-body"]);

    setBanners({ update: false });
    expectShellOrder(["header", "dek", "workspace-body"]);
    await resize(800, 500);
    expectShellOrder(["header", "dek", "workspace-body"]);

    setBanners({ dek: false });
    expectShellOrder(["header", "workspace-body"]);
    await resize(1024, 768);
    expectShellOrder(["header", "workspace-body"]);
  });

  it("keeps desktop window controls interactive and outside notification hit regions", async () => {
    envState.transport = "desktop";
    Object.assign(mocks.banners, { takeover: true, update: true, dek: true });
    mounted = mountApp();
    await flush();

    const header = mounted.container.querySelector<HTMLElement>(".nx-app-header");
    const workspaceBody = mounted.container.querySelector<HTMLElement>(".nx-workspace-body");
    expect(header).not.toBeNull();
    expect(workspaceBody).not.toBeNull();
    expect(header?.style.getPropertyValue("--wails-draggable")).toBe("drag");

    const controls = [
      ["最小化窗口", mocks.minimiseWindow],
      ["最大化或还原窗口", mocks.toggleMaximiseWindow],
      ["关闭窗口", mocks.closeWindow],
    ] as const;
    for (const [label, action] of controls) {
      const button = header?.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`);
      expect(button).not.toBeNull();
      expect(button?.disabled).toBe(false);
      expect(button?.style.getPropertyValue("--wails-draggable")).toBe("no-drag");
      if (!button) throw new Error(`Window control not found: ${label}`);
      click(button);
      expect(action).toHaveBeenCalledTimes(1);
    }

    mocks.toggleMaximiseWindow.mockClear();
    const dragRegion = header?.querySelector<HTMLElement>(
      '.nx-spacer[data-wails-drag-region]',
    );
    expect(dragRegion).not.toBeNull();
    if (!dragRegion) throw new Error("Header drag region not found");
    act(() => {
      dragRegion.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
    });
    expect(mocks.toggleMaximiseWindow).toHaveBeenCalledTimes(1);

    for (const testId of ["takeover-banner", "update-banner", "dek-banner"]) {
      const banner = mounted.container.querySelector<HTMLElement>(`[data-testid="${testId}"]`);
      if (!banner) throw new Error(`Banner not found: ${testId}`);
      act(() => {
        banner.dispatchEvent(new MouseEvent("dblclick", { bubbles: true }));
      });
    }
    expect(mocks.toggleMaximiseWindow).toHaveBeenCalledTimes(1);
  });
});
