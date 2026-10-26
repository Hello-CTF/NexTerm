/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { click, flush, flushUntil, mount, type MountedView } from "./reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  groupList: vi.fn(),
  sessionList: vi.fn(),
  vaultStatus: vi.fn(),
  connectAsset: vi.fn(),
  connectLocal: vi.fn(),
  sessionConnect: vi.fn(),
  dbConnect: vi.fn(),
  updateCheck: vi.fn(),
  closeWindow: vi.fn(),
  minimiseWindow: vi.fn(),
  toggleMaximiseWindow: vi.fn(),
  banners: { takeover: false, update: false, dek: false },
  terminalPaneProps: [] as { storeTabId: string; resumeTabId?: string }[],
  closeTab: vi.fn(),
  fsRoots: vi.fn(),
  fsList: vi.fn(),
  fsRead: vi.fn(),
  fsReadRange: vi.fn(),
}));

vi.mock("../../app/store", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../app/store")>();
  return { ...actual, connectAsset: mocks.connectAsset };
});

vi.mock("../../ipc/env", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/env")>();
  return {
    ...actual,
    get TRANSPORT() {
      return "web" as const;
    },
    get WEB() {
      return true;
    },
    get DESKTOP() {
      return false;
    },
  };
});

vi.mock("../../ipc/commands", () => ({
  assetApi: {
    list: mocks.assetList,
    search: vi.fn(),
    groupList: mocks.groupList,
    snippetList: vi.fn(),
  },
  sessionApi: {
    list: mocks.sessionList,
    connectLocal: mocks.connectLocal,
    connect: mocks.sessionConnect,
    reconnect: vi.fn(),
    prepareDurable: vi.fn(async () => ({ state: "preparing" })),
    probe: vi.fn(),
    lineExec: vi.fn(),
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
  terminalApi: { closeTab: mocks.closeTab, listLive: vi.fn(async () => []), write: vi.fn() },
  dbApi: { connect: mocks.dbConnect },
  fsApi: {
    roots: mocks.fsRoots,
    list: mocks.fsList,
    read: mocks.fsRead,
    readRange: mocks.fsReadRange,
    write: vi.fn(),
    rename: vi.fn(),
    chmod: vi.fn(),
    checksum: vi.fn(),
    mkdir: vi.fn(),
    delete: vi.fn(),
    upload: vi.fn(),
    download: vi.fn(),
    packDownload: vi.fn(),
    extract: vi.fn(),
  },
  localFileApi: {},
  toAppError: (e: { code?: string; message?: string } | null) => ({
    code: e?.code,
    message: e?.message ?? "错误",
  }),
}));
vi.mock("../../ipc/wails", () => ({
  closeWindow: mocks.closeWindow,
  minimiseWindow: mocks.minimiseWindow,
  toggleMaximiseWindow: mocks.toggleMaximiseWindow,
  listenWailsEvent: vi.fn(() => Promise.resolve(() => {})),
}));
vi.mock("../../app/layout", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../app/layout")>()),
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
vi.mock("../../app/useUpdate", () => ({
  getUpdateState: () => ({ check: mocks.updateCheck }),
}));
vi.mock("../../features/settings/useWebAutoSync", () => ({
  useDesktopAutoSync: () => {},
  useWebAutoSync: () => {},
}));

vi.mock("../../features/terminal/TerminalPane", () => ({
  TerminalPane: (props: { storeTabId: string; resumeTabId?: string }) => {
    mocks.terminalPaneProps.push(props);
    return null;
  },
}));
vi.mock("../../features/terminal/BackgroundSessions", () => ({ BackgroundSessions: () => null }));
vi.mock("../../features/terminal/TranscriptHistoryPanel", () => ({ TranscriptHistoryPanel: () => null }));
vi.mock("../../features/files/FileTree", () => ({ FileTree: () => null }));
vi.mock("../../features/files/MountPanel", () => ({ MountPanel: () => null }));
vi.mock("../../features/forward/ForwardPanel", () => ({ ForwardPanel: () => null }));
vi.mock("../../features/docker/DockerPanel", () => ({ DockerPanel: () => null }));
vi.mock("../../features/db/DbPanel", () => ({ DbPanel: () => null }));
vi.mock("../../features/ai/AiSidebar", () => ({ AiSidebar: () => null }));
vi.mock("../../features/settings/SettingsView", () => ({ SettingsView: () => null }));
vi.mock("../../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../../features/explorer/AssetTree", () => ({ AssetTree: () => null, AssetEditor: () => null }));
vi.mock("../../app/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../../app/QuickConnect", () => ({ QuickConnect: () => null }));
vi.mock("../../app/TakeoverBanner", () => ({
  TakeoverBanner: () => (mocks.banners.takeover ? <div data-testid="takeover-banner" /> : null),
}));
vi.mock("../../app/UpdateBanner", () => ({
  UpdateBanner: () => (mocks.banners.update ? <div data-testid="update-banner" /> : null),
}));
vi.mock("../../app/DekUnlockBanner", () => ({
  DekUnlockBanner: () => (mocks.banners.dek ? <div data-testid="dek-banner" /> : null),
}));

// The real FileBrowser/FileEditor/LogViewer mount in these tests; only their
// measurement/editor primitives and the clipboard are stubbed, while every fs/session
// call goes through the spies in the ipc/commands mock above.
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count }: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: count }, (_, i) => ({ index: i, start: i * 30, size: 30, key: i })),
    getTotalSize: () => count * 30,
  }),
}));
vi.mock("../../ui/clipboard", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ui/clipboard")>();
  return { ...actual, copyText: vi.fn(async () => undefined) };
});
vi.mock("@codemirror/state", () => {
  class Doc {
    constructor(private readonly text: string) {}
    toString() {
      return this.text;
    }
    get length() {
      return this.text.length;
    }
  }
  class Compartment {
    of(extension: unknown) {
      return { compartment: this, extension };
    }
    reconfigure(extension: unknown) {
      return { compartment: this, extension };
    }
  }
  return {
    Compartment,
    EditorState: {
      create: ({ doc, extensions }: { doc: string; extensions: unknown[] }) => ({
        doc: new Doc(doc),
        extensions,
      }),
    },
    Prec: { highest: (extension: unknown) => extension },
  };
});
vi.mock("@codemirror/view", () => {
  class EditorView {
    static updateListener = {
      of: (listener: unknown) => ({ codeMirrorListener: listener }),
    };
    constructor(config: { state: unknown }) {
      void config;
    }
    destroy() {}
  }
  return { EditorView, keymap: { of: (bindings: unknown) => bindings } };
});
vi.mock("codemirror", () => ({ basicSetup: [] }));
vi.mock("@codemirror/commands", () => ({
  indentWithTab: {},
  redo: vi.fn(),
  undo: vi.fn(),
}));
vi.mock("@codemirror/search", () => ({ search: () => [], openSearchPanel: vi.fn() }));
vi.mock("@codemirror/language", () => ({ StreamLanguage: { define: (mode: unknown) => mode } }));
vi.mock("@codemirror/lang-sql", () => ({ sql: () => [] }));
vi.mock("@codemirror/legacy-modes/mode/shell", () => ({ shell: {} }));
vi.mock("@codemirror/legacy-modes/mode/nginx", () => ({ nginx: {} }));
vi.mock("@codemirror/legacy-modes/mode/yaml", () => ({ yaml: {} }));
vi.mock("@codemirror/legacy-modes/mode/properties", () => ({ properties: {} }));
vi.mock("@codemirror/legacy-modes/mode/javascript", () => ({ javascript: {} }));
vi.mock("@codemirror/legacy-modes/mode/python", () => ({ python: {} }));
vi.mock("../../ui/editorTheme", () => ({ nxHighlight: [] }));

