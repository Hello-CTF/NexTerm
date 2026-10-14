/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, mount, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  sessionList: vi.fn(),
  vaultStatus: vi.fn(),
  connectAsset: vi.fn(),
  connectLocal: vi.fn(),
  dbConnect: vi.fn(),
  aiSidebarProps: [] as { sessionId?: string }[],
  backgroundProps: [] as { workspaceId?: string }[],
  historyProps: [] as { workspaceId?: string }[],
}));

vi.mock("../app/store", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../app/store")>();
  return { ...actual, connectAsset: mocks.connectAsset };
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
  terminalApi: {},
  dbApi: { connect: mocks.dbConnect },
}));
vi.mock("../ipc/wails", () => ({
  closeWindow: vi.fn(),
  minimiseWindow: vi.fn(),
  toggleMaximiseWindow: vi.fn(),
}));
vi.mock("../app/layout", () => ({
  layoutBootstrapped: Promise.resolve(),
  startLayoutSync: () => {},
}));
vi.mock("../app/useUpdate", () => ({
  getUpdateState: () => ({ check: vi.fn() }),
}));

vi.mock("../features/terminal/TerminalPane", () => ({ TerminalPane: () => null }));
vi.mock("../features/terminal/BackgroundSessions", () => ({
  BackgroundSessions: (props: { workspaceId?: string }) => {
    mocks.backgroundProps.push(props);
    return null;
  },
}));
vi.mock("../features/terminal/TranscriptHistoryPanel", () => ({
  TranscriptHistoryPanel: (props: { workspaceId?: string }) => {
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
vi.mock("../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));
vi.mock("../app/UpdateBanner", () => ({ UpdateBanner: () => null }));

import App from "../app/App";
import { resetAllKeybindings, setKeybinding } from "../app/keybindings";
import { useUi, type AppTab, type Workspace } from "../app/store";
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

function mountApp(): MountedView {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(App)));
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
  mocks.aiSidebarProps.length = 0;
  mocks.backgroundProps.length = 0;
  mocks.historyProps.length = 0;
  mocks.assetList.mockResolvedValue([]);
  mocks.sessionList.mockResolvedValue([]);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  mocks.connectAsset.mockResolvedValue({ ok: true });
  mocks.dbConnect.mockResolvedValue({ connId: "db-conn" });
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

describe("App workspace actions", () => {
  it("openFiles does not fall back to the first session without an explicit workspace", async () => {
    useUi.setState({ sessions: [sessionOf("s1")] });
    mounted = mountApp();
    await flush();

    await openPaletteFiles();

    expect(useUi.getState().workspaces).toEqual([]);
    expect(
      useUi.getState().toasts.some((toast) => toast.text === "先连接一台主机（双击左侧资产）"),
    ).toBe(true);
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
  it("does not offer a new-workspace action in the workspace switcher", async () => {
    useUi.setState({
      sessions: [sessionOf("s1")],
      workspaces: [workspaceOf("s1")],
      activeWorkspaceId: "ws",
    });
    mounted = mountApp();
    await flush();
    const switcher = mounted.container.querySelector('button[aria-label^="切换工作区"]');
    if (!switcher) throw new Error("Workspace switcher not found");
    click(switcher);
    await flush();
    const text = mounted.container.textContent ?? "";
    expect(text).toContain("关闭「workspace」");
    expect(text).not.toContain("新建工作区");
  });

  it("passes the owning workspace id to background-session and terminal-history tabs instead of the active workspace", async () => {
    useUi.setState({
      sessions: [sessionOf("s1"), sessionOf("s2")],
      workspaces: [
        { ...workspaceOf("s1"), id: "ws-active" },
        {
          ...workspaceOf("s2"),
          id: "ws-host",
          panes: [
            {
              id: "pane",
              tabs: [
                { id: "background", kind: "background", title: "Background sessions", closable: true },
                { id: "history", kind: "history", title: "Terminal history", closable: true },
              ],
              activeTabId: "background",
            },
          ],
        },
      ],
      activeWorkspaceId: "ws-active",
    });
    mounted = mountApp();
    await flush();
    expect(mocks.backgroundProps[mocks.backgroundProps.length - 1]?.workspaceId).toBe("ws-host");
    expect(mocks.historyProps[mocks.historyProps.length - 1]?.workspaceId).toBe("ws-host");
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

  it("binds the AI sidebar to the workspace session and falls back to the first session without a workspace", async () => {
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
    expect(mocks.aiSidebarProps[mocks.aiSidebarProps.length - 1]?.sessionId).toBe("s1");
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

describe("App tab markers", () => {
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
            unread: true,
            closeError: "资源不存在：tab not found",
          },
        ]),
      ],
      activeWorkspaceId: "ws",
    });
    mounted = mountApp();
    await flush();

    const tabEl = mounted.container.querySelector('[data-tab-id="t1"]');
    expect(tabEl?.getAttribute("title")).toContain("有新输出");
    expect(tabEl?.getAttribute("title")).toContain("上次关闭失败：资源不存在：tab not found");
    expect(tabEl?.querySelector('[role="img"][aria-label="有新输出"]')).not.toBeNull();
    expect(tabEl?.querySelector('[role="img"][aria-label="关闭失败：资源不存在：tab not found"]')).not.toBeNull();
  });
});
