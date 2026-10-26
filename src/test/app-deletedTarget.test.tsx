/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, createElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { flush, mount, type MountedView } from "./features/reactTestUtils";

const mocks = vi.hoisted(() => ({
  assetList: vi.fn(),
  sessionList: vi.fn(),
  vaultStatus: vi.fn(),
  banners: { takeover: false, update: false, dek: false },
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
  terminalApi: { renameTab: vi.fn() },
  dbApi: { connect: vi.fn() },
}));
vi.mock("../ipc/wails", () => ({
  closeWindow: vi.fn(),
  minimiseWindow: vi.fn(),
  toggleMaximiseWindow: vi.fn(),
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
  getUpdateState: () => ({ check: vi.fn() }),
}));
vi.mock("../features/settings/useWebAutoSync", () => ({
  useDesktopAutoSync: () => {},
  useWebAutoSync: () => {},
}));

vi.mock("../features/terminal/TerminalPane", () => ({
  TerminalPane: () => null,
}));
vi.mock("../features/terminal/BackgroundSessions", () => ({
  BackgroundSessions: () => null,
}));
vi.mock("../features/terminal/TranscriptHistoryPanel", () => ({
  TranscriptHistoryPanel: () => null,
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
  AiSidebar: () => null,
}));
vi.mock("../features/settings/SettingsView", () => ({ SettingsView: () => null }));
vi.mock("../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../features/explorer/AssetTree", () => ({ AssetTree: () => null, AssetEditor: () => null }));
vi.mock("../app/CommandPalette", () => ({
  CommandPalette: () => null,
}));
vi.mock("../app/QuickConnect", () => ({
  QuickConnect: () => null,
}));
vi.mock("../app/TakeoverBanner", () => ({
  TakeoverBanner: () => null,
}));
vi.mock("../app/UpdateBanner", () => ({
  UpdateBanner: () => null,
}));
vi.mock("../app/DekUnlockBanner", () => ({
  DekUnlockBanner: () => null,
}));

import App from "../app/App";
import { useUi, type Workspace } from "../app/store";
import type { Asset } from "../ipc/commands";

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

function hostWorkspace(id: string, assetId: string): Workspace {
  const paneId = `${id}-pane`;
  const tabId = `${id}-tab`;
  return {
    id,
    kind: "session",
    title: id,
    sessionId: `session-${id}`,
    assetId,
    panes: [
      {
        id: paneId,
        tabs: [{ id: tabId, kind: "terminal", title: tabId, sessionId: `session-${id}`, tabId: `kernel-${id}`, closable: true }],
        activeTabId: tabId,
      },
    ],
    activePaneId: paneId,
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

async function refreshAssets(assets: Asset[]): Promise<void> {
  mocks.assetList.mockResolvedValue(assets);
  const queryClient = client;
  if (!queryClient) throw new Error("App is not mounted");
  await act(() => queryClient.invalidateQueries({ queryKey: ["assets"] }));
  await flush();
}

beforeEach(() => {
  vi.clearAllMocks();
  document.body.replaceChildren();
  localStorage.clear();
  Element.prototype.scrollIntoView = vi.fn();
  envState.transport = "web";
  mocks.assetList.mockResolvedValue([]);
  mocks.sessionList.mockResolvedValue([]);
  mocks.vaultStatus.mockResolvedValue({ initialized: true, unlocked: true });
  useUi.setState({
    sessions: [],
    toasts: [],
    workspaces: [],
    activeWorkspaceId: null,
    connectingAssetIds: [],
    accountEpoch: 0,
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

describe("authoritative assets refresh and deletedTarget", () => {
  it("marks the workspace when the asset disappears, preserves it, and clears the mark on recovery", async () => {
    const workspace = hostWorkspace("ws", "asset-1");
    useUi.setState({ workspaces: [workspace], activeWorkspaceId: "ws" });
    mocks.assetList.mockResolvedValue([assetOf({ id: "asset-1", name: "host-1" })]);
    mounted = mountApp();
    await flush();
    expect(useUi.getState().workspaces[0]?.deletedTarget).toBeUndefined();

    await refreshAssets([]);

    const marked = useUi.getState().workspaces[0];
    expect(marked?.deletedTarget).toBe(true);
    expect(marked?.sessionId).toBe(workspace.sessionId);
    expect(marked?.panes).toEqual(workspace.panes);
    expect(useUi.getState().activeWorkspaceId).toBe("ws");

    await refreshAssets([assetOf({ id: "asset-1", name: "host-1" })]);

    expect(useUi.getState().workspaces[0]?.deletedTarget).toBeUndefined();
  });

  it("keeps a reconnect from marking the workspace", async () => {
    useUi.setState({ workspaces: [hostWorkspace("ws", "asset-1")], activeWorkspaceId: "ws" });
    mocks.assetList.mockResolvedValue([assetOf({ id: "asset-1", name: "host-1" })]);
    mounted = mountApp();
    await flush();

    act(() => useUi.getState().updateWorkspaceSession("ws", "session-reconnected"));
    await flush();

    const workspace = useUi.getState().workspaces[0];
    expect(workspace?.sessionId).toBe("session-reconnected");
    expect(workspace?.deletedTarget).toBeUndefined();
  });
});
