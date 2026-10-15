/** @vitest-environment jsdom */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const server = vi.hoisted(() => ({
  revision: 0,
  data: null as string | null,
  putAlwaysConflicts: false,
  handlers: [] as Array<(payload: { revision?: number } | null) => void>,
}));

vi.mock("../../ipc/commands", () => ({
  layoutApi: {
    get: vi.fn(async () => ({
      revision: server.revision,
      updatedAt: 0,
      data: server.data === null ? null : JSON.parse(server.data),
    })),
    put: vi.fn(async (data: string, revision: number) => {
      if (server.putAlwaysConflicts || revision !== server.revision) {
        return { saved: false, revision: server.revision, conflict: true };
      }
      server.revision = revision + 1;
      server.data = data;
      return { saved: true, revision: server.revision, conflict: false };
    }),
  },
  terminalApi: { listLive: vi.fn(async () => []) },
  dbApi: {},
  sessionApi: {},
  vaultApi: {},
}));

vi.mock("../../ipc/events", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../ipc/events")>();
  return {
    ...actual,
    listenEvent: vi.fn((topic: string, handler: (payload: never) => void) => {
      if (topic === "layout://changed") {
        server.handlers.push(handler as (payload: { revision?: number } | null) => void);
      }
      return Promise.resolve(() => {});
    }),
  };
});

import type { PersistedLayout } from "../../app/layout";
import type { AppTab, Workspace } from "../../app/store";

interface UiState {
  leftOpen: boolean;
  leftMode: string;
  rightOpen: boolean;
  leftWidth: number;
  rightWidth: number;
  workspaces: Workspace[];
  activeWorkspaceId: string | null;
  toasts: { text: string }[];
}

interface LayoutModule {
  startLayoutSync: () => void;
  flushLayout: () => Promise<void>;
  onRemoteChange: (payload: { revision?: number } | null) => void;
  layoutBootstrapped: Promise<void>;
  sanitizeLayout: (raw: unknown) => PersistedLayout | null;
  serializeLayout: (s: UiState) => PersistedLayout | null;
}

interface StoreModule {
  useUi: {
    getState: () => UiState;
    setState: (partial: Partial<UiState>) => void;
  };
}

function panelTab(id: string, kind: AppTab["kind"], title: string): AppTab {
  return { id, kind, title, closable: true };
}

function workspace(tabs: AppTab[], activeTabId: string | null = tabs[tabs.length - 1]?.id ?? null): Workspace {
  return {
    id: "ws",
    kind: "tools",
    title: "工具",
    panes: [{ id: "pane", tabs, activeTabId }],
    activePaneId: "pane",
    splitRatio: 0.5,
    closable: true,
  };
}

function deviceTabs(): AppTab[] {
  return [panelTab("devices", "devices", "设备管理"), panelTab("settings", "settings", "Settings")];
}

function persistedLayout(workspaces: Workspace[]): PersistedLayout {
  return {
    v: 2,
    leftOpen: true,
    leftMode: "assets",
    rightOpen: true,
    leftWidth: 248,
    rightWidth: 352,
    workspaces,
    activeWorkspaceId: workspaces[0]?.id ?? null,
    aiBoards: {},
  };
}

function findTab(state: UiState, id: string): AppTab | undefined {
  for (const w of state.workspaces) {
    for (const p of w.panes) {
      const t = p.tabs.find((x) => x.id === id);
      if (t) return t;
    }
  }
  return undefined;
}

async function openTab(): Promise<{ layout: LayoutModule; store: StoreModule["useUi"] }> {
  vi.resetModules();
  const layout = (await import("../../app/layout")) as unknown as LayoutModule;
  const store = (await import("../../app/store")) as unknown as StoreModule;
  layout.startLayoutSync();
  await layout.layoutBootstrapped;
  return { layout, store: store.useUi };
}

beforeEach(() => {
  server.revision = 0;
  server.data = null;
  server.putAlwaysConflicts = false;
  server.handlers.length = 0;
});

afterEach(() => {
  vi.resetModules();
  vi.clearAllMocks();
});

