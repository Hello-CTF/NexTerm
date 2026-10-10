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
vi.mock("../features/explorer/AssetTree", () => ({ AssetTree: () => null, AssetEditor: () => null }));
vi.mock("../app/CommandPalette", () => ({
  CommandPalette: (props: { onOpenFiles?: () => void }) => (
    <button type="button" data-testid="palette-files" onClick={props.onOpenFiles}>
      打开文件浏览器
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
    expect(databaseButton?.title).toContain("MySQL / PostgreSQL / Redis");
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

describe("App empty states", () => {
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
          { id: "broken-files", kind: "files", title: "文件", closable: true },
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

describe("App Ctrl+T 新建终端", () => {
  it("当前工作区会话已连接时在该工作区新建终端标签", async () => {
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

  it("没有工作区时回退为本地终端", async () => {
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

  it("数据库工作区不能开终端,同样回退为本地终端", async () => {
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

describe("App 标签栏标记", () => {
  it("未读与关闭失败的标记渲染在标签上并进入 tooltip", async () => {
    useUi.setState({
      sessions: [sessionOf("s1")],
      workspaces: [
        workspaceOf("s1", [
          {
            id: "t1",
            kind: "terminal",
            title: "终端 1",
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