import App from "../../app/App";
import { closedTabEntryFromTab, useClosedTabs } from "../../app/closedTabs";
import { restoreClosedTab, useUi, type AppTab, type Workspace } from "../../app/store";
import { resetUnreadTabs } from "../../app/unreadStore";
import type { Asset, AssetGroup, SessionInfo } from "../../ipc/commands";

function sessionOf(id: string): SessionInfo {
  return { id, assetId: "asset-1", name: id, kind: "ssh", status: "connected", tabs: [], createdAt: 1 };
}

function hostWorkspace(id: string, sessionId: string, tabs: AppTab[]): Workspace {
  const paneId = `${id}-pane`;
  return {
    id,
    kind: "session",
    title: id,
    sessionId,
    assetId: "asset-1",
    panes: [{ id: paneId, tabs, activeTabId: tabs[0]?.id ?? null }],
    activePaneId: paneId,
    closable: true,
  };
}

function terminalTab(id: string, overrides: Partial<AppTab> = {}): AppTab {
  return { id, kind: "terminal", title: id, sessionId: "s1", assetId: "asset-1", tabId: `kernel-${id}`, closable: true, ...overrides };
}

let mounted: MountedView | undefined;
let client: QueryClient | undefined;

function mountApp(): MountedView {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return mount(createElement(QueryClientProvider, { client }, createElement(App)));
}