describe("device tabs in the layout pane contract", () => {
  it("serialize → JSON → sanitize round-trip keeps the devices tab and the active tab", async () => {
    const { layout, store } = await openTab();
    store.setState({ workspaces: [workspace(deviceTabs(), "devices")], activeWorkspaceId: "ws" });

    const dto = layout.serializeLayout(store.getState());
    expect(dto).not.toBeNull();
    const parsed = layout.sanitizeLayout(JSON.parse(JSON.stringify(dto)));
    expect(parsed).not.toBeNull();

    expect(parsed?.workspaces).toHaveLength(1);
    expect(parsed?.workspaces[0].kind).toBe("tools");
    const pane = parsed?.workspaces[0].panes[0];
    expect(pane?.tabs.map((t) => t.kind)).toEqual(["devices", "settings"]);
    expect(pane?.activeTabId).toBe("devices");
    expect(pane?.tabs.find((t) => t.id === "devices")?.title).toBe("设备管理");
  });

  it("sanitizeLayout keeps a pane whose only tab is the device list", async () => {
    const { layout } = await openTab();
    const parsed = layout.sanitizeLayout(
      persistedLayout([workspace([panelTab("devices", "devices", "设备管理")], "devices")]),
    );
    expect(parsed?.workspaces).toHaveLength(1);
    expect(parsed?.workspaces[0].panes[0].tabs.map((t) => t.kind)).toEqual(["devices"]);
    expect(parsed?.workspaces[0].panes[0].activeTabId).toBe("devices");
    expect(parsed?.activeWorkspaceId).toBe("ws");
  });

  it("sanitizeLayout drops legacy deviceTerminal tabs from older layouts", async () => {
    const { layout } = await openTab();
    const raw = persistedLayout([
      workspace(
        [
          panelTab("devices", "devices", "设备管理"),
          { id: "devterm-1", kind: "deviceTerminal", title: "Terminal · build-box", deviceId: "device-1", closable: true } as unknown as AppTab,
          panelTab("settings", "settings", "Settings"),
        ],
        "devterm-1",
      ),
    ]);
    const parsed = layout.sanitizeLayout(JSON.parse(JSON.stringify(raw)));
    const pane = parsed?.workspaces[0].panes[0];
    expect(pane?.tabs.map((t) => t.id)).toEqual(["devices", "settings"]);
    expect(pane?.activeTabId).toBe("settings");
  });

  it("sanitizeLayout never restores a legacy deviceTerminal tab, even as the pane's only tab", async () => {
    const { layout } = await openTab();
    const raw = persistedLayout([
      workspace([panelTab("devices", "devices", "设备管理")], "devices"),
      {
        ...workspace([
          { id: "devterm-missing", kind: "deviceTerminal", title: "Terminal · missing", closable: true } as unknown as AppTab,
        ]),
        id: "ws-legacy",
        kind: "session",
        title: "legacy",
      },
    ]);
    const parsed = layout.sanitizeLayout(JSON.parse(JSON.stringify(raw)));
    expect(parsed?.workspaces).toHaveLength(2);
    expect(parsed?.workspaces[0].panes[0].tabs.map((t) => t.id)).toEqual(["devices"]);
    const emptied = parsed?.workspaces.find((w) => w.panes.every((p) => p.tabs.length === 0));
    expect(emptied?.panes[0].tabs).toEqual([]);
    expect(emptied?.panes[0].activeTabId).toBeNull();
  });

  it("boot application drops legacy deviceTerminal tabs and keeps the rest", async () => {
    const raw = persistedLayout([
      workspace(
        [
          panelTab("devices", "devices", "设备管理"),
          { id: "devterm-1", kind: "deviceTerminal", title: "Terminal · build-box", deviceId: "device-1", closable: true } as unknown as AppTab,
          panelTab("settings", "settings", "Settings"),
        ],
        "devterm-1",
      ),
    ]);
    server.data = JSON.stringify(raw);
    const { store } = await openTab();

    const pane = store.getState().workspaces[0]?.panes[0];
    expect(pane?.tabs.map((t) => t.kind)).toEqual(["devices", "settings"]);
    expect(pane?.activeTabId).toBe("settings");
    expect(findTab(store.getState(), "devices")?.title).toBe("设备管理");
    expect(findTab(store.getState(), "devterm-1")).toBeUndefined();
  });

  it("remote LAYOUT_CHANGED application keeps device tabs", async () => {
    server.data = JSON.stringify(persistedLayout([workspace([panelTab("settings", "settings", "Settings")])]));
    const { layout, store } = await openTab();
    expect(findTab(store.getState(), "devices")).toBeUndefined();

    server.revision = 1;
    server.data = JSON.stringify(persistedLayout([workspace(deviceTabs(), "devices")]));
    layout.onRemoteChange({ revision: 1 });

    await vi.waitFor(() => {
      expect(findTab(store.getState(), "devices")?.title).toBe("设备管理");
    });
    const pane = store.getState().workspaces[0]?.panes[0];
    expect(pane?.tabs.map((t) => t.kind)).toEqual(["devices", "settings"]);
    expect(pane?.activeTabId).toBe("devices");
  });
});
