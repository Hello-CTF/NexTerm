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

function panelTab(id: string, kind: AppTab["kind"], title: string, deviceId?: string): AppTab {
  return { id, kind, title, deviceId, closable: true };
}

function workspace(tabs: AppTab[], activeTabId: string | null = tabs[tabs.length - 1]?.id ?? null): Workspace {
  return {
    id: "ws",
    kind: "session",
    title: "workspace",
    panes: [{ id: "pane", tabs, activeTabId }],
    activePaneId: "pane",
    splitRatio: 0.5,
    closable: true,
  };
}

function deviceTabs(): AppTab[] {
  return [
    panelTab("devices", "devices", "设备管理"),
    panelTab("devterm-1", "deviceTerminal", "终端 · build-box", "device-1"),
    panelTab("settings", "settings", "设置"),
  ];
}

function persistedLayout(workspaces: Workspace[]): PersistedLayout {
  return {
    v: 1,
    leftOpen: true,
    leftMode: "assets",
    rightOpen: true,
    leftWidth: 248,
    rightWidth: 352,
    workspaces,
    activeWorkspaceId: workspaces[0]?.id ?? null,
    presets: [],
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
  it("serialize → JSON → sanitize round-trip keeps device tabs with deviceId and the active tab", async () => {
    const { layout, store } = await openTab();
    store.setState({ workspaces: [workspace(deviceTabs(), "devterm-1")], activeWorkspaceId: "ws" });

    const dto = layout.serializeLayout(store.getState());
    expect(dto).not.toBeNull();
    const parsed = layout.sanitizeLayout(JSON.parse(JSON.stringify(dto)));
    expect(parsed).not.toBeNull();

    // 设备管理/设备终端是全局工具标签, 往返后落在「工具」工作区
    expect(parsed?.workspaces).toHaveLength(1);
    expect(parsed?.workspaces[0].kind).toBe("tools");
    const pane = parsed?.workspaces[0].panes[0];
    expect(pane?.tabs.map((t) => t.kind)).toEqual(["devices", "deviceTerminal", "settings"]);
    expect(pane?.activeTabId).toBe("devterm-1");
    const devices = pane?.tabs.find((t) => t.id === "devices");
    expect(devices?.title).toBe("设备管理");
    const devterm = pane?.tabs.find((t) => t.id === "devterm-1");
    expect(devterm?.deviceId).toBe("device-1");
  });

  it("sanitizeLayout keeps a pane whose only tab is the device list", async () => {
    const { layout } = await openTab();
    const parsed = layout.sanitizeLayout(
      persistedLayout([workspace([panelTab("devices", "devices", "设备管理")], "devices")]),
    );
    expect(parsed?.workspaces).toHaveLength(1);
    expect(parsed?.workspaces[0].kind).toBe("tools");
    expect(parsed?.workspaces[0].panes[0].tabs.map((t) => t.kind)).toEqual(["devices"]);
    expect(parsed?.workspaces[0].panes[0].activeTabId).toBe("devices");
    expect(parsed?.activeWorkspaceId).toBe("ws-tools");
  });

  it("sanitizeLayout drops deviceTerminal tabs whose deviceId is missing or invalid", async () => {
    const { layout } = await openTab();
    const parsed = layout.sanitizeLayout(
      persistedLayout([
        workspace(
          [
            panelTab("devices", "devices", "设备管理"),
            panelTab("devterm-missing", "deviceTerminal", "终端 · 丢失"),
            panelTab("devterm-empty", "deviceTerminal", "终端 · 空", ""),
            panelTab("devterm-bad", "deviceTerminal", "终端 · 坏", 42 as unknown as string),
            panelTab("devterm-ok", "deviceTerminal", "终端 · 好", "device-9"),
          ],
          "devterm-bad",
        ),
      ]),
    );
    const pane = parsed?.workspaces.find((w) => w.kind === "tools")?.panes[0];
    expect(pane?.tabs.map((t) => t.id)).toEqual(["devices", "devterm-ok"]);
    expect(pane?.tabs[1].deviceId).toBe("device-9");
    expect(pane?.activeTabId).toBe("devterm-ok");
  });

  it("sanitizeLayout never restores a deviceTerminal tab without deviceId, even as the pane's only tab", async () => {
    const { layout } = await openTab();
    const parsed = layout.sanitizeLayout(
      persistedLayout([
        workspace([panelTab("devices", "devices", "设备管理")], "devices"),
        workspace([panelTab("devterm-missing", "deviceTerminal", "终端 · 丢失")], "devterm-missing"),
      ]),
    );
    // 只剩非法 deviceTerminal 标签的工作区保持空面板; devices 标签迁入工具工作区
    expect(parsed?.workspaces).toHaveLength(2);
    const emptied = parsed?.workspaces.find((w) => w.panes.every((p) => p.tabs.length === 0));
    expect(emptied?.panes[0].tabs).toEqual([]);
    expect(emptied?.panes[0].activeTabId).toBeNull();
    const tools = parsed?.workspaces.find((w) => w.kind === "tools");
    expect(tools?.panes[0].tabs.map((t) => t.id)).toEqual(["devices"]);
    expect(parsed?.activeWorkspaceId).toBe("ws");
  });

  it("boot application preserves the device tabs and their active position", async () => {
    server.data = JSON.stringify(persistedLayout([workspace(deviceTabs(), "devterm-1")]));
    const { store } = await openTab();

    const pane = store.getState().workspaces.find((w) => w.kind === "tools")?.panes[0];
    expect(pane?.tabs.map((t) => t.kind)).toEqual(["devices", "deviceTerminal", "settings"]);
    expect(pane?.activeTabId).toBe("devterm-1");
    expect(findTab(store.getState(), "devices")?.title).toBe("设备管理");
    expect(findTab(store.getState(), "devterm-1")?.deviceId).toBe("device-1");
  });

  it("remote LAYOUT_CHANGED application preserves the device tabs", async () => {
    server.data = JSON.stringify(persistedLayout([workspace([panelTab("settings", "settings", "设置")])]));
    const { layout, store } = await openTab();
    expect(findTab(store.getState(), "devterm-1")).toBeUndefined();

    server.revision = 1;
    server.data = JSON.stringify(persistedLayout([workspace(deviceTabs(), "devterm-1")]));
    layout.onRemoteChange({ revision: 1 });

    await vi.waitFor(() => {
      expect(findTab(store.getState(), "devterm-1")?.deviceId).toBe("device-1");
    });
    const pane = store.getState().workspaces.find((w) => w.kind === "tools")?.panes[0];
    expect(pane?.tabs.map((t) => t.kind)).toEqual(["devices", "deviceTerminal", "settings"]);
    expect(pane?.activeTabId).toBe("devterm-1");
  });

  it("conflict layout application preserves the device tabs and refreshes to the server copy", async () => {
    server.data = JSON.stringify(persistedLayout([workspace([panelTab("settings", "settings", "设置")])]));
    const { layout, store } = await openTab();

    server.revision = 7;
    server.data = JSON.stringify(persistedLayout([workspace(deviceTabs(), "devterm-1")]));
    server.putAlwaysConflicts = true;
    store.setState({ leftWidth: 300 });
    await layout.flushLayout();

    const state = store.getState();
    const pane = state.workspaces.find((w) => w.kind === "tools")?.panes[0];
    expect(pane?.tabs.map((t) => t.kind)).toEqual(["devices", "deviceTerminal", "settings"]);
    expect(pane?.activeTabId).toBe("devterm-1");
    expect(findTab(state, "devterm-1")?.deviceId).toBe("device-1");
    expect(state.leftWidth).toBe(248);
    expect(state.toasts.some((t) => t.text.includes("布局已在其他设备上更新"))).toBe(true);
  });
});