function findTab(id: string): AppTab | undefined {
  return useUi
    .getState()
    .workspaces.flatMap((workspace) => workspace.panes)
    .flatMap((pane) => pane.tabs)
    .find((tab) => tab.id === id);
}

function seedClosedTabStack(tab: AppTab, workspaceId: string, options?: { detached?: boolean }): void {
  useClosedTabs.getState().resetForTest();
  useClosedTabs.getState().ensureAccount("test-account");
  const entry = closedTabEntryFromTab(tab, { workspaceId, ...options });
  if (!entry) throw new Error("expected a restorable entry");
  const generation = useClosedTabs.getState().stack.generation;
  useClosedTabs.getState().capture(entry, generation);
}

async function openWorkspaceSwitcher(): Promise<void> {
  const button = mounted?.container.querySelector<HTMLButtonElement>('button[aria-label^="切换工作区"]');
  if (!button) throw new Error("Workspace switcher not found");
  click(button);
  await flush();
}

function restoreMenuItem(): HTMLButtonElement | undefined {
  return [...(mounted?.container.querySelectorAll<HTMLButtonElement>(".nx-menu-item") ?? [])].find(
    (item) => item.textContent?.includes("恢复关闭的标签"),
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  window.localStorage.clear();
  Element.prototype.scrollIntoView = vi.fn();
  Object.assign(mocks.banners, { takeover: false, update: false, dek: false });
  mocks.terminalPaneProps.length = 0;
  resetUnreadTabs();
  mocks.assetList.mockResolvedValue([]);
  mocks.groupList.mockResolvedValue([]);
  mocks.sessionList.mockImplementation(async () => useUi.getState().sessions);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  mocks.connectAsset.mockResolvedValue({ ok: true });
  mocks.closeTab.mockResolvedValue(undefined);
  mocks.fsRoots.mockResolvedValue({ pathStyle: "unknown", roots: [], homePath: null, supported: false });
  mocks.fsList.mockResolvedValue([]);
  mocks.fsReadRange.mockResolvedValue({ offset: 0, size: 0, contentBase64: "", truncated: false });
  mocks.fsRead.mockImplementation(async (_sessionId: string, path: string) => ({
    path,
    size: 8,
    contentBase64: btoa("original"),
    modifiedAt: 1,
  }));
  useUi.setState({
    sessions: [sessionOf("s1")],
    toasts: [],
    leftOpen: false,
    rightOpen: false,
    leftMode: "assets",
    workspaces: [hostWorkspace("ws-1", "s1", [])],
    activeWorkspaceId: "ws-1",
    connectingAssetIds: [],
  });
  useClosedTabs.getState().resetForTest();
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("closed-tab restore UX", () => {
  it("restores the most recently closed tab from the workspace switcher menu", async () => {
    seedClosedTabStack(terminalTab("t1"), "ws-1", { detached: true });
    mounted = mountApp();
    await flush();

    await openWorkspaceSwitcher();
    const item = restoreMenuItem();
    expect(item).toBeDefined();
    expect(item?.textContent).toContain("t1");
    click(item as HTMLButtonElement);
    await flush();

    expect(findTab("t1")?.tabId).toBe("kernel-t1");
    expect(useClosedTabs.getState().stack.entries).toEqual([]);
  });

  it("keeps the restore action visible but disabled when nothing was closed", async () => {
    mounted = mountApp();
    await flush();

    await openWorkspaceSwitcher();
    const item = restoreMenuItem();
    expect(item).toBeDefined();
    expect(item?.disabled).toBe(true);
  });

  it("restores the most recently closed tab with Ctrl+Shift+T", async () => {
    seedClosedTabStack(terminalTab("t1"), "ws-1", { detached: true });
    mounted = mountApp();
    await flush();

    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "T", ctrlKey: true, shiftKey: true, bubbles: true, cancelable: true }));
    });
    await flush();

    expect(findTab("t1")?.tabId).toBe("kernel-t1");
  });

  it("shows a pending restore placeholder with an explicit reconnect instead of attaching the terminal", async () => {
    const pending = terminalTab("t1", { tabId: undefined, restorePending: true });
    useUi.setState({ workspaces: [hostWorkspace("ws-1", "s1", [pending])] });
    mounted = mountApp();
    await flush();

    expect(mounted?.container.textContent).toContain("新开终端");
    expect(mocks.terminalPaneProps.some((props) => props.storeTabId === "t1")).toBe(false);

    const reconnect = [...(mounted?.container.querySelectorAll("button") ?? [])].find(
      (button) => button.textContent?.trim() === "新开终端",
    );
    expect(reconnect).toBeDefined();
    click(reconnect as HTMLButtonElement);
    await flush();

    expect(findTab("t1")?.restorePending).toBeUndefined();
    expect(mocks.terminalPaneProps.some((props) => props.storeTabId === "t1")).toBe(true);
  });

  it("does not restore across the account boundary from the menu action", async () => {
    seedClosedTabStack(terminalTab("t1"), "ws-1");
    useClosedTabs.getState().ensureAccount("other-account");
    mounted = mountApp();
    await flush();

    await openWorkspaceSwitcher();
    const item = restoreMenuItem();
    expect(item?.disabled).toBe(true);

    await expect(restoreClosedTab()).resolves.toBe(false);
    expect(findTab("t1")).toBeUndefined();
  });
});

