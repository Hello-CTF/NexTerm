/** @vitest-environment jsdom */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, createElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flush, mount, type MountedView } from "./features/reactTestUtils";

// Render probes: lazy panels are replaced by stubs that count their renders. A stub re-renders only when
// the component that places it re-renders, so each counter observes one level of the shell.
const probes = vi.hoisted(() => ({
  terminal: new Map<string, number>(),
  aiSidebar: 0,
  updateBanner: 0,
}));

vi.mock("../ipc/env", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../ipc/env")>();
  return { ...actual, TRANSPORT: "desktop", WEB: false, DESKTOP: true };
});

vi.mock("../ipc/commands", () => ({
  assetApi: {
    list: vi.fn().mockResolvedValue([]),
    search: vi.fn(),
    groupList: vi.fn().mockResolvedValue([]),
    snippetList: vi.fn(),
  },
  sessionApi: {
    list: vi.fn(() => new Promise(() => {})),
    connectLocal: vi.fn(),
    connect: vi.fn(),
    reconnect: vi.fn(),
    probe: vi.fn(),
  },
  vaultApi: { status: vi.fn().mockResolvedValue({ initialized: true, unlocked: true }), listCredentials: vi.fn() },
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
  terminalApi: {
    listLive: vi.fn(() => new Promise(() => {})),
    listDurable: vi.fn(() => new Promise(() => {})),
  },
  dbApi: {},
}));

vi.mock("../ipc/wails", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../ipc/wails")>()),
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
vi.mock("../app/useUpdate", () => ({ getUpdateState: () => ({ check: vi.fn().mockResolvedValue(undefined) }) }));
vi.mock("../features/settings/useWebAutoSync", () => ({
  useDesktopAutoSync: () => {},
  useWebAutoSync: () => {},
}));
vi.mock("../features/terminal/TerminalPane", () => ({
  TerminalPane: ({ sessionId }: { sessionId: string }) => {
    probes.terminal.set(sessionId, (probes.terminal.get(sessionId) ?? 0) + 1);
    return null;
  },
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
vi.mock("../features/ai/AiSidebar", () => ({
  AiSidebar: () => {
    probes.aiSidebar += 1;
    return null;
  },
}));
vi.mock("../features/settings/SettingsView", () => ({ SettingsView: () => null }));
vi.mock("../features/settings/AuditView", () => ({ AuditView: () => null }));
vi.mock("../features/credentials/CredentialsPanel", () => ({ CredentialsPanel: () => null }));
vi.mock("../features/credentials/CredentialsSidebar", () => ({ CredentialsSidebar: () => null }));
vi.mock("../features/explorer/AssetTree", () => ({ AssetTree: () => null, AssetEditor: () => null }));
vi.mock("../features/fleet/DeviceSessionBoundary", () => ({ DeviceSessionBoundary: () => null }));
vi.mock("../app/CommandPalette", () => ({ CommandPalette: () => null }));
vi.mock("../app/QuickConnect", () => ({ QuickConnect: () => null }));
vi.mock("../app/TakeoverBanner", () => ({ TakeoverBanner: () => null }));
vi.mock("../app/UpdateBanner", () => ({
  UpdateBanner: () => {
    probes.updateBanner += 1;
    return null;
  },
}));
vi.mock("../app/DekUnlockBanner", () => ({ DekUnlockBanner: () => null }));

import App from "../app/App";
import { sessionStatusText, useUi, type SessionRuntimeInfo, type Workspace } from "../app/store";

const WORKSPACES = 20;
const SESSIONS = 40;

function session(index: number, status: SessionRuntimeInfo["status"] = "connected"): SessionRuntimeInfo {
  return {
    id: `s-${index}`,
    assetId: `asset-${index}`,
    name: `host-${index}`,
    kind: "ssh",
    status,
    tabs: [`kernel-${index}`],
    createdAt: 1,
  };
}

function workspace(index: number): Workspace {
  return {
    id: `ws-${index}`,
    kind: "session",
    title: `host-${index}`,
    sessionId: `s-${index}`,
    assetId: `asset-${index}`,
    assetKind: "ssh",
    panes: [{
      id: `pane-${index}`,
      activeTabId: `tab-${index}`,
      tabs: [{ id: `tab-${index}`, kind: "terminal", title: "bash", sessionId: `s-${index}`, tabId: `kernel-${index}`, closable: true }],
    }],
    activePaneId: `pane-${index}`,
    splitRatio: 0.5,
    closable: true,
  };
}

function setStatus(id: string, status: SessionRuntimeInfo["status"]): void {
  act(() => {
    const state = useUi.getState();
    state.setSessions(state.sessions.map((item) => (item.id === id ? { ...item, status } : item)));
  });
}

function resetProbes(): void {
  probes.terminal.clear();
  probes.aiSidebar = 0;
  probes.updateBanner = 0;
}

function terminalRenders(): number {
  return [...probes.terminal.values()].reduce((sum, count) => sum + count, 0);
}

let mounted: MountedView | undefined;

beforeEach(() => {
  document.body.replaceChildren();
  localStorage.clear();
  useUi.setState({
    sessions: Array.from({ length: SESSIONS }, (_, index) => session(index)),
    toasts: [],
    leftOpen: false,
    rightOpen: false,
    leftMode: "assets",
    workspaces: Array.from({ length: WORKSPACES }, (_, index) => workspace(index)),
    activeWorkspaceId: "ws-0",
    connectingAssetIds: [],
  });
});

afterEach(() => {
  mounted?.unmount();
  mounted = undefined;
});

async function mountApp(): Promise<void> {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  mounted = mount(createElement(QueryClientProvider, { client }, createElement(App)));
  await flush();
  await flush();
  resetProbes();
}

function headerStatus(): string | null {
  return document.querySelector(".nx-ws-switch .nx-ws-status")?.getAttribute("aria-label") ?? null;
}

describe("app render counts with many workspaces and sessions", () => {
  it("mounts a terminal for every workspace", async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    mounted = mount(createElement(QueryClientProvider, { client }, createElement(App)));
    await flush();
    expect(probes.terminal.size).toBe(WORKSPACES);
  });

  it("does not re-render the shell or other panes when one background session changes status", async () => {
    await mountApp();
    setStatus("s-7", "reconnecting");
    await flush();
    expect(terminalRenders()).toBe(0);
    expect(probes.aiSidebar).toBe(0);
    expect(probes.updateBanner).toBe(0);
  });

  it("updates the header for the active session without re-rendering the shell or panes", async () => {
    await mountApp();
    setStatus("s-0", "reconnecting");
    await flush();
    expect(terminalRenders()).toBe(0);
    expect(probes.aiSidebar).toBe(0);
    expect(probes.updateBanner).toBe(0);
    expect(headerStatus()).toBe(sessionStatusText("reconnecting"));
  });

  it("re-renders only the edited workspace's terminal when one tab changes", async () => {
    await mountApp();
    act(() => useUi.getState().updateTab("tab-7", { title: "renamed" }));
    await flush();
    expect([...probes.terminal.entries()]).toEqual([["s-7", 1]]);
  });

  it("does not re-render anything when a resync returns an identical session list", async () => {
    await mountApp();
    act(() => useUi.getState().setSessions(Array.from({ length: SESSIONS }, (_, index) => session(index))));
    await flush();
    expect(terminalRenders()).toBe(0);
    expect(probes.aiSidebar).toBe(0);
    expect(probes.updateBanner).toBe(0);
  });
});