describe("restorePending file and log tabs mount only after an explicit reconnect", () => {
  function reconnectButton(): HTMLButtonElement {
    const button = [...(mounted?.container.querySelectorAll("button") ?? [])].find(
      (candidate) => candidate.textContent?.trim() === "连接并打开",
    );
    if (!button) throw new Error("Reconnect button not found");
    return button;
  }

  it("files editor: placeholder first, no fs read and no auto reconnect before the click", async () => {
    const pending: AppTab = {
      id: "f1",
      kind: "files",
      title: "f1.txt",
      sessionId: "s1",
      path: "/a/f1.txt",
      closable: true,
      restorePending: true,
    };
    useUi.setState({ workspaces: [hostWorkspace("ws-1", "s1", [pending])] });
    mounted = mountApp();
    await flush();

    expect(mounted?.container.textContent).toContain("连接并打开");
    expect(mocks.fsRead).not.toHaveBeenCalled();
    expect(mocks.fsReadRange).not.toHaveBeenCalled();
    expect(mocks.sessionConnect).not.toHaveBeenCalled();

    click(reconnectButton());
    await flush();

    expect(findTab("f1")?.restorePending).toBeUndefined();
    await flushUntil(() => mocks.fsRead.mock.calls.length > 0);
    expect(mocks.sessionConnect).not.toHaveBeenCalled();
  });

  it("log viewer: placeholder first, readRange only after the explicit reconnect", async () => {
    const pending: AppTab = {
      id: "log-1",
      kind: "log",
      title: "app.log",
      sessionId: "s1",
      path: "/var/log/app.log",
      closable: true,
      restorePending: true,
    };
    useUi.setState({ workspaces: [hostWorkspace("ws-1", "s1", [pending])] });
    mounted = mountApp();
    await flush();

    expect(mounted?.container.textContent).toContain("连接并打开");
    expect(mocks.fsReadRange).not.toHaveBeenCalled();
    expect(mocks.sessionConnect).not.toHaveBeenCalled();

    click(reconnectButton());
    await flush();

    expect(findTab("log-1")?.restorePending).toBeUndefined();
    await flushUntil(() => mocks.fsReadRange.mock.calls.length > 0);
  });

  it("files browser: placeholder first, roots/list only after the explicit reconnect", async () => {
    const pending: AppTab = {
      id: "files-s1",
      kind: "files",
      title: "文件",
      sessionId: "s1",
      closable: true,
      restorePending: true,
    };
    useUi.setState({ workspaces: [hostWorkspace("ws-1", "s1", [pending])] });
    mounted = mountApp();
    await flush();

    expect(mounted?.container.textContent).toContain("连接并打开");
    expect(mocks.fsRoots).not.toHaveBeenCalled();
    expect(mocks.fsList).not.toHaveBeenCalled();
    expect(mocks.sessionConnect).not.toHaveBeenCalled();

    click(reconnectButton());
    await flush();

    expect(findTab("files-s1")?.restorePending).toBeUndefined();
    await flushUntil(() => mocks.fsRoots.mock.calls.length > 0);
    await flushUntil(() => mocks.fsList.mock.calls.length > 0);
  });

  it("deletedTarget keeps the placeholder, reports the failure, and never mounts the editor", async () => {
    const pending: AppTab = {
      id: "f1",
      kind: "files",
      title: "f1.txt",
      sessionId: "s1",
      path: "/a/f1.txt",
      closable: true,
      restorePending: true,
    };
    useUi.setState({
      workspaces: [{ ...hostWorkspace("ws-1", "s1", [pending]), deletedTarget: true }],
    });
    mounted = mountApp();
    await flush();

    expect(mounted?.container.textContent).toContain("连接并打开");
    click(reconnectButton());
    await flush();

    expect(useUi.getState().toasts.some((toast) => toast.text.includes("已删除"))).toBe(true);
    expect(mounted?.container.textContent).toContain("连接并打开");
    expect(findTab("f1")?.restorePending).toBe(true);
    expect(mocks.fsRead).not.toHaveBeenCalled();
    expect(mocks.fsReadRange).not.toHaveBeenCalled();
  });
});

describe("workspace drag reorder through the real strip", () => {
  function groupOf(id: string): AssetGroup {
    return { id, parentId: null, name: `Project ${id}`, sort: 0, createdAt: 1, updatedAt: 1 };
  }

  function assetOf(id: string, groupId: string): Asset {
    return {
      id,
      groupId,
      kind: "ssh",
      name: id,
      host: null,
      port: null,
      username: null,
      authKind: null,
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
    };
  }

  function dragEvent(type: string, dt: unknown, init: MouseEventInit = {}): MouseEvent {
    const event = new MouseEvent(type, { bubbles: true, cancelable: true, ...init });
    Object.defineProperty(event, "dataTransfer", { value: dt });
    return event;
  }

  function makeDataTransfer(): unknown {
    const data = new Map<string, string>();
    return {
      get types() {
        return [...data.keys()];
      },
      effectAllowed: "",
      dropEffect: "",
      setData: (type: string, value: string) => {
        data.set(type, value);
      },
      getData: (type: string) => data.get(type) ?? "",
    };
  }

  function stubRect(el: HTMLElement, left: number, width: number): void {
    el.getBoundingClientRect = () =>
      ({ left, width, right: left + width, top: 0, bottom: 28, height: 28, x: left, y: 0, toJSON: () => ({}) }) as DOMRect;
  }

  it("moves the dragged workspace through the store and persists the new order", async () => {
    mocks.assetList.mockResolvedValue([assetOf("asset-1", "p1")]);
    mocks.groupList.mockResolvedValue([groupOf("p1")]);
    useUi.setState({
      sessions: [sessionOf("s1"), sessionOf("s2")],
      workspaces: [hostWorkspace("ws-1", "s1", []), hostWorkspace("ws-2", "s2", [])],
      activeWorkspaceId: "ws-1",
      activeProject: "project:p1",
      projectInventory: { groups: [groupOf("p1")], assets: [assetOf("asset-1", "p1")] },
    });
    mounted = mountApp();
    await flush();

    const strip = mounted?.container.querySelector<HTMLElement>("[data-project-focus='project:p1']");
    expect(strip).not.toBeNull();
    const first = strip?.querySelector<HTMLElement>('[data-project-workspace-id="ws-1"]');
    const second = strip?.querySelector<HTMLElement>('[data-project-workspace-id="ws-2"]');
    if (!first || !second) throw new Error("strip workspaces not found");
    stubRect(second, 100, 80);
    const dt = makeDataTransfer();

    act(() => {
      first.dispatchEvent(dragEvent("dragstart", dt));
    });
    act(() => {
      second.dispatchEvent(dragEvent("drop", dt, { clientX: 190 }));
    });
    await flush();

    expect(useUi.getState().workspaces.map((workspace) => workspace.id)).toEqual(["ws-2", "ws-1"]);
    const { serializeLayout } = await import("../../app/layout");
    expect(serializeLayout(useUi.getState())?.workspaces.map((workspace) => workspace.id)).toEqual(["ws-2", "ws-1"]);
  });
});
